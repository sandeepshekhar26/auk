package main

import (
	"context"
	"strings"
	"testing"

	"apitool/internal/appcore"
	"apitool/internal/core/model"
)

// newSnippetTestApp builds an App over a throwaway workspace dir with the
// real engine (same appcore.NewEngine every entrypoint uses), so these tests
// exercise the actual resolve path rather than a stand-in.
func newSnippetTestApp(t *testing.T) *App {
	t.Helper()
	engine, store, err := appcore.NewEngine(t.TempDir())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return &App{ctx: context.Background(), store: store, engine: engine}
}

// The headline behaviour: a request that has NEVER been sent resolves into a
// runnable snippet — environment variables expanded, `:name` path params
// substituted and encoded, query params merged, auth header computed.
func TestResolveForSnippet_ResolvesWithoutSending(t *testing.T) {
	a := newSnippetTestApp(t)
	const wsID = "ws1"
	if err := a.store.PutWorkspace(model.Workspace{ID: wsID, Name: "Demo"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := a.store.PutEnvironment(model.Environment{
		ID: "env1", WorkspaceID: wsID, Name: "Local",
		Variables: []model.KeyValue{
			{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
			{Key: "userId", Value: "u 42", Enabled: true},
		},
	}, nil); err != nil {
		t.Fatalf("PutEnvironment: %v", err)
	}
	if err := a.store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: wsID, Name: "Get user",
		Protocol: model.ProtocolHTTP, Method: "POST",
		URL:        "${baseUrl}/users/:id/posts",
		PathParams: []model.KeyValue{{Key: "id", Value: "${userId}", Enabled: true}},
		Params:     []model.KeyValue{{Key: "include", Value: "all", Enabled: true}},
		Headers:    []model.KeyValue{{Key: "X-Trace", Value: "on", Enabled: true}, {Key: "", Value: "dropped", Enabled: true}},
		Body:       &model.RequestBody{Kind: model.BodyJSON, Text: `{"hi":"there"}`},
		Auth:       &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: "tok-123"}},
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	// History is a GLOBAL store (internal/storage/history.go), not scoped to
	// this test's temp workspace dir, so "nothing was dispatched" is asserted
	// as "the count didn't move" rather than "the log is empty".
	before, err := a.store.ListHistory()
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}

	got, err := a.ResolveForSnippet("r1", "env1")
	if err != nil {
		t.Fatalf("ResolveForSnippet: %v", err)
	}

	// `u 42` percent-encodes to `u%2042` in the path; the query param is
	// merged by the http protocol's own buildURL.
	const wantURL = "https://api.example.com/users/u%2042/posts?include=all"
	if got.URL != wantURL {
		t.Errorf("URL = %q, want %q", got.URL, wantURL)
	}
	if got.Method != "POST" {
		t.Errorf("Method = %q, want POST", got.Method)
	}
	if !got.HasBody || got.Body != `{"hi":"there"}` {
		t.Errorf("Body = %q (hasBody=%v), want the request body verbatim", got.Body, got.HasBody)
	}
	if !hasHeader(got.Headers, "Authorization", "Bearer tok-123") {
		t.Errorf("headers %v missing the computed Authorization header", got.Headers)
	}
	if !hasHeader(got.Headers, "X-Trace", "on") {
		t.Errorf("headers %v missing X-Trace", got.Headers)
	}
	for _, h := range got.Headers {
		if h.Key == "" {
			t.Errorf("headers %v include a nameless row the real send would drop", got.Headers)
		}
	}

	after, err := a.store.ListHistory()
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("history grew from %d to %d entries; ResolveForSnippet must not send", len(before), len(after))
	}
}

// A pre-request script shapes the snippet exactly as it shapes the send —
// this is the case the old client-side generator could not reproduce at all.
func TestResolveForSnippet_RunsPreRequestScript(t *testing.T) {
	a := newSnippetTestApp(t)
	if err := a.store.PutWorkspace(model.Workspace{ID: "ws1", Name: "Demo"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := a.store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Name: "Signed",
		Protocol: model.ProtocolHTTP, Method: "GET",
		URL:              "https://api.example.com/ping",
		PreRequestScript: `ctx.setHeader("X-Signature", "abc123")`,
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	got, err := a.ResolveForSnippet("r1", "")
	if err != nil {
		t.Fatalf("ResolveForSnippet: %v", err)
	}
	if !hasHeader(got.Headers, "X-Signature", "abc123") {
		t.Errorf("headers %v missing the header the pre-request script sets", got.Headers)
	}
}

// GraphQL reproduces the protocol's own envelope + Content-Type, not the
// raw query text.
func TestResolveForSnippet_GraphQLEnvelope(t *testing.T) {
	a := newSnippetTestApp(t)
	if err := a.store.PutWorkspace(model.Workspace{ID: "ws1", Name: "Demo"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := a.store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Name: "GQL",
		Protocol: model.ProtocolGraphQL, Method: "GET", // overridden to POST
		URL: "https://api.example.com/t/:tenant/graphql",
		Body: &model.RequestBody{
			Kind: model.BodyGraphQL, Text: "query { me { id } }", GraphQLVariables: `{"a":1}`,
		},
		PathParams: []model.KeyValue{{Key: "tenant", Value: "acme", Enabled: true}},
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}

	got, err := a.ResolveForSnippet("r1", "")
	if err != nil {
		t.Fatalf("ResolveForSnippet: %v", err)
	}
	if got.Method != "POST" {
		t.Errorf("Method = %q, want POST (GraphQL is always a POST)", got.Method)
	}
	if got.URL != "https://api.example.com/t/acme/graphql" {
		t.Errorf("URL = %q, want the path param substituted", got.URL)
	}
	if !strings.Contains(got.Body, `"query":"query { me { id } }"`) || !strings.Contains(got.Body, `"variables":{"a":1}`) {
		t.Errorf("Body = %q, want the {query,variables} envelope", got.Body)
	}
	if !hasHeader(got.Headers, "Content-Type", "application/json") {
		t.Errorf("headers %v missing the Content-Type the graphql protocol always sets", got.Headers)
	}
}

// Streaming protocols have no single-shot snippet form; the binding refuses
// rather than emitting something misleading.
func TestResolveForSnippet_RejectsStreamingProtocols(t *testing.T) {
	a := newSnippetTestApp(t)
	if err := a.store.PutWorkspace(model.Workspace{ID: "ws1", Name: "Demo"}); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if err := a.store.PutRequest(model.RequestDef{
		ID: "r1", WorkspaceID: "ws1", Name: "Stream",
		Protocol: model.ProtocolWebSocket, URL: "wss://example.com/socket",
	}); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	if _, err := a.ResolveForSnippet("r1", ""); err == nil {
		t.Fatal("expected an error for a websocket request")
	}
}

func hasHeader(headers []model.KeyValue, key, value string) bool {
	for _, h := range headers {
		if h.Key == key && h.Value == value {
			return true
		}
	}
	return false
}
