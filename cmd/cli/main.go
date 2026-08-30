// Command apitool-cli is AUK's headless runner: the concrete proof that
// internal/core has zero Wails dependencies (docs/02-architecture.md §1 —
// "the CLI builds with zero Wails in the dependency tree"), and the thing
// that lets AUK FAIL A CI BUILD.
//
// It builds the exact same engine app.go wires up for the GUI (via
// internal/appcore — one construction path, not a fork) and runs requests
// through the identical RunRequest chokepoint with origin "cli". Beyond the
// original single-request smoke test it can now run a whole folder or
// workspace, iterate over a CSV/JSON data file, and emit JUnit/JSON reports —
// see docs/09-ci-runner.md.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/runner"
	"apitool/internal/storage"
)

const usageText = `apitool-cli — AUK's headless API test runner

USAGE
  apitool-cli run           <requestID>   [flags]   run one request
  apitool-cli run-folder    <folderID>    [flags]   run a folder and its subfolders
  apitool-cli run-workspace [workspaceID] [flags]   run every request in a workspace

FLAGS
  --workspace-dir DIR    workspace root (default: $AUK_WORKSPACE_DIR, else the current directory)
  --env ID               environment to resolve ${variables} against
  --data FILE            CSV or JSON data file — run the target once per row
  --iterations N         repeat count (with --data: stop after N rows)
  --reporter NAME        cli | junit | json — repeatable
  --reporter-out PATH    where to write the preceding --reporter (or NAME=PATH); default stdout
  --bail                 stop at the first failed request
  --timeout DUR          per-request timeout (default 60s; 0 disables)
  --delay DUR            pause between requests (e.g. 250ms)

EXIT CODES
  0  every request passed
  1  at least one request failed a test, assertion, script, or transport
  2  the run could not start or complete (bad flags, unknown id, bad data file)

Full documentation: docs/09-ci-runner.md`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "apitool-cli:", err)
		os.Exit(exitCode(err))
	}
}

// run dispatches the subcommand. Returns a *usageError for anything that
// stopped the run from starting (exit 2) and a plain error for a run that
// started and failed (exit 1) — see exitCode.
func run(args []string) error {
	if len(args) == 0 {
		return usagef("%s", usageText)
	}
	switch args[0] {
	case "run":
		return runRequestCmd(args[1:])
	case "run-folder":
		return runFolderCmd(args[1:])
	case "run-workspace":
		return runWorkspaceCmd(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usageText)
		return nil
	default:
		return usagef("unknown command %q\n\n%s", args[0], usageText)
	}
}

// usageError marks a failure that happened BEFORE any request ran — a bad
// flag, an unknown folder id, an unreadable data file. CI treats it
// differently from a genuine test failure (exit 2 vs exit 1): the build is
// broken, not the API.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error { return &usageError{err: fmt.Errorf(format, a...)} }

// exitCode maps an error to the documented process exit code.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

// reorderFlagsFirst moves any token matching one of knownFlags (in
// `--name=value`, `-name=value`, `--name value`, or `-name value` form)
// ahead of every other token, preserving relative order within each group.
// This lets `run <requestID> --workspace-dir=X` and
// `run --workspace-dir=X <requestID>` behave identically, which
// flag.FlagSet.Parse alone does not support (it stops consuming flags at
// the first non-flag token).
//
// Every flag named here is assumed to TAKE A VALUE. Boolean flags must go
// through reorderFlags instead — see the boolFlags argument there for why.
func reorderFlagsFirst(args []string, knownFlags ...string) []string {
	return reorderFlags(args, knownFlags, nil)
}

// reorderFlags is the arity-aware reorderer. A boolean flag (`--bail`) takes
// NO value, so treating it like a value flag would swallow the following
// token — turning `run-folder --bail <folderID> --env=x` into
// `--bail <folderID>` plus a silently-dropped `--env`, which is the exact
// class of bug reorderFlagsFirst was written to prevent.
func reorderFlags(args []string, valueFlags, boolFlags []string) []string {
	takesValue := make(map[string]bool, len(valueFlags))
	for _, f := range valueFlags {
		takesValue[f] = true
	}
	known := make(map[string]bool, len(valueFlags)+len(boolFlags))
	for _, f := range valueFlags {
		known[f] = true
	}
	for _, f := range boolFlags {
		known[f] = true
	}

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			positional = append(positional, args[i])
			continue
		}
		name := strings.TrimLeft(args[i], "-")
		bare, _, hasValueInline := strings.Cut(name, "=")
		if !known[bare] {
			positional = append(positional, args[i])
			continue
		}
		flags = append(flags, args[i])
		if takesValue[bare] && !hasValueInline && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

// exitError turns a completed RunRequest call into the error (if any) that
// should make the CLI process exit non-zero. It is a thin adapter over
// runner.Verdict so the single-request path, a folder run's ✓/✗ column, and
// the JUnit failure count can never disagree about what "failed" means.
func exitError(requestID string, resp model.ResponseData, runErr error) error {
	if passed, reason := runner.Verdict(resp, runErr); !passed {
		return fmt.Errorf("request %q failed: %s", requestID, reason)
	}
	return nil
}

// openWorkspace opens dir through internal/appcore — the SAME engine
// construction the GUI and the MCP server use, so a request behaves
// identically whether a human clicked Send or CI ran it (that includes
// post-response scripts, which is what produces the test() results a CI
// report is made of). An empty/fresh dir is seeded with one runnable demo
// request so the CLI is usable standalone, mirroring app.go's first-run
// seeding.
func openWorkspace(dir string) (*core.Engine, *storage.FileStore, error) {
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		return nil, nil, err
	}
	if len(store.ListWorkspaces()) == 0 {
		if _, err := seedDemoData(store, "https://httpbin.org/get"); err != nil {
			return nil, nil, fmt.Errorf("seed demo workspace: %w", err)
		}
	}
	return engine, store, nil
}

// seedDemoData gives a fresh/empty workspace directory a runnable request,
// mirroring app.go's seedDemoData. url is parameterized (rather than
// hardcoded to httpbin.org) so tests can point the seeded request at a
// local httptest.Server instead of the network; it returns the seeded
// request's id so callers (main and tests) can address it without knowing
// the generated uuid.
func seedDemoData(store *storage.FileStore, url string) (requestID model.ID, err error) {
	wsID := uuid.NewString()
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo Workspace"}); err != nil {
		return "", err
	}

	envID := uuid.NewString()
	if err := store.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "baseUrl", Value: "https://httpbin.org", Enabled: true}},
	}, nil); err != nil {
		return "", err
	}

	requestID = uuid.NewString()
	if err := store.PutRequest(model.RequestDef{
		ID: requestID, WorkspaceID: wsID, Name: "GET httpbin",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: url,
	}); err != nil {
		return "", err
	}
	return requestID, nil
}
