package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/appcore"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/importer"
	"apitool/internal/mcpserver"
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

	// Embedded MCP server state (Settings → MCP Server).
	mcpMu    sync.Mutex
	mcpHTTP  *mcpserver.HTTPServer
	mcpError string

	// approvals tracks MCP-initiated mutating requests waiting on the in-app
	// Allow/Deny modal, keyed by approval id.
	approvalMu sync.Mutex
	approvals  map[string]chan bool
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

	app := &App{
		store:       store,
		engine:      engine,
		perfCancels: map[string]context.CancelFunc{},
		approvals:   map[string]chan bool{},
	}
	// Replace the default allow-all policy: GUI/CLI/chain origins stay
	// unrestricted, but MCP-initiated mutating requests must be approved in
	// the app (docs/02-architecture.md §MCP — user-presence gating at the
	// Dispatch chokepoint, which scripts and chained sends also pass through).
	engine.Policy = &approvalPolicy{app: app}
	app.seedDemoData()
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.GetSettings().MCPEnabled {
		if err := a.startMCP(); err != nil {
			a.mcpMu.Lock()
			a.mcpError = err.Error()
			a.mcpMu.Unlock()
		}
	}
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

// DetectImportFormat classifies pasted content ("curl" | "openapi" |
// "postman" | "") so the import UI can preview what it's about to create.
func (a *App) DetectImportFormat(content string) string {
	return importer.Detect(content)
}

// ImportCollection auto-detects the format of content (cURL, OpenAPI, or
// Postman) and persists it as a NEW workspace: it mints a workspace id,
// re-parents every folder/request/environment onto it, assigns merge-safe
// order keys, and writes them all to the file store. Returns the new
// workspace id so the frontend can switch to it.
func (a *App) ImportCollection(content string) (string, error) {
	res, err := importer.Import(content)
	if err != nil {
		return "", err
	}

	wsID := uuid.NewString()
	if err := a.store.PutWorkspace(model.Workspace{ID: wsID, Name: res.WorkspaceName, OrderKey: "a0"}); err != nil {
		return "", err
	}

	for _, f := range res.Folders {
		f.WorkspaceID = wsID
		if err := a.store.PutFolder(f); err != nil {
			return "", fmt.Errorf("import folder %q: %w", f.Name, err)
		}
	}
	for _, r := range res.Requests {
		r.WorkspaceID = wsID
		if err := a.store.PutRequest(r); err != nil {
			return "", fmt.Errorf("import request %q: %w", r.Name, err)
		}
	}
	for _, e := range res.Environments {
		e.WorkspaceID = wsID
		// Imported environments carry no secrets (values came from a plaintext
		// spec/collection), so no secretValues map is needed.
		if err := a.store.PutEnvironment(e, nil); err != nil {
			return "", fmt.Errorf("import environment %q: %w", e.Name, err)
		}
	}

	return wsID, nil
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

// ---- Embedded MCP server (Settings → MCP Server) ----

// defaultMCPPort is fixed (not ephemeral) so a saved `claude mcp add` config
// keeps working across app restarts.
const defaultMCPPort = 8724

// mcpTokenPath stores the bearer token, generated once and reused across
// restarts (0600 — it guards the loopback endpoint, so it must not change on
// every launch or saved MCP client configs would break).
func mcpTokenPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".apitool", "mcp-token")
	}
	return "mcp-token"
}

func loadOrCreateMCPToken() (string, error) {
	path := mcpTokenPath()
	if b, err := os.ReadFile(path); err == nil {
		if tok := string(bytes.TrimSpace(b)); tok != "" {
			return tok, nil
		}
	}
	tok, err := mcpserver.NewToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func (a *App) startMCP() error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if a.mcpHTTP != nil {
		return nil
	}
	token, err := loadOrCreateMCPToken()
	if err != nil {
		return fmt.Errorf("mcp token: %w", err)
	}
	port := a.GetSettings().MCPPort
	if port == 0 {
		port = defaultMCPPort
	}
	hs, err := mcpserver.StartHTTP(mcpserver.New(a.engine, a.store), port, token)
	if err != nil {
		return err
	}
	a.mcpHTTP = hs
	a.mcpError = ""
	return nil
}

func (a *App) stopMCP() {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if a.mcpHTTP != nil {
		a.mcpHTTP.Stop()
		a.mcpHTTP = nil
	}
}

// MCPStatus is what the Settings UI renders.
type MCPStatus struct {
	Running        bool   `json:"running"`
	URL            string `json:"url"`
	Token          string `json:"token"`
	ConnectCommand string `json:"connectCommand"`
	Error          string `json:"error,omitempty"`
}

// GetMCPStatus reports the embedded server's state, including the exact
// `claude mcp add` command to paste.
func (a *App) GetMCPStatus() MCPStatus {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	st := MCPStatus{Error: a.mcpError}
	if a.mcpHTTP != nil {
		st.Running = true
		st.URL = a.mcpHTTP.URL
		st.Token = a.mcpHTTP.Token
		st.ConnectCommand = fmt.Sprintf(
			`claude mcp add --transport http apitool %s --header "Authorization: Bearer %s"`,
			a.mcpHTTP.URL, a.mcpHTTP.Token,
		)
	}
	return st
}

// SetMCPEnabled starts/stops the embedded server and persists the choice so
// it survives restarts. Returns the resulting status.
func (a *App) SetMCPEnabled(enabled bool) MCPStatus {
	s := a.GetSettings()
	s.MCPEnabled = enabled
	_ = storage.SaveSettings(storage.DefaultSettingsPath(), s)

	if enabled {
		if err := a.startMCP(); err != nil {
			a.mcpMu.Lock()
			a.mcpError = err.Error()
			a.mcpMu.Unlock()
		}
	} else {
		a.stopMCP()
	}
	return a.GetMCPStatus()
}

// ---- MCP approval gating ----

// mutatingMethods are the HTTP methods that require user presence when an
// MCP client initiates the request. Reads (GET/HEAD/OPTIONS) pass freely.
var mutatingMethods = map[string]bool{
	"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// approvalPolicy is the engine's PolicyEngine for the GUI process: human-
// initiated origins pass, MCP-initiated mutating requests block on an in-app
// Allow/Deny modal (60s timeout → deny). Because this sits at the engine's
// Dispatch chokepoint, chained auto-sends triggered by an MCP run are gated
// too — an agent can't launder a DELETE through a response() reference.
type approvalPolicy struct {
	app *App
}

func (p *approvalPolicy) Authorize(ctx context.Context, dc core.DispatchContext) (core.Decision, error) {
	if dc.Origin != "mcp" && dc.Origin != "chain-mcp" {
		return core.Decision{Allow: true}, nil
	}
	if !mutatingMethods[dc.Method] {
		return core.Decision{Allow: true}, nil
	}

	id := uuid.NewString()
	ch := make(chan bool, 1)
	p.app.approvalMu.Lock()
	p.app.approvals[id] = ch
	p.app.approvalMu.Unlock()
	defer func() {
		p.app.approvalMu.Lock()
		delete(p.app.approvals, id)
		p.app.approvalMu.Unlock()
	}()

	wailsruntime.EventsEmit(p.app.ctx, "mcp:approval", map[string]string{
		"id":     id,
		"method": dc.Method,
		"url":    dc.URL,
	})

	select {
	case allowed := <-ch:
		if allowed {
			return core.Decision{Allow: true}, nil
		}
		return core.Decision{Allow: false, Reason: "denied by user"}, nil
	case <-time.After(60 * time.Second):
		return core.Decision{Allow: false, Reason: "approval timed out (no user response)"}, nil
	case <-ctx.Done():
		return core.Decision{Allow: false, Reason: "request cancelled"}, nil
	}
}

// RespondMCPApproval resolves a pending approval from the modal.
func (a *App) RespondMCPApproval(id string, allow bool) {
	a.approvalMu.Lock()
	ch := a.approvals[id]
	a.approvalMu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}
