// Command apitool-cli is the headless runner: the concrete proof that
// internal/core has zero Wails dependencies (docs/02-architecture.md §1 —
// "the CLI builds with zero Wails in the dependency tree"). It builds the
// exact same core.Engine that app.go wires up for the GUI and runs one
// request through the identical RunRequest chokepoint (origin "cli"), which
// is what makes it usable in CI as a smoke-test/regression runner
// (docs/01-feature-roadmap.md "Headless CLI runner").
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"apitool/internal/assert"
	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	graphqlprotocol "apitool/internal/protocols/graphql"
	grpcprotocol "apitool/internal/protocols/grpc"
	httpprotocol "apitool/internal/protocols/http"
	sseprotocol "apitool/internal/protocols/sse"
	wsprotocol "apitool/internal/protocols/ws"
	"apitool/internal/storage"
	"apitool/internal/templating"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "apitool-cli:", err)
		os.Exit(1)
	}
}

// reorderFlagsFirst moves any token matching one of knownFlags (in
// `--name=value`, `-name=value`, `--name value`, or `-name value` form)
// ahead of every other token, preserving relative order within each group.
// This lets `run <requestID> --workspace-dir=X` and
// `run --workspace-dir=X <requestID>` behave identically, which
// flag.FlagSet.Parse alone does not support (it stops consuming flags at
// the first non-flag token).
func reorderFlagsFirst(args []string, knownFlags ...string) []string {
	isKnown := make(map[string]bool, len(knownFlags))
	for _, f := range knownFlags {
		isKnown[f] = true
	}

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			positional = append(positional, args[i])
			continue
		}
		name := strings.TrimLeft(args[i], "-")
		bare, _, hasValueInline := strings.Cut(name, "=")
		if !isKnown[bare] {
			positional = append(positional, args[i])
			continue
		}
		flags = append(flags, args[i])
		if !hasValueInline && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return fmt.Errorf("usage: apitool-cli run <requestID> [--workspace-dir=DIR] [--env=ENVIRONMENT_ID]")
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	workspaceDir := fs.String("workspace-dir", "", "workspace root directory (defaults to the current directory)")
	envID := fs.String("env", "", "environment id to resolve variables against")
	// Go's flag.Parse stops at the first non-flag token, so a natural
	// invocation like `run <requestID> --workspace-dir=X` would silently
	// drop the flag. reorderFlagsFirst moves recognized flags (and their
	// values) ahead of positional args so flag order never matters.
	if err := fs.Parse(reorderFlagsFirst(args[1:], "workspace-dir", "env")); err != nil {
		return err
	}

	requestID := fs.Arg(0)
	if requestID == "" {
		return fmt.Errorf("usage: apitool-cli run <requestID> [--workspace-dir=DIR] [--env=ENVIRONMENT_ID]")
	}

	dir := *workspaceDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		dir = wd
	}

	store, err := newStore(dir)
	if err != nil {
		return fmt.Errorf("open workspace store at %q: %w", dir, err)
	}
	engine := buildEngine(store)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sessionID := uuid.NewString()
	resp, runErr := engine.RunRequest(ctx, sessionID, requestID, *envID, "cli", core.NoopSink{})

	printResponse(resp)

	return exitError(requestID, resp, runErr)
}

// buildEngine wires up the same core.Engine shape app.go builds for the GUI
// (docs/02-architecture.md §1/§3 — GUI, CLI, and MCP all construct and drive
// the identical headless core.Engine). core.Engine and templating.Engine
// reference each other (the templater needs the engine as its ChainResolver
// for response()-style refs; the engine needs a Templater at construction),
// so the engine is built with a nil Templater first and wired in afterwards
// via the exported field, matching the pattern chaining.go documents.
func buildEngine(store core.Store) *core.Engine {
	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.Asserter = cliAsserter{}
	engine.RegisterProtocol(httpprotocol.New())
	engine.RegisterProtocol(wsprotocol.New())
	engine.RegisterProtocol(sseprotocol.New())
	engine.RegisterProtocol(graphqlprotocol.New())
	engine.RegisterProtocol(grpcprotocol.New())
	return engine
}

type cliAsserter struct{}

func (cliAsserter) Evaluate(a []model.Assertion, resp model.ResponseData) []model.AssertionResult {
	return assert.Evaluate(a, resp)
}

// exitError turns a completed RunRequest call into the error (if any) that
// should make the CLI process exit non-zero: a hard error from the engine,
// or an HTTP status >= 400, which is what makes this usable as a CI
// smoke-test/regression gate (docs/01-feature-roadmap.md "Headless CLI
// runner").
func exitError(requestID string, resp model.ResponseData, runErr error) error {
	if runErr != nil {
		return fmt.Errorf("run request %q: %w", requestID, runErr)
	}
	// Assertions are the primary CI gate: any failed assertion fails the run,
	// even on a 2xx response (a 200 with the wrong body is still a failure).
	if len(resp.AssertionResults) > 0 && !assert.AllPassed(resp.AssertionResults) {
		var failed int
		for _, r := range resp.AssertionResults {
			if !r.Passed {
				failed++
			}
		}
		return fmt.Errorf("request %q: %d/%d assertion(s) failed", requestID, failed, len(resp.AssertionResults))
	}
	if resp.Status >= 400 {
		return fmt.Errorf("request %q returned status %d", requestID, resp.Status)
	}
	return nil
}

// newStore opens the git-friendly, YAML-file-backed storage.FileStore
// rooted at dir, matching --workspace-dir's contract: `apitool-cli run`
// reads and runs a real request out of the same on-disk workspace format
// the GUI reads and writes (docs/02-architecture.md §1 — GUI and CLI share
// one storage format, not just one engine). An empty/fresh dir is seeded
// with one runnable demo request so the CLI is usable standalone, mirroring
// app.go's first-run seeding.
func newStore(dir string) (core.Store, error) {
	store, err := storage.NewFileStore(dir)
	if err != nil {
		return nil, err
	}
	if len(store.ListWorkspaces()) == 0 {
		if _, err := seedDemoData(store, "https://httpbin.org/get"); err != nil {
			return nil, fmt.Errorf("seed demo workspace: %w", err)
		}
	}
	return store, nil
}

// seedDemoData gives a fresh/empty workspace directory a runnable request,
// mirroring app.go's seedDemoData. url is parameterized (rather than
// hardcoded to httpbin.org) so tests can point the seeded request at a
// local httptest.Server instead of the network; it returns the seeded
// request's id so callers (main and tests) can address it without knowing
// the generated uuid.
func seedDemoData(store *storage.FileStore, url string) (requestID model.ID, err error) {
	wsID := uuid.NewString()
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo Workspace", OrderKey: "a0"}); err != nil {
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
		OrderKey: "a0",
	}); err != nil {
		return "", err
	}
	return requestID, nil
}
