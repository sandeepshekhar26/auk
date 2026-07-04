package core

import (
	"context"
	"fmt"

	"apitool/internal/core/model"
)

// Protocol is implemented once per wire protocol (HTTP, WebSocket, gRPC,
// GraphQL, SSE). The engine never speaks a protocol directly — it always
// goes through this interface, which is exactly what makes "run a request"
// identical whether the GUI, the CLI, or the MCP server initiated it.
type Protocol interface {
	Kind() model.ProtocolKind
	Execute(ctx context.Context, sess *Session, req model.RequestDef, resolved ResolvedRequest) (model.ResponseData, error)
}

// ResolvedRequest is a RequestDef with all templates expanded and auth
// applied — the only shape a Protocol implementation ever sees. Protocols
// never touch the template engine or environment directly.
type ResolvedRequest struct {
	URL     string
	Method  string
	Headers []model.KeyValue
	Params  []model.KeyValue
	Body    *model.RequestBody
}

// Templater resolves `${func(args)}` / response() references against an
// environment + prior-response cache into a ResolvedRequest.
type Templater interface {
	Resolve(ctx context.Context, req model.RequestDef, env *model.Environment, history ResponseLookup) (ResolvedRequest, error)
}

// ResponseLookup lets the templater/chaining DAG pull a previous response
// by request id (for `response('Other').body.token`-style references).
type ResponseLookup interface {
	Lookup(requestID model.ID) (model.ResponseData, bool)
}

// AuthApplier mutates a ResolvedRequest to add credentials (Basic/Bearer/API
// key/JWT/OAuth2). Kept separate from Templater so auth methods can be
// implemented and tested independently.
type AuthApplier interface {
	Apply(ctx context.Context, auth model.AuthConfig, req ResolvedRequest) (ResolvedRequest, error)
}

// Asserter evaluates a request's declarative assertions against a response.
// Optional (engine.Asserter may be nil) and kept an interface so core doesn't
// import the assert package — the adapter (appcore) injects it.
type Asserter interface {
	Evaluate(assertions []model.Assertion, resp model.ResponseData) []model.AssertionResult
}

// Scripter runs a request's optional pre-request script against its already
// resolved (templated + auth-applied) shape. It runs strictly BEFORE the
// Dispatch policy check in resolveAndAuthorize — the script can shape the
// request (e.g. add a computed signature header) but every request it
// produces still passes through the exact same Authorize() call every other
// origin does, so this can't become a way to bypass approval gating.
// Optional (engine.Scripter may be nil) and kept an interface so core
// doesn't import a JS runtime — the adapter (appcore) injects the sobek-
// backed implementation from internal/scripting.
type Scripter interface {
	RunPreRequest(ctx context.Context, script string, resolved ResolvedRequest) (ResolvedRequest, error)
}

// Store is the storage-layer contract the engine depends on (YAML files as
// source of truth + SQLite cache behind it) — see internal/storage.
type Store interface {
	GetRequest(id model.ID) (model.RequestDef, error)
	GetEnvironment(id model.ID) (*model.Environment, error)
	SaveResponse(model.ResponseData) error
	AppendHistory(model.HistoryEntry) error
	// LookupRequestByName resolves a request chaining reference like
	// response('Other Request').body.token, which addresses the target by
	// display name (scoped to one workspace) rather than by id.
	LookupRequestByName(workspaceID model.ID, name string) (model.RequestDef, error)
	// ListFolders backs folder-scoped variable resolution (resolveAndAuthorize
	// walks a request's folder chain via this).
	ListFolders(workspaceID model.ID) []model.Folder
}

// DispatchContext carries everything the PolicyEngine needs to decide
// whether an outbound request is allowed to fire. EVERY outbound request —
// GUI click, CLI run, MCP tool call, or a script's ctx.sendRequest() —
// passes through PolicyEngine.Authorize with one of these. This is the
// chokepoint that closes the "script bypasses approval" hole from the
// architecture critique (docs/04-architecture-critique.md).
type DispatchContext struct {
	Origin      string // "gui" | "cli" | "mcp" | "script"
	RequestID   model.ID
	Method      string
	URL         string
	Environment string
}

type Decision struct {
	Allow  bool
	Reason string
}

type PolicyEngine interface {
	Authorize(ctx context.Context, dc DispatchContext) (Decision, error)
}

// AllowAllPolicy is the default for the GUI (a human already clicked Send)
// and for the CLI (non-interactive, trusted by definition). MCP wires a
// stricter PolicyEngine that gates mutating/production requests.
type AllowAllPolicy struct{}

func (AllowAllPolicy) Authorize(context.Context, DispatchContext) (Decision, error) {
	return Decision{Allow: true}, nil
}

// Engine is the single headless execution core reused identically by the
// GUI, the CLI runner, and the MCP server (docs/02-architecture.md §1).
type Engine struct {
	Store     Store
	Templater Templater
	Auth      AuthApplier
	Asserter  Asserter // optional; nil skips assertion evaluation
	Scripter  Scripter // optional; nil skips pre-request scripting
	Policy    PolicyEngine
	Protocols map[model.ProtocolKind]Protocol
	Sessions  *Registry
}

func NewEngine(store Store, templater Templater, auth AuthApplier, policy PolicyEngine) *Engine {
	if policy == nil {
		policy = AllowAllPolicy{}
	}
	return &Engine{
		Store:     store,
		Templater: templater,
		Auth:      auth,
		Policy:    policy,
		Protocols: make(map[model.ProtocolKind]Protocol),
		Sessions:  NewRegistry(),
	}
}

func (e *Engine) RegisterProtocol(p Protocol) {
	e.Protocols[p.Kind()] = p
}

// RunRequest is THE code path: GUI Send button, CLI `run`, and MCP
// `run_request` all call exactly this. It resolves templates, applies auth,
// authorizes at the Dispatch chokepoint, executes via the matching Protocol,
// and persists the result — identically every time, parameterized only by
// origin (see DispatchContext).
func (e *Engine) RunRequest(ctx context.Context, sessionID model.ID, requestID model.ID, environmentID model.ID, origin string, sink EventSink) (model.ResponseData, error) {
	// Every request that runs — top-level or chained — joins the chain
	// bookkeeping so a response() ref reached from deeper in the chain can
	// detect "this would revisit a request already running in this chain"
	// even on its first hop (origin=="gui"/"cli"/"mcp" requests start a
	// fresh chain here; origin=="chain" requests already carry state
	// attached by ResolveChainRef and just extend it).
	ctx, err := withChainRequest(ctx, requestID)
	if err != nil {
		return model.ResponseData{}, err
	}

	req, resolved, err := e.resolveAndAuthorize(ctx, requestID, environmentID, origin)
	if err != nil {
		return model.ResponseData{}, err
	}

	protocol, ok := e.Protocols[req.Protocol]
	if !ok {
		return model.ResponseData{}, fmt.Errorf("no protocol registered for %q", req.Protocol)
	}

	sess := NewSession(sessionID, ctx, sink)
	e.Sessions.Put(sess)
	defer e.Sessions.Remove(sessionID)

	resp, err := protocol.Execute(sess.Context(), sess, req, resolved)
	if err != nil {
		return resp, err
	}

	// Feed any Set-Cookie headers into the templater's per-workspace cookie
	// jar so a later ${cookie(name)} reference in this workspace can read
	// them. Not part of the Templater interface (most callers/tests don't
	// need it) — a plain capability check, a no-op for any Templater that
	// doesn't implement it.
	if cc, ok := e.Templater.(interface {
		CaptureCookies(model.ID, []model.KeyValue)
	}); ok {
		cc.CaptureCookies(req.WorkspaceID, resp.Headers)
	}

	// Evaluate declarative assertions against the response. They ride on the
	// response object so every consumer (GUI card, CLI exit code, MCP result)
	// sees the same verdict from the same code path.
	if e.Asserter != nil && len(req.Assertions) > 0 {
		resp.AssertionResults = e.Asserter.Evaluate(req.Assertions, resp)
	}

	if err := e.Store.SaveResponse(resp); err != nil {
		return resp, fmt.Errorf("persist response: %w", err)
	}
	_ = e.Store.AppendHistory(model.HistoryEntry{
		ID:          resp.RequestID,
		RequestID:   req.ID,
		RequestName: req.Name,
		Method:      resolved.Method,
		URL:         resolved.URL,
		Status:      resp.Status,
		TimingMs:    resp.TimingMs,
		Timestamp:   resp.Timestamp,
	})

	return resp, nil
}

// resolveAndAuthorize runs the shared front half of any execution: load the
// request + environment, expand templates, apply auth, and pass through the
// Dispatch policy chokepoint. Both RunRequest and RunPerf (via
// ResolveForExecution) go through this, so a load test hits the exact same
// resolved URL/headers/auth a normal send would, and is gated by the same
// policy.
func (e *Engine) resolveAndAuthorize(ctx context.Context, requestID model.ID, environmentID model.ID, origin string) (model.RequestDef, ResolvedRequest, error) {
	req, err := e.Store.GetRequest(requestID)
	if err != nil {
		return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("load request: %w", err)
	}

	var env *model.Environment
	if environmentID != "" {
		env, err = e.Store.GetEnvironment(environmentID)
		if err != nil {
			return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("load environment: %w", err)
		}
	}

	// Folder-scoped variables sit between the workspace Environment and the
	// request: layer them into a shallow copy of env (appended after env's own
	// variables) so the existing "last write to the map wins" merge in
	// templating.Resolve gives the closest folder priority over the
	// environment, without templating needing to know folders exist at all.
	if folderVars := e.folderVariables(req.WorkspaceID, req.FolderID); len(folderVars) > 0 {
		merged := model.Environment{}
		if env != nil {
			merged = *env
		}
		merged.Variables = append(append([]model.KeyValue{}, merged.Variables...), folderVars...)
		env = &merged
	}

	resolved, err := e.Templater.Resolve(ctx, req, env, responseLookupFromStore{e.Store})
	if err != nil {
		return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("resolve templates: %w", err)
	}

	if req.Auth != nil && req.Auth.Kind != model.AuthNone {
		resolved, err = e.Auth.Apply(ctx, *req.Auth, resolved)
		if err != nil {
			return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("apply auth: %w", err)
		}
	}

	if e.Scripter != nil && req.PreRequestScript != "" {
		resolved, err = e.Scripter.RunPreRequest(ctx, req.PreRequestScript, resolved)
		if err != nil {
			return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("pre-request script: %w", err)
		}
	}

	decision, err := e.Policy.Authorize(ctx, DispatchContext{
		Origin:      origin,
		RequestID:   requestID,
		Method:      resolved.Method,
		URL:         resolved.URL,
		Environment: environmentID,
	})
	if err != nil {
		return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("policy check: %w", err)
	}
	if !decision.Allow {
		return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("blocked by policy: %s", decision.Reason)
	}

	return req, resolved, nil
}

// folderVariables walks a request's folder chain from its immediate parent up
// to the workspace root, returning every ancestor's enabled variables in
// ROOT-FIRST order — so appending them (in this order) after an environment's
// own variables, into the same "last write wins" map templating.Resolve
// already builds, gives a folder priority over the environment, and a nested
// folder priority over its own parent folder. Returns nil for a request with
// no folder (the common case), doing no work.
func (e *Engine) folderVariables(workspaceID model.ID, folderID *model.ID) []model.KeyValue {
	if folderID == nil {
		return nil
	}
	byID := make(map[model.ID]model.Folder)
	for _, f := range e.Store.ListFolders(workspaceID) {
		byID[f.ID] = f
	}

	var chain []model.Folder
	for id := folderID; id != nil; {
		f, ok := byID[*id]
		if !ok {
			break // dangling parentId (e.g. a deleted folder) — stop, don't error the whole send
		}
		chain = append(chain, f)
		id = f.ParentID
	}

	var vars []model.KeyValue
	for i := len(chain) - 1; i >= 0; i-- {
		for _, kv := range chain[i].Variables {
			if kv.Enabled {
				vars = append(vars, kv)
			}
		}
	}
	return vars
}

// ResolveForExecution exposes the resolve+auth+authorize front half for
// consumers that execute a request outside the Protocol path — notably the
// k6 perf runner, which needs the fully-resolved URL/headers/body to generate
// its load script but runs it in a separate process. Same policy chokepoint,
// origin recorded for the audit trail.
func (e *Engine) ResolveForExecution(ctx context.Context, requestID model.ID, environmentID model.ID, origin string) (model.RequestDef, ResolvedRequest, error) {
	return e.resolveAndAuthorize(ctx, requestID, environmentID, origin)
}

type responseLookupFromStore struct{ store Store }

func (r responseLookupFromStore) Lookup(requestID model.ID) (model.ResponseData, bool) {
	// The store's response cache is keyed by request id for the "last
	// response" case that response()-style chaining relies on.
	type lastResponseStore interface {
		LastResponse(model.ID) (model.ResponseData, bool)
	}
	if lrs, ok := r.store.(lastResponseStore); ok {
		return lrs.LastResponse(requestID)
	}
	return model.ResponseData{}, false
}
