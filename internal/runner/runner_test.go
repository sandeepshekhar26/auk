package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// ---------------------------------------------------------------------------
// Verdict — the single pass/fail rule the exit code and every reporter share.
// Responses are SYNTHESIZED so the rule is tested independently of live HTTP
// (and of whether the post-response scripting producer has landed yet).
// ---------------------------------------------------------------------------

func failedAssertion() model.AssertionResult {
	return model.AssertionResult{
		Assertion: model.Assertion{Source: model.AssertStatus, Operator: model.OpEq, Value: "200", Enabled: true},
		Passed:    false,
		Actual:    "500",
	}
}

func passedAssertion() model.AssertionResult {
	return model.AssertionResult{
		Assertion: model.Assertion{Source: model.AssertStatus, Operator: model.OpEq, Value: "404", Enabled: true},
		Passed:    true,
		Actual:    "404",
	}
}

func TestVerdict(t *testing.T) {
	cases := []struct {
		name       string
		resp       model.ResponseData
		err        error
		wantPassed bool
		wantReason string
	}{
		{name: "200 with no checks is a passing smoke test", resp: model.ResponseData{Status: 200}, wantPassed: true},
		{name: "399 passes", resp: model.ResponseData{Status: 399}, wantPassed: true},
		{name: "400 with no checks fails", resp: model.ResponseData{Status: 400}, wantReason: "HTTP 400"},
		{name: "500 with no checks fails", resp: model.ResponseData{Status: 500}, wantReason: "HTTP 500"},
		{
			name:       "deliberate 404 assertion passes despite the status",
			resp:       model.ResponseData{Status: 404, AssertionResults: []model.AssertionResult{passedAssertion()}},
			wantPassed: true,
		},
		{
			name:       "failed assertion on a 200 fails",
			resp:       model.ResponseData{Status: 200, AssertionResults: []model.AssertionResult{failedAssertion()}},
			wantReason: "1/1 check(s) failed",
		},
		{
			name:       "failed script test fails",
			resp:       model.ResponseData{Status: 200, TestResults: []model.TestResult{{Name: "has id", Passed: false, Error: "expected id"}}},
			wantReason: "1/1 check(s) failed",
		},
		{
			name:       "mixed results count every failure",
			resp:       model.ResponseData{Status: 200, AssertionResults: []model.AssertionResult{failedAssertion(), passedAssertion()}, TestResults: []model.TestResult{{Name: "t", Passed: false}}},
			wantReason: "2/3 check(s) failed",
		},
		{
			name:       "script that could not run fails",
			resp:       model.ResponseData{Status: 200, ScriptError: "ReferenceError: pm is not defined"},
			wantReason: "post-response script error",
		},
		{
			name:       "passing tests pass",
			resp:       model.ResponseData{Status: 201, TestResults: []model.TestResult{{Name: "created", Passed: true}}},
			wantPassed: true,
		},
		{name: "engine error fails", resp: model.ResponseData{Status: 200}, err: errors.New("connection refused"), wantReason: "connection refused"},
		{name: "response-level error fails", resp: model.ResponseData{Error: "dial tcp: no route to host"}, wantReason: "no route to host"},
		{name: "zero-value response is not a failure by itself", resp: model.ResponseData{}, wantPassed: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			passed, reason := Verdict(tc.resp, tc.err)
			if passed != tc.wantPassed {
				t.Fatalf("Verdict() passed = %v, want %v (reason %q)", passed, tc.wantPassed, reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("Verdict() reason = %q, want it to mention %q", reason, tc.wantReason)
			}
			if tc.wantPassed && reason != "" {
				t.Fatalf("a passing verdict should carry no reason, got %q", reason)
			}
		})
	}
}

// TestVerdictMatchesResponseDataPassed locks the contract: the runner never
// re-derives a verdict that contradicts model.ResponseData.Passed().
func TestVerdictMatchesResponseDataPassed(t *testing.T) {
	responses := []model.ResponseData{
		{Status: 200},
		{Status: 200, AssertionResults: []model.AssertionResult{passedAssertion()}},
		{Status: 200, AssertionResults: []model.AssertionResult{failedAssertion()}},
		{Status: 200, TestResults: []model.TestResult{{Name: "a", Passed: true}}},
		{Status: 200, TestResults: []model.TestResult{{Name: "a", Passed: false}}},
		{Status: 200, ScriptError: "boom"},
	}
	for i, resp := range responses {
		passed, _ := Verdict(resp, nil)
		if !resp.Passed() && passed {
			t.Errorf("case %d: ResponseData.Passed()=false but Verdict()=true", i)
		}
	}
}

func TestChecksOfFlattensAssertionsAndTests(t *testing.T) {
	resp := model.ResponseData{
		Status:           200,
		AssertionResults: []model.AssertionResult{passedAssertion(), failedAssertion()},
		TestResults:      []model.TestResult{{Name: "body has id", Passed: false, Error: "expected id to exist"}},
	}
	passed, reason := Verdict(resp, nil)
	checks := checksOf(resp, passed, reason)

	if len(checks) != 3 {
		t.Fatalf("checks = %d, want 3", len(checks))
	}
	if checks[0].Kind != CheckAssertion || !checks[0].Passed {
		t.Errorf("check[0] = %+v", checks[0])
	}
	if checks[1].Passed || checks[1].Message == "" {
		t.Errorf("a failed assertion must carry a message: %+v", checks[1])
	}
	if checks[2].Kind != CheckTest || checks[2].Message != "expected id to exist" {
		t.Errorf("check[2] = %+v", checks[2])
	}
}

// TestChecksOfSyntheticCheck: a request with no assertions and no tests must
// still produce ONE countable check, or a smoke-test folder would report
// "0 tests" to CI and pass.
func TestChecksOfSyntheticCheck(t *testing.T) {
	resp := model.ResponseData{Status: 500}
	passed, reason := Verdict(resp, nil)
	checks := checksOf(resp, passed, reason)

	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1 synthetic check", len(checks))
	}
	if checks[0].Kind != CheckRequest || checks[0].Passed {
		t.Fatalf("synthetic check = %+v, want a failed request check", checks[0])
	}
	if !strings.Contains(checks[0].Message, "HTTP 500") {
		t.Errorf("synthetic check message = %q", checks[0].Message)
	}
}

func TestChecksOfScriptError(t *testing.T) {
	resp := model.ResponseData{Status: 200, ScriptError: "SyntaxError: unexpected }"}
	passed, reason := Verdict(resp, nil)
	checks := checksOf(resp, passed, reason)

	if len(checks) != 1 || checks[0].Kind != CheckScript {
		t.Fatalf("checks = %+v, want one script check", checks)
	}
	if checks[0].Passed {
		t.Error("a script that could not run must be a FAILED check")
	}
}

// ---------------------------------------------------------------------------
// Iteration / bail loop, driven by a stubbed executor (no network).
// ---------------------------------------------------------------------------

func stubPlan(ids ...string) []PlannedRequest {
	plan := make([]PlannedRequest, 0, len(ids))
	for _, id := range ids {
		plan = append(plan, PlannedRequest{
			Request:    model.RequestDef{ID: id, Name: id, Method: "GET", URL: "https://example.test/" + id},
			FolderPath: []string{"Folder"},
		})
	}
	return plan
}

// stubExec returns canned responses by request id, recording the order of
// calls and the environment id each was run against.
func stubExec(responses map[string]model.ResponseData, calls *[]string, envs *[]string) execFunc {
	return func(_ context.Context, requestID, environmentID model.ID) (model.ResponseData, error) {
		*calls = append(*calls, requestID)
		if envs != nil {
			*envs = append(*envs, environmentID)
		}
		resp, ok := responses[requestID]
		if !ok {
			return model.ResponseData{Status: 200}, nil
		}
		return resp, nil
	}
}

func runStub(t *testing.T, plan []PlannedRequest, responses map[string]model.ResponseData, opts Options) (RunSummary, []string) {
	t.Helper()
	var calls []string
	rows, err := openRows(opts)
	if err != nil {
		t.Fatalf("openRows() error = %v", err)
	}
	defer rows.Close()
	summary, err := runPlan(context.Background(), stubExec(responses, &calls, nil), nil, plan, rows, opts.EnvironmentID, opts)
	if err != nil {
		t.Fatalf("runPlan() error = %v", err)
	}
	return summary, calls
}

func TestRunPlanIterations(t *testing.T) {
	plan := stubPlan("a", "b")
	summary, calls := runStub(t, plan, nil, Options{Iterations: 3, Target: FolderTarget("f")})

	if len(calls) != 6 {
		t.Fatalf("executed %d requests (%v), want 6", len(calls), calls)
	}
	if summary.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", summary.Iterations)
	}
	if summary.Total() != 6 || summary.PassedCount() != 6 {
		t.Errorf("summary = %d total / %d passed, want 6/6", summary.Total(), summary.PassedCount())
	}
	wantIterations := []int{1, 1, 2, 2, 3, 3}
	for i, want := range wantIterations {
		if summary.Results[i].Iteration != want {
			t.Errorf("result[%d].Iteration = %d, want %d", i, summary.Results[i].Iteration, want)
		}
	}
}

func TestRunPlanBailStopsAtTheFirstFailure(t *testing.T) {
	plan := stubPlan("a", "b", "c")
	responses := map[string]model.ResponseData{
		"b": {Status: 500},
	}

	t.Run("bail", func(t *testing.T) {
		summary, calls := runStub(t, plan, responses, Options{Bail: true, Target: FolderTarget("f")})
		if strings.Join(calls, ",") != "a,b" {
			t.Fatalf("calls = %v, want [a b] — c must never run", calls)
		}
		if !summary.Bailed {
			t.Error("Bailed = false, want true")
		}
		if summary.Total() != 2 || summary.FailedCount() != 1 {
			t.Errorf("summary = %d total / %d failed, want 2/1", summary.Total(), summary.FailedCount())
		}
		if summary.Passed() {
			t.Error("a bailed run with a failure must not report passed")
		}
	})

	t.Run("no bail runs everything", func(t *testing.T) {
		summary, calls := runStub(t, plan, responses, Options{Target: FolderTarget("f")})
		if strings.Join(calls, ",") != "a,b,c" {
			t.Fatalf("calls = %v, want all three", calls)
		}
		if summary.Bailed {
			t.Error("Bailed = true without --bail")
		}
		if summary.FailedCount() != 1 {
			t.Errorf("FailedCount = %d, want 1", summary.FailedCount())
		}
	})
}

func TestRunPlanBailStopsLaterIterations(t *testing.T) {
	plan := stubPlan("a", "b")
	responses := map[string]model.ResponseData{"b": {Status: 503}}
	summary, calls := runStub(t, plan, responses, Options{Iterations: 5, Bail: true, Target: FolderTarget("f")})

	if strings.Join(calls, ",") != "a,b" {
		t.Fatalf("calls = %v, want the run to stop inside iteration 1", calls)
	}
	if summary.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", summary.Iterations)
	}
}

func TestRunSummaryTallies(t *testing.T) {
	plan := stubPlan("ok", "bad")
	responses := map[string]model.ResponseData{
		"ok":  {Status: 200, AssertionResults: []model.AssertionResult{passedAssertion(), passedAssertion()}},
		"bad": {Status: 200, AssertionResults: []model.AssertionResult{passedAssertion(), failedAssertion()}},
	}
	summary, _ := runStub(t, plan, responses, Options{Target: FolderTarget("f")})

	if summary.Total() != 2 || summary.PassedCount() != 1 || summary.FailedCount() != 1 {
		t.Errorf("requests: %d/%d/%d", summary.Total(), summary.PassedCount(), summary.FailedCount())
	}
	if summary.Checks() != 4 || summary.ChecksPassed() != 3 || summary.ChecksFailed() != 1 {
		t.Errorf("checks: %d/%d/%d, want 4/3/1", summary.Checks(), summary.ChecksPassed(), summary.ChecksFailed())
	}
	if summary.Passed() {
		t.Error("Passed() = true with a failed request")
	}
}

// TestEmptySummaryIsNotAPass: "I ran nothing" must never green-light a build.
func TestEmptySummaryIsNotAPass(t *testing.T) {
	if (RunSummary{}).Passed() {
		t.Fatal("an empty RunSummary reports Passed() = true")
	}
}

func TestRunPlanDelayIsCancellable(t *testing.T) {
	plan := stubPlan("a", "b")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rows, _ := openRows(Options{})
	opts := Options{Delay: 10 * time.Second, Target: FolderTarget("f")}
	start := time.Now()
	_, err := runPlan(ctx, stubExec(nil, new([]string), nil), nil, plan, rows, "", opts)
	if err == nil {
		t.Fatal("expected the cancelled context to surface as an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("delay ignored cancellation (%s elapsed)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// End-to-end through the REAL engine (appcore) against httptest — the proof
// that folder traversal, data iteration, and variable precedence work on the
// same code path the GUI uses.
// ---------------------------------------------------------------------------

type e2e struct {
	dir    string
	engine *core.Engine
	store  *storage.FileStore
	envID  model.ID
	smoke  model.ID // folder
	data   model.ID // folder
	broken model.ID // folder
}

func newE2E(t *testing.T, serverURL string) *e2e {
	t.Helper()
	dir := t.TempDir()
	seed, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	ws := model.ID("ws-e2e")
	if err := seed.PutWorkspace(model.Workspace{ID: ws, Name: "E2E"}); err != nil {
		t.Fatal(err)
	}
	envID := model.ID("env-e2e")
	if err := seed.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: ws, Name: "CI",
		Variables: []model.KeyValue{
			{Key: "user", Value: "fromEnv", Enabled: true},
			{Key: "plan", Value: "envPlan", Enabled: true},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	smoke, deep, data, broken := model.ID("f-smoke"), model.ID("f-deep"), model.ID("f-data"), model.ID("f-broken")
	folders := []model.Folder{
		{ID: smoke, WorkspaceID: ws, Name: "Smoke", OrderKey: "a", Variables: []model.KeyValue{{Key: "user", Value: "fromFolder", Enabled: true}}},
		{ID: deep, WorkspaceID: ws, Name: "Deep", ParentID: &smoke, OrderKey: "a"},
		{ID: data, WorkspaceID: ws, Name: "Data", OrderKey: "b"},
		{ID: broken, WorkspaceID: ws, Name: "Broken", OrderKey: "c"},
	}
	for _, f := range folders {
		if err := seed.PutFolder(f); err != nil {
			t.Fatal(err)
		}
	}

	requests := []model.RequestDef{
		{ID: "r-echo", WorkspaceID: ws, FolderID: &smoke, Name: "echo", OrderKey: "a",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/echo?user=${user}&plan=${plan}"},
		{ID: "r-health", WorkspaceID: ws, FolderID: &smoke, Name: "health", OrderKey: "b",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/health",
			Assertions: []model.Assertion{{Source: model.AssertStatus, Operator: model.OpEq, Value: "200", Enabled: true}}},
		{ID: "r-deep", WorkspaceID: ws, FolderID: &deep, Name: "deep", OrderKey: "a",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/health"},
		{ID: "r-token", WorkspaceID: ws, FolderID: &data, Name: "token", OrderKey: "a",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/echo?token=${token}"},
		{ID: "r-boom", WorkspaceID: ws, FolderID: &broken, Name: "boom", OrderKey: "a",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/boom"},
		{ID: "r-after", WorkspaceID: ws, FolderID: &broken, Name: "after", OrderKey: "b",
			Protocol: model.ProtocolHTTP, Method: "GET", URL: serverURL + "/health"},
	}
	for _, r := range requests {
		if err := seed.PutRequest(r); err != nil {
			t.Fatal(err)
		}
	}

	// Re-open through appcore: the same engine construction the GUI and the
	// MCP server use, reading the collection back off disk.
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		t.Fatalf("appcore.NewEngine: %v", err)
	}
	return &e2e{dir: dir, engine: engine, store: store, envID: envID, smoke: smoke, data: data, broken: broken}
}

func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"kaboom"}`)
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"user":%q,"plan":%q,"token":%q}`,
				r.URL.Query().Get("user"), r.URL.Query().Get("plan"), r.URL.Query().Get("token"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func body(t *testing.T, r RequestResult) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(r.Response.BodyBase64)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return string(raw)
}

func TestE2EFolderRunOrderAndFolderVariables(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)

	summary, err := RunFolder(context.Background(), fx.engine, fx.store, fx.smoke, Options{EnvironmentID: fx.envID})
	if err != nil {
		t.Fatalf("RunFolder() error = %v", err)
	}

	var order []string
	for _, r := range summary.Results {
		order = append(order, r.RequestName)
	}
	if strings.Join(order, ",") != "echo,health,deep" {
		t.Fatalf("run order = %v, want echo,health,deep (own requests before the subfolder)", order)
	}
	if !summary.Passed() {
		t.Fatalf("summary should pass: %+v", summary.Results)
	}
	// Folder variables beat the environment (pre-existing engine behavior,
	// which the runner must not disturb).
	if got := body(t, summary.Results[0]); !strings.Contains(got, `"user":"fromFolder"`) || !strings.Contains(got, `"plan":"envPlan"`) {
		t.Fatalf("echo body = %s, want user=fromFolder and plan=envPlan", got)
	}
	if got := strings.Join(summary.Results[2].FolderPath, "/"); got != "Smoke/Deep" {
		t.Errorf("FolderPath = %q, want Smoke/Deep", got)
	}
	// The engine's store must be handed back exactly as it was found.
	if _, wrapped := fx.engine.Store.(*varOverlay); wrapped {
		t.Error("the variable overlay leaked past the end of the run")
	}
}

// TestE2EDataFileOverridesEnvironmentAndFolder is the precedence contract:
// data row > folder variable > environment variable.
func TestE2EDataFileOverridesEnvironmentAndFolder(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)
	dataFile := writeFile(t, "users.csv", "user\nfromData1\nfromData2\n")

	summary, err := RunFolder(context.Background(), fx.engine, fx.store, fx.smoke, Options{
		EnvironmentID: fx.envID,
		DataFile:      dataFile,
	})
	if err != nil {
		t.Fatalf("RunFolder() error = %v", err)
	}

	if summary.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2 (one per CSV row)", summary.Iterations)
	}
	if summary.Total() != 6 {
		t.Fatalf("Total = %d, want 6 (3 requests × 2 iterations)", summary.Total())
	}
	if !summary.Passed() {
		t.Fatalf("summary should pass: %+v", summary.Results)
	}

	first, second := body(t, summary.Results[0]), body(t, summary.Results[3])
	if !strings.Contains(first, `"user":"fromData1"`) {
		t.Errorf("iteration 1 body = %s, want the data row to win over the folder variable", first)
	}
	if !strings.Contains(second, `"user":"fromData2"`) {
		t.Errorf("iteration 2 body = %s, want the second data row", second)
	}
	// A column the data file does NOT define still comes from the environment.
	if !strings.Contains(first, `"plan":"envPlan"`) {
		t.Errorf("iteration 1 body = %s, want plan to fall through to the environment", first)
	}
	if _, wrapped := fx.engine.Store.(*varOverlay); wrapped {
		t.Error("the variable overlay leaked past the end of the run")
	}
}

// TestE2EDataFileWithoutEnvironment proves the synthetic environment id: a
// data-driven run needs no --env for its columns to resolve.
func TestE2EDataFileWithoutEnvironment(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)
	dataFile := writeFile(t, "tokens.json", `[{"token":"abc123"}]`)

	summary, err := RunFolder(context.Background(), fx.engine, fx.store, fx.data, Options{DataFile: dataFile})
	if err != nil {
		t.Fatalf("RunFolder() error = %v", err)
	}
	if summary.Total() != 1 || !summary.Passed() {
		t.Fatalf("summary = %+v", summary.Results)
	}
	if got := body(t, summary.Results[0]); !strings.Contains(got, `"token":"abc123"`) {
		t.Fatalf("body = %s, want the data column to resolve with no environment selected", got)
	}
}

func TestE2EIterationsWithoutDataFile(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)

	summary, err := RunFolder(context.Background(), fx.engine, fx.store, fx.smoke, Options{
		EnvironmentID: fx.envID,
		Iterations:    3,
	})
	if err != nil {
		t.Fatalf("RunFolder() error = %v", err)
	}
	if summary.Iterations != 3 || summary.Total() != 9 {
		t.Fatalf("Iterations=%d Total=%d, want 3 and 9", summary.Iterations, summary.Total())
	}
}

func TestE2EFailingRequestFailsTheRun(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)

	summary, err := RunFolder(context.Background(), fx.engine, fx.store, fx.broken, Options{EnvironmentID: fx.envID})
	if err != nil {
		t.Fatalf("RunFolder() error = %v", err)
	}
	if summary.Total() != 2 {
		t.Fatalf("Total = %d, want 2 (a failure must not abort the batch)", summary.Total())
	}
	if summary.Passed() {
		t.Fatal("a 500 with no assertions must fail the run")
	}
	if summary.Results[0].Status != 500 || summary.Results[0].Passed {
		t.Errorf("result[0] = %+v", summary.Results[0])
	}
	if !summary.Results[1].Passed {
		t.Errorf("the request after the failure should still have run and passed: %+v", summary.Results[1])
	}

	bailed, err := RunFolder(context.Background(), fx.engine, fx.store, fx.broken, Options{EnvironmentID: fx.envID, Bail: true})
	if err != nil {
		t.Fatalf("RunFolder(bail) error = %v", err)
	}
	if bailed.Total() != 1 || !bailed.Bailed {
		t.Fatalf("bailed run = %d result(s), Bailed=%v, want 1 and true", bailed.Total(), bailed.Bailed)
	}
}

func TestE2ERunErrors(t *testing.T) {
	srv := echoServer(t)
	fx := newE2E(t, srv.URL)

	t.Run("unknown folder", func(t *testing.T) {
		if _, err := RunFolder(context.Background(), fx.engine, fx.store, "nope", Options{}); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("data file with no rows", func(t *testing.T) {
		empty := writeFile(t, "empty.csv", "user\n")
		_, err := RunFolder(context.Background(), fx.engine, fx.store, fx.smoke, Options{DataFile: empty})
		if err == nil || !strings.Contains(err.Error(), "no data rows") {
			t.Fatalf("error = %v, want a 'no data rows' failure rather than a silent pass", err)
		}
	})
	t.Run("missing data file", func(t *testing.T) {
		if _, err := RunFolder(context.Background(), fx.engine, fx.store, fx.smoke, Options{DataFile: "/nope/nope.csv"}); err == nil {
			t.Fatal("expected an error")
		}
	})
}
