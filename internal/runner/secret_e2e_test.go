package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	httpprotocol "apitool/internal/protocols/http"
	"apitool/internal/scripting"
	"apitool/internal/storage"
	"apitool/internal/templating"
)

// ---------------------------------------------------------------------------
// The secret findings, end-to-end through the REAL sobek scripter (findings 1
// & 2). A fake in-memory keychain stands in for the OS keychain so these run
// in CI without a permission prompt.
// ---------------------------------------------------------------------------

type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemSecrets() *memSecrets { return &memSecrets{m: map[string]string{}} }

func (s *memSecrets) Get(service, account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[service+"/"+account]
	if !ok {
		return "", fmt.Errorf("no secret for %s/%s", service, account)
	}
	return v, nil
}
func (s *memSecrets) Set(service, account, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[service+"/"+account] = value
	return nil
}
func (s *memSecrets) Delete(service, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, service+"/"+account)
	return nil
}

// newSecretEngine wires the same engine appcore does, but with a fake keychain
// injected so secret values can be set and resolved in a test.
func newSecretEngine(t *testing.T) (*core.Engine, *storage.FileStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewFileStore(dir, storage.WithSecretStore(newMemSecrets()))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.Scripter = scripting.New()
	engine.RegisterProtocol(httpprotocol.New())
	return engine, store
}

// newFakeProtoEngine wires the real templater and real scripter (the surfaces
// finding 3's concurrency guarantee is about) over an in-process reflectProtocol
// — no net/http, so a -race run does not also exercise the shared HTTP client's
// transport internals.
func newFakeProtoEngine(t *testing.T) (*core.Engine, *storage.FileStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.Scripter = scripting.New()
	engine.RegisterProtocol(reflectProtocol{})
	return engine, store
}

// TestScriptCannotReadSecretValueEndToEnd proves finding 1 with the real
// runtime: vars.get('apiKey') returns undefined, and a laundering copy into a
// plain variable reaches disk without the secret value.
func TestScriptCannotReadSecretValueEndToEnd(t *testing.T) {
	srv := jsonEchoServer(t, nil)
	engine, store := newSecretEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	env := model.ID("prod")
	must(t, store.PutEnvironment(model.Environment{
		ID: env, WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}, map[string]string{"apiKey": "SUPER_SECRET_VALUE"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: ws, Name: "R1",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo",
		PostResponseScript: `
			test('secret is unreadable', function () { expect(vars.get('apiKey')).toBe(undefined) })
			vars.set('leak', 'v:' + vars.get('apiKey'))
		`,
	}))

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", env, "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if !resp.Passed() {
		t.Fatalf("the 'secret is unreadable' test must pass; tests=%+v scriptErr=%q", resp.TestResults, resp.ScriptError)
	}

	got, err := store.GetEnvironment(env)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	for _, kv := range got.Variables {
		if kv.Key == "leak" && strings.Contains(kv.Value, "SUPER_SECRET_VALUE") {
			t.Fatalf("the secret was laundered onto disk via 'leak': %q", kv.Value)
		}
	}
}

// TestCrossEnvSecretShadowEndToEnd proves finding 2 with the real runtime: a
// script writing apiKey with NO environment selected (where the runtime guard
// cannot know it is a secret) is stopped by the engine's workspace-wide union
// guard, so resolving under Prod still yields the real keychain value.
func TestCrossEnvSecretShadowEndToEnd(t *testing.T) {
	srv := jsonEchoServer(t, nil)
	engine, store := newSecretEngine(t)
	const ws = model.ID("ws")
	must(t, store.PutWorkspace(model.Workspace{ID: ws, Name: "W"}))
	prod := model.ID("prod")
	must(t, store.PutEnvironment(model.Environment{
		ID: prod, WorkspaceID: ws, Name: "Prod",
		Variables: []model.KeyValue{{Key: "apiKey", Enabled: true}},
		Secrets:   []string{"apiKey"},
	}, map[string]string{"apiKey": "REAL_KEYCHAIN"}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "writer", WorkspaceID: ws, Name: "Writer",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo",
		PostResponseScript: `vars.set('apiKey', 'EVIL')`,
	}))
	must(t, store.PutRequest(model.RequestDef{
		ID: "reader", WorkspaceID: ws, Name: "Reader",
		Protocol: model.ProtocolHTTP, Method: "GET", URL: srv.URL + "/echo?k=${apiKey}",
	}))

	// With no env selected, the runtime's own Secrets list is empty, so it
	// lets the write through — the engine's union guard is what must stop it.
	if _, err := engine.RunRequest(context.Background(), "s1", "writer", "", "gui", core.NoopSink{}); err != nil {
		t.Fatalf("writer: %v", err)
	}
	resp, err := engine.RunRequest(context.Background(), "s2", "reader", prod, "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	if k, _ := jsonField(resp, "k"); k != "REAL_KEYCHAIN" {
		t.Fatalf("the attacker's session value shadowed the real keychain secret: ${apiKey} resolved to %q", k)
	}
}
