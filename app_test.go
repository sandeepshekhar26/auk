package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"apitool/internal/appcore"
	"apitool/internal/core/model"
	"apitool/internal/exporter"
	"apitool/internal/storage"
)

// fakeSecretStore is an in-memory storage.SecretStore. Constructing an App
// via storage.NewFileStore's default options would hit the REAL OS keychain
// (storage.SecretStore's own doc comment: "must never run inside a
// non-interactive test") — this test needs a real PutEnvironment(name,
// secretValues) round trip to prove exportWorkspaceJSON excludes the
// resolved value, so it needs a real SecretStore, just not a real keychain.
type fakeSecretStore struct{ values map[string]string }

func newFakeSecretStore() *fakeSecretStore                    { return &fakeSecretStore{values: map[string]string{}} }
func (f *fakeSecretStore) key(service, account string) string { return service + "/" + account }
func (f *fakeSecretStore) Get(service, account string) (string, error) {
	return f.values[f.key(service, account)], nil
}
func (f *fakeSecretStore) Set(service, account, value string) error {
	f.values[f.key(service, account)] = value
	return nil
}
func (f *fakeSecretStore) Delete(service, account string) error {
	delete(f.values, f.key(service, account))
	return nil
}

// TestApp_ExportWorkspaceJSON exercises the actual App-level assembly step
// ExportWorkspace uses (ExportWorkspace itself isn't called directly here —
// it also opens a native save dialog, which must never run in a
// non-interactive test) with a REAL FileStore, proving folders/requests
// round-trip and — the critical property — a real environment secret,
// planted via the exact same PutEnvironment(env, secretValues) path the GUI
// uses, never appears in the exported JSON.
func TestApp_ExportWorkspaceJSON(t *testing.T) {
	store, err := storage.NewFileStore(t.TempDir(), storage.WithSecretStore(newFakeSecretStore()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	a := &App{store: store}

	const wsID = "ws1"
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := store.PutFolder(model.Folder{ID: "f1", WorkspaceID: wsID, Name: "Auth"}); err != nil {
		t.Fatalf("PutFolder: %v", err)
	}
	if err := store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "Login", Method: "POST", URL: "https://api.example.com/login",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	const realSecretValue = "sk-live-do-not-leak-9f8e7d6c5b4a"
	env := model.Environment{
		ID: "e1", WorkspaceID: wsID, Name: "Prod",
		Variables: []model.KeyValue{
			{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
			{Key: "apiKey", Value: "", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}
	if err := store.PutEnvironment(env, map[string]string{"apiKey": realSecretValue}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	out, err := a.exportWorkspaceJSON(wsID, "Demo")
	if err != nil {
		t.Fatalf("exportWorkspaceJSON: %v", err)
	}

	if strings.Contains(out, realSecretValue) {
		t.Fatalf("exported JSON leaked the real secret value:\n%s", out)
	}

	var doc exporter.ExportedWorkspace
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if doc.WorkspaceName != "Demo" {
		t.Fatalf("got workspace name %q, want %q", doc.WorkspaceName, "Demo")
	}
	if len(doc.Folders) != 1 || doc.Folders[0].Name != "Auth" {
		t.Fatalf("folders not exported correctly: %+v", doc.Folders)
	}
	if len(doc.Requests) != 1 || doc.Requests[0].Name != "Login" {
		t.Fatalf("requests not exported correctly: %+v", doc.Requests)
	}
	if len(doc.Environments) != 1 {
		t.Fatalf("got %d environments, want 1", len(doc.Environments))
	}
	if got := doc.Environments[0].Variables[1].Value; got != "" {
		t.Fatalf("got apiKey value %q, want empty (redacted)", got)
	}
}

func TestApp_ExportWorkspaceJSON_UnknownWorkspaceIsEmpty(t *testing.T) {
	store, err := storage.NewFileStore(t.TempDir(), storage.WithSecretStore(newFakeSecretStore()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	a := &App{store: store}

	out, err := a.exportWorkspaceJSON("nonexistent", "")
	if err != nil {
		t.Fatalf("exportWorkspaceJSON: %v", err)
	}
	var doc exporter.ExportedWorkspace
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if len(doc.Folders) != 0 || len(doc.Requests) != 0 || len(doc.Environments) != 0 {
		t.Fatalf("expected all-empty collections for an unknown workspace, got %+v", doc)
	}
}

// TestApp_RunFolder exercises the real engine (appcore.NewEngine, not a
// fake) against a real HTTP server returning mixed 200/404/500, proving:
// requests directly in the target folder AND in a subfolder are included;
// a sibling folder's request and a request with no folder at all are NOT;
// results come back in orderKey order (matching what the sidebar tree
// shows); and a request that can't even connect gets its own failed
// result instead of aborting the requests queued after it.
func TestApp_RunFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	engine, store, err := appcore.NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	a := &App{ctx: context.Background(), store: store, engine: engine}

	const wsID = "ws1"
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "test"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}

	parentID := uuid.NewString()
	if err := store.PutFolder(model.Folder{ID: parentID, WorkspaceID: wsID, Name: "parent", OrderKey: "1"}); err != nil {
		t.Fatalf("PutFolder(parent): %v", err)
	}
	childID := uuid.NewString()
	if err := store.PutFolder(model.Folder{ID: childID, WorkspaceID: wsID, ParentID: &parentID, Name: "child", OrderKey: "2"}); err != nil {
		t.Fatalf("PutFolder(child): %v", err)
	}
	otherID := uuid.NewString()
	if err := store.PutFolder(model.Folder{ID: otherID, WorkspaceID: wsID, Name: "other", OrderKey: "3"}); err != nil {
		t.Fatalf("PutFolder(other): %v", err)
	}

	reqs := []model.RequestDef{
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &parentID, Name: "A-ok", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/ok", OrderKey: "1"},
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &childID, Name: "B-missing", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/missing", OrderKey: "2"},
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &childID, Name: "C-broken", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/broken", OrderKey: "3"},
		// Port 1 (TCPMUX) is never listening — dial fails immediately, so
		// RunRequest returns a non-nil error rather than a real ResponseData.
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &parentID, Name: "D-unreachable", Protocol: model.ProtocolHTTP, Method: "GET", URL: "http://127.0.0.1:1/nope", OrderKey: "4"},
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &parentID, Name: "E-after-failure", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/ok", OrderKey: "5"},
		// Out of scope: a sibling folder, and no folder at all.
		{ID: uuid.NewString(), WorkspaceID: wsID, FolderID: &otherID, Name: "sibling-folder", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/ok", OrderKey: "0"},
		{ID: uuid.NewString(), WorkspaceID: wsID, Name: "no-folder", Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/ok", OrderKey: "0"},
	}
	for _, r := range reqs {
		if err := store.PutRequest(r); err != nil {
			t.Fatalf("PutRequest(%s): %v", r.Name, err)
		}
	}

	results := a.RunFolder(wsID, parentID, "")

	if len(results) != 5 {
		t.Fatalf("got %d results, want 5 (A, B, C, D, E only — not sibling-folder or no-folder): %+v", len(results), results)
	}
	wantOrder := []string{"A-ok", "B-missing", "C-broken", "D-unreachable", "E-after-failure"}
	for i, name := range wantOrder {
		if results[i].RequestName != name {
			t.Fatalf("result[%d].RequestName = %q, want %q (orderKey order)", i, results[i].RequestName, name)
		}
	}
	if got := results[0].Response.Status; got != http.StatusOK {
		t.Fatalf("A-ok status = %d, want 200", got)
	}
	if got := results[1].Response.Status; got != http.StatusNotFound {
		t.Fatalf("B-missing status = %d, want 404", got)
	}
	if got := results[2].Response.Status; got != http.StatusInternalServerError {
		t.Fatalf("C-broken status = %d, want 500", got)
	}
	if results[3].Response.Error == "" {
		t.Fatalf("D-unreachable: want a non-empty Error, got none (result: %+v)", results[3])
	}
	if got := results[4].Response.Status; got != http.StatusOK {
		t.Fatalf("E-after-failure status = %d, want 200 (the batch must continue past D's failure)", got)
	}
}

// TestApp_FetchGraphQLSchema exercises the real engine (appcore.NewEngine)
// against a real HTTP server, proving FetchGraphQLSchema resolves the
// request through the SAME template+auth path as a normal send — a
// ${envVar}-templated URL and a configured header both reach the
// introspection POST — rather than just hitting the request's literal,
// unresolved URL.
func TestApp_FetchGraphQLSchema(t *testing.T) {
	var gotAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"ping","args":[],"type":{"kind":"SCALAR","name":"String"}}]}]}}}`))
	}))
	defer srv.Close()

	engine, store, err := appcore.NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	a := &App{ctx: context.Background(), store: store, engine: engine}

	const wsID = "ws1"
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "test"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	envID := uuid.NewString()
	if err := store.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: wsID,
		Variables: []model.KeyValue{{Key: "gqlHost", Value: srv.URL, Enabled: true}},
	}, nil); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	req := model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, Name: "gql", Protocol: model.ProtocolGraphQL, Method: "POST",
		URL:     "${gqlHost}/graphql",
		Headers: []model.KeyValue{{Key: "Authorization", Value: "Bearer secret-token", Enabled: true}},
	}
	if err := store.PutRequest(req); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	raw, err := a.FetchGraphQLSchema(req.ID, envID)
	if err != nil {
		t.Fatalf("FetchGraphQLSchema: %v", err)
	}

	if gotAuthHeader != "Bearer secret-token" {
		t.Fatalf("got Authorization header %q, want the configured header to reach introspection (proves ResolveForExecution wiring, not a raw URL hit)", gotAuthHeader)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("FetchGraphQLSchema returned invalid JSON: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal schema response: %v", err)
	}
	if _, ok := decoded["data"]; !ok {
		t.Fatalf("decoded schema response missing \"data\" key: %v", decoded)
	}
}

// TestApp_FetchGraphQLSchema_UnresolvableRequest guards the "no such
// request" path — FetchGraphQLSchema must surface ResolveForExecution's
// error rather than panicking or silently returning an empty schema.
func TestApp_FetchGraphQLSchema_UnresolvableRequest(t *testing.T) {
	engine, store, err := appcore.NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	a := &App{ctx: context.Background(), store: store, engine: engine}

	if _, err := a.FetchGraphQLSchema("does-not-exist", ""); err == nil {
		t.Fatal("FetchGraphQLSchema: want an error for an unknown request id, got nil")
	}
}
