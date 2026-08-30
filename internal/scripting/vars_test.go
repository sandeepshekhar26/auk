package scripting

import (
	"context"
	"strings"
	"testing"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

func TestVars_SetGetUnsetRoundTrip(t *testing.T) {
	script := `
		test("reads what the environment already had", function () { expect(vars.get("base")).toBe("https://api.test") })
		test("misses read as undefined", function () { expect(vars.get("nope")).toBe(undefined) })
		vars.set("token", "abc123")
		test("reads back what it just wrote", function () { expect(vars.get("token")).toBe("abc123") })
		vars.unset("base")
		test("unset takes effect immediately", function () { expect(vars.get("base")).toBe(undefined) })
	`
	out := mustRunPost(t, script, core.PostResponseInput{
		Response: response(200, "{}"),
		Vars:     map[string]string{"base": "https://api.test"},
	})

	for _, tr := range out.Tests {
		if !tr.Passed {
			t.Errorf("%s: %s", tr.Name, tr.Error)
		}
	}
	if got := out.VarWrites["token"]; got != "abc123" {
		t.Errorf("VarWrites[token] = %q, want %q", got, "abc123")
	}
	if len(out.VarUnsets) != 1 || out.VarUnsets[0] != "base" {
		t.Errorf("VarUnsets = %v, want [base]", out.VarUnsets)
	}
	// Vars is the full mutated set: the write is in, the unset name is gone.
	if out.Vars["token"] != "abc123" {
		t.Errorf("Vars[token] = %q, want the written value", out.Vars["token"])
	}
	if _, ok := out.Vars["base"]; ok {
		t.Errorf("Vars still holds the unset name: %v", out.Vars)
	}
}

func TestVars_SetCoercesToStrings(t *testing.T) {
	out := mustRunPost(t, `
		vars.set("n", 42)
		vars.set("f", 1.5)
		vars.set("b", true)
		vars.set("o", {a:1})
		vars.set("a", [1,2])
	`, core.PostResponseInput{Response: response(200, "{}")})

	want := map[string]string{"n": "42", "f": "1.5", "b": "true", "o": `{"a":1}`, "a": "[1,2]"}
	for name, expected := range want {
		if out.VarWrites[name] != expected {
			t.Errorf("VarWrites[%s] = %q, want %q", name, out.VarWrites[name], expected)
		}
	}
}

// The single most common chaining bug is vars.set from a path that did not
// match. Storing "" there turns it into a mystery 401 two requests later, so
// it throws instead.
func TestVars_SetRejectsUndefinedAndNull(t *testing.T) {
	out := mustRunPost(t, `
		test("undefined", function () { vars.set("token", response.json().missing) })
		test("null", function () { vars.set("token", null) })
	`, core.PostResponseInput{Response: response(200, `{"other":1}`)})

	for _, tr := range out.Tests {
		if tr.Passed {
			t.Errorf("%s: expected vars.set to refuse the value", tr.Name)
		}
		if !strings.Contains(tr.Error, "nothing was stored") {
			t.Errorf("%s: expected a message explaining nothing was stored, got %q", tr.Name, tr.Error)
		}
	}
	if len(out.VarWrites) != 0 {
		t.Fatalf("expected no variable writes, got %v", out.VarWrites)
	}
}

func TestVars_RejectsEmptyName(t *testing.T) {
	out := mustRunPost(t, `
		test("empty", function () { vars.set("", "x") })
		test("non-string", function () { vars.set(7, "x") })
	`, core.PostResponseInput{Response: response(200, "{}")})
	for _, tr := range out.Tests {
		if tr.Passed {
			t.Errorf("%s: expected vars.set to refuse the name", tr.Name)
		}
	}
}

// The secrets guard: a script may READ a keychain-backed secret (that is
// what makes request signing possible) but may never overwrite or delete
// one, because its value is not allowed to exist anywhere on disk.
func TestVars_CannotWriteASecretBackedName(t *testing.T) {
	in := core.PostResponseInput{
		Response: response(200, "{}"),
		Vars:     map[string]string{"apiKey": "from-keychain", "base": "https://api.test"},
		Secrets:  []string{"apiKey"},
	}

	out := mustRunPost(t, `
		test("can read it", function () { expect(vars.get("apiKey")).toBe("from-keychain") })
		test("cannot overwrite it", function () { vars.set("apiKey", "clobbered") })
		test("cannot unset it", function () { vars.unset("apiKey") })
		vars.set("base", "https://other.test")
	`, in)

	if !out.Tests[0].Passed {
		t.Errorf("a script should be able to READ a secret: %s", out.Tests[0].Error)
	}
	for _, tr := range out.Tests[1:] {
		if tr.Passed {
			t.Errorf("%s: expected the secrets guard to refuse the write", tr.Name)
		}
		if !strings.Contains(tr.Error, "secret") || !strings.Contains(tr.Error, "keychain") {
			t.Errorf("%s: expected a message explaining the guard, got %q", tr.Name, tr.Error)
		}
	}
	if _, written := out.VarWrites["apiKey"]; written {
		t.Fatalf("the secret must not appear in VarWrites: %v", out.VarWrites)
	}
	for _, name := range out.VarUnsets {
		if name == "apiKey" {
			t.Fatalf("the secret must not appear in VarUnsets: %v", out.VarUnsets)
		}
	}
	// A refused secret must not stop the rest of the script writing normally.
	if out.VarWrites["base"] != "https://other.test" {
		t.Errorf("expected the non-secret write to go through, got %v", out.VarWrites)
	}
}

func TestVars_UnsetThenSetKeepsOnlyTheSet(t *testing.T) {
	out := mustRunPost(t, `vars.unset("token"); vars.set("token", "fresh")`,
		core.PostResponseInput{Response: response(200, "{}"), Vars: map[string]string{"token": "stale"}})

	if out.VarWrites["token"] != "fresh" {
		t.Errorf("VarWrites = %v, want token=fresh", out.VarWrites)
	}
	if len(out.VarUnsets) != 0 {
		t.Errorf("a later set should cancel the earlier unset, got VarUnsets = %v", out.VarUnsets)
	}
}

func TestVars_SetThenUnsetKeepsOnlyTheUnset(t *testing.T) {
	out := mustRunPost(t, `vars.set("token", "fresh"); vars.unset("token")`,
		core.PostResponseInput{Response: response(200, "{}")})

	if len(out.VarWrites) != 0 {
		t.Errorf("a later unset should cancel the earlier set, got VarWrites = %v", out.VarWrites)
	}
	if len(out.VarUnsets) != 1 || out.VarUnsets[0] != "token" {
		t.Errorf("VarUnsets = %v, want [token]", out.VarUnsets)
	}
}

// The input map belongs to the caller (the engine reuses it) — a script's
// writes must not reach back into it.
func TestVars_DoesNotMutateTheCallersMap(t *testing.T) {
	in := map[string]string{"base": "https://api.test"}
	mustRunPost(t, `vars.set("token", "abc"); vars.unset("base")`,
		core.PostResponseInput{Response: response(200, "{}"), Vars: in})

	if len(in) != 1 || in["base"] != "https://api.test" {
		t.Fatalf("the caller's variable map was mutated: %v", in)
	}
}

// ---- pre-request parity ---------------------------------------------------

func TestRunPreRequestWithVars_HasTheSameVarsAndConsole(t *testing.T) {
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://example.com"}

	out, err := New().RunPreRequestWithVars(context.Background(), `
		console.log("signing with", vars.get("apiKey"))
		ctx.setHeader("Authorization", "Bearer " + vars.get("token"))
		vars.set("lastRun", "now")
	`, resolved, core.PreRequestInput{
		Vars:    map[string]string{"token": "abc123", "apiKey": "from-keychain"},
		Secrets: []string{"apiKey"},
	})
	if err != nil {
		t.Fatalf("RunPreRequestWithVars: %v", err)
	}

	if len(out.Resolved.Headers) != 1 || out.Resolved.Headers[0].Value != "Bearer abc123" {
		t.Fatalf("expected the header to be built from a variable, got %+v", out.Resolved.Headers)
	}
	if out.VarWrites["lastRun"] != "now" {
		t.Errorf("expected the pre-request script to be able to write variables, got %v", out.VarWrites)
	}
	if len(out.Logs) != 1 || out.Logs[0] != "signing with from-keychain" {
		t.Errorf("expected captured console output, got %q", out.Logs)
	}
}

func TestRunPreRequestWithVars_SecretsGuardApplies(t *testing.T) {
	_, err := New().RunPreRequestWithVars(context.Background(), `vars.set("apiKey", "clobbered")`,
		core.ResolvedRequest{Method: "GET", URL: "https://example.com"},
		core.PreRequestInput{Vars: map[string]string{"apiKey": "x"}, Secrets: []string{"apiKey"}})

	if err == nil {
		t.Fatal("expected the secrets guard to fail the pre-request script")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected a message explaining the guard, got %v", err)
	}
}

// A failed script must leave the request exactly as it was — no half-applied
// header edits going out on the wire.
func TestRunPreRequestWithVars_FailedScriptLeavesTheRequestUnchanged(t *testing.T) {
	resolved := core.ResolvedRequest{
		Method:  "GET",
		URL:     "https://example.com",
		Headers: []model.KeyValue{{Key: "X-Original", Value: "kept", Enabled: true}},
	}

	out, err := New().RunPreRequestWithVars(context.Background(),
		`ctx.setHeader("X-Half-Applied", "1"); throw new Error("boom")`, resolved, core.PreRequestInput{})
	if err == nil {
		t.Fatal("expected the throw to surface as an error")
	}
	if len(out.Resolved.Headers) != 1 || out.Resolved.Headers[0].Key != "X-Original" {
		t.Fatalf("expected the request to come back unchanged, got %+v", out.Resolved.Headers)
	}
}

// test()/expect() are deliberately NOT part of the pre-request surface —
// there is no response to assert against yet.
func TestRunPreRequest_HasNoTestSurface(t *testing.T) {
	for _, script := range []string{`test("t", function () {})`, `expect(1).toBe(1)`, `response.status`} {
		if _, err := New().RunPreRequest(context.Background(), script, core.ResolvedRequest{Method: "GET", URL: "https://example.com"}); err == nil {
			t.Errorf("expected %q to be unavailable in a pre-request script", script)
		}
	}
}
