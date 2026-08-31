package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

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
	// PathParams are the request's `:name` path-placeholder values with
	// their own templates already expanded (so a value can itself be
	// `${userId}`). The Templater fills these in; the engine consumes them
	// in resolveAndAuthorize to rewrite URL, so by the time a Protocol sees
	// this ResolvedRequest the substitution is already baked into URL and
	// no protocol has to know placeholders exist.
	PathParams []model.KeyValue
	// Auth is the request's auth config with every credential field already
	// `${...}`-templated: the deep COPY produced by AuthTemplater.ResolveAuth
	// (cloned again here), NEVER the *AuthConfig pointer the store holds, so
	// nothing downstream can rewrite the user's saved credentials. It is nil
	// when the request has no auth (or AuthNone), and nil for any caller that
	// builds a ResolvedRequest by hand.
	//
	// It exists for the one auth kind that cannot be reduced to a header up
	// front. HTTP Digest is challenge-response, so internal/auth deliberately
	// passes it through and internal/protocols/http answers the 401 at the
	// transport. Without this field that protocol had no resolved auth to read
	// and took the RAW credentials off model.RequestDef instead, so
	// `${digestPassword}` (or a keychain secret, or a 1Password `op://` ref)
	// was hashed as the LITERAL string and every templated Digest request
	// failed to authenticate. A Protocol must prefer this over req.Auth,
	// falling back to req.Auth only when this is nil.
	Auth *model.AuthConfig
}

// Templater resolves `${func(args)}` / response() references against an
// environment + prior-response cache into a ResolvedRequest.
type Templater interface {
	Resolve(ctx context.Context, req model.RequestDef, env *model.Environment, history ResponseLookup) (ResolvedRequest, error)
}

// AuthTemplater is an OPTIONAL extension a Templater may implement to also
// template `${...}` inside an auth config's credential fields (so a
// script-stored token works in the Auth tab, not just in a raw header).
// Kept separate from Templater so existing fakes compile unchanged; the
// engine type-asserts for it and falls back to the raw auth when absent.
type AuthTemplater interface {
	ResolveAuth(ctx context.Context, req model.RequestDef, env *model.Environment, history ResponseLookup, auth *model.AuthConfig) (*model.AuthConfig, error)
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

// PreRequestInput carries everything BESIDES the resolved request that a
// pre-request script may read — the variable set it sees through vars.get
// (with secret values already REDACTED, see variableMap), and the names it
// must not write (see PostResponseInput.Secrets).
type PreRequestInput struct {
	Vars    map[string]string
	Secrets []string
}

// PreRequestOutput is what a pre-request script produced: the (possibly
// header-edited) request plus the same variable writes and captured console
// output a post-response script can produce.
type PreRequestOutput struct {
	Resolved  ResolvedRequest
	VarWrites map[string]string
	VarUnsets []string
	Logs      []string
}

// PreRequestVarScripter is the richer pre-request entry point: same script,
// same moment in the pipeline, but with the variable set and console capture
// wired up. Kept as a SEPARATE optional interface rather than a change to
// Scripter.RunPreRequest so an existing Scripter implementation (including
// the fakes in this package's tests) keeps compiling untouched; the engine
// prefers this one when the injected Scripter implements it.
type PreRequestVarScripter interface {
	RunPreRequestWithVars(ctx context.Context, script string, resolved ResolvedRequest, in PreRequestInput) (PreRequestOutput, error)
}

// PostResponseInput is everything a post-response script is allowed to see.
// Response is a COPY — the script reports on the response, it never edits the
// one that gets stored or returned.
type PostResponseInput struct {
	Response model.ResponseData
	// Vars is the variable set the script reads through vars.get: the active
	// environment's variables, plus folder-scoped ones, plus anything an
	// earlier script wrote this session — i.e. what ${name} would have
	// resolved to for this request, MINUS every secret value (variableMap
	// redacts secret-backed names entirely).
	Vars map[string]string
	// Secrets are the variable names whose values live in the OS keychain. A
	// script can neither read them (variableMap omits the resolved value) nor
	// write them (vars.set/vars.unset refuse these names) — the keychain-backed
	// value never enters the script runtime and no git-stored file may contain
	// it. Signing that genuinely needs a secret uses a declarative auth kind
	// (SigV4/OAuth1), not a script.
	Secrets []string
}

// PostResponseOutput is what a post-response script produced.
type PostResponseOutput struct {
	// Tests is one entry per test() the script declared, in declaration
	// order. Populated even when the script later threw — the tests that
	// already ran are real results.
	Tests []model.TestResult
	// Vars is the full variable set as the script left it (input plus every
	// vars.set, minus every vars.unset). Handy for a caller that wants the
	// end state; the engine persists from the delta below instead, because
	// this set is a MERGED view (environment + folders + session) and writing
	// all of it back into one environment would copy in variables that never
	// belonged to it.
	Vars map[string]string
	// VarWrites/VarUnsets are the delta: exactly the names the script set or
	// unset, which is what the engine persists.
	VarWrites map[string]string
	VarUnsets []string
	// Logs is captured console.log output, in order. A script never writes
	// to the process stdout — the GUI, the CLI and the MCP server all own
	// their own output streams.
	Logs []string
}

// PostResponseScripter is the second half of scripting: a script run AFTER
// the response arrives, which declares tests and can write variables back
// (the auth-token chaining unlock). Optional and separate from Scripter for
// the same compile-compatibility reason as PreRequestVarScripter; the engine
// type-asserts for it exactly like it does for the Templater's cookie
// capture.
type PostResponseScripter interface {
	RunPostResponse(ctx context.Context, script string, in PostResponseInput) (PostResponseOutput, error)
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

	// scriptVars is the FALLBACK home for variables a script writes (see
	// sessionVars). envWriteMu serialises the read-modify-write AUK does on
	// an environment when it can persist one instead.
	scriptVars sessionVars
	envWriteMu sync.Mutex
}

// sessionVars holds variables written by scripts, keyed by workspace, for
// this process's lifetime only.
//
// It is the fallback path, not the primary one. When a request runs with an
// active environment the Store can write, a vars.set lands IN that
// environment — durable, visible in the environment editor, diffable in git —
// and nothing is kept here. With no environment selected (or a Store that
// can't persist one), the write lands here instead so that a folder run
// still chains a token from one request into the next within the session,
// which is the whole point of the feature. Either way the next request picks
// the value up through the same ${name} templating path.
type sessionVars struct {
	mu          sync.RWMutex
	byWorkspace map[model.ID]map[string]string
}

func (s *sessionVars) snapshot(workspaceID model.ID) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vars := s.byWorkspace[workspaceID]
	if len(vars) == 0 {
		return nil
	}
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}

func (s *sessionVars) set(workspaceID model.ID, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byWorkspace == nil {
		s.byWorkspace = make(map[model.ID]map[string]string)
	}
	if s.byWorkspace[workspaceID] == nil {
		s.byWorkspace[workspaceID] = make(map[string]string)
	}
	s.byWorkspace[workspaceID][name] = value
}

// forget drops names from the overlay — called after those names have been
// written to a real environment, so the (now stale) session copy can never
// shadow a value the user edits in the environment editor afterwards.
func (s *sessionVars) forget(workspaceID model.ID, names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vars := s.byWorkspace[workspaceID]
	if vars == nil {
		return
	}
	for _, name := range names {
		delete(vars, name)
	}
}

// RunScopedVars is the run-scoped variable layer a data-driven run threads
// through context (see WithRunScopedVars) so mergedEnvironment can consult it
// without the engine ever wrapping or mutating its Store. It exists for the
// lifetime of ONE run and is reachable only through the context the runner
// attaches it to, which gives two properties at once:
//
//   - a concurrent GUI/MCP send, whose context carries no layer, never sees the
//     iteration's variables (correct — they are run-scoped) and never races on
//     this struct;
//   - the engine's Store stays exactly what appcore wired, so every optional
//     interface the engine relies on (LastResponse for response() chaining,
//     PutEnvironment/ListEnvironmentsRaw for persistence) keeps working under a
//     data run.
//
// It holds two sub-layers with distinct precedence:
//
//	data   — the current iteration's data-file row (the iteration's INPUT)
//	writes — variables a script set DURING this iteration (unsets tracked too)
//
// A script write beats the data row (a login→token chain minted in iteration N
// must be usable by later requests in iteration N), and mergedEnvironment
// appends the whole layer LAST, so both beat the environment and folder layers.
// Reset swaps in a new data row and drops every script write at each iteration
// boundary, so nothing a script wrote in iteration N leaks into iteration N+1.
//
// All access is mutex-guarded. A single *Engine cannot drive two data-driven
// runs at once (the runner installs one layer per run); that is a documented
// constraint of the CI runner, not a lock this type tries to enforce.
type RunScopedVars struct {
	mu     sync.RWMutex
	data   map[string]string
	writes map[string]string
	unsets map[string]bool
}

// NewRunScopedVars returns an empty layer; the runner calls Reset with the
// first iteration's row before the first request runs.
func NewRunScopedVars() *RunScopedVars {
	return &RunScopedVars{
		data:   map[string]string{},
		writes: map[string]string{},
		unsets: map[string]bool{},
	}
}

// Reset installs a new iteration's data row and clears every script write/unset
// from the previous iteration. Called at each iteration boundary by the runner
// — this is what makes iteration N+1 blind to what iteration N's scripts wrote.
func (rv *RunScopedVars) Reset(data []model.KeyValue) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	next := make(map[string]string, len(data))
	for _, kv := range data {
		if kv.Enabled {
			next[kv.Key] = kv.Value
		}
	}
	rv.data = next
	rv.writes = map[string]string{}
	rv.unsets = map[string]bool{}
}

// setScript records a script's vars.set within the current iteration.
func (rv *RunScopedVars) setScript(name, value string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	delete(rv.unsets, name)
	rv.writes[name] = value
}

// unsetScript records a script's vars.unset within the current iteration. It
// places a tombstone over the data row (so a later request in the same
// iteration no longer sees the column) without touching the user's stored
// environment — a data run is side-effect-free.
func (rv *RunScopedVars) unsetScript(name string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	delete(rv.writes, name)
	rv.unsets[name] = true
}

// overlay returns the layer's effective variables (data row overlaid by script
// writes, script unsets removed) as enabled KeyValues in a stable order, ready
// for mergedEnvironment to append after the environment and folder layers.
func (rv *RunScopedVars) overlay() []model.KeyValue {
	rv.mu.RLock()
	defer rv.mu.RUnlock()
	eff := make(map[string]string, len(rv.data)+len(rv.writes))
	for k, v := range rv.data {
		if rv.unsets[k] {
			continue
		}
		eff[k] = v
	}
	for k, v := range rv.writes {
		eff[k] = v
	}
	out := make([]model.KeyValue, 0, len(eff))
	for _, k := range sortedKeys(eff) {
		out = append(out, model.KeyValue{Key: k, Value: eff[k], Enabled: true})
	}
	return out
}

type runScopedVarsCtxKey struct{}

// WithRunScopedVars attaches a run-scoped variable layer to ctx. The runner
// calls this once per data-driven run; every RunRequest executed under the
// returned context (and every chained auto-send beneath it) resolves against —
// and lands script writes into — the layer. A context without a layer, which is
// every GUI and MCP send, is completely unaffected.
func WithRunScopedVars(ctx context.Context, rv *RunScopedVars) context.Context {
	return context.WithValue(ctx, runScopedVarsCtxKey{}, rv)
}

// RunScopedVarsFrom returns the run-scoped layer attached to ctx, or nil when
// there is none (an interactive send). The runner uses it to Reset the layer
// per iteration; the engine uses it to consult and write the layer.
func RunScopedVarsFrom(ctx context.Context) *RunScopedVars {
	rv, _ := ctx.Value(runScopedVarsCtxKey{}).(*RunScopedVars)
	return rv
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

	// The post-response script runs AFTER the declarative assertions, so both
	// feed one verdict (ResponseData.Passed) from one place, and a script can
	// read a response whose assertions have already been scored.
	e.runPostResponseScript(sess.Context(), req, environmentID, &resp)

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

	env, err := e.mergedEnvironment(ctx, req, environmentID)
	if err != nil {
		return model.RequestDef{}, ResolvedRequest{}, err
	}

	resolved, err := e.Templater.Resolve(ctx, req, env, responseLookupFromStore{e.Store})
	if err != nil {
		return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("resolve templates: %w", err)
	}

	// `:name` path placeholders are substituted here — AFTER `${...}`
	// templating (so both the URL and the values themselves are already
	// expanded) and BEFORE auth, so a signing scheme signs the real path
	// and the policy chokepoint below sees the real URL. Every consumer of
	// the resolve path inherits it: Send, RunFolder, the CLI, the k6 perf
	// runner (ResolveForExecution), and Copy-as-code (ResolveForSnippet).
	resolved.URL = applyPathParams(req.Protocol, resolved.URL, resolved.PathParams)

	// authMintedSecrets collects credential values the auth Applier mints so
	// the pre-request script snapshot can redact them (see below).
	var authMintedSecrets map[string]string
	if req.Auth != nil && req.Auth.Kind != model.AuthNone {
		// Template the auth credential fields (a COPY — never the stored
		// pointer) so `${token}` in the Auth tab resolves like anywhere else.
		// Falls back to the raw auth for a Templater that doesn't support it.
		authCfg := req.Auth
		if at, ok := e.Templater.(AuthTemplater); ok {
			authCfg, err = at.ResolveAuth(ctx, req, env, responseLookupFromStore{e.Store}, req.Auth)
			if err != nil {
				return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("resolve auth templates: %w", err)
			}
		}
		// Carry the RESOLVED auth down to the Protocol as well. Every kind
		// that reduces to a header is finished by Apply below, but a
		// challenge-response scheme (Digest) can only compute its credential
		// at the transport, after the server's 401 — and it must hash the
		// TEMPLATED password, not the `${...}` literal sitting in the store.
		// Cloned so the value a Protocol sees can never alias, let alone
		// mutate, the *AuthConfig the store handed us (relevant on the
		// fallback path above, where authCfg IS the stored pointer).
		resolved.Auth = cloneAuthConfig(authCfg)
		// Snapshot header values BEFORE Apply: anything Apply adds is a minted
		// credential (an OAuth access token, a signed JWT, a SigV4 signature,
		// base64(user:password)) and must join the script-redaction secret set
		// below. secretValues(env) alone misses these — an OAuth token exists
		// in no environment variable, and Basic's base64 form leaks a keychain
		// password even when the raw password itself is redacted. This is the
		// same "a guard shipped for one channel has a second door" shape the
		// vars.get redaction went through; close both doors at once.
		preAuthValues := make(map[string]bool, len(resolved.Headers))
		for _, h := range resolved.Headers {
			preAuthValues[h.Value] = true
		}
		resolved, err = e.Auth.Apply(ctx, *authCfg, resolved)
		if err != nil {
			return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("apply auth: %w", err)
		}
		for _, h := range resolved.Headers {
			if h.Value == "" || preAuthValues[h.Value] {
				continue
			}
			if authMintedSecrets == nil {
				authMintedSecrets = map[string]string{}
			}
			label := "auth:" + string(req.Auth.Kind)
			// Register the credential AFTER the scheme prefix ("Bearer x",
			// "Basic x", "AWS4-HMAC-SHA256 …") so a script still sees which
			// scheme a header carries — and, when the credential is a resolved
			// env secret, so the env map's variable-name entry wins the value
			// collision and the placeholder reads [secret:apiKey] rather than
			// the anonymous [secret:auth:bearer]. Values without a scheme
			// prefix (X-Amz-Security-Token, minted API keys) register whole.
			if _, bare, ok := strings.Cut(h.Value, " "); ok && bare != "" {
				authMintedSecrets[bare] = label
			} else {
				authMintedSecrets[h.Value] = label
			}
		}
	}

	if e.Scripter != nil && req.PreRequestScript != "" {
		// SECRETS: the script must never see resolved secret plaintext. By
		// this point templating has expanded `${apiKey}` to the real keychain
		// value and auth has built `Authorization: Bearer <real secret>`, so
		// the request itself carries exactly what variableMap goes to such
		// lengths to keep out of vars.get — and a script could read it off
		// ctx.request and vars.set() a copy into a plain variable, which then
		// persists to the plaintext, git-tracked environment YAML. The script
		// therefore runs against a REDACTED snapshot (see redactResolved).
		//
		// The request that actually goes on the wire is UNAFFECTED:
		// mergeScriptEdits folds the script's edits back onto the real,
		// unredacted request, restoring every field the script left exactly as
		// the snapshot had it. ctx.setHeader keeps working normally — a header
		// the script sets wins; one it never touched keeps its real value.
		snapshot := redactResolved(resolved, mergeSecretMaps(secretValues(env), authMintedSecrets))
		// baseline is snapshot's twin, and the one the merge compares against.
		// It must be a SEPARATE copy: internal/scripting rewrites an existing
		// header's value IN PLACE on the slice it is handed, so comparing
		// against snapshot itself would see the script's own edit on both
		// sides, read it as "unchanged", and silently revert every
		// ctx.setHeader that overrode an existing header.
		baseline := cloneResolved(snapshot)

		// A Scripter that knows about variables gets the richer call (it can
		// read a token an earlier response stored, and write one itself);
		// anything older still works through the original entry point.
		if vs, ok := e.Scripter.(PreRequestVarScripter); ok {
			out, runErr := vs.RunPreRequestWithVars(ctx, req.PreRequestScript, snapshot, PreRequestInput{
				Vars:    variableMap(env),
				Secrets: secretNames(env),
			})
			// Variable writes are applied even on a failed run: they are
			// side effects that already happened before the script threw.
			e.applyVariableWrites(ctx, req, environmentID, env, out.VarWrites, out.VarUnsets)
			if runErr != nil {
				return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("pre-request script: %w", runErr)
			}
			resolved = mergeScriptEdits(resolved, baseline, out.Resolved)
		} else {
			scripted, scriptErr := e.Scripter.RunPreRequest(ctx, req.PreRequestScript, snapshot)
			if scriptErr != nil {
				return model.RequestDef{}, ResolvedRequest{}, fmt.Errorf("pre-request script: %w", scriptErr)
			}
			resolved = mergeScriptEdits(resolved, baseline, scripted)
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

// mergedEnvironment builds the variable view a request actually resolves
// against, layering three sources into one shallow copy of the environment
// so templating.Resolve's existing "last write to the map wins" merge does
// the precedence work and never has to know these layers exist:
//
//	environment variables            (lowest)
//	folder-scoped variables, root folder first, closest folder last
//	session variables written by scripts (interactive runs)
//	data-file row + this iteration's script writes (data-driven runs, highest)
//
// Later layers win because their value is the freshest one by definition — a
// token this run just minted must beat the stale literal sitting in the
// environment file, and a data-driven iteration's row (plus a script write on
// top of it) is the run's actual input. The data-driven layer rides in through
// context (RunScopedVarsFrom), so it is present only for a data run's requests
// and never for a concurrent interactive send. Returns nil when there is
// nothing at all to resolve against, which is the no-environment case the
// templater already handles.
func (e *Engine) mergedEnvironment(ctx context.Context, req model.RequestDef, environmentID model.ID) (*model.Environment, error) {
	var env *model.Environment
	if environmentID != "" {
		loaded, err := e.Store.GetEnvironment(environmentID)
		if err != nil {
			return nil, fmt.Errorf("load environment: %w", err)
		}
		env = loaded
	}

	folderVars := e.folderVariables(req.WorkspaceID, req.FolderID)
	sessionVars := e.scriptVars.snapshot(req.WorkspaceID)
	var runVars []model.KeyValue
	if rv := RunScopedVarsFrom(ctx); rv != nil {
		runVars = rv.overlay()
	}
	if len(folderVars) == 0 && len(sessionVars) == 0 && len(runVars) == 0 {
		return env, nil
	}

	merged := model.Environment{}
	if env != nil {
		merged = *env
	}
	// Copy before appending: GetEnvironment can hand back a struct that
	// still shares its Variables backing array with the store's own copy.
	vars := append([]model.KeyValue{}, merged.Variables...)
	vars = append(vars, folderVars...)
	for _, name := range sortedKeys(sessionVars) {
		vars = append(vars, model.KeyValue{Key: name, Value: sessionVars[name], Enabled: true})
	}
	vars = append(vars, runVars...)
	merged.Variables = vars
	return &merged, nil
}

// variableMap flattens an environment's enabled variables into the plain
// string->string map a script sees through vars.get — with every secret-backed
// name REDACTED (omitted entirely, so vars.get("apiKey") returns undefined).
//
// This is AUK's "secrets never leave the keychain" promise, enforced. A
// GetEnvironment result layers the real keychain value into Variables for the
// templater; if a script could read that plaintext it could copy it into a
// NON-secret variable (vars.set("notes", vars.get("apiKey"))) which then
// persists to the plaintext, git-tracked environment YAML — laundering a secret
// past the keychain boundary. Omitting secret values here closes that at the
// source: a script never receives resolved secret plaintext at all. A signing
// scheme that legitimately needs a secret must use a declarative auth kind
// (AWS SigV4, OAuth1) — those hold their own credentials and never expose them
// to a script. See PostResponseInput.Secrets for the matching guard on WRITES.
//
// Redaction is by NAME (env.Secrets), so a plain variable that happens to share
// a secret's name is over-redacted rather than under-redacted — the safe
// direction.
func variableMap(env *model.Environment) map[string]string {
	if env == nil {
		return nil
	}
	secret := make(map[string]bool, len(env.Secrets))
	for _, name := range env.Secrets {
		secret[name] = true
	}
	out := make(map[string]string, len(env.Variables))
	for _, kv := range env.Variables {
		if !kv.Enabled {
			continue
		}
		if secret[kv.Key] {
			continue // never hand a script resolved secret plaintext
		}
		out[kv.Key] = kv.Value
	}
	return out
}

func secretNames(env *model.Environment) []string {
	if env == nil {
		return nil
	}
	return env.Secrets
}

// secretValues is variableMap's counterpart for the request itself: the
// resolved plaintext VALUES of the environment's secret-backed variables,
// keyed value -> variable name.
//
// variableMap redacts secrets out of vars.get by NAME, which is enough for
// the variable set but not for the REQUEST: by the time a pre-request script
// runs, templating has already expanded `${apiKey}` into the URL, a header or
// the body, and internal/auth has already built `Authorization: Bearer
// <real secret>`. Reading that plaintext back out of ctx.request and writing
// it to a plain variable is the same laundering path, just through a
// different window. These are the values redactResolved blanks so that
// window is closed too — they are collected here ONLY to be redacted, and
// never travel anywhere else.
//
// Every enabled occurrence of a secret NAME contributes its value, not just
// the last one, so a shadowed layer (folder/session) cannot smuggle a second
// copy of the same credential past the redaction.
func secretValues(env *model.Environment) map[string]string {
	if env == nil {
		return nil
	}
	secret := make(map[string]bool, len(env.Secrets))
	for _, name := range env.Secrets {
		secret[name] = true
	}
	out := make(map[string]string)
	for _, kv := range env.Variables {
		if !kv.Enabled || !secret[kv.Key] || kv.Value == "" {
			continue
		}
		out[kv.Value] = kv.Key
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// redactSecretValues replaces every occurrence of a resolved secret value in s
// with a `[secret:<name>]` placeholder — readable enough that a script author
// debugging their script can see WHY a value is missing rather than staring at
// a mangled string.
//
// Longest value first, so a secret that happens to be a substring of another
// can't chop the longer one into an unrecognizable fragment. Matching is plain
// substring containment with no minimum length: a pathologically short secret
// over-redacts, which is the safe direction (only the script's read-only view
// is affected — never the request on the wire).
func redactSecretValues(s string, secrets map[string]string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	for _, value := range secretValuesLongestFirst(secrets) {
		s = strings.ReplaceAll(s, value, "[secret:"+secrets[value]+"]")
	}
	return s
}

// containsSecretValue reports whether s carries a resolved secret anywhere
// inside it — the write-side counterpart of redactSecretValues.
func containsSecretValue(s string, secrets map[string]string) bool {
	if s == "" || len(secrets) == 0 {
		return false
	}
	for value := range secrets {
		if strings.Contains(s, value) {
			return true
		}
	}
	return false
}

// secretValuesLongestFirst orders the values deterministically: longest first
// (see redactSecretValues), ties broken lexicographically so the result never
// depends on Go's map iteration order.
// mergeSecretMaps unions two value->name maps without mutating either. Nil in,
// possibly nil out — redactResolved treats an empty map as "nothing to do".
//
// On a value collision `a` wins: it carries the environment's VARIABLE NAMES,
// and `[secret:apiKey]` tells a script author which value went missing where
// `[secret:auth:bearer]` only says that something did. (The collision is real:
// a Bearer token that IS `${apiKey}` appears in both maps.)
func mergeSecretMaps(a, b map[string]string) map[string]string {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	out := make(map[string]string, len(a)+len(b))
	for v, n := range b {
		out[v] = n
	}
	for v, n := range a {
		out[v] = n
	}
	return out
}

func secretValuesLongestFirst(secrets map[string]string) []string {
	values := make([]string, 0, len(secrets))
	for value := range secrets {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	return values
}

// redactResolved builds the read-only snapshot a pre-request script sees: an
// independent copy of the resolved request with every resolved secret value
// replaced by a placeholder in the three places the script API exposes —
// ctx.request.url, ctx.request.headers (keys as well as values), and
// ctx.request.body.
//
// The copy is deep for exactly the fields a Scripter may write through
// (headers, body, params), because internal/scripting edits headers IN PLACE;
// without the copy a script's edit — or the redaction itself — would reach
// back into the request that is about to be sent. Params and path params are
// copied but NOT redacted: no script API exposes them, and the merge back
// relies on them being identical on both sides.
func redactResolved(r ResolvedRequest, secrets map[string]string) ResolvedRequest {
	out := cloneResolved(r)
	if len(secrets) == 0 {
		return out
	}
	out.URL = redactSecretValues(r.URL, secrets)
	for i := range out.Headers {
		out.Headers[i].Key = redactSecretValues(out.Headers[i].Key, secrets)
		out.Headers[i].Value = redactSecretValues(out.Headers[i].Value, secrets)
	}
	if out.Body != nil {
		out.Body.Text = redactSecretValues(out.Body.Text, secrets)
	}
	return out
}

// cloneResolved copies a resolved request deeply enough that a Scripter
// writing through it — internal/scripting edits headers in place — cannot
// reach the original. Auth is shared by pointer on purpose: it is not part of
// the script contract and mergeScriptEdits restores the real one regardless.
func cloneResolved(r ResolvedRequest) ResolvedRequest {
	out := r
	out.Headers = append([]model.KeyValue(nil), r.Headers...)
	out.Params = append([]model.KeyValue(nil), r.Params...)
	out.PathParams = append([]model.KeyValue(nil), r.PathParams...)
	if r.Body != nil {
		body := *r.Body
		out.Body = &body
	}
	return out
}

// mergeScriptEdits folds a pre-request script's output back onto the REAL
// (unredacted) resolved request.
//
// The rule is one line long: anything the script left exactly as the redacted
// baseline had it is restored to its real value; anything it changed is the
// script's, verbatim. So a placeholder can never reach the wire, a real secret
// can never be un-redacted by the script's own hand, and ctx.setHeader behaves
// exactly as it always did — including setting a header whose value the script
// composed out of a placeholder, which then goes out AS the placeholder rather
// than as the secret.
//
// baseline is the untouched twin of the snapshot the script was handed — NOT
// the snapshot itself, which internal/scripting rewrites in place (see the
// call site). Headers are matched positionally because that is what
// internal/scripting produces (upsertHeader rewrites in place and appends new
// headers at the end); a header at an index whose key no longer matches, or
// beyond the baseline's length, is treated as the script's own and kept.
func mergeScriptEdits(real, baseline, scripted ResolvedRequest) ResolvedRequest {
	out := scripted
	if out.URL == baseline.URL {
		out.URL = real.URL
	}
	if out.Body != nil && baseline.Body != nil && real.Body != nil && out.Body.Text == baseline.Body.Text {
		body := *out.Body
		body.Text = real.Body.Text
		out.Body = &body
	}
	headers := append([]model.KeyValue(nil), scripted.Headers...)
	for i := range headers {
		if i >= len(baseline.Headers) || i >= len(real.Headers) {
			break
		}
		if headers[i].Key != baseline.Headers[i].Key {
			continue
		}
		headers[i].Key = real.Headers[i].Key
		if headers[i].Value == baseline.Headers[i].Value {
			headers[i].Value = real.Headers[i].Value
		}
	}
	out.Headers = headers
	// Auth is not part of the script contract: a script shapes headers, it
	// never picks credentials (and the resolved credentials are precisely what
	// it must not be able to reach).
	out.Auth = real.Auth
	return out
}

// cloneAuthConfig deep-copies an auth config so a ResolvedRequest can carry
// resolved credentials without ever aliasing the *AuthConfig the store holds.
// Returns nil for nil.
func cloneAuthConfig(auth *model.AuthConfig) *model.AuthConfig {
	if auth == nil {
		return nil
	}
	out := *auth
	if auth.Basic != nil {
		v := *auth.Basic
		out.Basic = &v
	}
	if auth.Bearer != nil {
		v := *auth.Bearer
		out.Bearer = &v
	}
	if auth.APIKey != nil {
		v := *auth.APIKey
		out.APIKey = &v
	}
	if auth.JWT != nil {
		v := *auth.JWT
		out.JWT = &v
	}
	if auth.OAuth2 != nil {
		v := *auth.OAuth2
		out.OAuth2 = &v
	}
	if auth.AWSSigV4 != nil {
		v := *auth.AWSSigV4
		out.AWSSigV4 = &v
	}
	if auth.OAuth1 != nil {
		v := *auth.OAuth1
		out.OAuth1 = &v
	}
	if auth.Digest != nil {
		v := *auth.Digest
		out.Digest = &v
	}
	return &out
}

// workspaceSecretNames returns the UNION of every environment's secret names in
// the workspace, so the write guard in applyVariableWrites can refuse a name
// that is keychain-backed in ANY environment — not only the one the writing
// request selected. Without the union, a script run with no environment
// selected (the GUI default) could write e.g. "apiKey" into the session overlay
// under a plain name; mergedEnvironment appends session vars LAST, so that plain
// value would then shadow the real keychain secret the instant a Prod
// environment (where apiKey IS a secret) is selected.
//
// It reads names only, so it never resolves a keychain value — ListEnvironments
// (the raw variant when the store offers it) is enough. A store that exposes
// neither yields an empty set, and applyVariableWrites still layers in the
// selected environment's own secrets as a floor.
func (e *Engine) workspaceSecretNames(workspaceID model.ID) map[string]bool {
	names := make(map[string]bool)
	add := func(envs []model.Environment) {
		for _, env := range envs {
			for _, name := range env.Secrets {
				names[name] = true
			}
		}
	}
	switch lister := e.Store.(type) {
	case interface {
		ListEnvironmentsRaw(model.ID) []model.Environment
	}:
		add(lister.ListEnvironmentsRaw(workspaceID))
	case interface {
		ListEnvironments(model.ID) []model.Environment
	}:
		add(lister.ListEnvironments(workspaceID))
	}
	return names
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runPostResponseScript executes req.PostResponseScript and records its
// outcome ON resp: TestResults for what it asserted, ScriptError when the
// script itself could not run (syntax error, timeout, or a throw outside a
// test). A script that cannot run is a FAILED run, never a silent pass —
// see ResponseData.Passed.
//
// It never changes the response: the script gets a copy, and only tests,
// variable writes and console output come back.
func (e *Engine) runPostResponseScript(ctx context.Context, req model.RequestDef, environmentID model.ID, resp *model.ResponseData) {
	if e.Scripter == nil || req.PostResponseScript == "" {
		return
	}
	scripter, ok := e.Scripter.(PostResponseScripter)
	if !ok {
		return
	}

	// Re-read rather than reusing the resolve-time environment: a
	// pre-request script on THIS request may have written a variable that
	// its post-response script should now see.
	env, err := e.mergedEnvironment(ctx, req, environmentID)
	if err != nil {
		resp.ScriptError = err.Error()
		return
	}

	out, err := scripter.RunPostResponse(ctx, req.PostResponseScript, PostResponseInput{
		Response: *resp,
		Vars:     variableMap(env),
		Secrets:  secretNames(env),
	})
	if len(out.Tests) > 0 {
		resp.TestResults = out.Tests
	}
	// Logs are surfaced even when the script failed — console output from
	// before the throw is usually what explains the failure.
	if len(out.Logs) > 0 {
		resp.ScriptLogs = out.Logs
	}
	if err != nil {
		resp.ScriptError = err.Error()
	}
	// Applied even on a failed run: a vars.set that already executed before
	// the script threw is a side effect that really happened.
	e.applyVariableWrites(ctx, req, environmentID, env, out.VarWrites, out.VarUnsets)
}

// applyVariableWrites lands a script's vars.set/vars.unset writes somewhere
// the NEXT request will resolve them from. Where depends on the run:
//
//   - DATA-DRIVEN run (a run-scoped layer is attached to ctx): the write is
//     run-scoped — usable by later requests in the SAME iteration, cleared at
//     the next iteration boundary, and NEVER persisted to the user's stored
//     environment. A CI run must not mutate the workspace it tests.
//   - INTERACTIVE run (no layer): the active environment when one is selected
//     and the Store can persist it, otherwise this process's session overlay
//     (see sessionVars) — the manual auth-chaining-across-sends behavior.
//
// Secret-backed names are dropped here as well as being refused inside the
// script runtime. The guard is the UNION of secret names across EVERY
// environment in the workspace, not just the one this request selected: with
// no environment selected a write would otherwise land in the session overlay
// under a plain name and then shadow the real keychain secret the moment an
// environment that DOES mark it secret is selected. This is the guarantee —
// a keychain-backed secret cannot be clobbered by a script no matter which
// Scripter implementation is wired in, which environment is active, or where
// the write would have landed.
func (e *Engine) applyVariableWrites(ctx context.Context, req model.RequestDef, environmentID model.ID, env *model.Environment, writes map[string]string, unsets []string) {
	workspaceID := req.WorkspaceID
	if len(writes) == 0 && len(unsets) == 0 {
		return
	}

	protected := e.workspaceSecretNames(workspaceID)
	for _, name := range secretNames(env) {
		// Defense in depth: the selected environment's own secrets always
		// count, even if the store can't enumerate every environment.
		protected[name] = true
	}
	// The NAME guard above stops a script overwriting a secret. This is the
	// other direction — a write whose VALUE *is* (or contains) a resolved
	// secret, under whatever innocuous name, which is how laundering actually
	// looks: vars.set("notes", <the apiKey>). Both read paths are redacted
	// already (variableMap for vars.get, redactResolved for the pre-request
	// request snapshot), so a script should never be holding one; this is the
	// backstop that keeps a secret off disk even if some future read path
	// leaks one. Over-blocking is deliberate: a dropped variable write is
	// recoverable, a keychain secret committed to a git-tracked YAML is not.
	leaks := secretValues(env)

	safeWrites := make(map[string]string, len(writes))
	for name, value := range writes {
		if !protected[name] && !containsSecretValue(value, leaks) {
			safeWrites[name] = value
		}
	}
	safeUnsets := make([]string, 0, len(unsets))
	for _, name := range unsets {
		if !protected[name] {
			safeUnsets = append(safeUnsets, name)
		}
	}
	if len(safeWrites) == 0 && len(safeUnsets) == 0 {
		return
	}

	// Data-driven run: run-scoped, non-persisted, cleared per iteration.
	if rv := RunScopedVarsFrom(ctx); rv != nil {
		for name, value := range safeWrites {
			rv.setScript(name, value)
		}
		for _, name := range safeUnsets {
			rv.unsetScript(name)
		}
		return
	}

	if environmentID != "" && e.persistVariables(environmentID, safeWrites, safeUnsets) {
		// A folder-scoped variable of the same name outranks the environment
		// (see mergedEnvironment), so persisting alone would let a folder
		// literal silently shadow the value a script just wrote — the chain
		// would break with no error anywhere. Those names keep their session
		// entry, which outranks both.
		shadowed := make(map[string]bool)
		for _, kv := range e.folderVariables(workspaceID, req.FolderID) {
			shadowed[kv.Key] = true
		}

		var settled []string
		for _, name := range append(sortedKeys(safeWrites), safeUnsets...) {
			if value, ok := safeWrites[name]; ok && shadowed[name] {
				e.scriptVars.set(workspaceID, name, value)
				continue
			}
			settled = append(settled, name)
		}
		// The environment is now the authoritative copy of these, and it is
		// re-read on every request, so drop any older session value rather
		// than letting it shadow an edit made in the environment editor.
		e.scriptVars.forget(workspaceID, settled...)
		return
	}

	for name, value := range safeWrites {
		e.scriptVars.set(workspaceID, name, value)
	}
	e.scriptVars.forget(workspaceID, safeUnsets...)
}

// persistVariables writes the delta into environmentID's PLAIN variables,
// reporting whether it got there. The Store contract (core.Store) has no
// environment-writing method — the engine only ever needed to read them —
// so this is a capability check on the concrete store, exactly like the
// Templater cookie-capture hook in RunRequest. Both known stores are
// covered; anything else falls back to the session overlay.
func (e *Engine) persistVariables(environmentID model.ID, writes map[string]string, unsets []string) bool {
	e.envWriteMu.Lock()
	defer e.envWriteMu.Unlock()

	env, ok := e.rawEnvironment(environmentID)
	if !ok {
		return false
	}

	vars := append([]model.KeyValue{}, env.Variables...)
	for _, name := range sortedKeys(writes) {
		vars = upsertVariable(vars, name, writes[name])
	}
	for _, name := range unsets {
		vars = removeVariable(vars, name)
	}
	env.Variables = vars

	switch store := e.Store.(type) {
	case interface {
		PutEnvironment(model.Environment, map[string]string) error
	}:
		// storage.FileStore: nil secretValues leaves every keychain entry
		// exactly as it was.
		return store.PutEnvironment(env, nil) == nil
	case interface {
		PutEnvironment(model.Environment) error
	}:
		return store.PutEnvironment(env) == nil
	case interface {
		PutEnvironment(model.Environment)
	}:
		store.PutEnvironment(env)
		return true
	}
	return false
}

// rawEnvironment reads an environment in the shape it is STORED in, which
// for secrets means "name listed, value absent".
//
// This matters: storage.FileStore.GetEnvironment layers the real keychain
// values into Variables for the templater, and writing that shape back would
// park plaintext secrets in the store's in-memory copy — from where a
// workspace export would happily write them to a file. ListEnvironmentsRaw
// exists precisely to avoid that, so it is preferred; the fallback blanks
// secret-backed values itself rather than trusting them.
func (e *Engine) rawEnvironment(id model.ID) (model.Environment, bool) {
	if lister, ok := e.Store.(interface {
		ListEnvironmentsRaw(model.ID) []model.Environment
	}); ok {
		for _, env := range lister.ListEnvironmentsRaw("") {
			if env.ID == id {
				return env, true
			}
		}
		return model.Environment{}, false
	}

	loaded, err := e.Store.GetEnvironment(id)
	if err != nil || loaded == nil {
		return model.Environment{}, false
	}
	env := *loaded
	env.Variables = append([]model.KeyValue{}, loaded.Variables...)
	secret := make(map[string]bool, len(env.Secrets))
	for _, name := range env.Secrets {
		secret[name] = true
	}
	for i, kv := range env.Variables {
		if secret[kv.Key] {
			env.Variables[i].Value = ""
		}
	}
	return env, true
}

func upsertVariable(vars []model.KeyValue, key, value string) []model.KeyValue {
	for i, kv := range vars {
		if kv.Key == key {
			vars[i].Value = value
			vars[i].Enabled = true
			return vars
		}
	}
	return append(vars, model.KeyValue{Key: key, Value: value, Enabled: true})
}

func removeVariable(vars []model.KeyValue, key string) []model.KeyValue {
	out := vars[:0]
	for _, kv := range vars {
		if kv.Key != key {
			out = append(out, kv)
		}
	}
	return out
}

// ResolveForExecution exposes the resolve+auth+authorize front half for
// consumers that execute a request outside the Protocol path — notably the
// k6 perf runner, which needs the fully-resolved URL/headers/body to generate
// its load script but runs it in a separate process. Same policy chokepoint,
// origin recorded for the audit trail.
func (e *Engine) ResolveForExecution(ctx context.Context, requestID model.ID, environmentID model.ID, origin string) (model.RequestDef, ResolvedRequest, error) {
	return e.resolveAndAuthorize(ctx, requestID, environmentID, origin)
}

// NewResponseLookup exposes the store-backed ResponseLookup so app-layer
// callers that resolve auth OUTSIDE a send (the OAuth2 sign-in binding) can
// hand ResolveAuth the same chaining semantics a real send has.
func NewResponseLookup(s Store) ResponseLookup { return responseLookupFromStore{s} }

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
