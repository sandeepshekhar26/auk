package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/reporters"
	"apitool/internal/runner"
	"apitool/internal/storage"
)

// WorkspaceDirEnv lets CI point every command at one collection without
// repeating --workspace-dir on each invocation.
const WorkspaceDirEnv = "AUK_WORKSPACE_DIR"

// Flag names, split by arity so reorderFlags can move them ahead of
// positional args without a boolean flag swallowing the id that follows it.
var (
	valueFlagNames = []string{"workspace-dir", "env", "data", "iterations", "reporter", "reporter-out", "timeout", "delay"}
	boolFlagNames  = []string{"bail"}
)

// stringList is a repeatable string flag (`--reporter cli --reporter junit`).
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// reporterFlags records --reporter and --reporter-out in the ORDER they were
// given, because a bare `--reporter-out PATH` binds to "the preceding
// --reporter" — an ordering relationship that two independent repeatable
// slices would lose (a positional out would then bind to whichever reporter
// happened to be first, silently writing e.g. the console summary into
// results.xml). One shared ordered log preserves the interleave. NAME=PATH
// still targets a reporter by name regardless of position.
type reporterFlags struct {
	names []string // every --reporter, in order
	outs  []repOut // every --reporter-out, in order
}

type repOut struct {
	raw string
	idx int // index into names of the reporter this out immediately followed (-1 if none precedes it)
}

func (r *reporterFlags) reporterValue() flag.Value { return (*reporterName)(r) }
func (r *reporterFlags) outValue() flag.Value      { return (*reporterOut)(r) }

type reporterName reporterFlags

func (r *reporterName) String() string { return strings.Join(r.names, ",") }
func (r *reporterName) Set(v string) error {
	r.names = append(r.names, v)
	return nil
}

type reporterOut reporterFlags

func (r *reporterOut) String() string { return "" }
func (r *reporterOut) Set(v string) error {
	// Bind to the reporter that immediately precedes this --reporter-out.
	r.outs = append(r.outs, repOut{raw: v, idx: len(r.names) - 1})
	return nil
}

type runFlags struct {
	workspaceDir string
	env          string
	data         string
	iterations   int
	reporterSpec reporterFlags
	bail         bool
	timeout      time.Duration
	delay        time.Duration
}

func (f *runFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.workspaceDir, "workspace-dir", "", "workspace root directory (default: $"+WorkspaceDirEnv+", else the current directory)")
	fs.StringVar(&f.env, "env", "", "environment id to resolve ${variables} against")
	fs.StringVar(&f.data, "data", "", "CSV or JSON data file — runs the target once per row")
	fs.IntVar(&f.iterations, "iterations", 0, "repeat count; with --data, stop after N rows")
	fs.Var(f.reporterSpec.reporterValue(), "reporter", "reporter to emit ("+strings.Join(reporters.Names(), "|")+"), repeatable")
	fs.Var(f.reporterSpec.outValue(), "reporter-out", "file for the preceding --reporter (or NAME=PATH); default stdout")
	fs.BoolVar(&f.bail, "bail", false, "stop at the first failed request")
	fs.DurationVar(&f.timeout, "timeout", runner.DefaultTimeout, "per-request timeout (0 disables)")
	fs.DurationVar(&f.delay, "delay", 0, "pause between requests, e.g. 250ms")
}

// parseRunFlags parses one subcommand's flags, tolerating any flag/positional
// ordering, and returns the leftover positional arguments.
func parseRunFlags(name string, args []string) (*runFlags, []string, error) {
	f := &runFlags{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f.bind(fs)
	if err := fs.Parse(reorderFlags(args, valueFlagNames, boolFlagNames)); err != nil {
		return nil, nil, err
	}
	return f, fs.Args(), nil
}

// options maps the parsed flags onto the runner's Options.
func (f *runFlags) options() runner.Options {
	timeout := f.timeout
	if timeout == 0 {
		// The runner reads 0 as "use the default", so an explicit
		// `--timeout 0` (meaning "no timeout") becomes a negative sentinel.
		timeout = -1
	}
	return runner.Options{
		EnvironmentID: f.env,
		DataFile:      f.data,
		Iterations:    f.iterations,
		Bail:          f.bail,
		Timeout:       timeout,
		Delay:         f.delay,
		Origin:        "cli",
	}
}

// dir resolves the workspace directory: the flag, then $AUK_WORKSPACE_DIR,
// then the current directory (the original CLI's behavior).
func (f *runFlags) dir() (string, error) {
	if f.workspaceDir != "" {
		return f.workspaceDir, nil
	}
	if env := os.Getenv(WorkspaceDirEnv); env != "" {
		return env, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	return wd, nil
}

// open resolves the workspace directory and builds the shared engine.
// seedIfEmpty is true only for the single-request `run` command, which has
// always seeded a demo request into an empty directory so it is usable
// standalone; a folder/workspace run must never write files into whatever
// directory CI happened to be in.
func (f *runFlags) open(seedIfEmpty bool) (*core.Engine, *storage.FileStore, error) {
	dir, err := f.dir()
	if err != nil {
		return nil, nil, err
	}
	if seedIfEmpty {
		engine, store, err := openWorkspace(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("open workspace store at %q: %w", dir, err)
		}
		return engine, store, nil
	}
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open workspace store at %q: %w", dir, err)
	}
	return engine, store, nil
}

// runRequestCmd is the original `run <requestID>`: same output, same exit
// semantics, now sharing the runner's single verdict rule. Passing any
// --reporter switches the output to the reporters instead of the legacy
// human dump.
func runRequestCmd(args []string) error {
	f, rest, err := parseRunFlags("run", args)
	if err != nil {
		return usagef("%v", err)
	}
	if len(rest) == 0 || rest[0] == "" {
		return usagef("usage: apitool-cli run <requestID> [flags]\n\n%s", usageText)
	}

	engine, store, err := f.open(true)
	if err != nil {
		return usagef("%v", err)
	}

	opts := f.options()
	opts.Target = runner.RequestTarget(rest[0])
	summary, runErr := runner.Run(context.Background(), engine, store, opts)

	if len(f.reporterSpec.names) == 0 && len(summary.Results) > 0 {
		// Legacy single-request output: status, timing, headers, body,
		// assertions — what `apitool-cli run` has always printed. With
		// --iterations the last response is the one shown; the reporters
		// are the way to see every iteration.
		printResponse(summary.Results[len(summary.Results)-1].Response)
	} else if len(f.reporterSpec.names) > 0 {
		if err := emitReports(f, summary); err != nil {
			return usagef("%v", err)
		}
	}
	if runErr != nil {
		return usagef("%v", runErr)
	}
	return summaryError(summary)
}

// runFolderCmd runs a folder and every subfolder beneath it, in sidebar-tree
// order.
func runFolderCmd(args []string) error {
	f, rest, err := parseRunFlags("run-folder", args)
	if err != nil {
		return usagef("%v", err)
	}
	if len(rest) == 0 || rest[0] == "" {
		return usagef("usage: apitool-cli run-folder <folderID> [flags]\n\n%s", usageText)
	}
	opts := f.options()
	opts.Target = runner.FolderTarget(rest[0])
	return execute(f, opts)
}

// runWorkspaceCmd runs every request in a workspace. The id may be omitted
// when the directory holds exactly one workspace.
func runWorkspaceCmd(args []string) error {
	f, rest, err := parseRunFlags("run-workspace", args)
	if err != nil {
		return usagef("%v", err)
	}
	workspaceID := ""
	if len(rest) > 0 {
		workspaceID = rest[0]
	}
	opts := f.options()
	opts.Target = runner.WorkspaceTarget(workspaceID)
	return execute(f, opts)
}

// execute is the shared body of the multi-request commands: open, run,
// report, then derive the exit status.
func execute(f *runFlags, opts runner.Options) error {
	engine, store, err := f.open(false)
	if err != nil {
		return usagef("%v", err)
	}
	summary, runErr := runner.Run(context.Background(), engine, store, opts)
	// runner.Run returns a PARTIAL summary alongside a mid-run error (a
	// malformed data row after some iterations already ran, a cancellation).
	// Emit reports for whatever completed before surfacing the error, so a CI
	// job still gets the JUnit/JSON artifact for the requests that did run
	// rather than an empty report — and never a false "no tests, all green".
	// ALWAYS emit: buildSinks defaults to the console reporter, so a plain
	// `run-folder <id>` prints its per-request lines and tally instead of
	// silently printing nothing but an exit code (which is what gating this
	// on len(names)>0 used to do, contradicting the documented default).
	if emitErr := emitReports(f, summary); emitErr != nil {
		return usagef("%v", emitErr)
	}
	if runErr != nil {
		return usagef("%v", runErr)
	}
	return summaryError(summary)
}

// summaryError converts a finished run into the process's exit status:
// nil (exit 0) when everything passed, a plain error (exit 1) otherwise.
func summaryError(summary runner.RunSummary) error {
	if summary.Passed() {
		return nil
	}
	if summary.Total() == 0 {
		return fmt.Errorf("no requests ran")
	}
	if summary.Total() == 1 {
		r := summary.Results[0]
		name := r.RequestName
		if name == "" {
			name = r.RequestID
		}
		return fmt.Errorf("request %q failed: %s", name, r.Reason)
	}
	return fmt.Errorf("%d of %d request(s) failed", summary.FailedCount(), summary.Total())
}

// reporterSink is one selected reporter and where its output goes ("" or
// "-" means stdout).
type reporterSink struct {
	reporter reporters.Reporter
	path     string
}

// buildSinks pairs --reporter with --reporter-out. Paths bind by POSITION
// (the first --reporter-out belongs to the first --reporter) or explicitly
// as NAME=PATH, which is what makes two file reporters in one command
// unambiguous:
//
//	--reporter junit --reporter-out results.xml --reporter json --reporter-out results.json
//	--reporter junit --reporter json --reporter-out json=results.json
//
// buildSinks pairs each selected reporter with its output destination.
//
// A bare `--reporter-out PATH` binds to the --reporter that IMMEDIATELY
// PRECEDED it (recorded via reporterFlags' ordered log) — so
// `--reporter cli --reporter junit --reporter-out results.xml` writes the
// JUnit report to results.xml and leaves the console summary on stdout, the
// way every CI recipe expects. `NAME=PATH` targets a reporter by name
// regardless of position. A path-less --reporter-out given before any
// --reporter (or when the reporter defaulted) binds to the first sink.
func buildSinks(spec reporterFlags, defaultName string) ([]reporterSink, error) {
	names := spec.names
	if len(names) == 0 {
		names = []string{defaultName}
	}
	sinks := make([]reporterSink, len(names))
	for i, n := range names {
		r, err := reporters.New(n)
		if err != nil {
			return nil, err
		}
		sinks[i] = reporterSink{reporter: r}
	}

	registered := map[string]bool{}
	for _, n := range reporters.Names() {
		registered[n] = true
	}

	for _, out := range spec.outs {
		if name, path, hasName := strings.Cut(out.raw, "="); hasName && registered[strings.ToLower(name)] {
			// An empty path (e.g. `--reporter-out junit=` from an unset shell
			// variable) used to fall through to stdout, so the expected
			// artifact simply never appeared and the job still exited 0.
			if strings.TrimSpace(path) == "" {
				return nil, fmt.Errorf("--reporter-out %s has an empty path", out.raw)
			}
			bound := false
			for i := range sinks {
				if sinks[i].reporter.Name() == strings.ToLower(name) && sinks[i].path == "" {
					sinks[i].path = path
					bound = true
					break
				}
			}
			if !bound {
				return nil, fmt.Errorf("--reporter-out %s: reporter %q was not selected with --reporter (or already has an output)", out.raw, name)
			}
			continue
		}
		// Positional: bind to the immediately preceding --reporter. With no
		// preceding one there is nothing to bind to — refuse rather than
		// guessing. Guessing "the first sink" wrote the human console summary
		// into results.xml, and a bare `--reporter-out results.xml` with no
		// --reporter at all silently produced NO file while exiting 0, so a
		// CI job went green with a missing artifact.
		idx := out.idx
		if idx < 0 {
			return nil, fmt.Errorf(
				"--reporter-out %s must follow a --reporter (e.g. --reporter junit --reporter-out %s), or name it explicitly as NAME=PATH",
				out.raw, out.raw)
		}
		if idx >= len(sinks) {
			return nil, fmt.Errorf("--reporter-out %s has no preceding --reporter", out.raw)
		}
		if sinks[idx].path != "" {
			return nil, fmt.Errorf("--reporter-out %s: the %q reporter already has an output — use NAME=PATH to disambiguate", out.raw, sinks[idx].reporter.Name())
		}
		sinks[idx].path = out.raw
	}
	return sinks, nil
}

// emitReports writes every selected reporter. A report that cannot be
// written is fatal: a CI job that silently produced no JUnit file would
// report "no tests" and pass.
func emitReports(f *runFlags, summary runner.RunSummary) error {
	sinks, err := buildSinks(f.reporterSpec, "cli")
	if err != nil {
		return err
	}
	for _, s := range sinks {
		if s.path == "" || s.path == "-" {
			if err := s.reporter.Report(os.Stdout, summary); err != nil {
				return fmt.Errorf("write %s report: %w", s.reporter.Name(), err)
			}
			continue
		}
		if dir := filepath.Dir(s.path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create report directory %q: %w", dir, err)
			}
		}
		file, err := os.Create(s.path)
		if err != nil {
			return fmt.Errorf("create %s report %q: %w", s.reporter.Name(), s.path, err)
		}
		if err := s.reporter.Report(file, summary); err != nil {
			file.Close()
			return fmt.Errorf("write %s report %q: %w", s.reporter.Name(), s.path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s report %q: %w", s.reporter.Name(), s.path, err)
		}
	}
	return nil
}
