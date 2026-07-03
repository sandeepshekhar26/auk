package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/appcore"
	"apitool/internal/core/model"
	"apitool/internal/storage"
)

// newTestServerAndClient wires the real MCP server against a temp file store,
// connects an in-memory client, and returns the initialized client session.
func newTestServerAndClient(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	srv := New(engine, store)

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

func seedWorkspace(t *testing.T, dir, url string) (wsID, reqID string) {
	t.Helper()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	wsID = "ws-1"
	if err := store.PutWorkspace(model.Workspace{ID: wsID, Name: "Test WS", OrderKey: "a0"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	reqID = "req-1"
	if err := store.PutRequest(model.RequestDef{
		ID: reqID, WorkspaceID: wsID, Name: "Ping", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: url, OrderKey: "a0",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	return wsID, reqID
}

func TestMCPToolsListed(t *testing.T) {
	cs := newTestServerAndClient(t, t.TempDir())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{"list_workspaces": false, "list_requests": false, "run_request": false, "run_perf_test": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q not advertised by the server", name)
		}
	}
}

func TestMCPRunRequestEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Probe", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"pong":true}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, reqID := seedWorkspace(t, dir, srv.URL)
	cs := newTestServerAndClient(t, dir)

	// list_workspaces returns our seeded workspace.
	lw, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_workspaces"})
	if err != nil {
		t.Fatalf("call list_workspaces: %v", err)
	}
	if lw.IsError {
		t.Fatalf("list_workspaces returned error: %v", lw.Content)
	}

	// run_request executes it and returns the real response.
	rr, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_request",
		Arguments: map[string]any{"requestId": reqID},
	})
	if err != nil {
		t.Fatalf("call run_request: %v", err)
	}
	if rr.IsError {
		t.Fatalf("run_request returned error: %v", rr.Content)
	}
	out, ok := rr.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected structured content, got %T", rr.StructuredContent)
	}
	if status, _ := out["status"].(float64); int(status) != 200 {
		t.Errorf("expected status 200, got %v", out["status"])
	}
	if body, _ := out["body"].(string); body != `{"pong":true}` {
		t.Errorf("expected pong body, got %q", body)
	}
}

func TestMCPRunRequestMissingIDErrors(t *testing.T) {
	cs := newTestServerAndClient(t, t.TempDir())
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_request",
		Arguments: map[string]any{"requestId": ""},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected an is_error result for a missing requestId")
	}
}

// Guard against a hang: the whole suite must finish quickly.
func TestMain(m *testing.M) {
	done := make(chan int, 1)
	go func() { done <- m.Run() }()
	select {
	case code := <-done:
		if code != 0 {
			panic("tests failed")
		}
	case <-time.After(60 * time.Second):
		panic("MCP tests timed out")
	}
}
