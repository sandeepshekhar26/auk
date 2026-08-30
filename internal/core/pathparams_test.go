package core

import (
	"context"
	"testing"

	"apitool/internal/core/model"
)

func kvs(pairs ...string) []model.KeyValue {
	out := make([]model.KeyValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, model.KeyValue{Key: pairs[i], Value: pairs[i+1], Enabled: true})
	}
	return out
}

func TestApplyPathParams(t *testing.T) {
	tests := []struct {
		name     string
		protocol model.ProtocolKind
		url      string
		params   []model.KeyValue
		want     string
	}{
		{
			name:     "basic substitution",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id/posts",
			params:   kvs("id", "42"),
			want:     "https://api.example.com/users/42/posts",
		},
		{
			// A literal `:name` sitting in the host position must NOT be
			// rewritten — the authority is off-limits, only the path is
			// substituted. Regression test for the authority-split fix.
			name:     "placeholder in host position is left untouched",
			protocol: model.ProtocolHTTP,
			url:      "https://:gateway/api/:id",
			params:   kvs("gateway", "evil.example.com", "id", "7"),
			want:     "https://:gateway/api/7",
		},
		{
			name:     "scheme-relative url still protects the authority",
			protocol: model.ProtocolHTTP,
			url:      "//:host/api/:id",
			params:   kvs("host", "nope", "id", "7"),
			want:     "//:host/api/7",
		},
		{
			name:     "multiple placeholders including a repeat",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/orgs/:org/users/:id/ref/:id",
			params:   kvs("org", "acme", "id", "7"),
			want:     "https://api.example.com/orgs/acme/users/7/ref/7",
		},
		{
			name:     "value is percent-encoded as one segment",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id",
			params:   kvs("id", "a b/c?d#e&f=g;h+i"),
			want:     "https://api.example.com/users/a%20b%2Fc%3Fd%23e%26f%3Dg%3Bh%2Bi",
		},
		{
			name:     "unreserved characters survive unescaped",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/files/:name",
			params:   kvs("name", "report-2026.v1_final~draft"),
			want:     "https://api.example.com/files/report-2026.v1_final~draft",
		},
		{
			name:     "non-ascii is utf-8 percent-encoded per byte",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:name",
			params:   kvs("name", "über"),
			want:     "https://api.example.com/users/%C3%BCber",
		},
		{
			name:     "missing row leaves the literal placeholder",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id",
			params:   kvs("other", "x"),
			want:     "https://api.example.com/users/:id",
		},
		{
			name:     "empty value leaves the literal placeholder",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id",
			params:   kvs("id", ""),
			want:     "https://api.example.com/users/:id",
		},
		{
			name:     "nil params leave the url untouched",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id",
			params:   nil,
			want:     "https://api.example.com/users/:id",
		},
		{
			name:     "disabled rows still substitute (Enabled is meaningless here)",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id",
			params:   []model.KeyValue{{Key: "id", Value: "9"}},
			want:     "https://api.example.com/users/9",
		},
		{
			name:     "query string is never substituted",
			protocol: model.ProtocolHTTP,
			url:      "https://api.example.com/users/:id?next=/users/:id&filter=:id",
			params:   kvs("id", "42"),
			want:     "https://api.example.com/users/42?next=/users/:id&filter=:id",
		},
		{
			name:     "explicit port in the host is untouched",
			protocol: model.ProtocolHTTP,
			url:      "https://example.com:443/users/:id",
			params:   kvs("id", "42", "443", "nope"),
			want:     "https://example.com:443/users/42",
		},
		{
			name:     "a numeric-leading segment is not a placeholder",
			protocol: model.ProtocolHTTP,
			url:      "https://example.com/v1/:443/x",
			params:   kvs("443", "nope"),
			want:     "https://example.com/v1/:443/x",
		},
		{
			name:     "a colon inside a longer segment is not a placeholder",
			protocol: model.ProtocolHTTP,
			url:      "https://example.com/v1/items:batchGet",
			params:   kvs("batchGet", "nope"),
			want:     "https://example.com/v1/items:batchGet",
		},
		{
			name:     "templated host is left alone, path still substitutes",
			protocol: model.ProtocolHTTP,
			url:      "${baseUrl}/users/:id",
			params:   kvs("id", "42"),
			want:     "${baseUrl}/users/42",
		},
		{
			name:     "graphql urls get substitution too",
			protocol: model.ProtocolGraphQL,
			url:      "https://api.example.com/t/:tenant/graphql",
			params:   kvs("tenant", "acme"),
			want:     "https://api.example.com/t/acme/graphql",
		},
		{
			name:     "grpc host:port target is untouched",
			protocol: model.ProtocolGRPC,
			url:      "example.com:443",
			params:   kvs("443", "nope"),
			want:     "example.com:443",
		},
		{
			name:     "grpc url-shaped target with a placeholder is still untouched",
			protocol: model.ProtocolGRPC,
			url:      "example.com:443/pkg.Service/:method",
			params:   kvs("method", "Get"),
			want:     "example.com:443/pkg.Service/:method",
		},
		{
			name:     "websocket urls are untouched",
			protocol: model.ProtocolWebSocket,
			url:      "wss://example.com/rooms/:room",
			params:   kvs("room", "lobby"),
			want:     "wss://example.com/rooms/:room",
		},
		{
			name:     "sse urls are untouched",
			protocol: model.ProtocolSSE,
			url:      "https://example.com/streams/:id",
			params:   kvs("id", "42"),
			want:     "https://example.com/streams/:id",
		},
		{
			name:     "hostless url with no path is untouched",
			protocol: model.ProtocolHTTP,
			url:      "example.com:8080",
			params:   kvs("8080", "nope"),
			want:     "example.com:8080",
		},
		{
			name:     "empty url",
			protocol: model.ProtocolHTTP,
			url:      "",
			params:   kvs("id", "42"),
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyPathParams(tt.protocol, tt.url, tt.params); got != tt.want {
				t.Errorf("applyPathParams()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// Substitution must be idempotent in the sense that a substituted VALUE can
// never be re-read as another placeholder on a later pass — the strict
// segment encoding escapes a leading colon.
func TestApplyPathParamsValueCannotIntroduceAPlaceholder(t *testing.T) {
	once := applyPathParams(model.ProtocolHTTP, "https://example.com/u/:id", kvs("id", ":other"))
	if once != "https://example.com/u/%3Aother" {
		t.Fatalf("got %q", once)
	}
	twice := applyPathParams(model.ProtocolHTTP, once, kvs("other", "pwned"))
	if twice != once {
		t.Errorf("second pass rewrote an already-substituted value: %q", twice)
	}
}

// The engine must substitute BEFORE auth runs, so a signing auth method
// signs the URL that actually goes on the wire.
func TestResolveAndAuthorizeSubstitutesPathParamsBeforeAuth(t *testing.T) {
	store := newFakeChainStore()
	store.requests["req-1"] = model.RequestDef{
		ID:          "req-1",
		WorkspaceID: "ws-1",
		Protocol:    model.ProtocolHTTP,
		Method:      "GET",
		URL:         "https://api.example.com/users/:id/posts",
		PathParams:  kvs("id", "a b"),
		Auth:        &model.AuthConfig{Kind: model.AuthBearer},
	}

	seen := &urlRecordingAuth{}
	engine := NewEngine(store, pathParamTemplater{}, seen, AllowAllPolicy{})

	_, resolved, err := engine.ResolveForExecution(t.Context(), "req-1", "", "gui")
	if err != nil {
		t.Fatalf("ResolveForExecution: %v", err)
	}
	const want = "https://api.example.com/users/a%20b/posts"
	if resolved.URL != want {
		t.Errorf("resolved URL = %q, want %q", resolved.URL, want)
	}
	if seen.url != want {
		t.Errorf("auth saw URL %q, want the substituted %q", seen.url, want)
	}
}

// pathParamTemplater expands no `${...}` refs but DOES copy PathParams
// through, which is the contract templating.Engine.Resolve fulfils and
// applyPathParams depends on. (chaining_test.go's passthroughTemplater
// predates PathParams and deliberately drops them.)
type pathParamTemplater struct{}

func (pathParamTemplater) Resolve(_ context.Context, req model.RequestDef, _ *model.Environment, _ ResponseLookup) (ResolvedRequest, error) {
	return ResolvedRequest{URL: req.URL, Method: req.Method, PathParams: req.PathParams}, nil
}

// urlRecordingAuth captures the URL as it looked when auth ran, so a test
// can assert ordering (substitution first, then signing).
type urlRecordingAuth struct{ url string }

func (a *urlRecordingAuth) Apply(_ context.Context, _ model.AuthConfig, req ResolvedRequest) (ResolvedRequest, error) {
	a.url = req.URL
	return req, nil
}
