package appcore

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// TestCookieCapture_EndToEnd exercises the real wiring (appcore.NewEngine's
// engine + a real templating.Engine + the real http protocol) rather than
// fakes: request 1 hits a server that sets a cookie; request 2, to a
// DIFFERENT host, references ${cookie(name)} and should see the value
// request 1's response captured. This is the integration point unit tests on
// the jar/templating packages in isolation don't cover — that
// core.Engine.RunRequest actually calls CaptureCookies after a real response.
//
// internal/protocols/http.New() also attaches its own real
// net/http/cookiejar to the shared http.Client every request reuses (see its
// doc comment) — a real, separate, pre-existing auto-resend mechanism
// (notably not scoped per AUK workspace; every workspace currently shares one
// http.Client and jar, and cookies aren't port-scoped, so two httptest
// servers on 127.0.0.1 count as the same host to it regardless of port).
// This test targets ${cookie(name)} specifically, so it reads the captured
// value into a header that jar has no opinion about (X-Session-Token, not
// Cookie) — a realistic use on its own (reusing a session value under a
// different name/header, or against a different host/API shape) and one
// that isolates what's actually new here from that pre-existing behavior.
func TestCookieCapture_EndToEnd(t *testing.T) {
	loginSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "tok-abc123"})
		w.WriteHeader(http.StatusOK)
	}))
	defer loginSrv.Close()

	whoamiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echoes back the custom header so the test can confirm the value
		// round-tripped through ${cookie(...)}.
		w.Header().Set("X-Echo-Token", r.Header.Get("X-Session-Token"))
		w.WriteHeader(http.StatusOK)
	}))
	defer whoamiSrv.Close()

	engine, store, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	wsID := uuid.NewString()
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "test"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}

	loginReq := model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, Name: "login",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: loginSrv.URL,
	}
	if err := store.PutRequest(loginReq); err != nil {
		t.Fatalf("PutRequest(login): %v", err)
	}

	whoamiReq := model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, Name: "whoami",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: whoamiSrv.URL,
		Headers: []model.KeyValue{{Key: "X-Session-Token", Value: "${cookie(session)}", Enabled: true}},
	}
	if err := store.PutRequest(whoamiReq); err != nil {
		t.Fatalf("PutRequest(whoami): %v", err)
	}

	ctx := t.Context()

	if _, err := engine.RunRequest(ctx, "sess-1", loginReq.ID, "", "cli", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest(login): %v", err)
	}

	resp, err := engine.RunRequest(ctx, "sess-2", whoamiReq.ID, "", "cli", core.NoopSink{})
	if err != nil {
		t.Fatalf("RunRequest(whoami): %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("whoami response error: %s", resp.Error)
	}

	var echoed string
	for _, h := range resp.Headers {
		if h.Key == "X-Echo-Token" {
			echoed = h.Value
		}
	}
	want := "tok-abc123"
	if echoed != want {
		t.Fatalf("got echoed X-Echo-Token %q, want %q", echoed, want)
	}
}

// TestFolderVariables_InheritAndOverride exercises the real store + engine
// (not a fake) with a 3-level chain — workspace Environment -> parent folder
// -> child folder -> request — proving both inheritance (a var only the
// Environment defines still resolves for a request two folders deep) and
// override precedence (the closer a variable's definition, the more it
// wins), using the exact appcore.NewEngine construction the GUI/CLI/MCP all
// share.
func TestFolderVariables_InheritAndOverride(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine, store, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	wsID := uuid.NewString()
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "test"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	envID := uuid.NewString()
	if err := store.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: wsID,
		Variables: []model.KeyValue{
			{Key: "envOnly", Value: "from-env", Enabled: true},
			{Key: "tier", Value: "from-env", Enabled: true},
		},
	}, nil); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	parentID := uuid.NewString()
	if err := store.PutFolder(model.Folder{
		ID: parentID, WorkspaceID: wsID, Name: "parent",
		Variables: []model.KeyValue{
			{Key: "tier", Value: "from-parent-folder", Enabled: true},
			{Key: "parentOnly", Value: "from-parent", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("PutFolder(parent): %v", err)
	}

	childID := uuid.NewString()
	if err := store.PutFolder(model.Folder{
		ID: childID, WorkspaceID: wsID, ParentID: &parentID, Name: "child",
		Variables: []model.KeyValue{
			{Key: "tier", Value: "from-child-folder", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("PutFolder(child): %v", err)
	}

	req := model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &childID, Name: "deep",
		Protocol: model.ProtocolHTTP, Method: "GET",
		URL: srv.URL + "/${envOnly}/${parentOnly}/${tier}",
	}
	if err := store.PutRequest(req); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	if _, err := engine.RunRequest(t.Context(), "sess-1", req.ID, envID, "cli", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	want := "/from-env/from-parent/from-child-folder"
	if gotPath != want {
		t.Fatalf("got path %q, want %q (envOnly inherited from environment, parentOnly inherited from the parent folder, tier overridden all the way down to the child folder)", gotPath, want)
	}
}

// TestFolderVariables_NoFolderUnaffected guards the common case: a request
// with no FolderID must resolve exactly as before this feature existed —
// folderVariables should short-circuit without even touching the store's
// folder list.
func TestFolderVariables_NoFolderUnaffected(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine, store, err := NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	wsID := uuid.NewString()
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "test"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	req := model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, FolderID: nil, Name: "no-folder",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/plain",
	}
	if err := store.PutRequest(req); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	if _, err := engine.RunRequest(t.Context(), "sess-1", req.ID, "", "cli", core.NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if gotPath != "/plain" {
		t.Fatalf("got path %q, want /plain", gotPath)
	}
}
