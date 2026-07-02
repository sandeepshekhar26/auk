package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/importer"
	"apitool/internal/perf"
	"apitool/internal/storage"
)

// App is the Wails-bound adapter over the headless core.Engine. Every method
// here is a thin call into the engine — the engine itself has zero Wails
// imports, which is what lets the exact same RunRequest path be reused by
// cmd/cli and the future MCP server (docs/02-architecture.md §3).
type App struct {
	ctx    context.Context
	store  *storage.FileStore
	engine *core.Engine

	// perfCancels lets StopPerfTest cancel an in-flight k6 run by request id.
	perfMu      sync.Mutex
	perfCancels map[string]context.CancelFunc
}

func NewApp() *App {
	// The GUI builds the exact same engine (same store dir, same protocols,
	// same chain-resolver wiring) that cmd/cli and cmd/mcp build, via the
	// shared appcore.NewEngine — one construction path, so behavior can't
	// drift between the three consumers (docs/02-architecture.md §1).
	engine, store, err := appcore.NewEngine(appcore.DefaultWorkspaceDir())
	if err != nil {
		// Only fails on an unwritable/unreadable rootDir — unrecoverable for a
		// GUI app that has nowhere else to persist to, so fail fast.
		panic(fmt.Errorf("init file store: %w", err))
	}

	app := &App{store: store, engine: engine, perfCancels: map[string]context.CancelFunc{}}
	app.seedDemoData()
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// seedDemoData gives a first-run user something real to look at and send.
// It only runs when the file store has no workspaces yet (first launch, or
// a fresh ~/.apitool/workspace), so a returning user's saved data is never
// clobbered.
func (a *App) seedDemoData() {
	if len(a.store.ListWorkspaces()) > 0 {
		return
	}

	wsID := uuid.NewString()
	if err := a.store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo Workspace", OrderKey: "a0"}); err != nil {
		return
	}

	envID := uuid.NewString()
	_ = a.store.PutEnvironment(model.Environment{
		ID: envID, WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{{Key: "baseUrl", Value: "https://httpbin.org", Enabled: true}},
	}, nil)

	_ = a.store.PutRequest(model.RequestDef{
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

// ListFolders is bound to the frontend.
func (a *App) ListFolders(workspaceID string) []model.Folder {
	return a.store.ListFolders(workspaceID)
}

// ListEnvironments is bound to the frontend.
func (a *App) ListEnvironments(workspaceID string) []model.Environment {
	return a.store.ListEnvironments(workspaceID)
}

// ListHistory is bound to the frontend.
func (a *App) ListHistory() []model.HistoryEntry {
	entries, err := a.store.ListHistory()
	if err != nil {
		return nil
	}
	return entries
}

// SendRequest runs one request through the shared engine — origin "gui"
// distinguishes this call at the policy Dispatch chokepoint from a future
// MCP-initiated run, which will use a stricter PolicyEngine.
func (a *App) SendRequest(requestID string, environmentID string) (model.ResponseData, error) {
	sessionID := uuid.NewString()
	return a.engine.RunRequest(a.ctx, sessionID, requestID, environmentID, "gui", core.NoopSink{})
}

// CreateRequest persists a new request definition. The caller is expected to
// have already assigned an ID (uuid, generated client-side or via a prior
// round trip) — matching this codebase's convention that ID generation
// happens at the call site, not inside storage.
func (a *App) CreateRequest(req model.RequestDef) error {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	return a.store.PutRequest(req)
}

// UpdateRequest overwrites an existing request definition. Same
// write-through semantics as CreateRequest — PutRequest is create-or-replace,
// so this is intentionally the same call.
func (a *App) UpdateRequest(req model.RequestDef) error {
	return a.store.PutRequest(req)
}

// ImportCurl parses a pasted cURL command into a RequestDef the frontend can
// preview/edit before saving via CreateRequest. It does not persist
// anything itself — matching internal/importer's design of staying
// storage-agnostic.
func (a *App) ImportCurl(command string) (model.RequestDef, error) {
	return importer.ParseCurl(command)
}

// CreateEnvironment persists a new environment. secretValues carries pending
// plaintext values for any variable name listed in env.Secrets; FileStore
// peels those off into the OS keychain and never writes them to the YAML
// file (docs/02-architecture.md §7).
func (a *App) CreateEnvironment(env model.Environment, secretValues map[string]string) error {
	if env.ID == "" {
		env.ID = uuid.NewString()
	}
	return a.store.PutEnvironment(env, secretValues)
}

// UpdateEnvironment overwrites an existing environment. Same
// create-or-replace semantics as CreateEnvironment.
func (a *App) UpdateEnvironment(env model.Environment, secretValues map[string]string) error {
	return a.store.PutEnvironment(env, secretValues)
}

// GetSettings returns app-level preferences (theme, ...). A missing
// settings file is a normal first run and yields defaults.
func (a *App) GetSettings() model.AppSettings {
	s, err := storage.LoadSettings(storage.DefaultSettingsPath())
	if err != nil {
		return model.AppSettings{Theme: "system"}
	}
	return s
}

// UpdateSettings persists app-level preferences.
func (a *App) UpdateSettings(s model.AppSettings) error {
	return storage.SaveSettings(storage.DefaultSettingsPath(), s)
}

// CheckK6 returns "" if a k6 binary is resolvable, or a human-readable reason
// it isn't, so the perf UI can tell the user to install/bundle k6 before they
// configure a whole load test.
func (a *App) CheckK6() string {
	if _, err := perf.ResolveK6(); err != nil {
		return err.Error()
	}
	return ""
}

// wailsPerfSink forwards coalesced (≤1/sec) perf sample points to the webview
// as Wails events. At this rate direct EventsEmit is safe — the
// high-frequency-stream caveat in docs/02-architecture.md §6 applies to
// per-frame WS/gRPC data, not to metrics the backend already bucketed to one
// point per second.
type wailsPerfSink struct {
	ctx       context.Context
	requestID string
}

func (s wailsPerfSink) Emit(e core.Event) {
	if e.Kind != "perf" {
		return
	}
	wailsruntime.EventsEmit(s.ctx, "perf:sample:"+s.requestID, string(e.Payload))
}

// RunPerfTest runs a k6 load test against a request, streaming live per-second
// samples to the webview via "perf:sample:<requestID>" events and returning
// the authoritative end-of-test result. The request is resolved through the
// same template + auth + policy path as a normal send (origin "gui"), so the
// load test hits exactly what the user sees.
func (a *App) RunPerfTest(requestID string, environmentID string, cfg model.PerfConfig) (model.PerfResult, error) {
	runner, err := perf.NewRunner()
	if err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, err
	}

	_, resolved, err := a.engine.ResolveForExecution(a.ctx, requestID, environmentID, "gui")
	if err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, err
	}

	ctx, cancel := context.WithCancel(a.ctx)
	a.perfMu.Lock()
	a.perfCancels[requestID] = cancel
	a.perfMu.Unlock()
	defer func() {
		cancel()
		a.perfMu.Lock()
		delete(a.perfCancels, requestID)
		a.perfMu.Unlock()
	}()

	return runner.Run(ctx, requestID, resolved, cfg, wailsPerfSink{ctx: a.ctx, requestID: requestID})
}

// StopPerfTest cancels an in-flight load test for a request (the Cancel
// button). k6 receives a kill; the partial result is still returned by the
// pending RunPerfTest call.
func (a *App) StopPerfTest(requestID string) {
	a.perfMu.Lock()
	cancel := a.perfCancels[requestID]
	a.perfMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
