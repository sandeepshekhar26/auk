package main

import (
	"encoding/json"
	"strings"
	"testing"

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
