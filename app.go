package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/importer"
	graphqlprotocol "apitool/internal/protocols/graphql"
	grpcprotocol "apitool/internal/protocols/grpc"
	httpprotocol "apitool/internal/protocols/http"
	sseprotocol "apitool/internal/protocols/sse"
	wsprotocol "apitool/internal/protocols/ws"
	"apitool/internal/storage"
	"apitool/internal/templating"
)

// App is the Wails-bound adapter over the headless core.Engine. Every method
// here is a thin call into the engine — the engine itself has zero Wails
// imports, which is what lets the exact same RunRequest path be reused by
// cmd/cli and the future MCP server (docs/02-architecture.md §3).
type App struct {
	ctx    context.Context
	store  *storage.FileStore
	engine *core.Engine
}

func NewApp() *App {
	store, err := storage.NewFileStore(defaultWorkspaceDir())
	if err != nil {
		// NewFileStore only fails on an unwritable/unreadable rootDir (mkdir
		// or an existing-workspace YAML load error) — both are unrecoverable
		// for a GUI app that has nowhere else to persist to, so fail fast
		// with a clear message instead of limping along with a nil store.
		panic(fmt.Errorf("init file store: %w", err))
	}

	// Engine is constructed first (with a nil Templater) because
	// templating.New needs the engine itself as its chain resolver for
	// response('Name').path auto-send refs — see internal/core/chaining.go.
	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.RegisterProtocol(httpprotocol.New())
	engine.RegisterProtocol(wsprotocol.New())
	engine.RegisterProtocol(sseprotocol.New())
	engine.RegisterProtocol(graphqlprotocol.New())
	engine.RegisterProtocol(grpcprotocol.New())

	app := &App{store: store, engine: engine}
	app.seedDemoData()
	return app
}

// defaultWorkspaceDir is ~/.apitool/workspace — a sibling of the
// ~/.apitool/history.jsonl path internal/storage.NewFileStore already
// defaults to, so everything this app persists lives under one well-known
// directory. Falls back to a "workspace" dir next to the binary's cwd if the
// home directory can't be resolved (e.g. a locked-down sandbox).
func defaultWorkspaceDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".apitool", "workspace")
	}
	return "workspace"
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
