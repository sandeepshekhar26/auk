package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// fakeChainStore is a minimal in-memory core.Store for chaining tests: it
// supports GetRequest/LookupRequestByName/SaveResponse/AppendHistory plus a
// LastResponse cache, and lets a test pre-seed a cached response to
// distinguish "cache hit" from "auto-send" in ResolveChainRef.
type fakeChainStore struct {
	requests  map[model.ID]model.RequestDef
	responses map[model.ID]model.ResponseData
}

func newFakeChainStore() *fakeChainStore {
	return &fakeChainStore{
		requests:  map[model.ID]model.RequestDef{},
		responses: map[model.ID]model.ResponseData{},
	}
}

func (s *fakeChainStore) GetRequest(id model.ID) (model.RequestDef, error) {
	r, ok := s.requests[id]
	if !ok {
		return model.RequestDef{}, fmt.Errorf("request %q not found", id)
	}
	return r, nil
}

func (s *fakeChainStore) GetEnvironment(model.ID) (*model.Environment, error) {
	return nil, nil
}

func (s *fakeChainStore) SaveResponse(r model.ResponseData) error {
	s.responses[r.RequestID] = r
	return nil
}

func (s *fakeChainStore) AppendHistory(model.HistoryEntry) error { return nil }

func (s *fakeChainStore) LastResponse(id model.ID) (model.ResponseData, bool) {
	r, ok := s.responses[id]
	return r, ok
}

func (s *fakeChainStore) LookupRequestByName(workspaceID model.ID, name string) (model.RequestDef, error) {
	for _, r := range s.requests {
		if r.WorkspaceID == workspaceID && r.Name == name {
			return r, nil
		}
	}
	return model.RequestDef{}, fmt.Errorf("request named %q not found in workspace %q", name, workspaceID)
}

// fakeProtocol answers every Execute call with a canned JSON body so tests
// don't need real network I/O; it also counts invocations per request id so
// tests can assert auto-send happened exactly once (not on every reference).
type fakeProtocol struct {
	sends map[model.ID]int
}

func newFakeProtocol() *fakeProtocol { return &fakeProtocol{sends: map[model.ID]int{}} }

func (p *fakeProtocol) Kind() model.ProtocolKind { return model.ProtocolHTTP }

func (p *fakeProtocol) Execute(_ context.Context, _ *Session, req model.RequestDef, _ ResolvedRequest) (model.ResponseData, error) {
	p.sends[req.ID]++
	body := fmt.Sprintf(`{"token":"tok-for-%s"}`, req.Name)
	return model.ResponseData{
		RequestID:  req.ID,
		Status:     200,
		StatusText: "200 OK",
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
	}, nil
}

// passthroughTemplater resolves no `${...}` refs; only used by tests that
// exercise ResolveChainRef directly rather than through Resolve.
type passthroughTemplater struct{}

func (passthroughTemplater) Resolve(_ context.Context, req model.RequestDef, _ *model.Environment, _ ResponseLookup) (ResolvedRequest, error) {
	return ResolvedRequest{URL: req.URL, Method: req.Method}, nil
}

func newTestEngine(store Store, protocol Protocol) *Engine {
	e := NewEngine(store, passthroughTemplater{}, noopAuth{}, nil)
	e.RegisterProtocol(protocol)
	return e
}

type noopAuth struct{}

func (noopAuth) Apply(_ context.Context, _ model.AuthConfig, req ResolvedRequest) (ResolvedRequest, error) {
	return req, nil
}

func TestResolveChainRef_AutoSendsWhenUncached(t *testing.T) {
	store := newFakeChainStore()
	const wsID = "ws1"
	store.requests["login"] = model.RequestDef{ID: "login", WorkspaceID: wsID, Name: "Login", Protocol: model.ProtocolHTTP, Method: "POST", URL: "https://example.test/login"}

	proto := newFakeProtocol()
	engine := newTestEngine(store, proto)

	got, err := engine.ResolveChainRef(context.Background(), wsID, "Login", "body.token")
	if err != nil {
		t.Fatalf("ResolveChainRef returned error: %v", err)
	}
	if want := "tok-for-Login"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if proto.sends["login"] != 1 {
		t.Errorf("expected exactly 1 auto-send, got %d", proto.sends["login"])
	}

	// Second call should hit the now-cached response, not auto-send again.
	if _, err := engine.ResolveChainRef(context.Background(), wsID, "Login", "body.token"); err != nil {
		t.Fatalf("second ResolveChainRef returned error: %v", err)
	}
	if proto.sends["login"] != 1 {
		t.Errorf("expected cache hit on second call, got %d sends", proto.sends["login"])
	}
}

func TestResolveChainRef_UsesCachedResponseWithoutResending(t *testing.T) {
	store := newFakeChainStore()
	const wsID = "ws1"
	store.requests["login"] = model.RequestDef{ID: "login", WorkspaceID: wsID, Name: "Login", Protocol: model.ProtocolHTTP}
	store.responses["login"] = model.ResponseData{
		RequestID:  "login",
		Status:     200,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"token":"cached-tok"}`)),
	}

	proto := newFakeProtocol()
	engine := newTestEngine(store, proto)

	got, err := engine.ResolveChainRef(context.Background(), wsID, "Login", "body.token")
	if err != nil {
		t.Fatalf("ResolveChainRef returned error: %v", err)
	}
	if got != "cached-tok" {
		t.Errorf("got %q, want %q", got, "cached-tok")
	}
	if proto.sends["login"] != 0 {
		t.Errorf("expected no auto-send for a cached response, got %d sends", proto.sends["login"])
	}
}

func TestResolveChainRef_StatusAndHeaderPaths(t *testing.T) {
	store := newFakeChainStore()
	const wsID = "ws1"
	store.requests["r1"] = model.RequestDef{ID: "r1", WorkspaceID: wsID, Name: "R1", Protocol: model.ProtocolHTTP}
	store.responses["r1"] = model.ResponseData{
		RequestID:  "r1",
		Status:     201,
		Headers:    []model.KeyValue{{Key: "X-Trace-Id", Value: "abc123", Enabled: true}},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)),
	}
	engine := newTestEngine(store, newFakeProtocol())

	if got, err := engine.ResolveChainRef(context.Background(), wsID, "R1", "status"); err != nil || got != "201" {
		t.Errorf("status path: got (%q, %v), want (\"201\", nil)", got, err)
	}
	if got, err := engine.ResolveChainRef(context.Background(), wsID, "R1", "header.X-Trace-Id"); err != nil || got != "abc123" {
		t.Errorf("header path: got (%q, %v), want (\"abc123\", nil)", got, err)
	}
	if _, err := engine.ResolveChainRef(context.Background(), wsID, "R1", "header.Missing"); err == nil {
		t.Error("expected error for missing header, got nil")
	}
}

// TestChainCycle_DirectlyDetected covers A -> B -> A: resolving A's
// reference to B auto-sends B, whose own resolution references A again.
// Since A is already the request being resolved (it's in-flight at the top
// of the chain), this must fail with a clear cycle error instead of
// recursing forever.
func TestChainCycle_DirectlyDetected(t *testing.T) {
	store := newFakeChainStore()
	const wsID = "ws1"
	store.requests["a"] = model.RequestDef{ID: "a", WorkspaceID: wsID, Name: "A", Protocol: model.ProtocolHTTP}
	store.requests["b"] = model.RequestDef{ID: "b", WorkspaceID: wsID, Name: "B", Protocol: model.ProtocolHTTP}

	proto := newFakeProtocol()
	engine := newTestEngine(store, proto)
	// Templater: resolving "A" triggers a chain lookup of "B"; resolving "B"
	// triggers a chain lookup back to "A", closing the cycle.
	engine.Templater = cyclicTemplater{engine: engine, next: map[model.ID]string{"a": "B", "b": "A"}}

	_, err := engine.RunRequest(context.Background(), "sess1", "a", "", "gui", NoopSink{})
	if err == nil {
		t.Fatal("expected a cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected error to mention a cycle, got: %v", err)
	}
}

// cyclicTemplater simulates a request whose resolution needs a
// response('<next>') reference, driving Engine.ResolveChainRef the same way
// the real templating.Engine would for a `${response('Name').body}` ref.
type cyclicTemplater struct {
	engine *Engine
	next   map[model.ID]string
}

func (c cyclicTemplater) Resolve(ctx context.Context, req model.RequestDef, _ *model.Environment, _ ResponseLookup) (ResolvedRequest, error) {
	nextName, ok := c.next[req.ID]
	if !ok {
		return ResolvedRequest{URL: req.URL, Method: req.Method}, nil
	}
	if _, err := c.engine.ResolveChainRef(ctx, req.WorkspaceID, nextName, "body"); err != nil {
		return ResolvedRequest{}, err
	}
	return ResolvedRequest{URL: req.URL, Method: req.Method}, nil
}

// TestChainDepthCap covers a long, strictly-acyclic chain (r0 -> r1 -> r2 ->
// ...) that must still be rejected once it exceeds maxChainDepth, guarding
// against any bug that would let the visited-set check be defeated.
func TestChainDepthCap(t *testing.T) {
	store := newFakeChainStore()
	const wsID = "ws1"

	n := maxChainDepth + 5
	names := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%d", i)
		names[i] = fmt.Sprintf("R%d", i)
		store.requests[id] = model.RequestDef{ID: id, WorkspaceID: wsID, Name: names[i], Protocol: model.ProtocolHTTP}
	}

	next := map[model.ID]string{}
	for i := 0; i < n-1; i++ {
		next[fmt.Sprintf("r%d", i)] = names[i+1]
	}

	proto := newFakeProtocol()
	engine := newTestEngine(store, proto)
	engine.Templater = cyclicTemplater{engine: engine, next: next}

	_, err := engine.RunRequest(context.Background(), "sess1", "r0", "", "gui", NoopSink{})
	if err == nil {
		t.Fatal("expected a depth-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected error to mention chain depth, got: %v", err)
	}
}

func TestResolveChainRef_UnknownRequestName(t *testing.T) {
	store := newFakeChainStore()
	engine := newTestEngine(store, newFakeProtocol())

	_, err := engine.ResolveChainRef(context.Background(), "ws1", "DoesNotExist", "body")
	if err == nil {
		t.Fatal("expected an error for an unknown request name, got nil")
	}
	if !strings.Contains(err.Error(), "DoesNotExist") {
		t.Errorf("expected error to name the missing request, got: %v", err)
	}
}
