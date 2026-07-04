package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core/model"
)

type echoIn struct {
	Message string `json:"message" jsonschema:"the text to echo back"`
}

type echoOut struct {
	Echoed string `json:"echoed"`
}

func newTestMCPServer(t *testing.T) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "echo",
		Description: "Echoes the given message back.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Echoed: in.Message}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "boom",
		Description: "Always fails, to test IsError handling.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, echoOut, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "boom: something went wrong"}},
		}, echoOut{}, nil
	})

	return srv
}

// newTestHTTPServer serves a real fixture MCP server over Streamable HTTP,
// optionally requiring a bearer token — this exercises the ACTUAL wire
// protocol (not an in-memory shortcut), including this package's own
// bearerRoundTripper, the same way the app's embedded MCP server is tested
// in internal/mcpserver/http_test.go.
func newTestHTTPServer(t *testing.T, requireToken string) *httptest.Server {
	t.Helper()
	srv := newTestMCPServer(t)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireToken != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || got != requireToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestBuildTransport_StdioRequiresCommand(t *testing.T) {
	_, err := buildTransport(model.McpConnection{Name: "x", Transport: model.McpTransportStdio})
	if err == nil {
		t.Fatal("expected an error for a stdio connection with no command")
	}
}

func TestBuildTransport_HTTPRequiresURL(t *testing.T) {
	_, err := buildTransport(model.McpConnection{Name: "x", Transport: model.McpTransportHTTP})
	if err == nil {
		t.Fatal("expected an error for an http connection with no url")
	}
}

func TestBuildTransport_UnknownTransportErrors(t *testing.T) {
	_, err := buildTransport(model.McpConnection{Name: "x", Transport: "carrier-pigeon"})
	if err == nil {
		t.Fatal("expected an error for an unknown transport kind")
	}
}

func TestConnect_HTTPListAndCallTools(t *testing.T) {
	ts := newTestHTTPServer(t, "")
	conn := model.McpConnection{Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp"}

	client, err := Connect(context.Background(), conn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}

	var echoTool *ToolInfo
	for i := range tools {
		if tools[i].Name == "echo" {
			echoTool = &tools[i]
		}
	}
	if echoTool == nil {
		t.Fatalf("expected an 'echo' tool in %+v", tools)
	}
	if !echoTool.ReadOnlyHint {
		t.Errorf("expected echo tool to be flagged ReadOnlyHint, got %+v", echoTool)
	}
	if echoTool.DestructiveHint {
		t.Errorf("a ReadOnlyHint tool must never also read as DestructiveHint, got %+v", echoTool)
	}

	res, err := client.CallTool(context.Background(), "echo", `{"message":"hi there"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected a successful call, got IsError=true: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("expected one text content block, got %+v", res.Content)
	}
}

func TestCallTool_ServerSideErrorReportsIsError(t *testing.T) {
	ts := newTestHTTPServer(t, "")
	conn := model.McpConnection{Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp"}

	client, err := Connect(context.Background(), conn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	res, err := client.CallTool(context.Background(), "boom", `{"message":"anything"}`)
	if err != nil {
		t.Fatalf("CallTool should not return a Go error for a tool-level failure: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true, got %+v", res)
	}
	if len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "boom") {
		t.Fatalf("expected the failure text in content, got %+v", res.Content)
	}
}

func TestCallTool_InvalidArgumentsJSONErrors(t *testing.T) {
	ts := newTestHTTPServer(t, "")
	conn := model.McpConnection{Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp"}
	client, err := Connect(context.Background(), conn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	if _, err := client.CallTool(context.Background(), "echo", "not json"); err == nil {
		t.Fatal("expected an error for malformed arguments JSON")
	}
}

func TestConnect_HTTPBearerToken(t *testing.T) {
	ts := newTestHTTPServer(t, "secret-token")

	// Wrong/missing token must fail to connect.
	_, err := Connect(context.Background(), model.McpConnection{
		Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp",
	})
	if err == nil {
		t.Fatal("expected Connect to fail without the required bearer token")
	}

	// The right token must succeed.
	client, err := Connect(context.Background(), model.McpConnection{
		Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp", BearerToken: "secret-token",
	})
	if err != nil {
		t.Fatalf("Connect with the correct bearer token: %v", err)
	}
	defer client.Close()

	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools after authenticated connect: %v", err)
	}
}

func TestClose_EndsSession(t *testing.T) {
	ts := newTestHTTPServer(t, "")
	conn := model.McpConnection{Name: "fixture", Transport: model.McpTransportHTTP, URL: ts.URL + "/mcp"}
	client, err := Connect(context.Background(), conn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("expected ListTools to fail after Close")
	}
}
