// Package runner is AUK's headless test runner: the reusable engine behind
// `auk run-folder`/`auk run-workspace` and the thing that lets AUK FAIL A CI
// BUILD (docs/09-ci-runner.md).
//
// It is deliberately Wails-free and GUI-free. It drives the exact same
// core.Engine.RunRequest chokepoint the GUI's Send button and the MCP server
// use — no second execution path — and adds only the three things a CI
// runner needs on top of a single send:
//
//  1. a TARGET larger than one request (a folder subtree, or a whole
//     workspace), walked in the same order the sidebar tree shows;
//  2. DATA-DRIVEN ITERATIONS (run the whole target once per row of a CSV or
//     JSON file, with the row's columns exposed as ${variables});
//  3. a machine-consumable RunSummary that reporters (internal/reporters)
//     turn into JUnit XML / JSON / a console summary, and that the CLI
//     turns into an exit code.
//
// The runner never re-derives a pass/fail verdict of its own beyond what
// model.ResponseData.Passed() says (see Verdict) — one verdict, shared by
// the GUI badge, the CLI exit code, and every reporter.
package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// Store is what the runner needs on top of core.Store: the two listing calls
// that make a folder/workspace subtree resolvable. Both storage.FileStore and
// storage.MemoryStore satisfy it, and both treat an empty workspaceID as "all
// workspaces" — which is how a folder can be located by id alone.
type Store interface {
	core.Store
	ListWorkspaces() []model.Workspace
	ListRequests(workspaceID model.ID) []model.RequestDef
}

// DefaultTimeout is the per-request ceiling when Options.Timeout is zero,
// matching the 60s the single-request CLI path has always used.
const DefaultTimeout = 60 * time.Second

// Options configures one run. The zero value plus a Target is a valid
// single-pass run with no data file and a 60s per-request timeout.
type Options struct {
	// Target selects what to run: one request, a folder subtree, or a
	// whole workspace. Required.
	Target Target
	// EnvironmentID is the environment variables resolve against; empty
	// means "no environment" (the engine skips environment lookup entirely,
	// exactly as the GUI does with no environment selected).
	EnvironmentID model.ID
	// DataFile points at a CSV or JSON file driving iterations — one
	// iteration per row, the row's columns layered ON TOP of the
	// environment (see data.go and docs/09-ci-runner.md for the precedence
	// rules). Empty runs Iterations passes with no extra variables.
	DataFile string
	// Iterations is how many times to run the whole target. With a data
	// file it CAPS the number of rows consumed (0 = every row); without
	// one it is the repeat count (0 or 1 = a single pass).
	Iterations int
	// Bail stops the run at the first failed request instead of executing
	// the rest of the target.
	Bail bool
	// Timeout is the per-request ceiling. Zero uses DefaultTimeout; a
	// negative value disables the per-request timeout entirely (the parent
	// context still applies).
	Timeout time.Duration
	// Delay is slept between requests (not before the first, not after the
	// last) — the throttle for rate-limited APIs.
	Delay time.Duration
	// Origin is recorded on every DispatchContext for the audit trail and
	// policy checks; defaults to "cli".
	Origin string
}

// CheckKind distinguishes where an individual pass/fail check came from, so
// a reporter can label it without inspecting the raw response.
type CheckKind string

const (
	// CheckAssertion is one declarative assertion saved on the request.
	CheckAssertion CheckKind = "assertion"
	// CheckTest is one test() declared by a post-response script.
	CheckTest CheckKind = "test"
	// CheckScript is the synthetic check standing in for a post-response
	// script that failed to run at all (ResponseData.ScriptError).
	CheckScript CheckKind = "script"
	// CheckRequest is the synthetic check a request with NO assertions and
	// NO script tests gets, so a bare smoke-test request still produces a
	// countable test case in a JUnit report instead of an empty suite.
	CheckRequest CheckKind = "request"
)

// Check is one pass/fail unit inside a request's result — an assertion, a
// script test, or the synthetic whole-request check. Reporters render these
// one-to-one as JUnit <testcase> elements.
type Check struct {
	Name    string    `json:"name"`
	Kind    CheckKind `json:"kind"`
	Passed  bool      `json:"passed"`
	Message string    `json:"message,omitempty"` // failure detail, empty when passed
}

// RequestResult is one request's outcome in one iteration.
type RequestResult struct {
	// Iteration is 1-based; always 1 for a run with no data file and no
	// --iterations.
	Iteration int `json:"iteration"`
	// FolderPath is the request's ancestor folder names, root-first, so a
	// reporter can render "Checkout / Payments / Charge card" without a
	// second lookup.
	FolderPath  []string `json:"folderPath,omitempty"`
	RequestID   model.ID `json:"requestId"`
	RequestName string   `json:"requestName"`
	Method      string   `json:"method"`
	// URL is the request's CONFIGURED url (before ${...} templating) —
	// the runner deliberately does not re-resolve templates just to log
	// them, since resolution can have side effects (auth token fetch,
	// pre-request script).
	URL        string `json:"url"`
	Status     int    `json:"status"`
	StatusText string `json:"statusText,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Passed     bool   `json:"passed"`
	// Reason summarizes why a failed request failed (empty when passed).
	Reason string `json:"reason,omitempty"`
	// Error is a transport/engine-level failure (connection refused, bad
	// template, blocked by policy). Distinct from a check failure: the
	// request never produced a usable response.
	Error string `json:"error,omitempty"`
	// ScriptError is a post-response script that could not run.
	ScriptError string  `json:"scriptError,omitempty"`
	Checks      []Check `json:"checks,omitempty"`
	// Response is the full raw response, kept for reporters that need more
	// than the flattened Checks (the CLI's single-request printer prints
	// its body). Not serialized: the JSON reporter defines its own shape.
	Response model.ResponseData `json:"-"`
}

// RunSummary is the whole run — what every reporter consumes and what the
// CLI derives its exit code from.
type RunSummary struct {
	// Target describes what was run, e.g. `folder "Smoke tests"`.
	Target     string    `json:"target"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	// Iterations is how many passes actually ran (rows consumed, for a
	// data-driven run).
	Iterations int `json:"iterations"`
	// DataFile is the data file that drove the iterations, if any.
	DataFile string `json:"dataFile,omitempty"`
	// EnvironmentID is the environment the run resolved variables against.
	EnvironmentID model.ID `json:"environmentId,omitempty"`
	// Bailed is true when --bail cut the run short.
	Bailed bool `json:"bailed"`
	// Aborted carries the error when the run could not COMPLETE (an unknown
	// target, a malformed data row partway through, a cancellation). It is
	// distinct from a failing request: the run itself did not finish, so a
	// reporter must render it as an error rather than emitting a tidy
	// "0 tests, 0 failures" document that reads as a green build.
	Aborted string          `json:"aborted,omitempty"`
	Results []RequestResult `json:"results"`
}

// Total is the number of requests executed (across all iterations).
func (s RunSummary) Total() int { return len(s.Results) }

// PassedCount / FailedCount count REQUESTS, not checks.
func (s RunSummary) PassedCount() int {
	n := 0
	for _, r := range s.Results {
		if r.Passed {
			n++
		}
	}
	return n
}

func (s RunSummary) FailedCount() int { return s.Total() - s.PassedCount() }

// Checks / ChecksPassed / ChecksFailed count individual assertions, script
// tests, and synthetic per-request checks across the whole run.
func (s RunSummary) Checks() int {
	n := 0
	for _, r := range s.Results {
		n += len(r.Checks)
	}
	return n
}

func (s RunSummary) ChecksPassed() int {
	n := 0
	for _, r := range s.Results {
		for _, c := range r.Checks {
			if c.Passed {
				n++
			}
		}
	}
	return n
}

func (s RunSummary) ChecksFailed() int { return s.Checks() - s.ChecksPassed() }

// Passed is the run's single verdict: every request passed and at least
// nothing was cut short by --bail. An empty run (no requests) is NOT a pass —
// "I ran nothing" must never green-light a CI build.
func (s RunSummary) Passed() bool {
	if len(s.Results) == 0 {
		return false
	}
	return s.FailedCount() == 0
}

// Verdict is THE pass/fail rule for one request, in one place, so the CLI
// exit code, the JUnit <failure> count, and the console ✓/✗ can never
// disagree:
//
//   - a transport/engine error (or ResponseData.Error) always fails;
//   - otherwise ResponseData.Passed() decides — any failed assertion, any
//     failed script test, or a script that could not run;
//   - a request that declared NO checks at all is treated as a smoke test:
//     an HTTP status >= 400 fails it. Once a request declares even one
//     assertion or test, those checks are the whole verdict — so a
//     deliberate `status eq 404` assertion passes on a 404.
func Verdict(resp model.ResponseData, runErr error) (passed bool, reason string) {
	if runErr != nil {
		return false, runErr.Error()
	}
	if resp.Error != "" {
		return false, resp.Error
	}
	if resp.ScriptError != "" {
		return false, "post-response script error: " + resp.ScriptError
	}
	if !resp.Passed() {
		failed := 0
		total := len(resp.AssertionResults) + len(resp.TestResults)
		for _, a := range resp.AssertionResults {
			if !a.Passed {
				failed++
			}
		}
		for _, t := range resp.TestResults {
			if !t.Passed {
				failed++
			}
		}
		return false, fmt.Sprintf("%d/%d check(s) failed", failed, total)
	}
	if len(resp.AssertionResults) == 0 && len(resp.TestResults) == 0 && resp.Status >= 400 {
		return false, fmt.Sprintf("HTTP %d (no assertions declared — treated as a smoke test)", resp.Status)
	}
	return true, ""
}

// RunRequest runs a single request. Convenience wrapper over Run.
func RunRequest(ctx context.Context, engine *core.Engine, store Store, requestID model.ID, opts Options) (RunSummary, error) {
	opts.Target = RequestTarget(requestID)
	return Run(ctx, engine, store, opts)
}

// RunFolder runs every request in folderID's subtree, sequentially, in
// sidebar-tree order (see Plan).
func RunFolder(ctx context.Context, engine *core.Engine, store Store, folderID model.ID, opts Options) (RunSummary, error) {
	opts.Target = FolderTarget(folderID)
	return Run(ctx, engine, store, opts)
}

// RunWorkspace runs every request in a workspace. An empty workspaceID
// resolves to the only workspace on disk (and errors if there are several).
func RunWorkspace(ctx context.Context, engine *core.Engine, store Store, workspaceID model.ID, opts Options) (RunSummary, error) {
	opts.Target = WorkspaceTarget(workspaceID)
	return Run(ctx, engine, store, opts)
}

// Run executes opts.Target and returns the summary. The returned error is
// non-nil only for a run that could not START (unknown folder, unreadable
// data file, empty target) — a request that FAILED is reported in the
// summary, not as an error, because a CI run wants every outcome, not just
// the first.
func Run(ctx context.Context, engine *core.Engine, store Store, opts Options) (outSummary RunSummary, outErr error) {
	// Any error return stamps Aborted onto the summary that goes back to the
	// caller, so a reporter can render "this run did not complete" instead of
	// a tidy zero-test document that reads as a green build (see
	// RunSummary.Aborted).
	defer func() {
		if outErr != nil && outSummary.Aborted == "" {
			outSummary.Aborted = outErr.Error()
		}
	}()

	if engine == nil {
		return RunSummary{}, errors.New("runner: nil engine")
	}
	if store == nil {
		return RunSummary{}, errors.New("runner: nil store")
	}

	// Resolve the plan BEFORE any variable overlay is installed, so the
	// traversal always sees the store's real folders.
	plan, err := Plan(store, opts.Target)
	if err != nil {
		return RunSummary{}, err
	}
	if len(plan) == 0 {
		return RunSummary{}, fmt.Errorf("%s contains no requests to run", opts.Target.Describe())
	}

	rows, err := openRows(opts)
	if err != nil {
		return RunSummary{}, err
	}
	defer rows.Close()

	envID := opts.EnvironmentID
	// EVERY runner pass — data-driven or not — carries its variables through a
	// run-scoped layer attached to the CONTEXT, never by wrapping or mutating
	// engine.Store.
	//
	// Two reasons it is unconditional. (1) A data-driven run needs it to carry
	// each iteration's columns. (2) More importantly, this is the CI/CLI path,
	// and a CI run MUST NOT have side effects on the user's workspace: without
	// the layer a post-response `vars.set("token", …)` falls through to
	// persistVariables and rewrites the git-tracked environment YAML on disk,
	// so every `auk run-folder smoke --env prod` in CI would commit a live
	// bearer token into the repo. With the layer, script writes stay in memory
	// for the run and are discarded when it ends — while intra-run chaining
	// still works, because mergedEnvironment consults the layer.
	//
	// A concurrent GUI/MCP send, whose context has no layer, is unaffected and
	// still sees the real store (LastResponse, environment persistence) intact.
	// runPlan resets the layer per iteration. No --env is required:
	// mergedEnvironment merges the layer even with no environment loaded.
	ctx = core.WithRunScopedVars(ctx, core.NewRunScopedVars())

	origin := opts.Origin
	if origin == "" {
		origin = "cli"
	}
	exec := func(ctx context.Context, requestID, environmentID model.ID) (model.ResponseData, error) {
		return engine.RunRequest(ctx, uuid.NewString(), requestID, environmentID, origin, core.NoopSink{})
	}

	return runPlan(ctx, exec, engine, plan, rows, envID, opts)
}

// execFunc is the per-request execution seam: engine.RunRequest in
// production, a synthetic stub in tests that need to drive exact
// ResponseData values without a live server.
type execFunc func(ctx context.Context, requestID, environmentID model.ID) (model.ResponseData, error)

// runPlan is the iteration/request loop shared by Run and the tests. The
// run-scoped variable layer (if any) travels on ctx, so it is reset per
// iteration from there; the engine parameter is retained only for signature
// stability with the test seam (which passes nil) and is otherwise unused.
func runPlan(ctx context.Context, exec execFunc, engine *core.Engine, plan []PlannedRequest, rows *rowSource, envID model.ID, opts Options) (RunSummary, error) {
	_ = engine // see doc comment: reset now happens via the context-borne layer
	started := time.Now()
	summary := RunSummary{
		Target:        opts.Target.Describe(),
		StartedAt:     started,
		DataFile:      opts.DataFile,
		EnvironmentID: envID,
	}

	first := true
iterations:
	for {
		row, ok, err := rows.next()
		if err != nil {
			// A malformed row mid-file is a hard failure: silently running
			// fewer iterations than the data file describes would let a
			// broken fixture pass CI.
			summary.DurationMs = time.Since(started).Milliseconds()
			return summary, err
		}
		if !ok {
			break
		}
		summary.Iterations++
		if rv := core.RunScopedVarsFrom(ctx); rv != nil {
			// A new iteration: install this row and CLEAR everything the
			// previous iteration's scripts wrote, so iteration N+1 never
			// inherits iteration N's run-scoped variables.
			rv.Reset(row.Values)
		}

		for _, planned := range plan {
			if err := ctx.Err(); err != nil {
				summary.DurationMs = time.Since(started).Milliseconds()
				return summary, err
			}
			if !first && opts.Delay > 0 {
				if err := sleep(ctx, opts.Delay); err != nil {
					summary.DurationMs = time.Since(started).Milliseconds()
					return summary, err
				}
			}
			first = false

			result := runOne(ctx, exec, planned, envID, row.Index, opts.Timeout)
			summary.Results = append(summary.Results, result)
			if !result.Passed && opts.Bail {
				summary.Bailed = true
				break iterations
			}
		}
	}

	summary.DurationMs = time.Since(started).Milliseconds()
	if summary.Iterations == 0 {
		// A data file with a header row and nothing under it would
		// otherwise "pass" by running zero requests.
		return summary, fmt.Errorf("data file %s contains no data rows", opts.DataFile)
	}
	return summary, nil
}

func runOne(ctx context.Context, exec execFunc, planned PlannedRequest, envID model.ID, iteration int, timeout time.Duration) RequestResult {
	req := planned.Request
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	start := time.Now()
	resp, err := exec(runCtx, req.ID, envID)
	elapsed := time.Since(start).Milliseconds()

	passed, reason := Verdict(resp, err)
	// Verdict sees only the ResponseData, so it treats a request with no
	// assertions and no tests as a passing smoke test. But when the request
	// HAS a post-response script that produced no tests (and did not itself
	// error), that is NOT a bare smoke test — it is a script that asserted
	// nothing: a shadowed global `test`, an all-skipped suite, or a typo. A
	// green there is false confidence, so it fails here, the one place the
	// RequestDef is in scope. A request that also has declarative assertions is
	// left alone — those are its checks, and the script may legitimately be a
	// pure token-extractor. The refined verdict flows into the result and its
	// Checks below, so the exit code, JUnit count, and console mark all agree.
	if passed && err == nil && resp.Error == "" && resp.ScriptError == "" &&
		req.PostResponseScript != "" &&
		len(resp.AssertionResults) == 0 && len(resp.TestResults) == 0 {
		passed = false
		reason = "post-response script registered no tests"
	}
	result := RequestResult{
		Iteration:   iteration,
		FolderPath:  planned.FolderPath,
		RequestID:   req.ID,
		RequestName: req.Name,
		Method:      req.Method,
		URL:         req.URL,
		Status:      resp.Status,
		StatusText:  resp.StatusText,
		DurationMs:  elapsed,
		Passed:      passed,
		Reason:      reason,
		ScriptError: resp.ScriptError,
		Checks:      checksOf(resp, passed, reason),
		Response:    resp,
	}
	if resp.TimingMs > 0 {
		result.DurationMs = resp.TimingMs
	}
	if err != nil {
		result.Error = err.Error()
	} else if resp.Error != "" {
		result.Error = resp.Error
	}
	return result
}

// checksOf flattens a response's assertions and script tests into the
// uniform Check list reporters render. A request that declared nothing gets
// one synthetic check so it still appears as a test case in JUnit — a
// smoke-test folder must be able to fail a build.
func checksOf(resp model.ResponseData, passed bool, reason string) []Check {
	checks := make([]Check, 0, len(resp.AssertionResults)+len(resp.TestResults)+1)
	for _, a := range resp.AssertionResults {
		c := Check{Name: AssertionLabel(a.Assertion), Kind: CheckAssertion, Passed: a.Passed}
		if !a.Passed {
			if a.Error != "" {
				c.Message = a.Error
			} else {
				c.Message = fmt.Sprintf("expected %s, actual: %s", AssertionLabel(a.Assertion), a.Actual)
			}
		}
		checks = append(checks, c)
	}
	for _, t := range resp.TestResults {
		c := Check{Name: t.Name, Kind: CheckTest, Passed: t.Passed}
		if !t.Passed {
			c.Message = t.Error
			if c.Message == "" {
				c.Message = "test failed"
			}
		}
		checks = append(checks, c)
	}
	if resp.ScriptError != "" {
		checks = append(checks, Check{
			Name:    "post-response script",
			Kind:    CheckScript,
			Passed:  false,
			Message: resp.ScriptError,
		})
	}
	if len(checks) == 0 {
		c := Check{Name: "request completed", Kind: CheckRequest, Passed: passed}
		if !passed {
			c.Message = reason
		}
		checks = append(checks, c)
	}
	return checks
}

// AssertionLabel renders an assertion as a compact one-line description
// ("body.user.id gt 10", "status eq 200") — the name a reporter shows for
// the check.
func AssertionLabel(a model.Assertion) string {
	target := string(a.Source)
	switch a.Source {
	case model.AssertBody:
		if a.Path != "" {
			target = "body." + a.Path
		}
	case model.AssertHeader:
		target = "header[" + a.Name + "]"
	}
	if a.Value == "" {
		return fmt.Sprintf("%s %s", target, a.Operator)
	}
	return fmt.Sprintf("%s %s %s", target, a.Operator, a.Value)
}

// sleep waits d, but wakes early (returning ctx.Err()) if the run is
// cancelled — a --delay must never outlive a Ctrl-C or a CI job timeout.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
