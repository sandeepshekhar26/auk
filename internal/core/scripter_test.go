package core

import (
	"context"
	"encoding/base64"
	"testing"

	"apitool/internal/core/model"
)

// fakeScripter is a minimal Scripter for testing the ENGINE's wiring
// (does RunRequest call it at the right point, with the right data, and
// does its output actually reach Protocol.Execute) — internal/scripting's
// own test suite covers the real sobek execution semantics; this only
// needs to prove the chokepoint ordering (after auth, before Policy).
type fakeScripter struct {
	calls   int
	lastReq ResolvedRequest
}

func (s *fakeScripter) RunPreRequest(_ context.Context, script string, resolved ResolvedRequest) (ResolvedRequest, error) {
	s.calls++
	s.lastReq = resolved
	resolved.Headers = append(resolved.Headers, model.KeyValue{Key: "X-Script-Ran", Value: script, Enabled: true})
	return resolved, nil
}

// capturingProtocol records the ResolvedRequest it was actually asked to
// execute, so a test can assert a script's header edit reached the wire,
// not just the intermediate resolved value inside resolveAndAuthorize.
type capturingProtocol struct {
	lastResolved ResolvedRequest
}

func (p *capturingProtocol) Kind() model.ProtocolKind { return model.ProtocolHTTP }

func (p *capturingProtocol) Execute(_ context.Context, _ *Session, req model.RequestDef, resolved ResolvedRequest) (model.ResponseData, error) {
	p.lastResolved = resolved
	return model.ResponseData{RequestID: req.ID, Status: 200, StatusText: "200 OK", BodyBase64: base64.StdEncoding.EncodeToString([]byte("{}"))}, nil
}

func TestRunRequest_InvokesScripterAndAppliesItsHeaders(t *testing.T) {
	store := newFakeChainStore()
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Name: "Scripted", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://example.test/thing",
		PreRequestScript: `ctx.setHeader("ignored-by-fake", "x")`,
	}

	proto := &capturingProtocol{}
	scripter := &fakeScripter{}
	engine := newTestEngine(store, proto)
	engine.Scripter = scripter

	if _, err := engine.RunRequest(context.Background(), "sess1", "r1", "", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}

	if scripter.calls != 1 {
		t.Fatalf("expected the scripter to run exactly once, got %d", scripter.calls)
	}
	found := false
	for _, h := range proto.lastResolved.Headers {
		if h.Key == "X-Script-Ran" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the script's header to reach Protocol.Execute, got headers: %+v", proto.lastResolved.Headers)
	}
}

func TestRunRequest_SkipsScripterWhenNoScriptConfigured(t *testing.T) {
	store := newFakeChainStore()
	store.requests["r1"] = model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Name: "Unscripted", Protocol: model.ProtocolHTTP,
		Method: "GET", URL: "https://example.test/thing",
	}

	proto := &capturingProtocol{}
	scripter := &fakeScripter{}
	engine := newTestEngine(store, proto)
	engine.Scripter = scripter

	if _, err := engine.RunRequest(context.Background(), "sess1", "r1", "", "gui", NoopSink{}); err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if scripter.calls != 0 {
		t.Fatalf("expected the scripter NOT to run when PreRequestScript is empty, got %d calls", scripter.calls)
	}
}
