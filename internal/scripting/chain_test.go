package scripting_test

// Engine-level tests for the feature this whole package exists to enable:
// saving a token out of one response and using it in the NEXT request. They
// wire the real engine, the real templater and the real sobek scripter
// together — the unit tests next door prove the script semantics, these
// prove the plumbing between a vars.set and a ${token} that resolves.

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/scripting"
	"apitool/internal/templating"
)

// ---- fakes ---------------------------------------------------------------

// chainStore is a core.Store with NO environment-writing capability, so an
// engine on top of it must fall back to the session overlay.
type chainStore struct {
	requests  map[model.ID]model.RequestDef
	folders   map[model.ID]model.Folder
	envs      map[model.ID]model.Environment
	keychain  map[string]string // secret name -> value, as the OS keychain would hold it
	responses map[model.ID]model.ResponseData
	puts      int
}

func newChainStore() *chainStore {
	return &chainStore{
		requests:  map[model.ID]model.RequestDef{},
		folders:   map[model.ID]model.Folder{},
		envs:      map[model.ID]model.Environment{},
		keychain:  map[string]string{},
		responses: map[model.ID]model.ResponseData{},
	}
}

func (s *chainStore) GetRequest(id model.ID) (model.RequestDef, error) {
	r, ok := s.requests[id]
	if !ok {
		return model.RequestDef{}, errNotFound
	}
	return r, nil
}

// GetEnvironment mirrors storage.FileStore: secret VALUES are layered in
// from the keychain for the templater, which is exactly the shape the engine
// must be careful never to write back.
func (s *chainStore) GetEnvironment(id model.ID) (*model.Environment, error) {
	e, ok := s.envs[id]
	if !ok {
		return nil, errNotFound
	}
	resolved := e
	resolved.Variables = append([]model.KeyValue{}, e.Variables...)
	for i, kv := range resolved.Variables {
		if v, ok := s.keychain[kv.Key]; ok && containsString(e.Secrets, kv.Key) {
			resolved.Variables[i].Value = v
		}
	}
	return &resolved, nil
}

func (s *chainStore) SaveResponse(r model.ResponseData) error {
	s.responses[r.RequestID] = r
	return nil
}
func (s *chainStore) LastResponse(id model.ID) (model.ResponseData, bool) {
	r, ok := s.responses[id]
	return r, ok
}
func (s *chainStore) AppendHistory(model.HistoryEntry) error { return nil }
func (s *chainStore) LookupRequestByName(workspaceID model.ID, name string) (model.RequestDef, error) {
	for _, r := range s.requests {
		if r.WorkspaceID == workspaceID && r.Name == name {
			return r, nil
		}
	}
	return model.RequestDef{}, errNotFound
}
func (s *chainStore) ListFolders(workspaceID model.ID) []model.Folder {
	out := make([]model.Folder, 0, len(s.folders))
	for _, f := range s.folders {
		if workspaceID == "" || f.WorkspaceID == workspaceID {
			out = append(out, f)
		}
	}
	return out
}

// variable reads the stored (raw) value of an environment variable.
func (s *chainStore) variable(envID model.ID, name string) (string, bool) {
	for _, kv := range s.envs[envID].Variables {
		if kv.Key == name {
			return kv.Value, true
		}
	}
	return "", false
}

// persistingStore adds the same two methods storage.FileStore exposes, which
// is what lets the engine write a script's variables into a real
// environment instead of only keeping them for the session.
type persistingStore struct{ *chainStore }

func (s persistingStore) ListEnvironmentsRaw(workspaceID model.ID) []model.Environment {
	out := make([]model.Environment, 0, len(s.envs))
	for _, e := range s.envs {
		if workspaceID == "" || e.WorkspaceID == workspaceID {
			out = append(out, e)
		}
	}
	return out
}

func (s persistingStore) PutEnvironment(e model.Environment, secretValues map[string]string) error {
	s.puts++
	s.envs[e.ID] = e
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

const errNotFound = errString("not found")

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// echoProtocol answers with a canned body per request id and records the
// headers it was actually asked to send — which is where a chained variable
// has to show up for the feature to be real.
type echoProtocol struct {
	bodies map[model.ID]string
	seen   map[model.ID]core.ResolvedRequest
}

func newEchoProtocol() *echoProtocol {
	return &echoProtocol{bodies: map[model.ID]string{}, seen: map[model.ID]core.ResolvedRequest{}}
}

func (p *echoProtocol) Kind() model.ProtocolKind { return model.ProtocolHTTP }

func (p *echoProtocol) Execute(_ context.Context, _ *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	p.seen[req.ID] = resolved
	body := p.bodies[req.ID]
	return model.ResponseData{
		RequestID:  req.ID,
		Status:     200,
		StatusText: "200 OK",
		Headers:    []model.KeyValue{{Key: "Content-Type", Value: "application/json"}},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
		BodySize:   len(body),
	}, nil
}

func (p *echoProtocol) header(requestID model.ID, name string) string {
	for _, h := range p.seen[requestID].Headers {
		if strings.EqualFold(h.Key, name) {
			return h.Value
		}
	}
	return ""
}

type noopAuth struct{}

func (noopAuth) Apply(_ context.Context, _ model.AuthConfig, req core.ResolvedRequest) (core.ResolvedRequest, error) {
	return req, nil
}

func newEngine(store core.Store, proto core.Protocol) *core.Engine {
	engine := core.NewEngine(store, nil, noopAuth{}, nil)
	engine.Templater = templating.New(engine)
	engine.Scripter = scripting.New()
	engine.RegisterProtocol(proto)
	return engine
}

// ---- the canonical auth chain -------------------------------------------

const wsID = model.ID("ws1")

// loginThenCall seeds the two requests every API test suite starts with:
// POST /login, then a call that must carry the token it returned.
func loginThenCall(store *chainStore, proto *echoProtocol) {
	store.requests["login"] = model.RequestDef{
		ID: "login", WorkspaceID: wsID, Name: "Login", Protocol: model.ProtocolHTTP,
		Method: "POST", URL: "https://api.test/login",
		PostResponseScript: `
			test("login succeeded", function () { expect(response.status).toBe(200) })
			vars.set("token", response.json().token)
		`,
	}
	store.requests["me"] = model.RequestDef{
		ID: "me", WorkspaceID: wsID, Name: "Me", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/me",
		Headers: []model.KeyValue{{Key: "Authorization", Value: "Bearer ${token}", Enabled: true}},
	}
	proto.bodies["login"] = `{"token":"tok-abc123"}`
	proto.bodies["me"] = `{"id":1}`
}

func TestEngine_PostResponseVarSetReachesTheNextRequest(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	loginThenCall(store, proto)
	store.envs["env1"] = model.Environment{ID: "env1", WorkspaceID: wsID, Name: "Local"}
	engine := newEngine(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "login", "env1", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !resp.Passed() {
		t.Fatalf("login should have passed: tests=%+v scriptError=%q", resp.TestResults, resp.ScriptError)
	}

	// The write landed in the environment itself: durable, visible in the
	// environment editor, diffable in git.
	if got, ok := store.variable("env1", "token"); !ok || got != "tok-abc123" {
		t.Fatalf("expected token to be persisted into the environment, got %q (present=%v)", got, ok)
	}

	if _, err := engine.RunRequest(context.Background(), "s2", "me", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("me: %v", err)
	}
	if got := proto.header("me", "Authorization"); got != "Bearer tok-abc123" {
		t.Fatalf("the next request did not pick up the chained token: Authorization = %q", got)
	}
}

// The same chain has to work with no environment selected — that is the
// state a user is in the first time they try this, and a silent no-op there
// would be indistinguishable from the feature not existing.
func TestEngine_PostResponseVarSetChainsWithNoEnvironment(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	loginThenCall(store, proto)
	engine := newEngine(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "login", "", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := engine.RunRequest(context.Background(), "s2", "me", "", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("me: %v", err)
	}
	if got := proto.header("me", "Authorization"); got != "Bearer tok-abc123" {
		t.Fatalf("expected the session overlay to carry the token, got Authorization = %q", got)
	}
	if store.puts != 0 {
		t.Errorf("no environment was selected, so nothing should have been persisted (puts=%d)", store.puts)
	}
}

// A Store with no way to write environments still has to chain.
func TestEngine_PostResponseVarSetChainsWithoutAWritableStore(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	loginThenCall(store, proto)
	store.envs["env1"] = model.Environment{ID: "env1", WorkspaceID: wsID, Name: "Local"}
	engine := newEngine(store, proto) // note: not persistingStore

	if _, err := engine.RunRequest(context.Background(), "s1", "login", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := engine.RunRequest(context.Background(), "s2", "me", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("me: %v", err)
	}
	if got := proto.header("me", "Authorization"); got != "Bearer tok-abc123" {
		t.Fatalf("expected the session overlay to carry the token, got Authorization = %q", got)
	}
}

// A script-written variable must beat the stale literal sitting in the
// environment — the value this run just minted is the fresher one.
func TestEngine_ScriptVariableOverridesTheEnvironmentValue(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	loginThenCall(store, proto)
	store.envs["env1"] = model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "token", Value: "stale-token", Enabled: true}},
	}
	engine := newEngine(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "login", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := engine.RunRequest(context.Background(), "s2", "me", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("me: %v", err)
	}
	if got := proto.header("me", "Authorization"); got != "Bearer tok-abc123" {
		t.Fatalf("expected the fresh token to win over the stored one, got %q", got)
	}
}

func TestEngine_PreRequestVarWriteReachesTheNextRequest(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["first"] = "{}"
	proto.bodies["second"] = "{}"
	store.requests["first"] = model.RequestDef{
		ID: "first", WorkspaceID: wsID, Name: "First", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/first",
		PreRequestScript: `vars.set("nonce", "n-42")`,
	}
	store.requests["second"] = model.RequestDef{
		ID: "second", WorkspaceID: wsID, Name: "Second", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/second",
		Headers: []model.KeyValue{{Key: "X-Nonce", Value: "${nonce}", Enabled: true}},
	}
	store.envs["env1"] = model.Environment{ID: "env1", WorkspaceID: wsID, Name: "Local"}
	engine := newEngine(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "first", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := engine.RunRequest(context.Background(), "s2", "second", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := proto.header("second", "X-Nonce"); got != "n-42" {
		t.Fatalf("expected the pre-request write to chain, got X-Nonce = %q", got)
	}
}

func TestEngine_VarUnsetRemovesTheVariableFromTheEnvironment(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = "{}"
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `vars.unset("token")`,
	}
	store.envs["env1"] = model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "token", Value: "old", Enabled: true}, {Key: "base", Value: "https://api.test", Enabled: true}},
	}
	engine := newEngine(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("r1: %v", err)
	}
	if _, ok := store.variable("env1", "token"); ok {
		t.Error("expected vars.unset to remove the variable from the environment")
	}
	if got, ok := store.variable("env1", "base"); !ok || got != "https://api.test" {
		t.Errorf("expected unrelated variables to survive, got %q (present=%v)", got, ok)
	}
}

// A folder-scoped variable outranks the environment, so a script write that
// only landed in the environment would be silently shadowed by the folder's
// literal — the chain would break with no error anywhere.
func TestEngine_ScriptVariableBeatsAFolderVariableOfTheSameName(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	loginThenCall(store, proto)

	folderID := model.ID("f1")
	store.folders[folderID] = model.Folder{
		ID: folderID, WorkspaceID: wsID, Name: "Suite",
		Variables: []model.KeyValue{{Key: "token", Value: "folder-literal", Enabled: true}},
	}
	login := store.requests["login"]
	login.FolderID = &folderID
	store.requests["login"] = login
	me := store.requests["me"]
	me.FolderID = &folderID
	store.requests["me"] = me

	store.envs["env1"] = model.Environment{ID: "env1", WorkspaceID: wsID, Name: "Local"}
	engine := newEngine(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "login", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := engine.RunRequest(context.Background(), "s2", "me", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("me: %v", err)
	}
	if got := proto.header("me", "Authorization"); got != "Bearer tok-abc123" {
		t.Fatalf("the folder variable shadowed the script's write: Authorization = %q", got)
	}
}

// ---- the secrets guard, at the engine layer ------------------------------

// clobberingScripter bypasses the JS-level guard entirely to prove the
// engine enforces the same rule on its own: whatever a Scripter hands back,
// a keychain-backed secret is never overwritten.
type clobberingScripter struct{}

func (clobberingScripter) RunPreRequest(_ context.Context, _ string, resolved core.ResolvedRequest) (core.ResolvedRequest, error) {
	return resolved, nil
}

func (clobberingScripter) RunPostResponse(_ context.Context, _ string, _ core.PostResponseInput) (core.PostResponseOutput, error) {
	return core.PostResponseOutput{
		VarWrites: map[string]string{"apiKey": "clobbered", "token": "fine"},
		VarUnsets: []string{"apiKey"},
	}, nil
}

func TestEngine_ScriptCannotClobberASecretBackedVariable(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = "{}"
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1", PostResponseScript: `// handled by the fake`,
	}
	store.envs["env1"] = model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = "from-keychain"

	engine := newEngine(persistingStore{store}, proto)
	engine.Scripter = clobberingScripter{}

	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("r1: %v", err)
	}

	got, present := store.variable("env1", "apiKey")
	if !present {
		t.Fatal("the secret's entry was removed from the environment")
	}
	if got != "" {
		t.Fatalf("a secret's value must never be written into the environment file, got %q", got)
	}
	if !containsString(store.envs["env1"].Secrets, "apiKey") {
		t.Fatal("the secret must still be listed as a secret")
	}
	// The non-secret write in the same batch still goes through.
	if v, ok := store.variable("env1", "token"); !ok || v != "fine" {
		t.Fatalf("expected the non-secret write to be applied, got %q (present=%v)", v, ok)
	}
}

// Writing a variable must not smuggle a RESOLVED secret value into the
// stored environment — that file is the one a user commits or exports.
func TestEngine_PersistingAVariableDoesNotLeakResolvedSecrets(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = "{}"
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `vars.set("token", "fresh")`,
	}
	store.envs["env1"] = model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	store.keychain["apiKey"] = "from-keychain"

	engine := newEngine(persistingStore{store}, proto)
	if _, err := engine.RunRequest(context.Background(), "s1", "r1", "env1", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("r1: %v", err)
	}

	if got, _ := store.variable("env1", "apiKey"); got != "" {
		t.Fatalf("the keychain value leaked into the stored environment: %q", got)
	}
}

// ---- verdicts ------------------------------------------------------------

func TestEngine_FailingScriptTestMakesTheResponseNotPassed(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = `{"status":"error"}`
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `
			test("is ok", function () { expect(response.json().status).toBe("ok") })
			test("has a status", function () { expect(response.json()).toHaveProperty("status") })
		`,
	}
	engine := newEngine(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", "", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("r1: %v", err)
	}
	if len(resp.TestResults) != 2 {
		t.Fatalf("expected both test results on the response, got %+v", resp.TestResults)
	}
	if resp.TestResults[0].Passed || resp.TestResults[0].Error != `expected "error" to be "ok"` {
		t.Fatalf("expected a readable failure on the first test, got %+v", resp.TestResults[0])
	}
	if !resp.TestResults[1].Passed {
		t.Fatalf("expected the second test to pass, got %+v", resp.TestResults[1])
	}
	if resp.Passed() {
		t.Fatal("a failing script test must make ResponseData.Passed() false")
	}
	if resp.ScriptError != "" {
		t.Fatalf("a merely failing test is not a script error, got %q", resp.ScriptError)
	}
}

func TestEngine_ScriptErrorIsRecordedAndFailsTheRun(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = "{}"
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `test("ok", function () { expect(1).toBe(1) }); throw new Error("boom")`,
	}
	engine := newEngine(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", "", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("a failing script must not fail the whole send: %v", err)
	}
	if resp.ScriptError == "" || !strings.Contains(resp.ScriptError, "boom") {
		t.Fatalf("expected the thrown message in ScriptError, got %q", resp.ScriptError)
	}
	if resp.Passed() {
		t.Fatal("a script error must make ResponseData.Passed() false")
	}
	if len(resp.TestResults) != 1 || !resp.TestResults[0].Passed {
		t.Fatalf("tests that ran before the throw should still be reported, got %+v", resp.TestResults)
	}
}

func TestEngine_ScriptSyntaxErrorIsRecordedNotSilentlyPassed(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = "{}"
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `this is not valid js {{{`,
	}
	engine := newEngine(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", "", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("r1: %v", err)
	}
	if resp.ScriptError == "" {
		t.Fatal("a script that cannot compile must set ScriptError")
	}
	if resp.Passed() {
		t.Fatal("a script that cannot compile must not report a passing run")
	}
}

// The script reports on the response; it never edits the one that is stored.
func TestEngine_ScriptCannotChangeTheStoredResponse(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	proto.bodies["r1"] = `{"a":1}`
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/r1",
		PostResponseScript: `response.status = 500; response.body = "rewritten"`,
	}
	engine := newEngine(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", "", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("r1: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want the real 200", resp.Status)
	}
	if got := string(mustDecode(t, resp.BodyBase64)); got != `{"a":1}` {
		t.Fatalf("body = %q, want the real body", got)
	}
	if stored := store.responses["r1"]; stored.Status != 200 {
		t.Fatalf("stored response was modified: %+v", stored)
	}
}

func mustDecode(t *testing.T, b64 string) []byte {
	t.Helper()
	out, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

// ---- a pre-request script cannot launder a resolved secret ---------------
//
// The engine redacts secret VALUES out of the snapshot a pre-request script
// reads (core.redactResolved) because by that point templating has expanded
// ${apiKey} into the URL/headers/body and internal/auth has built
// `Authorization: Bearer <secret>`. The tests in internal/core prove the
// engine's own logic against a fake scripter; this one runs the REAL sobek
// runtime, the REAL templater and the REAL auth applier end to end, which is
// the only way to show that what a user's JS actually sees is redacted.

const launderableSecret = "SUPER_SECRET_VALUE"

// newEngineWithAuth is newEngine plus the real auth applier, so a
// secret-backed ${apiKey} really does become an Authorization header before
// the pre-request script runs.
func newEngineWithAuth(store core.Store, proto core.Protocol) *core.Engine {
	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.Scripter = scripting.New()
	engine.RegisterProtocol(proto)
	return engine
}

func secretLeakStore() *chainStore {
	store := newChainStore()
	store.envs["prod"] = model.Environment{
		ID: "prod", WorkspaceID: wsID, Name: "Prod",
		Variables: []model.KeyValue{
			{Key: "apiKey", Enabled: true}, // value lives in the keychain
			{Key: "host", Value: "api.test", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}
	store.keychain["apiKey"] = launderableSecret
	store.requests["send"] = model.RequestDef{
		ID: "send", WorkspaceID: wsID, Name: "Send", Protocol: model.ProtocolHTTP,
		Method: "POST", URL: "https://${host}/v1?key=${apiKey}",
		Headers: []model.KeyValue{{Key: "X-Api-Key", Value: "${apiKey}", Enabled: true}},
		Body:    &model.RequestBody{Kind: model.BodyJSON, Text: `{"key":"${apiKey}"}`},
		Auth:    &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: "${apiKey}"}},
		PreRequestScript: `
			vars.set("fromUrl", ctx.request.url)
			vars.set("fromBody", ctx.request.body)
			vars.set("fromHeader", ctx.request.headers["X-Api-Key"] || "")
			vars.set("fromAuth", ctx.request.headers["Authorization"] || "")
			vars.set("fromVarsGet", vars.get("apiKey") || "")
			ctx.setHeader("X-Script", "ran")
		`,
	}
	return store
}

// TestEngine_PreRequestScriptCannotLaunderASecretOutOfTheRequest is the
// [CRITICAL] finding end to end: the script copies every window it has onto
// the request into plain variables, and NONE of them may reach the plaintext,
// git-tracked environment.
func TestEngine_PreRequestScriptCannotLaunderASecretOutOfTheRequest(t *testing.T) {
	store := secretLeakStore()
	proto := newEchoProtocol()
	proto.bodies["send"] = `{}`
	engine := newEngineWithAuth(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "send", "prod", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	for _, kv := range store.envs["prod"].Variables {
		if strings.Contains(kv.Value, launderableSecret) {
			t.Errorf("the environment YAML would carry the keychain secret under %q: %q", kv.Key, kv.Value)
		}
	}
	// Named placeholders, so a script author can see what happened.
	for _, name := range []string{"fromHeader", "fromAuth", "fromUrl", "fromBody"} {
		got, ok := store.variable("prod", name)
		if !ok {
			t.Errorf("expected %q to have been written (redacted)", name)
			continue
		}
		if !strings.Contains(got, "[secret:apiKey]") {
			t.Errorf("%s = %q, expected the [secret:apiKey] placeholder", name, got)
		}
	}
	if got, _ := store.variable("prod", "fromVarsGet"); got != "" {
		t.Errorf("vars.get on a secret must stay undefined, got %q", got)
	}
}

// TestEngine_RedactionDoesNotChangeWhatGoesOnTheWire is the guarantee that
// makes the fix shippable: the server still gets the real credential in every
// field, and ctx.setHeader still works.
func TestEngine_RedactionDoesNotChangeWhatGoesOnTheWire(t *testing.T) {
	store := secretLeakStore()
	proto := newEchoProtocol()
	proto.bodies["send"] = `{}`
	engine := newEngineWithAuth(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "send", "prod", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	sent := proto.seen["send"]
	if want := "https://api.test/v1?key=" + launderableSecret; sent.URL != want {
		t.Errorf("URL on the wire = %q, want %q", sent.URL, want)
	}
	if sent.Body == nil || sent.Body.Text != `{"key":"`+launderableSecret+`"}` {
		t.Errorf("body on the wire = %+v, want the real secret", sent.Body)
	}
	if got := proto.header("send", "X-Api-Key"); got != launderableSecret {
		t.Errorf("X-Api-Key on the wire = %q, want the real secret", got)
	}
	if got := proto.header("send", "Authorization"); got != "Bearer "+launderableSecret {
		t.Errorf("Authorization on the wire = %q, want the real secret", got)
	}
	if got := proto.header("send", "X-Script"); got != "ran" {
		t.Errorf("ctx.setHeader must keep working, X-Script = %q", got)
	}
}

// TestEngine_StaleInactiveAuthBlockDoesNotBreakTheSend is the [MAJOR]
// ResolveAuth finding at the engine, where it actually hurt: the Auth tab
// preserves the sub-objects of kinds you switched away from, so a request that
// once used Basic keeps a `${legacyPassword}` block forever. Deleting that
// variable used to turn every send of that request into
// "resolve auth templates: unresolved variable".
func TestEngine_StaleInactiveAuthBlockDoesNotBreakTheSend(t *testing.T) {
	store := newChainStore()
	proto := newEchoProtocol()
	store.envs["env1"] = model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "token", Value: "tok-live", Enabled: true}},
		// legacyPassword has been deleted.
	}
	store.requests["call"] = model.RequestDef{
		ID: "call", WorkspaceID: wsID, Name: "Call", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.test/me",
		Auth: &model.AuthConfig{
			Kind:   model.AuthBearer,
			Bearer: &model.BearerAuth{Token: "${token}"},
			Basic:  &model.BasicAuth{Username: "old", Password: "${legacyPassword}"},
		},
	}
	proto.bodies["call"] = `{"ok":true}`
	engine := newEngineWithAuth(persistingStore{store}, proto)

	resp, err := engine.RunRequest(context.Background(), "s1", "call", "env1", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("a stale INACTIVE auth block must not fail the send: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("expected the send to go through, got %d", resp.Status)
	}
	if got := proto.header("call", "Authorization"); got != "Bearer tok-live" {
		t.Errorf("the ACTIVE Bearer must still resolve, Authorization = %q", got)
	}
}

// TestEngine_SetHeaderCanStillOverrideASecretBackedHeader is the same guard
// with the real sobek runtime: overriding a header that already carried a
// resolved secret must reach the wire as the script's value. (The engine
// compares the script's output against an independent baseline precisely
// because upsertHeader writes through the slice it was handed.)
func TestEngine_SetHeaderCanStillOverrideASecretBackedHeader(t *testing.T) {
	store := secretLeakStore()
	req := store.requests["send"]
	req.PreRequestScript = `ctx.setHeader("X-Api-Key", "overridden-by-script")`
	store.requests["send"] = req

	proto := newEchoProtocol()
	proto.bodies["send"] = `{}`
	engine := newEngineWithAuth(persistingStore{store}, proto)

	if _, err := engine.RunRequest(context.Background(), "s1", "send", "prod", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if got := proto.header("send", "X-Api-Key"); got != "overridden-by-script" {
		t.Errorf("ctx.setHeader must override a secret-backed header, got %q", got)
	}
	// The headers it did NOT touch still carry the real credential.
	if got := proto.header("send", "Authorization"); got != "Bearer "+launderableSecret {
		t.Errorf("an untouched header lost its real value: Authorization = %q", got)
	}
}
