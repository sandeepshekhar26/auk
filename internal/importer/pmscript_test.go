package importer

import (
	"strings"
	"testing"

	"github.com/grafana/sobek"
)

// TestTranslatePostmanScriptExact pins the EXACT AUK script text produced for
// every mapping in the translation table. These are the lines a Postman
// refugee will read on their first day; a near-miss (a matcher that doesn't
// exist, a half-rewritten chain) is worse than no translation, so the
// expectations are literal rather than "contains".
func TestTranslatePostmanScriptExact(t *testing.T) {
	cases := []struct {
		name string
		kind ScriptKind
		in   string
		want string
	}{
		{
			name: "test block with status assertion",
			kind: ScriptPostResponse,
			in: `pm.test("Status code is 200", function () {
    pm.response.to.have.status(200);
});`,
			want: `test("Status code is 200", () => {
    expect(response.status).toBe(200);
});`,
		},
		{
			name: "arrow-form test",
			kind: ScriptPostResponse,
			in:   `pm.test('has an id', () => { pm.expect(pm.response.json().id).to.eql(7) })`,
			want: `test('has an id', () => { expect(response.json().id).toEqual(7) })`,
		},
		{
			name: "response accessors",
			kind: ScriptPostResponse,
			in: `var code = pm.response.code;
var body = pm.response.text();
var ms = pm.response.responseTime;
var reason = pm.response.status;`,
			want: `var code = response.status;
var body = response.body;
var ms = response.timingMs;
var reason = response.statusText;`,
		},
		{
			name: "equal maps to toBe and eql to toEqual",
			kind: ScriptPostResponse,
			in: `pm.expect(a).to.equal(1);
pm.expect(b).to.eql({ x: 1 });`,
			want: `expect(a).toBe(1);
expect(b).toEqual({ x: 1 });`,
		},
		{
			name: "truthy falsy include property above below match",
			kind: ScriptPostResponse,
			in: `pm.expect(ok).to.be.true;
pm.expect(no).to.be.false;
pm.expect(list).to.include("a");
pm.expect(obj).to.have.property("id");
pm.expect(n).to.be.above(3);
pm.expect(n).to.be.below(9);
pm.expect(s).to.match(/^ab/);`,
			want: `expect(ok).toBeTruthy();
expect(no).toBeFalsy();
expect(list).toContain("a");
expect(obj).toHaveProperty("id");
expect(n).toBeGreaterThan(3);
expect(n).toBeLessThan(9);
expect(s).toMatch(/^ab/);`,
		},
		{
			name: "negation on either side of to",
			kind: ScriptPostResponse,
			in: `pm.expect(x).to.not.eql(3);
pm.expect(y).not.to.include("z");`,
			want: `expect(x).not.toEqual(3);
expect(y).not.toContain("z");`,
		},
		{
			name: "every variable scope collapses onto vars",
			kind: ScriptPostResponse,
			in: `pm.environment.set("token", pm.response.json().token);
pm.collectionVariables.set("id", 1);
var t = pm.globals.get("t");
pm.environment.unset("stale");`,
			want: `vars.set("token", response.json().token);
vars.set("id", 1);
var t = vars.get("t");
vars.unset("stale");`,
		},
		{
			name: "console passes through",
			kind: ScriptPostResponse,
			in:   `console.log("code", pm.response.code);`,
			want: `console.log("code", response.status);`,
		},
		{
			name: "header assertion becomes a headers.get check",
			kind: ScriptPostResponse,
			in:   `pm.response.to.have.header("Content-Type");`,
			want: `expect(response.headers.get("Content-Type")).toBeTruthy();`,
		},
		{
			name: "pre-request header add becomes ctx.setHeader",
			kind: ScriptPreRequest,
			in:   `pm.request.headers.add({ key: "X-Trace", value: "abc" });`,
			want: `ctx.setHeader("X-Trace", "abc");`,
		},
		{
			name: "pre-request header add with reversed fields and an expression",
			kind: ScriptPreRequest,
			in:   `pm.request.headers.add({value: vars.get('sig'), key: 'X-Sig'});`,
			want: `ctx.setHeader('X-Sig', vars.get('sig'));`,
		},
		{
			name: "legacy v1 tests[] sugar becomes a real test",
			kind: ScriptPostResponse,
			in:   `tests["Status code is 200"] = responseCode.code === 200;`,
			want: `test("Status code is 200", () => { expect(response.status === 200).toBeTruthy() })`,
		},
		{
			name: "legacy v1 variable helpers",
			kind: ScriptPostResponse,
			in:   `postman.setEnvironmentVariable("token", "abc");`,
			want: `vars.set("token", "abc");`,
		},
		{
			name: "blank lines and comments survive untouched",
			kind: ScriptPostResponse,
			in: `// keep me

pm.expect(1).to.eql(1);`,
			want: `// keep me

expect(1).toEqual(1);`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslatePostmanScript(tc.in, tc.kind)
			if got.Text != tc.want {
				t.Errorf("translation mismatch\n--- got ---\n%s\n--- want ---\n%s", got.Text, tc.want)
			}
			if got.Partial() {
				t.Errorf("expected a complete translation, got untranslated lines: %v (%v)", got.Untranslated, got.Reasons)
			}
			assertParses(t, got.Text)
		})
	}
}

// TestTranslateSendRequestIsRefusedNotDropped is the sandbox rule made
// visible: pm.sendRequest has no translation (AUK scripts cannot make HTTP
// calls), so the whole callback block must survive as comments rather than
// half-translating into something that runs.
func TestTranslateSendRequestIsRefusedNotDropped(t *testing.T) {
	src := `pm.sendRequest("https://auth.example.com/token", function (err, res) {
    pm.environment.set("token", res.json().token);
});
pm.test("still translated", function () {
    pm.response.to.have.status(200);
});`

	got := TranslatePostmanScript(src, ScriptPostResponse)

	if !got.UsesSendRequest {
		t.Error("UsesSendRequest should be true")
	}
	if !got.Partial() {
		t.Fatal("expected the script to be reported as partial")
	}
	// The original line survives verbatim, commented, under the TODO marker.
	for _, want := range []string{
		migrateTODO,
		`// pm.sendRequest("https://auth.example.com/token", function (err, res) {`,
		`// pm.environment.set("token", res.json().token);`,
		`// });`,
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("expected the output to contain %q\n--- got ---\n%s", want, got.Text)
		}
	}
	// ...and the rest of the script is still translated normally.
	if !strings.Contains(got.Text, "test(\"still translated\", () => {") {
		t.Errorf("the untranslatable block should not stop the rest of the script:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "expect(response.status).toBe(200);") {
		t.Errorf("expected the following test to translate:\n%s", got.Text)
	}
	if len(got.Untranslated) != 3 {
		t.Errorf("expected the 3 sendRequest lines to be recorded, got %d: %v", len(got.Untranslated), got.Untranslated)
	}
	assertParses(t, got.Text)
}

// TestTranslateRefusesAsyncAndSandboxEscapes covers the constructs AUK's
// runtime cannot run at all.
func TestTranslateRefusesAsyncAndSandboxEscapes(t *testing.T) {
	cases := []struct {
		name, src, wantReason string
	}{
		{"await", `const r = await pm.sendRequest(url);`, reasonSendRequest},
		{"async arrow", `pm.test("x", async () => { pm.response.to.have.status(200) });`, reasonAsync},
		{"done callback", `pm.test("x", function (done) { done() });`, reasonAsync},
		{"require", `const _ = require('lodash');`, reasonRequire},
		{"timers", `setTimeout(function () { console.log(1) }, 10);`, reasonTimers},
		{"setNextRequest", `postman.setNextRequest("Login");`, reasonNextRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslatePostmanScript(tc.src, ScriptPostResponse)
			if !got.Partial() {
				t.Fatalf("expected %q to be left untranslated, got:\n%s", tc.src, got.Text)
			}
			if !contains(got.Reasons, tc.wantReason) {
				t.Errorf("expected reason %q, got %v", tc.wantReason, got.Reasons)
			}
			if !strings.Contains(got.Text, strings.TrimSpace(strings.SplitN(tc.src, "\n", 2)[0])) {
				t.Errorf("the original line should survive as a comment:\n%s", got.Text)
			}
			assertParses(t, got.Text)
		})
	}
}

// TestTranslatePreRequestRejectsResponseUse: a pre-request script has no
// response and no test()/expect() in AUK, so anything reaching for one is a
// TODO rather than a line that would throw at runtime.
func TestTranslatePreRequestRejectsResponseUse(t *testing.T) {
	got := TranslatePostmanScript(`pm.expect(pm.response.code).to.eql(200);`, ScriptPreRequest)
	if !got.Partial() {
		t.Fatalf("expected a partial translation, got: %s", got.Text)
	}
	if !contains(got.Reasons, reasonPreNoResp) {
		t.Errorf("expected the pre-request reason, got %v", got.Reasons)
	}
}

// TestTranslateNeverSplicesControlFlow guards the nastiest failure mode: an
// untranslatable `} else {` commented out in place would merge two branches
// into one and still compile — a silent behaviour change. The translator must
// bail out to a fully-commented script instead.
func TestTranslateNeverSplicesControlFlow(t *testing.T) {
	src := `if (pm.response.code === 200) {
    console.log("ok");
} else if (pm.info.eventName === "test") {
    console.log("other");
}`
	got := TranslatePostmanScript(src, ScriptPostResponse)
	if !got.FullyCommented {
		t.Fatalf("expected the whole script to be preserved as comments, got:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "\nconsole.log(\"other\");") {
		t.Errorf("no line may survive uncommented in a fully-commented script:\n%s", got.Text)
	}
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(got.Text, "// "+strings.TrimSpace(line)) {
			t.Errorf("original line %q must be preserved as a comment:\n%s", line, got.Text)
		}
	}
	assertParses(t, got.Text)
}

// TestTranslateEmptyScript keeps the "no script" case from producing a body.
func TestTranslateEmptyScript(t *testing.T) {
	for _, src := range []string{"", "   ", "\n\n"} {
		if got := TranslatePostmanScript(src, ScriptPostResponse); !got.Empty() {
			t.Errorf("expected an empty translation for %q, got %q", src, got.Text)
		}
	}
}

// assertParses compiles the translated script with the SAME interpreter that
// will run it. A migrated request must never fail with a syntax error.
func assertParses(t *testing.T, src string) {
	t.Helper()
	if _, err := sobek.Compile("migrated.js", src, false); err != nil {
		t.Fatalf("translated script does not parse: %v\n--- script ---\n%s", err, src)
	}
}
