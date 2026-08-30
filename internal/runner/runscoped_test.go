package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// ---------------------------------------------------------------------------
// End-to-end proofs of the data-driven run-scoped variable redesign (finding
// 3) and the empty-script verdict (finding 4), through the SAME engine the GUI
// uses (appcore) against a live httptest server and the real sobek scripter.
// ---------------------------------------------------------------------------

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// jsonEchoServer reflects query params back as a JSON object; requests under
// /a additionally return a token (for the response()-chaining test). When
// postCount is non-nil every POST is tallied, so a re-fired chain target is
// visible as an extra POST.
func jsonEchoServer(t *testing.T, postCount *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if postCount != nil && r.Method == http.MethodPost {
			atomic.AddInt32(postCount, 1)
		}
		out := map[string]string{}
		for k, vs := range r.URL.Query() {
			if len(vs) > 0 {
				out[k] = vs[0]
			}
		}
		if strings.HasPrefix(r.URL.Path, "/a") {
			out["token"] = "AAA"
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(out)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// reflectProtocol is an in-process stand-in for the HTTP protocol: it echoes
// the RESOLVED url's query params back as a JSON body, with no net/http at all.
// The race test uses it so the only shared state under -race is the engine, the
// store, the scripter, and the run-scoped layer — the surfaces finding 3 is
// about — rather than the HTTP client's own transport internals.
type reflectProtocol struct{}

func (reflectProtocol) Kind() model.ProtocolKind { return model.ProtocolHTTP }

func (reflectProtocol) Execute(_ context.Context, _ *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	out := map[string]string{}
	if u, err := url.Parse(resolved.URL); err == nil {
		for k, vs := range u.Query() {
			if len(vs) > 0 {
				out[k] = vs[0]
			}
		}
	}
	b, _ := json.Marshal(out)
	return model.ResponseData{
		RequestID:  req.ID,
		Status:     200,
		StatusText: "200 OK",
		Headers:    []model.KeyValue{{Key: "Content-Type", Value: "application/json"}},
		BodyBase64: base64.StdEncoding.EncodeToString(b),
	}, nil
}

func jsonField(resp model.ResponseData, name string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	v, ok := m[name].(string)
	return v, ok
}

func failedChecks(s RunSummary) string {
	var b strings.Builder
	for _, r := range s.Results {
		if r.Passed {
			continue
		}
		fmt.Fprintf(&b, "  [iter %d] %s: %s\n", r.Iteration, r.RequestName, r.Reason)
		for _, c := range r.Checks {
			if !c.Passed {
				fmt.Fprintf(&b, "    - %s: %s\n", c.Name, c.Message)
			}
		}
	}
	return b.String()
}

func newScriptEngine(t *testing.T) (*core.Engine, *storage.FileStore) {
	t.Helper()
	dir := t.TempDir()
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		t.Fatalf("appcore.NewEngine: %v", err)
	}
	return engine, store
}

// TestDataRunResetAndIntraIterationChaining proves BOTH per-iteration reset and
// intra-iteration chaining at once: request A asserts ${token} is empty on entry
// (so nothing leaked from the previous iteration) and mints a token; request B
// asserts it sees exactly that token (chaining within the iteration).
func TestDataRunResetAndIntraIterationChaining(t *testing.T) {
	srv := jsonEchoServer(t, nil)
	engine, store := newScriptEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	env := model.ID("e")
	must(t, store.PutEnvironment(model.Environment{
		ID: env, WorkspaceID: ws, Name: "E",
		Variables: []model.KeyValue{{Key: "token", Value: "", Enabled: true}},
	}, nil))
	folder := model.ID("chain")
	must(t, store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "Chain", OrderKey: "a"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "A", WorkspaceID: ws, FolderID: &folder, Name: "A", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo?seen=${token}",
		PostResponseScript: `
			test('token empty at entry', function () { expect(response.json().seen).toBe('') })
			vars.set('token', 'tok-' + vars.get('row'))
		`,
	}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "B", WorkspaceID: ws, FolderID: &folder, Name: "B", OrderKey: "b",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo?seen=${token}",
		PostResponseScript: `
			test('token chained within iteration', function () { expect(response.json().seen).toBe('tok-' + vars.get('row')) })
		`,
	}))
	csv := writeFile(t, "rows.csv", "row\nr1\nr2\n")

	summary, err := RunFolder(context.Background(), engine, store, folder, Options{EnvironmentID: env, DataFile: csv})
	if err != nil {
		t.Fatalf("RunFolder: %v", err)
	}
	if summary.Iterations != 2 || summary.Total() != 4 {
		t.Fatalf("iterations=%d total=%d, want 2 and 4", summary.Iterations, summary.Total())
	}
	if !summary.Passed() {
		t.Fatalf("run must pass — a failure here means per-iteration reset OR intra-iteration chaining broke:\n%s", failedChecks(summary))
	}
}

// TestDataRunResponseChainingDoesNotRefire is the reviewer's exact repro: a
// folder [POST /a, GET /b uses response('A')] under --data must resolve the
// CACHED response of A, not re-POST /a — which it does now that the runner no
// longer wraps the store away from its LastResponse method.
func TestDataRunResponseChainingDoesNotRefire(t *testing.T) {
	var posts int32
	srv := jsonEchoServer(t, &posts)
	engine, store := newScriptEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	folder := model.ID("chain2")
	must(t, store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "Chain2", OrderKey: "a"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "A", WorkspaceID: ws, FolderID: &folder, Name: "A", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "POST", URL: srv.URL + "/a",
	}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "B", WorkspaceID: ws, FolderID: &folder, Name: "B", OrderKey: "b",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/b?t=${response('A').body.token}",
		PostResponseScript: `test('chained token', function () { expect(response.json().t).toBe('AAA') })`,
	}))
	csv := writeFile(t, "one.csv", "n\n1\n")

	summary, err := RunFolder(context.Background(), engine, store, folder, Options{DataFile: csv})
	if err != nil {
		t.Fatalf("RunFolder: %v", err)
	}
	if !summary.Passed() {
		t.Fatalf("B must chain A's cached token under --data:\n%s", failedChecks(summary))
	}
	if n := atomic.LoadInt32(&posts); n != 1 {
		t.Fatalf("POST /a fired %d times; response('A') under --data must hit the CACHED response, not re-send the target", n)
	}
}

// TestDataRunVarSetIsNotPersisted proves the persistence semantics: in a data
// run a vars.set is run-scoped and must never mutate the stored environment.
func TestDataRunVarSetIsNotPersisted(t *testing.T) {
	srv := jsonEchoServer(t, nil)
	engine, store := newScriptEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	env := model.ID("e")
	must(t, store.PutEnvironment(model.Environment{ID: env, WorkspaceID: ws, Name: "E"}, nil))
	folder := model.ID("f")
	must(t, store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "F", OrderKey: "a"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "W1", WorkspaceID: ws, FolderID: &folder, Name: "W1", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo",
		PostResponseScript: `
			test('ok', function () { expect(response.status).toBe(200) })
			vars.set('token', 'fresh-' + vars.get('row'))
		`,
	}))
	csv := writeFile(t, "rows.csv", "row\nr1\n")

	summary, err := RunFolder(context.Background(), engine, store, folder, Options{EnvironmentID: env, DataFile: csv})
	if err != nil {
		t.Fatalf("RunFolder: %v", err)
	}
	if !summary.Passed() {
		t.Fatalf("run should pass:\n%s", failedChecks(summary))
	}

	got, err := store.GetEnvironment(env)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	for _, kv := range got.Variables {
		if kv.Key == "token" {
			t.Fatalf("a data run must not persist vars.set to the environment; found token=%q", kv.Value)
		}
	}
}

// TestEmptyScriptSuiteFailsTheRun is finding 4: a post-response script that
// registered no tests is a bug, not a passing smoke test.
func TestEmptyScriptSuiteFailsTheRun(t *testing.T) {
	scriptReq := func(id string) model.RequestDef {
		return model.RequestDef{
			ID: id, Name: id, Method: "GET", URL: "https://x/" + id,
			PostResponseScript: `/* a script that registered nothing */`,
		}
	}

	t.Run("script with zero tests fails", func(t *testing.T) {
		plan := []PlannedRequest{{Request: scriptReq("r1"), FolderPath: []string{"F"}}}
		responses := map[string]model.ResponseData{"r1": {Status: 200}}
		summary, _ := runStub(t, plan, responses, Options{Target: FolderTarget("f")})
		if summary.Passed() {
			t.Fatal("a post-response script that registered no tests must fail the run")
		}
		if !strings.Contains(summary.Results[0].Reason, "registered no tests") {
			t.Fatalf("reason = %q, want it to mention the empty script suite", summary.Results[0].Reason)
		}
	})

	t.Run("script with a passing test passes", func(t *testing.T) {
		plan := []PlannedRequest{{Request: scriptReq("r1"), FolderPath: []string{"F"}}}
		responses := map[string]model.ResponseData{"r1": {Status: 200, TestResults: []model.TestResult{{Name: "t", Passed: true}}}}
		summary, _ := runStub(t, plan, responses, Options{Target: FolderTarget("f")})
		if !summary.Passed() {
			t.Fatalf("a script with a passing test should pass: %+v", summary.Results[0])
		}
	})

	t.Run("script with no tests but passing assertions passes", func(t *testing.T) {
		plan := []PlannedRequest{{Request: scriptReq("r1"), FolderPath: []string{"F"}}}
		responses := map[string]model.ResponseData{"r1": {Status: 200, AssertionResults: []model.AssertionResult{passedAssertion()}}}
		summary, _ := runStub(t, plan, responses, Options{Target: FolderTarget("f")})
		if !summary.Passed() {
			t.Fatalf("declarative assertions are the verdict; a token-extractor script alongside must not fail it: %+v", summary.Results[0])
		}
	})

	t.Run("bare request with no script still smoke-tests", func(t *testing.T) {
		plan := stubPlan("r1")
		responses := map[string]model.ResponseData{"r1": {Status: 200}}
		summary, _ := runStub(t, plan, responses, Options{Target: FolderTarget("f")})
		if !summary.Passed() {
			t.Fatal("a bare 200 must still pass as a smoke test")
		}
	})
}

// TestDataRunNoDataRaceWithConcurrentSends is finding 3's concurrency guarantee:
// a data-driven run and concurrent GUI sends on the SAME engine must not race
// (run under -race) AND the GUI sends must never observe an iteration's
// variables — their context carries no run-scoped layer, so ${col} resolves to
// the environment value, never a data row.
func TestDataRunNoDataRaceWithConcurrentSends(t *testing.T) {
	// An in-process protocol keeps the shared surfaces under -race to exactly
	// the engine, store, scripter and run-scoped layer — the code finding 3
	// touched — instead of the HTTP transport's own internals.
	engine, store := newFakeProtoEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	env := model.ID("e")
	must(t, store.PutEnvironment(model.Environment{
		ID: env, WorkspaceID: ws, Name: "E",
		Variables: []model.KeyValue{{Key: "col", Value: "ENVVAL", Enabled: true}},
	}, nil))
	folder := model.ID("loop")
	must(t, store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "Loop", OrderKey: "a"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "worker", WorkspaceID: ws, FolderID: &folder, Name: "worker", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "http://local/echo?col=${col}",
		PostResponseScript: `test('ok', function(){ expect(response.status).toBe(200) }); vars.set('scratch', vars.get('col'))`,
	}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "gui", WorkspaceID: ws, Name: "gui",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "http://local/echo?col=${col}",
	}))

	var rows strings.Builder
	rows.WriteString("col\n")
	for i := 0; i < 25; i++ {
		rows.WriteString("ROWVAL\n")
	}
	csv := writeFile(t, "rows.csv", rows.String())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := RunFolder(context.Background(), engine, store, folder, Options{EnvironmentID: env, DataFile: csv}); err != nil {
			t.Errorf("data run: %v", err)
		}
	}()

	var seen sync.Map
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				resp, err := engine.RunRequest(context.Background(), uuid.NewString(), "gui", env, "gui", core.NoopSink{})
				if err != nil {
					t.Errorf("gui send: %v", err)
					return
				}
				if col, ok := jsonField(resp, "col"); ok {
					seen.Store(col, true)
				}
			}
		}()
	}
	wg.Wait()

	seen.Range(func(k, _ any) bool {
		if k.(string) != "ENVVAL" {
			t.Errorf("a concurrent GUI send observed a run-scoped value for ${col}: %q (iteration vars must not leak to interactive sends)", k)
		}
		return true
	})
}

// TestNonDataRunAlsoDoesNotPersistVars is the CI-safety guard: a run WITHOUT
// --data (the ordinary `auk run-folder smoke --env prod`) must not rewrite the
// user's git-tracked environment YAML either. Before the run-scoped layer was
// made unconditional, a login request's post-response vars.set("token", ...)
// persisted a live bearer token into prod.yaml on every CI run.
func TestNonDataRunAlsoDoesNotPersistVars(t *testing.T) {
	srv := jsonEchoServer(t, nil)
	engine, store := newScriptEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	env := model.ID("e")
	must(t, store.PutEnvironment(model.Environment{ID: env, WorkspaceID: ws, Name: "E"}, nil))
	folder := model.ID("f")
	must(t, store.PutFolder(model.Folder{ID: folder, WorkspaceID: ws, Name: "F", OrderKey: "a"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "W1", WorkspaceID: ws, FolderID: &folder, Name: "W1", OrderKey: "a",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo",
		PostResponseScript: `
			test('ok', function () { expect(response.status).toBe(200) })
			vars.set('token', 'live-bearer-token')
		`,
	}))

	// No DataFile: the ordinary CI invocation.
	summary, err := RunFolder(context.Background(), engine, store, folder, Options{EnvironmentID: env, Origin: "cli"})
	if err != nil {
		t.Fatalf("RunFolder: %v", err)
	}
	if !summary.Passed() {
		t.Fatalf("run should pass:\n%s", failedChecks(summary))
	}

	got, err := store.GetEnvironment(env)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	for _, kv := range got.Variables {
		if kv.Key == "token" {
			t.Fatalf("a CI run must not persist vars.set into the stored environment; found token=%q", kv.Value)
		}
	}
}
