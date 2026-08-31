package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/appcore"
	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// recordingGuard captures what a write tool asked permission for, and answers
// however the test says. The SUMMARY it records is the thing a human would
// actually see, so asserting on it is asserting that the prompt is decidable.
type recordingGuard struct {
	mu      sync.Mutex
	allow   bool
	reason  string
	intents []WriteIntent
}

func (g *recordingGuard) AuthorizeWrite(_ context.Context, in WriteIntent) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.intents = append(g.intents, in)
	return g.allow, g.reason
}

func (g *recordingGuard) seen() []WriteIntent {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]WriteIntent(nil), g.intents...)
}

// connectScoped wires a server at the given scope and returns a client session.
func connectScoped(t *testing.T, dir string, opts Options) *mcp.ClientSession {
	t.Helper()
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	srv, err := NewWithOptions(engine, store, opts)
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}

// callJSON invokes a tool and decodes its structured result into v.
func callJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, v any) error {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		return &toolError{msg: msg.String()}
	}
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal into %T: %v (raw %s)", v, err, raw)
	}
	return nil
}

type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

// ---- scope gates the TOOL LIST, not just the answers --------------------

func TestScopeDecidesWhichToolsExist(t *testing.T) {
	dir := t.TempDir()
	seedWorkspace(t, dir, "https://example.test/ping")

	cases := []struct {
		scope       Scope
		guard       WriteGuard
		mustHave    []string
		mustNotHave []string
	}{
		{
			scope:       ScopeReadOnly,
			mustHave:    []string{"list_workspaces", "get_request", "search_requests", "resolve_variables", "get_last_response"},
			mustNotHave: []string{"run_request", "run_folder", "run_perf_test", "create_request", "delete_request"},
		},
		{
			scope:       ScopeRun,
			mustHave:    []string{"get_request", "run_request", "run_folder", "run_perf_test"},
			mustNotHave: []string{"create_request", "update_request", "delete_request", "create_folder", "set_environment_variable"},
		},
		{
			scope:    ScopeWrite,
			guard:    AllowAllWrites{},
			mustHave: []string{"run_request", "create_request", "update_request", "delete_request", "create_folder", "set_environment_variable"},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			cs := connectScoped(t, dir, Options{Scope: tc.scope, Writes: tc.guard})
			names := toolNames(t, cs)
			for _, n := range tc.mustHave {
				if !names[n] {
					t.Errorf("scope %s is missing tool %q", tc.scope, n)
				}
			}
			for _, n := range tc.mustNotHave {
				if names[n] {
					t.Errorf("scope %s must NOT expose tool %q", tc.scope, n)
				}
			}
		})
	}
}

// A write scope with no guard is a wiring mistake that must fail loudly at
// construction, never produce a server that writes unsupervised.
func TestWriteScopeRequiresAGuard(t *testing.T) {
	dir := t.TempDir()
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := NewWithOptions(engine, store, Options{Scope: ScopeWrite}); err == nil {
		t.Fatal("a write-scoped server was built with no WriteGuard")
	}
}

func TestParseScopeRejectsTypos(t *testing.T) {
	for _, in := range []string{"wrte", "readonly", "full", "rw"} {
		if _, err := ParseScope(in); err == nil {
			t.Errorf("ParseScope(%q) succeeded; a typo must not silently downgrade the scope", in)
		}
	}
	got, err := ParseScope("")
	if err != nil || got != ScopeRun {
		t.Fatalf("ParseScope(\"\") = %v, %v; want the run default", got, err)
	}
}

// ---- secrets never leave, through any read tool -------------------------

// This is the security core of the read surface. An MCP tool that returned a
// resolved secret would be a cleaner exfiltration path than any of the script
// channels the engine's redaction was built to close: one call, straight out
// to whatever model is driving.
func TestSecretValuesAreNeverReturned(t *testing.T) {
	dir := t.TempDir()
	const secretPlaintext = "SUPER-SECRET-TOKEN-VALUE"

	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.PutWorkspace(model.Workspace{ID: "ws-1", Name: "WS"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	env := model.Environment{
		ID: "env-1", WorkspaceID: "ws-1", Name: "Prod",
		Variables: []model.KeyValue{
			{Key: "baseUrl", Value: "https://api.example.test", Enabled: true},
			{Key: "apiKey", Enabled: true}, // value lives in the keychain
		},
		Secrets: []string{"apiKey"},
	}
	if err := store.PutEnvironment(env, map[string]string{"apiKey": secretPlaintext}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	cs := connectScoped(t, dir, Options{Scope: ScopeReadOnly})

	var resolved resolveVariablesOut
	if err := callJSON(t, cs, "resolve_variables", map[string]any{"workspaceId": "ws-1"}, &resolved); err != nil {
		t.Fatalf("resolve_variables: %v", err)
	}

	var sawSecretName, sawBaseURL bool
	for _, v := range resolved.Variables {
		if strings.Contains(v.Value, secretPlaintext) {
			t.Fatalf("resolve_variables leaked the keychain secret: %s = %q", v.Name, v.Value)
		}
		if v.Name == "apiKey" {
			sawSecretName = true
			if !v.Secret {
				t.Error("apiKey was not marked secret")
			}
			if v.Value != "[secret:apiKey]" {
				t.Errorf("apiKey value = %q, want the named placeholder", v.Value)
			}
		}
		if v.Name == "baseUrl" {
			sawBaseURL = true
			if v.Value != "https://api.example.test" {
				t.Errorf("a NON-secret variable was redacted: %q", v.Value)
			}
		}
	}
	if !sawSecretName {
		t.Error("apiKey vanished entirely — an agent must see that it exists, just not its value")
	}
	if !sawBaseURL {
		t.Error("baseUrl missing from resolved variables")
	}

	// list_environments must name the secret without valuing it.
	var envs listEnvironmentsOut
	if err := callJSON(t, cs, "list_environments", map[string]any{"workspaceId": "ws-1"}, &envs); err != nil {
		t.Fatalf("list_environments: %v", err)
	}
	raw, _ := json.Marshal(envs)
	if strings.Contains(string(raw), secretPlaintext) {
		t.Fatalf("list_environments leaked the secret: %s", raw)
	}
}

// get_request must describe the auth SCHEME without ever handing over the
// credential — an agent reasons about a 401 from the scheme, not the token.
func TestGetRequestNeverReturnsCredentials(t *testing.T) {
	dir := t.TempDir()
	const token = "BEARER-TOKEN-PLAINTEXT-XYZ"
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.PutWorkspace(model.Workspace{ID: "ws-1", Name: "WS"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: "ws-1", Name: "Secured", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://api.example.test/me",
		Auth: &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: token}},
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	cs := connectScoped(t, dir, Options{Scope: ScopeReadOnly})
	var got getRequestOut
	if err := callJSON(t, cs, "get_request", map[string]any{"requestId": "r1"}, &got); err != nil {
		t.Fatalf("get_request: %v", err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), token) {
		t.Fatalf("get_request returned the bearer token: %s", raw)
	}
	if got.AuthKind != string(model.AuthBearer) {
		t.Errorf("authKind = %q, want bearer — the scheme must still be visible", got.AuthKind)
	}
}

// ---- authoring ----------------------------------------------------------

func TestCreateUpdateDeleteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	seedWorkspace(t, dir, "https://example.test/ping")
	guard := &recordingGuard{allow: true}
	cs := connectScoped(t, dir, Options{Scope: ScopeWrite, Writes: guard})

	var created mutationOut
	if err := callJSON(t, cs, "create_request", map[string]any{
		"workspaceId": "ws-1",
		"name":        "Refund charge",
		"method":      "post",
		"url":         "https://api.example.test/v1/refunds",
		"headers":     []map[string]any{{"key": "Idempotency-Key", "value": "abc"}},
		"bodyKind":    "json",
		"body":        `{"amount":1499}`,
	}, &created); err != nil {
		t.Fatalf("create_request: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create_request returned no id")
	}

	// The prompt a human sees must name the thing, not the protocol.
	seen := guard.seen()
	if len(seen) != 1 || !strings.Contains(seen[0].Summary, "Refund charge") || !strings.Contains(seen[0].Summary, "POST") {
		t.Fatalf("write intent summary = %+v; want it to name the request and method", seen)
	}

	var got getRequestOut
	if err := callJSON(t, cs, "get_request", map[string]any{"requestId": created.ID}, &got); err != nil {
		t.Fatalf("get_request after create: %v", err)
	}
	if got.Method != "POST" {
		t.Errorf("method = %q, want the lowercase input normalised to POST", got.Method)
	}
	if len(got.Headers) != 1 || !got.Headers[0].Enabled {
		t.Errorf("headers = %+v; a created header must be enabled", got.Headers)
	}

	// An update that mentions only the URL must not wipe everything else.
	if err := callJSON(t, cs, "update_request", map[string]any{
		"requestId": created.ID,
		"url":       "https://api.example.test/v2/refunds",
	}, nil); err != nil {
		t.Fatalf("update_request: %v", err)
	}
	var after getRequestOut
	if err := callJSON(t, cs, "get_request", map[string]any{"requestId": created.ID}, &after); err != nil {
		t.Fatalf("get_request after update: %v", err)
	}
	if after.URL != "https://api.example.test/v2/refunds" {
		t.Errorf("url = %q, not updated", after.URL)
	}
	if after.Name != "Refund charge" || len(after.Headers) != 1 || after.Body == "" {
		t.Errorf("a partial update destroyed untouched fields: %+v", after)
	}

	if err := callJSON(t, cs, "delete_request", map[string]any{"requestId": created.ID}, nil); err != nil {
		t.Fatalf("delete_request: %v", err)
	}
	if err := callJSON(t, cs, "get_request", map[string]any{"requestId": created.ID}, nil); err == nil {
		t.Fatal("the request still exists after delete_request")
	}
}

// A refused guard must block the write — and the workspace must be untouched.
func TestRefusedGuardBlocksTheWrite(t *testing.T) {
	dir := t.TempDir()
	wsID, _ := seedWorkspace(t, dir, "https://example.test/ping")
	guard := &recordingGuard{allow: false, reason: "user said no"}
	cs := connectScoped(t, dir, Options{Scope: ScopeWrite, Writes: guard})

	err := callJSON(t, cs, "create_request", map[string]any{
		"workspaceId": wsID, "name": "Should not exist", "method": "GET", "url": "https://x.test",
	}, nil)
	if err == nil {
		t.Fatal("create_request succeeded despite a refusing guard")
	}
	if !strings.Contains(err.Error(), "user said no") {
		t.Errorf("error = %v; the agent should see the refusal reason", err)
	}

	var hits searchRequestsOut
	if err := callJSON(t, cs, "search_requests", map[string]any{"query": "Should not exist"}, &hits); err != nil {
		t.Fatalf("search_requests: %v", err)
	}
	if len(hits.Hits) != 0 {
		t.Fatalf("a refused create still wrote the request: %+v", hits.Hits)
	}
}

// An agent must not be able to write a SECRET variable: the value would either
// land in git-tracked YAML or move into the keychain invisibly.
func TestSetEnvVarRefusesSecretNames(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.PutWorkspace(model.Workspace{ID: "ws-1", Name: "WS"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := store.PutEnvironment(model.Environment{
		ID: "env-1", WorkspaceID: "ws-1", Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}, map[string]string{"apiKey": "REAL-SECRET"}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	guard := &recordingGuard{allow: true}
	cs := connectScoped(t, dir, Options{Scope: ScopeWrite, Writes: guard})

	err = callJSON(t, cs, "set_environment_variable", map[string]any{
		"environmentId": "env-1", "name": "apiKey", "value": "agent-supplied",
	}, nil)
	if err == nil {
		t.Fatal("an agent was allowed to overwrite a secret variable")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Errorf("error = %v; it should explain WHY and point at the app", err)
	}
	// Refused before the guard: there is nothing to approve here, and asking
	// would train the user to approve something that can never be right.
	if len(guard.seen()) != 0 {
		t.Errorf("the guard was consulted for an impossible write: %+v", guard.seen())
	}

	// A plain variable in the same environment still works.
	if err := callJSON(t, cs, "set_environment_variable", map[string]any{
		"environmentId": "env-1", "name": "baseUrl", "value": "https://api.example.test",
	}, nil); err != nil {
		t.Fatalf("setting a plain variable: %v", err)
	}
	var resolved resolveVariablesOut
	if err := callJSON(t, cs, "resolve_variables", map[string]any{"workspaceId": "ws-1"}, &resolved); err != nil {
		t.Fatalf("resolve_variables: %v", err)
	}
	found := false
	for _, v := range resolved.Variables {
		if v.Name == "baseUrl" && v.Value == "https://api.example.test" {
			found = true
		}
	}
	if !found {
		t.Errorf("the plain variable was not written: %+v", resolved.Variables)
	}
}

// ---- read tools an agent needs to debug without re-firing ---------------

func TestSearchRequestsFindsByNameAndURL(t *testing.T) {
	dir := t.TempDir()
	seedWorkspace(t, dir, "https://example.test/ping")
	cs := connectScoped(t, dir, Options{Scope: ScopeReadOnly})

	for _, q := range []string{"ping", "PING", "example.test", "get"} {
		var out searchRequestsOut
		if err := callJSON(t, cs, "search_requests", map[string]any{"query": q}, &out); err != nil {
			t.Fatalf("search_requests(%q): %v", q, err)
		}
		if len(out.Hits) == 0 {
			t.Errorf("search_requests(%q) found nothing", q)
		}
	}
	var none searchRequestsOut
	if err := callJSON(t, cs, "search_requests", map[string]any{"query": "no-such-thing"}, &none); err != nil {
		t.Fatalf("search_requests: %v", err)
	}
	if len(none.Hits) != 0 {
		t.Errorf("expected no hits, got %+v", none.Hits)
	}
}

func TestGetLastResponseReportsAbsenceRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	_, reqID := seedWorkspace(t, dir, "https://example.test/ping")
	cs := connectScoped(t, dir, Options{Scope: ScopeReadOnly})

	var out getLastResponseOut
	if err := callJSON(t, cs, "get_last_response", map[string]any{"requestId": reqID}, &out); err != nil {
		t.Fatalf("get_last_response: %v", err)
	}
	// "never run" is an ANSWER, not an error: an agent should report it, not
	// treat it as a broken tool and retry.
	if out.Found {
		t.Fatal("found=true for a request that has never run")
	}
}
