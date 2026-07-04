package scripting

import (
	"context"
	"strings"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

func TestRunPreRequest_SetHeaderAddsNewHeader(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}

	out, err := New().RunPreRequest(context.Background(), `ctx.setHeader("X-Signature", "abc123")`, resolved)
	if err != nil {
		t.Fatalf("RunPreRequest: %v", err)
	}
	if len(out.Headers) != 1 || out.Headers[0].Key != "X-Signature" || out.Headers[0].Value != "abc123" {
		t.Fatalf("expected X-Signature=abc123 header, got %+v", out.Headers)
	}
}

func TestRunPreRequest_SetHeaderOverridesExisting(t *testing.T) {
	resolved := core.ResolvedRequest{
		Method:  "GET",
		URL:     "https://example.com",
		Headers: []model.KeyValue{{Key: "X-Foo", Value: "old", Enabled: true}},
	}

	out, err := New().RunPreRequest(context.Background(), `ctx.setHeader("x-foo", "new")`, resolved)
	if err != nil {
		t.Fatalf("RunPreRequest: %v", err)
	}
	if len(out.Headers) != 1 {
		t.Fatalf("expected override in place (1 header), got %d: %+v", len(out.Headers), out.Headers)
	}
	if out.Headers[0].Value != "new" {
		t.Fatalf("expected overridden value %q, got %q", "new", out.Headers[0].Value)
	}
}

func TestRunPreRequest_CanReadRequestShape(t *testing.T) {
	resolved := core.ResolvedRequest{
		Method:  "POST",
		URL:     "https://example.com/orders",
		Headers: []model.KeyValue{{Key: "Content-Type", Value: "application/json", Enabled: true}},
		Body:    &model.RequestBody{Kind: model.BodyJSON, Text: `{"amount":42}`},
	}

	script := `
		ctx.setHeader("X-Echo-Method", ctx.request.method)
		ctx.setHeader("X-Echo-Url", ctx.request.url)
		ctx.setHeader("X-Echo-Body-Len", String(ctx.request.body.length))
		ctx.setHeader("X-Echo-Content-Type", ctx.request.headers["Content-Type"])
	`
	out, err := New().RunPreRequest(context.Background(), script, resolved)
	if err != nil {
		t.Fatalf("RunPreRequest: %v", err)
	}
	get := func(key string) string {
		for _, h := range out.Headers {
			if strings.EqualFold(h.Key, key) {
				return h.Value
			}
		}
		return ""
	}
	if get("X-Echo-Method") != "POST" {
		t.Fatalf("expected echoed method POST, got %q", get("X-Echo-Method"))
	}
	if get("X-Echo-Url") != "https://example.com/orders" {
		t.Fatalf("expected echoed url, got %q", get("X-Echo-Url"))
	}
	if get("X-Echo-Body-Len") != "13" {
		t.Fatalf("expected body length 13, got %q", get("X-Echo-Body-Len"))
	}
	if get("X-Echo-Content-Type") != "application/json" {
		t.Fatalf("expected echoed content-type, got %q", get("X-Echo-Content-Type"))
	}
}

func TestRunPreRequest_SyntaxErrorReturnsError(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}
	if _, err := New().RunPreRequest(context.Background(), `this is not valid js {{{`, resolved); err == nil {
		t.Fatal("expected an error for invalid script syntax")
	}
}

func TestRunPreRequest_NoSandboxEscape(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}
	// sobek has no require()/fetch()/fs access built in — a script trying to
	// reach outside its sandbox must fail, not silently succeed.
	for _, script := range []string{
		`require("fs")`,
		`fetch("https://evil.example.com")`,
	} {
		if _, err := New().RunPreRequest(context.Background(), script, resolved); err == nil {
			t.Fatalf("expected %q to fail (no such global should exist)", script)
		}
	}
}

func TestRunPreRequest_TimesOutOnInfiniteLoop(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}

	start := time.Now()
	_, err := New().RunPreRequest(context.Background(), `while (true) {}`, resolved)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error for an infinite loop")
	}
	if elapsed > scriptTimeout+2*time.Second {
		t.Fatalf("expected the timeout to be enforced near %s, took %s", scriptTimeout, elapsed)
	}
}

func TestRunPreRequest_EmptyScriptIsANoOp(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}
	out, err := New().RunPreRequest(context.Background(), ``, resolved)
	if err != nil {
		t.Fatalf("RunPreRequest with empty script: %v", err)
	}
	if len(out.Headers) != 0 {
		t.Fatalf("expected no headers added by an empty script, got %+v", out.Headers)
	}
}
