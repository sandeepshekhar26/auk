package main

import (
	"context"

	"github.com/google/uuid"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	httpprotocol "apitool/internal/protocols/http"
	"apitool/internal/storage"
	"apitool/internal/templating"
)

// App is the Wails-bound adapter over the headless core.Engine. Every method
// here is a thin call into the engine — the engine itself has zero Wails
// imports, which is what lets the exact same RunRequest path be reused by
// cmd/cli and the future MCP server (docs/02-architecture.md §3).
type App struct {
	ctx    context.Context
	store  *storage.MemoryStore
	engine *core.Engine
}

func NewApp() *App {
	store := storage.NewMemoryStore()
	engine := core.NewEngine(store, templating.New(), auth.New(), nil)
	engine.RegisterProtocol(httpprotocol.New())

	app := &App{store: store, engine: engine}
	app.seedDemoData()
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// seedDemoData gives a first-run user something real to look at and send —
// swapped for "load the last-opened workspace from disk" once
// internal/storage grows a YAML-file-backed Store.
func (a *App) seedDemoData() {
	wsID := uuid.NewString()
	a.store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo Workspace", OrderKey: "a0"})

	envID := uuid.NewString()
	a.store.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "baseUrl", Value: "https://httpbin.org", Enabled: true}},
	})

	a.store.PutRequest(model.RequestDef{
		ID: uuid.NewString(), WorkspaceID: wsID, Name: "GET httpbin",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: "https://httpbin.org/get",
		OrderKey: "a0",
	})
}

// ListWorkspaces is bound to the frontend.
func (a *App) ListWorkspaces() []model.Workspace {
	return a.store.ListWorkspaces()
}

// ListRequests is bound to the frontend.
func (a *App) ListRequests(workspaceID string) []model.RequestDef {
	return a.store.ListRequests(workspaceID)
}

// ListEnvironments is bound to the frontend.
func (a *App) ListEnvironments(workspaceID string) []model.Environment {
	return a.store.ListEnvironments(workspaceID)
}

// ListHistory is bound to the frontend.
func (a *App) ListHistory() []model.HistoryEntry {
	return a.store.ListHistory()
}

// SendRequest runs one request through the shared engine — origin "gui"
// distinguishes this call at the policy Dispatch chokepoint from a future
// MCP-initiated run, which will use a stricter PolicyEngine.
func (a *App) SendRequest(requestID string, environmentID string) (model.ResponseData, error) {
	sessionID := uuid.NewString()
	return a.engine.RunRequest(a.ctx, sessionID, requestID, environmentID, "gui", core.NoopSink{})
}
