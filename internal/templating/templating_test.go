package templating

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"apitool/internal/core/model"
)

func TestEncodeURL(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"a b&c"}, want: "a+b%26c"},
		{name: "missing arg", args: nil, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["encode.url"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJSONGet(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "happy path nested field",
			args: []string{`{"a":{"b":[1,2,{"c":"hello"}]}}`, "a.b[2].c"},
			want: "hello",
		},
		{
			name: "top level array index",
			args: []string{`[10,20,30]`, "[1]"},
			want: "20",
		},
		{
			name:    "missing field",
			args:    []string{`{"a":1}`, "b"},
			wantErr: true,
		},
		{
			name:    "invalid json",
			args:    []string{`not json`, "a"},
			wantErr: true,
		},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["json.get"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegexMatch(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"order-4821", `\d+`}, want: "4821"},
		{name: "no match", args: []string{"no digits here", `\d+`}, wantErr: true},
		{name: "invalid pattern", args: []string{"abc", `(unclosed`}, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["regex.match"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegexReplace(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "happy path", args: []string{"foo123bar", `\d+`, "X"}, want: "fooXbar"},
		{name: "invalid pattern", args: []string{"abc", `(unclosed`, "X"}, wantErr: true},
		{name: "missing args", args: []string{"abc"}, wantErr: true},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["regex.replace"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimestampOffset(t *testing.T) {
	base := int64(1_700_000_000)
	cases := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "happy path plus one hour",
			args: []string{strconv.FormatInt(base, 10), "+1h"},
			want: strconv.FormatInt(base+3600, 10),
		},
		{
			name: "minus thirty minutes",
			args: []string{strconv.FormatInt(base, 10), "-30m"},
			want: strconv.FormatInt(base-1800, 10),
		},
		{
			name:    "invalid offset spec",
			args:    []string{strconv.FormatInt(base, 10), "not-a-duration"},
			wantErr: true,
		},
		{
			name:    "invalid unix seconds",
			args:    []string{"not-a-number", "+1h"},
			wantErr: true,
		},
	}
	e := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.funcs["timestamp.offset"](tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run(`"now" resolves relative to the current time, not a literal timestamp`, func(t *testing.T) {
		before := time.Now().Unix()
		got, err := e.funcs["timestamp.offset"]([]string{"now", "+1h"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		gotSecs, _ := strconv.ParseInt(got, 10, 64)
		after := time.Now().Unix()
		// gotSecs should be ~1h after "now", bounded by [before+3600, after+3600]
		// to tolerate the (near-zero) time elapsed running the test itself.
		if gotSecs < before+3600 || gotSecs > after+3600 {
			t.Fatalf("got %d, want within [%d, %d]", gotSecs, before+3600, after+3600)
		}
	})

	t.Run("empty string also means now", func(t *testing.T) {
		_, err := e.funcs["timestamp.offset"]([]string{"", "+1h"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestTimestampFormat(t *testing.T) {
	e := New(nil)
	t.Run("happy path", func(t *testing.T) {
		got, err := e.funcs["timestamp.format"]([]string{"0", "2006-01-02T15:04:05Z"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05Z")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("invalid unix seconds", func(t *testing.T) {
		_, err := e.funcs["timestamp.format"]([]string{"not-a-number", "2006-01-02"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
	})
}

func TestFsRead(t *testing.T) {
	e := New(nil)
	t.Run("happy path", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/hello.txt"
		if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		got, err := e.funcs["fs.read"]([]string{path})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "hello world" {
			t.Fatalf("got %q, want %q", got, "hello world")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := e.funcs["fs.read"]([]string{"/nonexistent/path/does-not-exist.txt"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
	})
}

func TestCookie(t *testing.T) {
	t.Run("no cookie captured yet", func(t *testing.T) {
		e := New(nil)
		_, err := e.eval(context.Background(), "cookie(session)", "ws-1", nil, nil)
		if err == nil {
			t.Fatalf("expected error, got none")
		}
		if !strings.Contains(err.Error(), "no such cookie") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("reads a captured cookie for its workspace", func(t *testing.T) {
		e := New(nil)
		e.CaptureCookies("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=abc123"}})

		got, err := e.eval(context.Background(), "cookie(session)", "ws-1", nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "abc123" {
			t.Fatalf("got %q, want %q", got, "abc123")
		}
	})

	t.Run("workspace-scoped: not visible from a different workspace", func(t *testing.T) {
		e := New(nil)
		e.CaptureCookies("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "session=abc123"}})

		_, err := e.eval(context.Background(), "cookie(session)", "ws-2", nil, nil)
		if err == nil {
			t.Fatalf("expected error reading ws-1's cookie from ws-2, got none")
		}
	})

	t.Run("resolves end-to-end through a request URL", func(t *testing.T) {
		e := New(nil)
		e.CaptureCookies("ws-1", []model.KeyValue{{Key: "Set-Cookie", Value: "token=xyz"}})

		req := model.RequestDef{WorkspaceID: "ws-1", URL: "https://api.example.com/x?t=${cookie(token)}", Method: "GET"}
		resolved, err := e.Resolve(context.Background(), req, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "https://api.example.com/x?t=xyz"
		if resolved.URL != want {
			t.Fatalf("got %q, want %q", resolved.URL, want)
		}
	})
}

// TestOnePasswordVariable_AbsentCLI forces the "op not on PATH" path
// deterministically (PATH pointed at an empty temp dir) rather than relying
// on whatever machine runs this suite — op is confirmed not installed in
// the environment this was written in, and there's no test 1Password vault
// to exercise a real read against, matching the same "verify the graceful-
// absence path" approach used in internal/onepassword's own tests.
func TestOnePasswordVariable_AbsentCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	e := New(nil)
	env := &model.Environment{
		Variables: []model.KeyValue{
			{Key: "apiToken", Value: "op://Work/GitHub/token", Enabled: true},
		},
	}
	req := model.RequestDef{URL: "https://api.example.com/x?t=${apiToken}", Method: "GET"}

	_, err := e.Resolve(context.Background(), req, env, nil)
	if err == nil {
		t.Fatal("Resolve: want an error when op isn't on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "apiToken") {
		t.Fatalf("Resolve error = %q, want it to name the variable (apiToken)", err.Error())
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("Resolve error = %q, want it to mention op isn't on PATH", err.Error())
	}
}

// TestOnePasswordVariable_UnreferencedBrokenRefDoesNotFailRequest guards
// against resolving every environment variable eagerly (an earlier version
// of this feature did exactly that): an op:// variable that EXISTS,
// ENABLED, in the environment, but that this particular request never
// references, must not make the request fail just because op isn't
// installed. Only a variable a request actually uses should ever trigger a
// 1Password lookup.
func TestOnePasswordVariable_UnreferencedBrokenRefDoesNotFailRequest(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	e := New(nil)
	env := &model.Environment{
		Variables: []model.KeyValue{
			{Key: "host", Value: "api.example.com", Enabled: true},
			{Key: "unrelatedToken", Value: "op://Work/GitHub/token", Enabled: true},
		},
	}
	req := model.RequestDef{URL: "https://${host}/x", Method: "GET"}

	resolved, err := e.Resolve(context.Background(), req, env, nil)
	if err != nil {
		t.Fatalf("unexpected error (request never references unrelatedToken): %v", err)
	}
	want := "https://api.example.com/x"
	if resolved.URL != want {
		t.Fatalf("got %q, want %q", resolved.URL, want)
	}
}

// TestOnePasswordVariable_PlainVariablesUnaffected guards the common case:
// a request with no op:// values must resolve exactly as before this
// feature existed, even with a variable disabled and PATH forced empty (so
// this can't accidentally pass only because the real host has op installed).
func TestOnePasswordVariable_PlainVariablesUnaffected(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	e := New(nil)
	env := &model.Environment{
		Variables: []model.KeyValue{
			{Key: "host", Value: "api.example.com", Enabled: true},
			{Key: "ignored", Value: "op://should/not/be/touched", Enabled: false},
		},
	}
	req := model.RequestDef{URL: "https://${host}/x", Method: "GET"}

	resolved, err := e.Resolve(context.Background(), req, env, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://api.example.com/x"
	if resolved.URL != want {
		t.Fatalf("got %q, want %q", resolved.URL, want)
	}
}

func TestPrompt(t *testing.T) {
	e := New(nil)
	t.Run("not supported headlessly", func(t *testing.T) {
		_, err := e.funcs["prompt"]([]string{"Enter value:"})
		if err == nil {
			t.Fatalf("expected error, got none")
		}
		if !strings.Contains(err.Error(), "interactive UI") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

// TestResolveAuthTemplatesFieldsAndDoesNotMutate proves the auth-chaining
// unlock: a `${token}` in the Auth config resolves against the environment,
// and the stored *AuthConfig (a pointer into the store) is NOT mutated.
//
// It also pins the scoping rule: ONLY the sub-struct matching auth.Kind is
// templated. The Auth tab preserves the sub-objects of kinds you switched
// away from, so an inactive Basic block rides along on a Bearer request; it
// must be carried through verbatim (and still deep-copied), never resolved.
func TestResolveAuthTemplatesFields(t *testing.T) {
	e := New(nil)
	env := &model.Environment{Variables: []model.KeyValue{
		{Key: "token", Value: "sk-abc-123", Enabled: true},
		{Key: "user", Value: "ada", Enabled: true},
	}}
	stored := &model.AuthConfig{
		Kind:   model.AuthBearer,
		Bearer: &model.BearerAuth{Token: "${token}"},
		Basic:  &model.BasicAuth{Username: "${user}", Password: "static"},
	}
	req := model.RequestDef{WorkspaceID: "w1"}

	out, err := e.ResolveAuth(context.Background(), req, env, nil, stored)
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if out.Bearer.Token != "sk-abc-123" {
		t.Errorf("bearer token not resolved: %q", out.Bearer.Token)
	}
	if out.Basic.Username != "${user}" || out.Basic.Password != "static" {
		t.Errorf("the INACTIVE basic block must pass through untemplated, got %+v", out.Basic)
	}
	// The stored config must be untouched — it's a pointer into the store.
	if stored.Bearer.Token != "${token}" {
		t.Errorf("stored bearer token was MUTATED to %q — must stay ${token}", stored.Bearer.Token)
	}
	if out.Bearer == stored.Bearer {
		t.Error("resolved Bearer aliases the stored Bearer pointer — must be a copy")
	}
	if out.Basic == stored.Basic {
		t.Error("the inactive Basic aliases the stored Basic pointer — untemplated still means copied")
	}
}

// TestResolveAuth_InactiveBlockWithMissingVariableDoesNotFail is the [MAJOR]
// finding: ResolveAuth used to walk EVERY non-nil sub-struct, and any eval
// error there became a hard "resolve auth templates:" failure in the engine.
// The Auth tab preserves the sub-objects of kinds you switched away from, so a
// request that once used Basic still carries `${legacyPassword}` — and
// deleting that variable aborted every send of a request now using Bearer.
func TestResolveAuth_InactiveBlockWithMissingVariableDoesNotFail(t *testing.T) {
	e := New(nil)
	env := &model.Environment{Variables: []model.KeyValue{
		{Key: "token", Value: "sk-live", Enabled: true},
		// legacyPassword was deleted from the environment.
	}}
	stored := &model.AuthConfig{
		Kind:   model.AuthBearer,
		Bearer: &model.BearerAuth{Token: "${token}"},
		Basic:  &model.BasicAuth{Username: "old", Password: "${legacyPassword}"},
	}

	out, err := e.ResolveAuth(context.Background(), model.RequestDef{WorkspaceID: "w1"}, env, nil, stored)
	if err != nil {
		t.Fatalf("an INACTIVE auth block referencing a missing variable must not fail the send: %v", err)
	}
	if out.Bearer.Token != "sk-live" {
		t.Errorf("the ACTIVE kind must still resolve, got %q", out.Bearer.Token)
	}
	if out.Basic.Password != "${legacyPassword}" {
		t.Errorf("the inactive block must pass through verbatim, got %q", out.Basic.Password)
	}
}

// The mirror image: the ACTIVE kind's unresolvable reference is still an error,
// because that one really does mean the request cannot be sent correctly.
func TestResolveAuth_ActiveBlockWithMissingVariableStillFails(t *testing.T) {
	e := New(nil)
	stored := &model.AuthConfig{
		Kind:   model.AuthBearer,
		Bearer: &model.BearerAuth{Token: "${missingToken}"},
	}
	if _, err := e.ResolveAuth(context.Background(), model.RequestDef{WorkspaceID: "w1"}, nil, nil, stored); err == nil {
		t.Fatal("an unresolvable reference in the ACTIVE auth kind must still be reported")
	}
}

// Every kind gets the same treatment, so this can't rot as kinds are added:
// whichever kind is active resolves, and the seven riding along do not.
func TestResolveAuth_OnlyTheActiveKindIsTemplated(t *testing.T) {
	e := New(nil)
	env := &model.Environment{Variables: []model.KeyValue{{Key: "v", Value: "RESOLVED", Enabled: true}}}
	full := func() *model.AuthConfig {
		return &model.AuthConfig{
			Basic:    &model.BasicAuth{Password: "${v}"},
			Bearer:   &model.BearerAuth{Token: "${v}"},
			APIKey:   &model.APIKeyAuth{Value: "${v}"},
			JWT:      &model.JWTAuth{Secret: "${v}"},
			OAuth2:   &model.OAuth2Auth{ClientSecret: "${v}"},
			AWSSigV4: &model.AWSSigV4Auth{SecretAccessKey: "${v}"},
			OAuth1:   &model.OAuth1Auth{ConsumerSecret: "${v}"},
			Digest:   &model.DigestAuth{Password: "${v}"},
		}
	}
	for _, tc := range []struct {
		kind   model.AuthKind
		active func(*model.AuthConfig) string
	}{
		{model.AuthBasic, func(a *model.AuthConfig) string { return a.Basic.Password }},
		{model.AuthBearer, func(a *model.AuthConfig) string { return a.Bearer.Token }},
		{model.AuthAPIKey, func(a *model.AuthConfig) string { return a.APIKey.Value }},
		{model.AuthJWT, func(a *model.AuthConfig) string { return a.JWT.Secret }},
		{model.AuthOAuth2, func(a *model.AuthConfig) string { return a.OAuth2.ClientSecret }},
		{model.AuthAWSSigV4, func(a *model.AuthConfig) string { return a.AWSSigV4.SecretAccessKey }},
		{model.AuthOAuth1, func(a *model.AuthConfig) string { return a.OAuth1.ConsumerSecret }},
		{model.AuthDigest, func(a *model.AuthConfig) string { return a.Digest.Password }},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			stored := full()
			stored.Kind = tc.kind
			out, err := e.ResolveAuth(context.Background(), model.RequestDef{WorkspaceID: "w1"}, env, nil, stored)
			if err != nil {
				t.Fatalf("ResolveAuth: %v", err)
			}
			if got := tc.active(out); got != "RESOLVED" {
				t.Errorf("the active %s field did not resolve: %q", tc.kind, got)
			}
			// Count how many of the eight still hold the literal template:
			// seven inactive ones must, and the stored config must be intact.
			literals := 0
			for _, got := range []string{
				out.Basic.Password, out.Bearer.Token, out.APIKey.Value, out.JWT.Secret,
				out.OAuth2.ClientSecret, out.AWSSigV4.SecretAccessKey, out.OAuth1.ConsumerSecret, out.Digest.Password,
			} {
				if got == "${v}" {
					literals++
				}
			}
			if literals != 7 {
				t.Errorf("expected exactly 7 untouched inactive blocks, got %d", literals)
			}
			if stored.Bearer.Token != "${v}" || stored.Digest.Password != "${v}" {
				t.Errorf("the stored config was mutated: %+v", stored)
			}
		})
	}
}
