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

	req, err := e.Store.GetRequest(requestID)
	if err != nil {
		return model.ResponseData{}, fmt.Errorf("load request: %w", err)
	}

	var env *model.Environment
	if environmentID != "" {
		env, err = e.Store.GetEnvironment(environmentID)
		if err != nil {
			return model.ResponseData{}, fmt.Errorf("load environment: %w", err)
		}
	}

	resolved, err := e.Templater.Resolve(ctx, req, env, responseLookupFromStore{e.Store})
	if err != nil {
		return model.ResponseData{}, fmt.Errorf("resolve templates: %w", err)
	}

	if req.Auth != nil && req.Auth.Kind != model.AuthNone {
		resolved, err = e.Auth.Apply(ctx, *req.Auth, resolved)
		if err != nil {
			return model.ResponseData{}, fmt.Errorf("apply auth: %w", err)
		}
	}

	decision, err := e.Policy.Authorize(ctx, DispatchContext{
		Origin:    origin,
		RequestID: requestID,
		Method:    resolved.Method,
		URL:       resolved.URL,
	})
	if err != nil {
		return model.ResponseData{}, fmt.Errorf("policy check: %w", err)
	}
	if !decision.Allow {
		return model.ResponseData{}, fmt.Errorf("blocked by policy: %s", decision.Reason)
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
