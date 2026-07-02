// Command apitool-mcp is the Model Context Protocol server: it exposes the
// user's saved API requests as MCP tools so Claude Code (or any MCP client)
// can list and run them, and run load tests against them. It is a headless
// consumer of the exact same core.Engine the GUI drives (docs/02-architecture.md
// §1 — one engine, N consumers), reading the git-friendly workspace files that
// are the source of truth.
//
// Add to Claude Code with:
//
//	claude mcp add apitool -- /path/to/apitool-mcp
//
// or in .mcp.json:
//
//	{ "mcpServers": { "apitool": { "command": "/path/to/apitool-mcp" } } }
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/perf"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apitool-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := appcore.DefaultWorkspaceDir()
	if v := os.Getenv("APITOOL_WORKSPACE_DIR"); v != "" {
		dir = v
	}

	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		return fmt.Errorf("open workspace %q: %w", dir, err)
	}

	srv := newServer(engine, store)
	// StdioTransport is what Claude Code's .mcp.json launches by default:
	// the client speaks JSON-RPC over this process's stdin/stdout.
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func newServer(engine *core.Engine, store appStore) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "apitool", Version: version}, nil)
	h := &handlers{engine: engine, store: store}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List all API workspaces (top-level collections of saved requests).",
	}, h.listWorkspaces)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_requests",
		Description: "List the saved API requests in a workspace (id, name, method, url). Call list_workspaces first to get a workspaceId.",
	}, h.listRequests)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_request",
		Description: "Execute a saved API request by id and return the response status, headers, and body. Optionally resolve variables against an environment.",
	}, h.runRequest)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_perf_test",
		Description: "Run a k6 load test against a saved request and return throughput, latency percentiles, and SLA-threshold pass/fail. Requires the k6 binary to be available.",
	}, h.runPerfTest)

	return s
}

// appStore is the subset of storage.FileStore the MCP tools need for listing
// (the engine handles execution). Declared as an interface so the server is
// testable with a fake.
type appStore interface {
	ListWorkspaces() []model.Workspace
	ListRequests(workspaceID model.ID) []model.RequestDef
	GetRequest(id model.ID) (model.RequestDef, error)
}

type handlers struct {
	engine *core.Engine
	store  appStore
}

// ---- list_workspaces ----

type listWorkspacesIn struct{}

type workspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listWorkspacesOut struct {
	Workspaces []workspaceInfo `json:"workspaces"`
}

func (h *handlers) listWorkspaces(_ context.Context, _ *mcp.CallToolRequest, _ listWorkspacesIn) (*mcp.CallToolResult, listWorkspacesOut, error) {
	ws := h.store.ListWorkspaces()
	out := listWorkspacesOut{Workspaces: make([]workspaceInfo, 0, len(ws))}
	for _, w := range ws {
		out.Workspaces = append(out.Workspaces, workspaceInfo{ID: w.ID, Name: w.Name})
	}
	sort.Slice(out.Workspaces, func(i, j int) bool { return out.Workspaces[i].Name < out.Workspaces[j].Name })
	return nil, out, nil
}

// ---- list_requests ----

type listRequestsIn struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"the id of the workspace to list requests from"`
}

type requestInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Method   string `json:"method"`
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
}

type listRequestsOut struct {
	Requests []requestInfo `json:"requests"`
}

func (h *handlers) listRequests(_ context.Context, _ *mcp.CallToolRequest, in listRequestsIn) (*mcp.CallToolResult, listRequestsOut, error) {
	reqs := h.store.ListRequests(in.WorkspaceID)
	out := listRequestsOut{Requests: make([]requestInfo, 0, len(reqs))}
	for _, r := range reqs {
		out.Requests = append(out.Requests, requestInfo{
			ID: r.ID, Name: r.Name, Method: r.Method, URL: r.URL, Protocol: string(r.Protocol),
		})
	}
	sort.Slice(out.Requests, func(i, j int) bool { return out.Requests[i].Name < out.Requests[j].Name })
	return nil, out, nil
}

// ---- run_request ----

type runRequestIn struct {
	RequestID     string `json:"requestId" jsonschema:"the id of the saved request to run"`
	EnvironmentID string `json:"environmentId,omitempty" jsonschema:"optional environment id to resolve variables against"`
}

type headerKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type runRequestOut struct {
	Status     int        `json:"status"`
	StatusText string     `json:"statusText"`
	Headers    []headerKV `json:"headers"`
	Body       string     `json:"body"`
	TimingMs   int64      `json:"timingMs"`
	Error      string     `json:"error,omitempty"`
}

func (h *handlers) runRequest(ctx context.Context, _ *mcp.CallToolRequest, in runRequestIn) (*mcp.CallToolResult, runRequestOut, error) {
	if in.RequestID == "" {
		return nil, runRequestOut{}, fmt.Errorf("requestId is required")
	}
	// origin "mcp" records at the Dispatch policy chokepoint that this run was
	// agent-initiated (docs/02-architecture.md §MCP). The default policy
	// allows it (the user configured this server); a stricter production-gate
	// policy plugs in at engine.Policy without changing this call.
	resp, err := h.engine.RunRequest(ctx, uuid.NewString(), in.RequestID, in.EnvironmentID, "mcp", core.NoopSink{})
	if err != nil {
		return nil, runRequestOut{}, err
	}

	body, decErr := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if decErr != nil {
		body = []byte(resp.BodyBase64)
	}
	out := runRequestOut{
		Status:     resp.Status,
		StatusText: resp.StatusText,
		Headers:    make([]headerKV, 0, len(resp.Headers)),
		Body:       truncate(string(body), 100_000),
		TimingMs:   resp.TimingMs,
		Error:      resp.Error,
	}
	for _, hh := range resp.Headers {
		out.Headers = append(out.Headers, headerKV{Key: hh.Key, Value: hh.Value})
	}
	return nil, out, nil
}

// ---- run_perf_test ----

type runPerfTestIn struct {
	RequestID     string `json:"requestId" jsonschema:"the id of the saved request to load test"`
	EnvironmentID string `json:"environmentId,omitempty" jsonschema:"optional environment id to resolve variables against"`
	VUs           int    `json:"vus,omitempty" jsonschema:"number of virtual users (concurrent clients); default 10"`
	Duration      string `json:"duration,omitempty" jsonschema:"test duration like 30s or 1m; default 30s"`
}

func (h *handlers) runPerfTest(ctx context.Context, _ *mcp.CallToolRequest, in runPerfTestIn) (*mcp.CallToolResult, model.PerfResult, error) {
	if in.RequestID == "" {
		return nil, model.PerfResult{}, fmt.Errorf("requestId is required")
	}
	runner, err := perf.NewRunner()
	if err != nil {
		return nil, model.PerfResult{}, err
	}
	_, resolved, err := h.engine.ResolveForExecution(ctx, in.RequestID, in.EnvironmentID, "mcp")
	if err != nil {
		return nil, model.PerfResult{}, err
	}

	cfg := model.PerfConfig{Executor: model.PerfConstantVUs, VUs: in.VUs, Duration: in.Duration}
	// If the saved request carries its own perf config (thresholds etc.),
	// prefer it, letting the tool args override VUs/duration when provided.
	if req, gErr := h.store.GetRequest(in.RequestID); gErr == nil && req.Perf != nil {
		cfg = *req.Perf
		if in.VUs > 0 {
			cfg.VUs = in.VUs
		}
		if in.Duration != "" {
			cfg.Duration = in.Duration
		}
	}

	res, err := runner.Run(ctx, in.RequestID, resolved, cfg, core.NoopSink{})
	if err != nil {
		return nil, res, err
	}
	return nil, res, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…(truncated, %d bytes total)", len(s))
}
