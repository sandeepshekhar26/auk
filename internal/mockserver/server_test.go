package mockserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// fakeSecretStore keeps the tests off the real OS keychain (go-keyring can pop
// a permission dialog on first use, which would hang a non-interactive run).
// storage's own fake is test-package-private, so this mirrors it.
type fakeSecretStore struct{ values map[string]string }

func newFakeSecretStore() *fakeSecretStore { return &fakeSecretStore{values: map[string]string{}} }

func (f *fakeSecretStore) key(service, account string) string { return service + "\x00" + account }

func (f *fakeSecretStore) Get(service, account string) (string, error) {
	v, ok := f.values[f.key(service, account)]
	if !ok {
		return "", fmt.Errorf("secret not found: %s/%s", service, account)
	}
	return v, nil
}

func (f *fakeSecretStore) Set(service, account, value string) error {
	f.values[f.key(service, account)] = value
	return nil
}

func (f *fakeSecretStore) Delete(service, account string) error {
	delete(f.values, f.key(service, account))
	return nil
}

const testWorkspace = "ws-mock"

func newStore(t *testing.T) *storage.FileStore {
	t.Helper()
	dir := t.TempDir()
	fs, err := storage.NewFileStore(dir,
		storage.WithSecretStore(newFakeSecretStore()),
		storage.WithHistoryPath(filepath.Join(dir, "history.jsonl")),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.PutWorkspace(model.Workspace{ID: testWorkspace, Name: "Mock WS"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	return fs
}

// seed saves a request plus the recorded response that makes it a mock.
func seed(t *testing.T, fs *storage.FileStore, id, method, url string, resp model.ResponseData) {
	t.Helper()
	if err := fs.PutRequest(model.RequestDef{
		ID: id, WorkspaceID: testWorkspace, Name: id,
		Protocol: model.ProtocolHTTP, Method: method, URL: url,
	}); err != nil {
		t.Fatalf("PutRequest(%s): %v", id, err)
	}
	resp.RequestID = id
	if err := fs.SaveResponse(resp); err != nil {
		t.Fatalf("SaveResponse(%s): %v", id, err)
	}
}

func jsonResp(status int, body string, headers ...model.KeyValue) model.ResponseData {
	h := append([]model.KeyValue{{Key: "Content-Type", Value: "application/json", Enabled: true}}, headers...)
	return model.ResponseData{
		Status: status, StatusText: http.StatusText(status), Headers: h,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)), BodySize: len(body),
	}
}

// startServer boots the REAL server on an ephemeral port (no httptest).
func startServer(t *testing.T, store Store) *Server {
	t.Helper()
	srv, err := Start(store, testWorkspace, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

// do sends a real request over the loopback socket.
func do(t *testing.T, srv *Server, method, path string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL()+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(b)
}

// devOriginURL is a stand-in for the Vite/CRA/Next dev server the mock exists
// to serve: a loopback origin, and therefore the one kind that gets CORS
// headers back. devOrigin is it as a request-header map.
const devOriginURL = "http://localhost:5173"

var devOrigin = map[string]string{"Origin": devOriginURL}

func TestReplayFidelity(t *testing.T) {
	fs := newStore(t)
	body := `{"id":7,"name":"Ada"}`
	seed(t, fs, "r-user", "GET", "https://api.example.com/users/7", jsonResp(201, body,
		model.KeyValue{Key: "X-Custom", Value: "kept", Enabled: true},
		model.KeyValue{Key: "Set-Cookie", Value: "a=1", Enabled: true},
		model.KeyValue{Key: "Set-Cookie", Value: "b=2", Enabled: true},
		// Hop-by-hop + recomputed + regenerated headers that must be dropped.
		model.KeyValue{Key: "Transfer-Encoding", Value: "chunked", Enabled: true},
		model.KeyValue{Key: "Connection", Value: "keep-alive", Enabled: true},
		model.KeyValue{Key: "Content-Length", Value: "99999", Enabled: true},
		model.KeyValue{Key: "Date", Value: "Tue, 01 Jan 1980 00:00:00 GMT", Enabled: true},
		// A recorded CORS header must not double up with ours.
		model.KeyValue{Key: "Access-Control-Allow-Origin", Value: "https://evil.test", Enabled: true},
	))
	srv := startServer(t, fs)

	resp, got := do(t, srv, "GET", "/users/7", devOrigin)
	if resp.StatusCode != 201 {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if resp.Header.Get("X-Custom") != "kept" {
		t.Errorf("X-Custom = %q, want kept", resp.Header.Get("X-Custom"))
	}
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) != 2 {
		t.Errorf("Set-Cookie = %v, want both recorded values", cookies)
	}
	if resp.Header.Get(MockHeader) != "1" {
		t.Errorf("%s header missing", MockHeader)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("Content-Length = %d, want %d (recomputed, not the recorded 99999)", resp.ContentLength, len(body))
	}
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Errorf("Transfer-Encoding leaked: %q", te)
	}
	if c := resp.Header.Get("Connection"); strings.EqualFold(c, "keep-alive") && len(resp.Header.Values("Connection")) > 1 {
		t.Errorf("recorded Connection leaked: %v", resp.Header.Values("Connection"))
	}
	if d := resp.Header.Get("Date"); strings.Contains(d, "1980") {
		t.Errorf("stale recorded Date replayed: %q", d)
	}
	if origins := resp.Header.Values("Access-Control-Allow-Origin"); len(origins) != 1 || origins[0] != devOriginURL {
		t.Errorf("Access-Control-Allow-Origin = %v, want exactly one %q (the recorded one must not double up)", origins, devOriginURL)
	}
}

func TestReplayNoContentHasNoBody(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-del", "DELETE", "https://api.example.com/users/7",
		model.ResponseData{Status: 204, StatusText: "No Content"})
	srv := startServer(t, fs)

	resp, body := do(t, srv, "DELETE", "/users/7", nil)
	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if body != "" {
		t.Errorf("204 carried a body: %q", body)
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Errorf("204 carried Content-Length: %q", resp.Header.Get("Content-Length"))
	}
}

func TestNotFoundIsJSONWithHint(t *testing.T) {
	srv := startServer(t, newStore(t))
	resp, body := do(t, srv, "GET", "/nope", devOrigin)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
		Hint  string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("404 body is not JSON (%v): %s", err, body)
	}
	if e.Error != "no mock for GET /nope" {
		t.Errorf("error = %q", e.Error)
	}
	if !strings.Contains(e.Hint, "send the request once in AUK") {
		t.Errorf("hint = %q", e.Hint)
	}
	// CORS must be present on errors too, or a local frontend can't even read
	// them.
	if resp.Header.Get("Access-Control-Allow-Origin") != devOriginURL {
		t.Error("404 missing CORS header for a loopback origin")
	}
	if resp.Header.Get(MockHeader) != "1" {
		t.Errorf("404 missing %s header", MockHeader)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-post", "POST", "https://api.example.com/users", jsonResp(200, `{}`))
	seed(t, fs, "r-del", "DELETE", "https://api.example.com/users", jsonResp(200, `{}`))
	srv := startServer(t, fs)

	resp, body := do(t, srv, "PUT", "/users", devOrigin)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "DELETE, POST" {
		t.Errorf("Allow = %q, want \"DELETE, POST\"", allow)
	}
	if !strings.Contains(body, "no mock for PUT /users") {
		t.Errorf("405 body = %s", body)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != devOriginURL {
		t.Error("405 missing CORS header for a loopback origin")
	}
}

func TestCORSPreflight(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-post", "POST", "https://api.example.com/users", jsonResp(200, `{}`))
	srv := startServer(t, fs)

	resp, body := do(t, srv, "OPTIONS", "/users", map[string]string{
		"Origin":                         "http://localhost:5173",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type, authorization",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	if body != "" {
		t.Errorf("preflight carried a body: %q", body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("preflight Allow-Origin = %q, want the reflected loopback origin", got)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("Allow-Methods = %q", resp.Header.Get("Access-Control-Allow-Methods"))
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "content-type, authorization" {
		t.Errorf("Allow-Headers = %q, want the echoed request headers", got)
	}

	// A preflight for a path the mock has never heard of still succeeds —
	// otherwise the browser reports a CORS failure instead of the 404 the
	// frontend actually needs to see.
	resp, _ = do(t, srv, "OPTIONS", "/unknown", map[string]string{
		"Origin": "http://localhost:5173", "Access-Control-Request-Method": "GET",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight on unknown path = %d, want 204", resp.StatusCode)
	}
}

func TestHeadServedFromGetRecording(t *testing.T) {
	fs := newStore(t)
	body := `{"ok":true}`
	seed(t, fs, "r-get", "GET", "https://api.example.com/health", jsonResp(200, body))
	srv := startServer(t, fs)

	resp, got := do(t, srv, "HEAD", "/health", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
	}
	if got != "" {
		t.Errorf("HEAD returned a body: %q", got)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("HEAD Content-Length = %d, want %d", resp.ContentLength, len(body))
	}
}

func TestLiveStoreRead(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-live", "GET", "https://api.example.com/config", jsonResp(200, `{"v":1}`))
	srv := startServer(t, fs)

	if _, body := do(t, srv, "GET", "/config", nil); body != `{"v":1}` {
		t.Fatalf("first hit body = %q", body)
	}

	// Re-sending the request in the app replaces the recording. No restart.
	if err := fs.SaveResponse(model.ResponseData{
		RequestID: "r-live", Status: 500, StatusText: "Internal Server Error",
		Headers:    []model.KeyValue{{Key: "Content-Type", Value: "application/json", Enabled: true}},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"v":2}`)),
	}); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	resp, body := do(t, srv, "GET", "/config", nil)
	if resp.StatusCode != 500 || body != `{"v":2}` {
		t.Errorf("second hit = %d %q, want 500 {\"v\":2}", resp.StatusCode, body)
	}

	// A brand new request+response becomes a route with no restart either.
	seed(t, fs, "r-new", "GET", "https://api.example.com/added", jsonResp(200, `"new"`))
	if resp, _ := do(t, srv, "GET", "/added", nil); resp.StatusCode != 200 {
		t.Errorf("newly recorded route = %d, want 200", resp.StatusCode)
	}
	if n := len(srv.Routes()); n != 2 {
		t.Errorf("Routes() = %d, want 2", n)
	}
}

func TestOnlyRecordedMockableRequestsBecomeRoutes(t *testing.T) {
	fs := newStore(t)
	// Recorded → a route.
	seed(t, fs, "r-ok", "GET", "https://api.example.com/ok", jsonResp(200, `{}`))
	// Saved but never sent → no recorded response → no route.
	if err := fs.PutRequest(model.RequestDef{
		ID: "r-unsent", WorkspaceID: testWorkspace, Name: "unsent",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://api.example.com/unsent",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	// Sent but FAILED (status 0 + Error) → not a mock.
	seed(t, fs, "r-failed", "GET", "https://api.example.com/failed",
		model.ResponseData{Status: 0, Error: "dial tcp: connection refused"})
	// A non-HTTP protocol has no replayable request/response pair.
	if err := fs.PutRequest(model.RequestDef{
		ID: "r-ws", WorkspaceID: testWorkspace, Name: "ws",
		Protocol: model.ProtocolWebSocket, Method: "GET", URL: "wss://api.example.com/socket",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	if err := fs.SaveResponse(model.ResponseData{RequestID: "r-ws", Status: 101}); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	// Another workspace's recorded request must never leak in.
	if err := fs.PutWorkspace(model.Workspace{ID: "other-ws", Name: "Other"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := fs.PutRequest(model.RequestDef{
		ID: "r-other", WorkspaceID: "other-ws", Name: "other",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://api.example.com/other",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	if err := fs.SaveResponse(model.ResponseData{RequestID: "r-other", Status: 200}); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}

	routes := DeriveRoutes(fs, testWorkspace)
	if len(routes) != 1 || routes[0].Path != "/ok" {
		t.Fatalf("routes = %+v, want exactly GET /ok", routes)
	}

	srv := startServer(t, fs)
	for _, p := range []string{"/unsent", "/failed", "/socket", "/other"} {
		if resp, _ := do(t, srv, "GET", p, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestGraphQLRequestIsMocked(t *testing.T) {
	fs := newStore(t)
	if err := fs.PutRequest(model.RequestDef{
		ID: "r-gql", WorkspaceID: testWorkspace, Name: "gql",
		Protocol: model.ProtocolGraphQL, URL: "https://api.example.com/graphql",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	if err := fs.SaveResponse(model.ResponseData{
		RequestID: "r-gql", Status: 200,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"data":{}}`)),
	}); err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	// A GraphQL request with no explicit method routes as POST.
	routes := DeriveRoutes(fs, testWorkspace)
	if len(routes) != 1 || routes[0].Method != "POST" || routes[0].Path != "/graphql" {
		t.Fatalf("routes = %+v, want POST /graphql", routes)
	}
	srv := startServer(t, fs)
	if resp, body := do(t, srv, "POST", "/graphql", nil); resp.StatusCode != 200 || body != `{"data":{}}` {
		t.Errorf("POST /graphql = %d %q", resp.StatusCode, body)
	}
}

func TestStartStopIdempotency(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-ok", "GET", "https://api.example.com/ok", jsonResp(200, `{}`))

	srv, err := Start(fs, testWorkspace, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if srv.Port() == 0 {
		t.Fatal("Start(port 0) must report the resolved ephemeral port")
	}
	if srv.WorkspaceID() != testWorkspace {
		t.Errorf("WorkspaceID = %q", srv.WorkspaceID())
	}
	if !strings.HasPrefix(srv.URL(), "http://127.0.0.1:") {
		t.Errorf("URL = %q, want a loopback address", srv.URL())
	}
	if resp, _ := do(t, srv, "GET", "/ok", nil); resp.StatusCode != 200 {
		t.Fatalf("pre-stop request = %d", resp.StatusCode)
	}

	srv.Stop()
	// Stopping twice (and stopping a nil server) must not panic.
	srv.Stop()
	var nilSrv *Server
	nilSrv.Stop()

	if _, err := http.Get(srv.URL() + "/ok"); err == nil {
		t.Error("expected the listener to be closed after Stop")
	}

	// The same port can be rebound after a stop.
	again, err := Start(fs, testWorkspace, srv.Port())
	if err != nil {
		t.Fatalf("restart on the same port: %v", err)
	}
	defer again.Stop()
	if resp, _ := do(t, again, "GET", "/ok", nil); resp.StatusCode != 200 {
		t.Errorf("post-restart request = %d", resp.StatusCode)
	}
}

func TestStartValidatesArguments(t *testing.T) {
	fs := newStore(t)
	if _, err := Start(nil, testWorkspace, 0); err == nil {
		t.Error("expected an error for a nil store")
	}
	if _, err := Start(fs, "  ", 0); err == nil {
		t.Error("expected an error for an empty workspace id")
	}
	if _, err := Start(fs, testWorkspace, 70000); err == nil {
		t.Error("expected an error for an out-of-range port")
	}

	// A port already in use must fail at Start, not silently later.
	first, err := Start(fs, testWorkspace, 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer first.Stop()
	if _, err := Start(fs, testWorkspace, first.Port()); err == nil {
		t.Error("expected a bind error on an occupied port")
	}
}

func TestCorruptRecordingIsBadGateway(t *testing.T) {
	fs := newStore(t)
	seed(t, fs, "r-bad", "GET", "https://api.example.com/bad", model.ResponseData{
		Status: 200, BodyBase64: "!!!not base64!!!",
	})
	srv := startServer(t, fs)
	resp, body := do(t, srv, "GET", "/bad", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(body, "could not be decoded") {
		t.Errorf("body = %s", body)
	}
}

// TestMockHeaderIsSkippedOnReplay pins the one place skipHeaders repeats a
// value that lives in a constant: if MockHeader is ever renamed, a recorded
// X-AUK-Mock from a mock-of-a-mock would start stacking values.
func TestMockHeaderIsSkippedOnReplay(t *testing.T) {
	if !skipHeaders[strings.ToLower(MockHeader)] {
		t.Fatalf("skipHeaders is missing %q — rename it there too", strings.ToLower(MockHeader))
	}
	fs := newStore(t)
	seed(t, fs, "r-nest", "GET", "https://api.example.com/nested", jsonResp(200, `{}`,
		model.KeyValue{Key: MockHeader, Value: "1", Enabled: true}))
	srv := startServer(t, fs)
	resp, _ := do(t, srv, "GET", "/nested", nil)
	if got := resp.Header.Values(MockHeader); len(got) != 1 {
		t.Errorf("%s = %v, want exactly one value", MockHeader, got)
	}
}
