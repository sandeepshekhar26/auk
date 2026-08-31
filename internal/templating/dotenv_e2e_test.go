package templating_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apitool/internal/core/model"
	"apitool/internal/secretref"
	"apitool/internal/templating"
)

// The connector's whole promise, end to end: a variable points at the
// project's own .env, and ${apiToken} resolves to what is in that file —
// without AUK ever storing a copy.
func TestDotEnvVariableResolvesThroughTemplating(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, ".env"),
		[]byte("# project secrets\nAPI_TOKEN=tok-from-dotenv-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := templating.New(nil).WithSecretRefs(secretref.Default(ws))
	env := &model.Environment{
		ID: "env-1", Name: "Local",
		Variables: []model.KeyValue{
			{Key: "apiToken", Value: "env://.env#API_TOKEN", Enabled: true},
			{Key: "host", Value: "api.local", Enabled: true},
		},
	}
	req := model.RequestDef{
		ID: "r1", Protocol: model.ProtocolHTTP, Method: "GET",
		URL:     "https://${host}/v1/me",
		Headers: []model.KeyValue{{Key: "Authorization", Value: "Bearer ${apiToken}", Enabled: true}},
	}

	out, err := e.Resolve(context.Background(), req, env, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var authHeader string
	for _, h := range out.Headers {
		if h.Key == "Authorization" {
			authHeader = h.Value
		}
	}
	if authHeader != "Bearer tok-from-dotenv-123" {
		t.Fatalf("Authorization = %q, want the value read from .env", authHeader)
	}
	if out.URL != "https://api.local/v1/me" {
		t.Fatalf("URL = %q", out.URL)
	}

	// A broken reference must fail LOUDLY, never send the literal string as a
	// credential.
	env.Variables[0].Value = "env://.env#NOT_THERE"
	if _, err := e.Resolve(context.Background(), req, env, nil); err == nil {
		t.Fatal("a missing .env key resolved silently")
	} else if !strings.Contains(err.Error(), "NOT_THERE") {
		t.Errorf("err = %v; it should name the missing key", err)
	}
}
