package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

func newTestFileStore(t *testing.T) (*FileStore, string) {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return fs, dir
}

func TestFileStore_RequestRoundTrip(t *testing.T) {
	fs, dir := newTestFileStore(t)

	req := model.RequestDef{
		ID: "req1", WorkspaceID: "ws1", Name: "Get thing",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://example.com",
	}
	if err := fs.PutRequest(req); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	path := requestFile(dir, "ws1", "req1")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected request file at %s: %v", path, err)
	}

	got, err := fs.GetRequest("req1")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.Name != "Get thing" || got.URL != "https://example.com" {
		t.Errorf("GetRequest returned unexpected data: %+v", got)
	}
	if got.OrderKey == "" {
		t.Errorf("expected OrderKey to be auto-assigned, got empty")
	}

	// Reload from disk into a fresh store and confirm it round-trips.
	fs2, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatalf("reload NewFileStore: %v", err)
	}
	reloaded, err := fs2.GetRequest("req1")
	if err != nil {
		t.Fatalf("GetRequest after reload: %v", err)
	}
	if reloaded.Name != "Get thing" {
		t.Errorf("reloaded request name = %q, want %q", reloaded.Name, "Get thing")
	}
}

func TestFileStore_RequestOrderKeyAssignedForSiblings(t *testing.T) {
	fs, _ := newTestFileStore(t)

	r1 := model.RequestDef{ID: "r1", WorkspaceID: "ws1", Name: "one", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://a"}
	r2 := model.RequestDef{ID: "r2", WorkspaceID: "ws1", Name: "two", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://b"}
	r3 := model.RequestDef{ID: "r3", WorkspaceID: "ws1", Name: "three", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://c"}

	for _, r := range []model.RequestDef{r1, r2, r3} {
		if err := fs.PutRequest(r); err != nil {
			t.Fatalf("PutRequest(%s): %v", r.ID, err)
		}
	}

	got1, _ := fs.GetRequest("r1")
	got2, _ := fs.GetRequest("r2")
	got3, _ := fs.GetRequest("r3")

	if !(got1.OrderKey < got2.OrderKey && got2.OrderKey < got3.OrderKey) {
		t.Errorf("expected strictly increasing order keys, got %q, %q, %q", got1.OrderKey, got2.OrderKey, got3.OrderKey)
	}
}

func TestFileStore_McpConnectionRoundTrip(t *testing.T) {
	fs, dir := newTestFileStore(t)

	conn := model.McpConnection{
		ID: "conn1", WorkspaceID: "ws1", Name: "My dev server",
		Transport: model.McpTransportStdio, Command: "node", Args: []string{"server.js"},
	}
	if err := fs.PutMcpConnection(conn); err != nil {
		t.Fatalf("PutMcpConnection: %v", err)
	}

	path := mcpConnectionFile(dir, "ws1", "conn1")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected mcp connection file at %s: %v", path, err)
	}

	got, err := fs.GetMcpConnection("conn1")
	if err != nil {
		t.Fatalf("GetMcpConnection: %v", err)
	}
	if got.Name != "My dev server" || got.Command != "node" {
		t.Errorf("GetMcpConnection returned unexpected data: %+v", got)
	}

	list := fs.ListMcpConnections("ws1")
	if len(list) != 1 || list[0].ID != "conn1" {
		t.Errorf("ListMcpConnections = %+v, want exactly conn1", list)
	}

	// Reload from disk into a fresh store and confirm it round-trips.
	fs2, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(filepath.Join(dir, "history.jsonl")))
	if err != nil {
		t.Fatalf("reload NewFileStore: %v", err)
	}
	reloaded, err := fs2.GetMcpConnection("conn1")
	if err != nil {
		t.Fatalf("GetMcpConnection after reload: %v", err)
	}
	if reloaded.Name != "My dev server" {
		t.Errorf("reloaded name = %q, want %q", reloaded.Name, "My dev server")
	}
}

func TestFileStore_RemoveMcpConnection(t *testing.T) {
	fs, dir := newTestFileStore(t)

	conn := model.McpConnection{ID: "conn1", WorkspaceID: "ws1", Name: "Temp", Transport: model.McpTransportHTTP, URL: "http://localhost:1234/mcp"}
	if err := fs.PutMcpConnection(conn); err != nil {
		t.Fatalf("PutMcpConnection: %v", err)
	}
	path := mcpConnectionFile(dir, "ws1", "conn1")

	if err := fs.RemoveMcpConnection("conn1"); err != nil {
		t.Fatalf("RemoveMcpConnection: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected the connection file to be deleted, stat err = %v", err)
	}
	if list := fs.ListMcpConnections("ws1"); len(list) != 0 {
		t.Errorf("expected no connections after removal, got %+v", list)
	}

	// Removing an id that never existed is a no-op, not an error.
	if err := fs.RemoveMcpConnection("does-not-exist"); err != nil {
		t.Errorf("RemoveMcpConnection on unknown id should be a no-op, got: %v", err)
	}
}

func TestFileStore_EnvironmentSecretsExcludedFromYAML(t *testing.T) {
	fs, dir := newTestFileStore(t)

	env := model.Environment{
		ID: "env1", WorkspaceID: "ws1", Name: "Prod",
		Variables: []model.KeyValue{
			{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
			{Key: "apiKey", Value: "super-secret-value", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}
	if err := fs.PutEnvironment(env, map[string]string{"apiKey": "super-secret-value"}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	raw, err := os.ReadFile(environmentFile(dir, "ws1", "env1"))
	if err != nil {
		t.Fatalf("read environment file: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-value") {
		t.Errorf("secret value leaked into environment YAML file:\n%s", raw)
	}
	if !strings.Contains(string(raw), "apiKey") {
		t.Errorf("expected secret variable NAME to still appear in YAML:\n%s", raw)
	}
	if !strings.Contains(string(raw), "https://api.example.com") {
		t.Errorf("expected non-secret variable value to appear in YAML:\n%s", raw)
	}

	resolved, err := fs.GetEnvironment("env1")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	var gotValue string
	for _, kv := range resolved.Variables {
		if kv.Key == "apiKey" {
			gotValue = kv.Value
		}
	}
	if gotValue != "super-secret-value" {
		t.Errorf("GetEnvironment did not resolve secret value from SecretStore: got %q", gotValue)
	}
}

func TestFileStore_EnvironmentSecretMissingFromKeyringResolvesEmpty(t *testing.T) {
	fs, _ := newTestFileStore(t)

	env := model.Environment{
		ID: "env1", WorkspaceID: "ws1", Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Value: "", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	// Note: no secretValues passed, simulating a cloned repo where the
	// local keychain never had this secret set.
	if err := fs.PutEnvironment(env, nil); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	resolved, err := fs.GetEnvironment("env1")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if len(resolved.Variables) != 1 || resolved.Variables[0].Value != "" {
		t.Errorf("expected empty value for unset secret, got %+v", resolved.Variables)
	}
}

// TestFileStore_ListEnvironmentsRawNeverResolvesSecrets guards the export
// feature's safety property at the real storage layer (not a hand-built
// struct): ListEnvironments resolves the real keychain value as designed,
// but ListEnvironmentsRaw — what workspace export must use — never touches
// the SecretStore at all, through a real FileStore + fake keychain, not a
// mock of the redaction logic itself.
func TestFileStore_ListEnvironmentsRawNeverResolvesSecrets(t *testing.T) {
	fs, _ := newTestFileStore(t)

	env := model.Environment{
		ID: "env1", WorkspaceID: "ws1", Name: "Prod",
		Variables: []model.KeyValue{
			{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
			{Key: "apiKey", Value: "", Enabled: true},
		},
		Secrets: []string{"apiKey"},
	}
	if err := fs.PutEnvironment(env, map[string]string{"apiKey": "sk-live-do-not-leak"}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}

	resolved, err := fs.GetEnvironment("env1")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if got := valueFor(resolved.Variables, "apiKey"); got != "sk-live-do-not-leak" {
		t.Fatalf("sanity check failed: GetEnvironment should resolve the real secret, got %q", got)
	}

	raw := fs.ListEnvironmentsRaw("ws1")
	if len(raw) != 1 {
		t.Fatalf("got %d environments, want 1", len(raw))
	}
	if got := valueFor(raw[0].Variables, "apiKey"); got != "" {
		t.Fatalf("ListEnvironmentsRaw resolved a secret value (got %q) — it must never touch the SecretStore", got)
	}
	if got := valueFor(raw[0].Variables, "baseUrl"); got != "https://api.example.com" {
		t.Fatalf("ListEnvironmentsRaw lost a non-secret variable: got %q", got)
	}
	if len(raw[0].Secrets) != 1 || raw[0].Secrets[0] != "apiKey" {
		t.Fatalf("got Secrets %v, want [apiKey] (names are not sensitive, should still be present)", raw[0].Secrets)
	}
}

func valueFor(vars []model.KeyValue, key string) string {
	for _, kv := range vars {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

func TestFileStore_AppendAndListHistory(t *testing.T) {
	fs, _ := newTestFileStore(t)

	entries := []model.HistoryEntry{
		{RequestID: "r1", RequestName: "one", Method: "GET", URL: "https://a", Status: 200},
		{RequestID: "r2", RequestName: "two", Method: "POST", URL: "https://b", Status: 500},
	}
	for _, e := range entries {
		if err := fs.AppendHistory(e); err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
	}

	got, err := fs.ListHistory()
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(got))
	}
	if got[0].RequestName != "one" || got[1].RequestName != "two" {
		t.Errorf("history entries out of order or wrong data: %+v", got)
	}
	for _, h := range got {
		if h.ID == "" {
			t.Errorf("expected auto-assigned ID, got empty for %+v", h)
		}
		if h.Timestamp == "" {
			t.Errorf("expected auto-assigned Timestamp, got empty for %+v", h)
		}
	}
}

func TestFileStore_HistoryNotWrittenUnderWorkspaceRoot(t *testing.T) {
	dir := t.TempDir()
	historyPath := filepath.Join(t.TempDir(), "history.jsonl")
	fs, err := NewFileStore(dir, WithSecretStore(newFakeSecretStore()), WithHistoryPath(historyPath))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.AppendHistory(model.HistoryEntry{RequestID: "r1", RequestName: "x"}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspaces")); err != nil {
		t.Fatalf("workspaces dir missing: %v", err)
	}
	// The history file must live at historyPath (outside dir/workspaces).
	if _, err := os.Stat(historyPath); err != nil {
		t.Fatalf("expected history file at %s: %v", historyPath, err)
	}
}

func TestFileStore_LastResponseForChaining(t *testing.T) {
	fs, _ := newTestFileStore(t)

	resp := model.ResponseData{RequestID: "r1", Status: 200, BodyBase64: "aGk="}
	if err := fs.SaveResponse(resp); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	got, ok := fs.LastResponse("r1")
	if !ok {
		t.Fatalf("expected LastResponse to find r1")
	}
	if got.Status != 200 {
		t.Errorf("LastResponse status = %d, want 200", got.Status)
	}
	if _, ok := fs.LastResponse("does-not-exist"); ok {
		t.Errorf("expected LastResponse to report not-found for unknown id")
	}
}

func TestFileStore_FolderOrderKeyPerParent(t *testing.T) {
	fs, _ := newTestFileStore(t)

	parentA := "folderA"
	parentB := "folderB"
	f1 := model.Folder{ID: "f1", WorkspaceID: "ws1", ParentID: &parentA, Name: "child of A"}
	f2 := model.Folder{ID: "f2", WorkspaceID: "ws1", ParentID: &parentA, Name: "another child of A"}
	f3 := model.Folder{ID: "f3", WorkspaceID: "ws1", ParentID: &parentB, Name: "child of B"}

	for _, f := range []model.Folder{f1, f2, f3} {
		if err := fs.PutFolder(f); err != nil {
			t.Fatalf("PutFolder(%s): %v", f.ID, err)
		}
	}

	folders := fs.ListFolders("ws1")
	byID := map[model.ID]model.Folder{}
	for _, f := range folders {
		byID[f.ID] = f
	}
	if byID["f1"].OrderKey >= byID["f2"].OrderKey {
		t.Errorf("expected f1 order key < f2 (same parent), got %q >= %q", byID["f1"].OrderKey, byID["f2"].OrderKey)
	}
	// f3 has a different parent, so its order key can independently be
	// anything valid — just confirm it's non-empty.
	if byID["f3"].OrderKey == "" {
		t.Errorf("expected f3 to have a non-empty order key")
	}
}

func TestFileStore_RemoveRequest(t *testing.T) {
	fs, dir := newTestFileStore(t)

	req := model.RequestDef{ID: "req1", WorkspaceID: "ws1", Name: "Temp", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://example.com"}
	if err := fs.PutRequest(req); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	if err := fs.SaveResponse(model.ResponseData{RequestID: "req1", Status: 200}); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	path := requestFile(dir, "ws1", "req1")

	if err := fs.RemoveRequest("req1"); err != nil {
		t.Fatalf("RemoveRequest: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected the request file to be deleted, stat err = %v", err)
	}
	if _, err := fs.GetRequest("req1"); err == nil {
		t.Errorf("expected GetRequest to fail after removal")
	}
	if _, ok := fs.LastResponse("req1"); ok {
		t.Errorf("expected the cached lastResponse to be cleared on removal")
	}

	// Removing an id that never existed is a no-op, not an error.
	if err := fs.RemoveRequest("does-not-exist"); err != nil {
		t.Errorf("RemoveRequest on unknown id should be a no-op, got: %v", err)
	}
}

// TestFileStore_RemoveFolder_CascadesToChildFoldersAndRequests builds a
// two-level tree (folder -> subfolder -> request, plus a request directly in
// the top folder) alongside an untouched sibling folder+request, and
// confirms deleting the top folder removes every nested file but leaves the
// sibling alone — the cascade must recurse without over-deleting.
func TestFileStore_RemoveFolder_CascadesToChildFoldersAndRequests(t *testing.T) {
	fs, dir := newTestFileStore(t)

	top := model.Folder{ID: "top", WorkspaceID: "ws1", Name: "Top"}
	sub := model.Folder{ID: "sub", WorkspaceID: "ws1", ParentID: &top.ID, Name: "Sub"}
	sibling := model.Folder{ID: "sibling", WorkspaceID: "ws1", Name: "Sibling"}
	for _, f := range []model.Folder{top, sub, sibling} {
		if err := fs.PutFolder(f); err != nil {
			t.Fatalf("PutFolder(%s): %v", f.ID, err)
		}
	}

	reqInTop := model.RequestDef{ID: "reqTop", WorkspaceID: "ws1", FolderID: &top.ID, Name: "in top", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://a"}
	reqInSub := model.RequestDef{ID: "reqSub", WorkspaceID: "ws1", FolderID: &sub.ID, Name: "in sub", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://b"}
	reqInSibling := model.RequestDef{ID: "reqSibling", WorkspaceID: "ws1", FolderID: &sibling.ID, Name: "in sibling", Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://c"}
	for _, r := range []model.RequestDef{reqInTop, reqInSub, reqInSibling} {
		if err := fs.PutRequest(r); err != nil {
			t.Fatalf("PutRequest(%s): %v", r.ID, err)
		}
	}

	if err := fs.RemoveFolder("top"); err != nil {
		t.Fatalf("RemoveFolder: %v", err)
	}

	for _, gone := range []string{"top", "sub"} {
		if _, err := os.Stat(folderFile(dir, "ws1", gone)); !os.IsNotExist(err) {
			t.Errorf("expected folder %q file to be deleted, stat err = %v", gone, err)
		}
	}
	for _, gone := range []string{"reqTop", "reqSub"} {
		if _, err := os.Stat(requestFile(dir, "ws1", gone)); !os.IsNotExist(err) {
			t.Errorf("expected request %q file to be deleted, stat err = %v", gone, err)
		}
		if _, err := fs.GetRequest(gone); err == nil {
			t.Errorf("expected GetRequest(%q) to fail after cascading removal", gone)
		}
	}

	// The sibling subtree must survive untouched.
	if _, err := os.Stat(folderFile(dir, "ws1", "sibling")); err != nil {
		t.Errorf("expected sibling folder file to survive: %v", err)
	}
	if _, err := fs.GetRequest("reqSibling"); err != nil {
		t.Errorf("expected sibling's request to survive: %v", err)
	}

	remainingFolders := fs.ListFolders("ws1")
	if len(remainingFolders) != 1 || remainingFolders[0].ID != "sibling" {
		t.Errorf("ListFolders after cascade = %+v, want exactly [sibling]", remainingFolders)
	}

	// Removing an id that never existed is a no-op, not an error.
	if err := fs.RemoveFolder("does-not-exist"); err != nil {
		t.Errorf("RemoveFolder on unknown id should be a no-op, got: %v", err)
	}
}

func TestFileStore_RemoveEnvironment(t *testing.T) {
	fs, dir := newTestFileStore(t)

	env := model.Environment{
		ID: "env1", WorkspaceID: "ws1", Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Value: "super-secret-value", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}
	if err := fs.PutEnvironment(env, map[string]string{"apiKey": "super-secret-value"}); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}
	path := environmentFile(dir, "ws1", "env1")

	if err := fs.RemoveEnvironment("env1"); err != nil {
		t.Fatalf("RemoveEnvironment: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected the environment file to be deleted, stat err = %v", err)
	}
	if _, err := fs.GetEnvironment("env1"); err == nil {
		t.Errorf("expected GetEnvironment to fail after removal")
	}
	if _, err := fs.secrets.Get(secretServiceName, secretAccount("ws1", "env1", "apiKey")); err == nil {
		t.Errorf("expected the secret to be removed from the keychain too")
	}

	// Removing an id that never existed is a no-op, not an error.
	if err := fs.RemoveEnvironment("does-not-exist"); err != nil {
		t.Errorf("RemoveEnvironment on unknown id should be a no-op, got: %v", err)
	}
}
