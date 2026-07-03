// Package mcpserver holds the ONE implementation of the app's MCP tool
// surface (list_workspaces, list_requests, run_request, run_perf_test).
// Both entrypoints terminate here (docs/02-architecture.md §MCP):
//   - cmd/mcp: the stdio binary launched by .mcp.json / `claude mcp add`
//   - the GUI's embedded Streamable-HTTP server (Settings → MCP Server),
//     which drives the LIVE app state and gates mutating calls behind the
//     in-app approval modal via the engine's Dispatch policy chokepoint.
package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/perf"
)

// Version is reported to MCP clients in the initialize handshake.
const Version = "0.1.0"

// Store is the read-only subset of storage the listing tools need (the
// engine handles all execution). An interface so tests can use a fake.
type Store interface {
	ListWorkspaces() []model.Workspace
	ListRequests(workspaceID model.ID) []model.RequestDef
	GetRequest(id model.ID) (model.RequestDef, error)
}

// New builds the MCP server with the full tool surface wired to the given
// engine + store. Every run_request/run_perf_test call passes the engine's
// Dispatch policy chokepoint with origin "mcp", so whatever PolicyEngine the
// host installed (allow-all for the headless stdio binary, approval-gated
// for the embedded GUI server) applies uniformly.
func New(engine *core.Engine, store Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "apitool", Version: Version}, nil)
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
		Description: "Execute a saved API request by id and return the response status, headers, body, and assertion results. Optionally resolve variables against an environment.",
	}, h.runRequest)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_perf_test",
		Description: "Run a k6 load test against a saved request and return throughput, latency percentiles, and SLA-threshold pass/fail. Requires the k6 binary to be available.",
	}, h.runPerfTest)

	return s
}

type handlers struct {
	engine *core.Engine
	store  Store
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

type assertionResult struct {
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Actual      string `json:"actual,omitempty"`
	Error       string `json:"error,omitempty"`
}

type runRequestOut struct {
	Status           int               `json:"status"`
	StatusText       string            `json:"statusText"`
	Headers          []headerKV        `json:"headers"`
	Body             string            `json:"body"`
	TimingMs         int64             `json:"timingMs"`
	AssertionsPassed *bool             `json:"assertionsPassed,omitempty"`
	Assertions       []assertionResult `json:"assertions,omitempty"`
	Error            string            `json:"error,omitempty"`
}

func (h *handlers) runRequest(ctx context.Context, _ *mcp.CallToolRequest, in runRequestIn) (*mcp.CallToolResult, runRequestOut, error) {
	if in.RequestID == "" {
		return nil, runRequestOut{}, fmt.Errorf("requestId is required")
	}
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
	if len(resp.AssertionResults) > 0 {
		allPassed := true
		for _, ar := range resp.AssertionResults {
			if !ar.Passed {
				allPassed = false
			}
			out.Assertions = append(out.Assertions, assertionResult{
				Description: assertionLabel(ar.Assertion),
				Passed:      ar.Passed,
				Actual:      ar.Actual,
				Error:       ar.Error,
			})
		}
		out.AssertionsPassed = &allPassed
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

// assertionLabel renders an assertion as a compact human-readable line for
// the MCP client, e.g. `body.user.id gt 10`.
func assertionLabel(a model.Assertion) string {
	target := string(a.Source)
	switch a.Source {
	case model.AssertBody:
		if a.Path != "" {
			target = "body." + a.Path
		}
	case model.AssertHeader:
		target = "header[" + a.Name + "]"
	}
	if a.Value == "" {
		return fmt.Sprintf("%s %s", target, a.Operator)
	}
	return fmt.Sprintf("%s %s %s", target, a.Operator, a.Value)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…(truncated, %d bytes total)", len(s))
}
