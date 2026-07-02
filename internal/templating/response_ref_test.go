package templating

import (
	"context"
	"fmt"
	"testing"

	"apitool/internal/core/model"
)

func TestParseResponseRef(t *testing.T) {
	cases := []struct {
		name        string
		expr        string
		wantReqName string
		wantPath    string
		wantOk      bool
	}{
		{"body path, single quotes", `response('Login').body.token`, "Login", "body.token", true},
		{"body path, double quotes", `response("Login").body.token`, "Login", "body.token", true},
		{"status only", `response('Login').status`, "Login", "status", true},
		{"header path", `response('Login').header.X-Trace-Id`, "Login", "header.X-Trace-Id", true},
		{"no path suffix", `response('Login')`, "Login", "", true},
		{"name with spaces", `response('Get User Profile').body.id`, "Get User Profile", "body.id", true},
		{"not a response ref", `uuid()`, "", "", false},
		{"bare variable", `baseUrl`, "", "", false},
		{"unrelated func call", `hash.md5('x')`, "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotPath, gotOk := ParseResponseRef(tc.expr)
			if gotOk != tc.wantOk || gotName != tc.wantReqName || gotPath != tc.wantPath {
				t.Errorf("ParseResponseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.expr, gotName, gotPath, gotOk, tc.wantReqName, tc.wantPath, tc.wantOk)
			}
		})
	}
}

// fakeResolver lets eval()'s response() branch be exercised without a real
// core.Engine (which would import this package and cycle).
type fakeResolver struct {
	calls int
	err   error
	value string
}

func (f *fakeResolver) ResolveChainRef(_ context.Context, _ model.ID, requestName, path string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

func TestResolve_ResponseRefCallsResolver(t *testing.T) {
	resolver := &fakeResolver{value: "resolved-token"}
	e := New(resolver)

	req := model.RequestDef{
		WorkspaceID: "ws1",
		Method:      "GET",
		URL:         "https://example.test/x?token=${response('Login').body.token}",
	}

	resolved, err := e.Resolve(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if want := "https://example.test/x?token=resolved-token"; resolved.URL != want {
		t.Errorf("got URL %q, want %q", resolved.URL, want)
	}
	if resolver.calls != 1 {
		t.Errorf("expected resolver to be called once, got %d", resolver.calls)
	}
}

func TestResolve_ResponseRefWithoutResolverFails(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{
		WorkspaceID: "ws1",
		Method:      "GET",
		URL:         "https://example.test/x?token=${response('Login').body.token}",
	}

	if _, err := e.Resolve(context.Background(), req, nil, nil); err == nil {
		t.Fatal("expected an error when no ChainResolver is configured, got nil")
	}
}

func TestResolve_ResponseRefPropagatesResolverError(t *testing.T) {
	resolver := &fakeResolver{err: fmt.Errorf("boom")}
	e := New(resolver)
	req := model.RequestDef{
		WorkspaceID: "ws1",
		Method:      "GET",
		URL:         "https://example.test/x?token=${response('Login').body.token}",
	}

	_, err := e.Resolve(context.Background(), req, nil, nil)
	if err == nil {
		t.Fatal("expected an error to propagate from the resolver, got nil")
	}
}
