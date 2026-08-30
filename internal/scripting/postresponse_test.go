package scripting

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// response builds a ResponseData the way a protocol would hand one to the
// engine (body base64-encoded), so these tests exercise the same decode path
// production does.
func response(status int, body string, headers ...model.KeyValue) model.ResponseData {
	return model.ResponseData{
		Status:     status,
		StatusText: "OK",
		Headers:    headers,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
		BodySize:   len(body),
		TimingMs:   123,
	}
}

func runPost(t *testing.T, script string, in core.PostResponseInput) (core.PostResponseOutput, error) {
	t.Helper()
	return New().RunPostResponse(context.Background(), script, in)
}

// mustRunPost fails the test if the SCRIPT itself failed — most cases below
// care about test outcomes, where a script-level error means the test is
// broken rather than the assertion.
func mustRunPost(t *testing.T, script string, in core.PostResponseInput) core.PostResponseOutput {
	t.Helper()
	out, err := runPost(t, script, in)
	if err != nil {
		t.Fatalf("RunPostResponse: %v", err)
	}
	return out
}

func TestRunPostResponse_TestPassFailAndThrow(t *testing.T) {
	script := `
		test("passes", function () { expect(response.status).toBe(200) })
		test("fails", function () { expect(response.status).toBe(404) })
		test("throws something that is not an assertion", function () { null.boom })
		test("still runs after the failures above", function () { expect(true).toBeTruthy() })
	`
	out := mustRunPost(t, script, core.PostResponseInput{Response: response(200, "{}")})

	if len(out.Tests) != 4 {
		t.Fatalf("expected 4 test results (one failing test must not abort the rest), got %d: %+v", len(out.Tests), out.Tests)
	}
	want := []struct {
		name   string
		passed bool
	}{
		{"passes", true},
		{"fails", false},
		{"throws something that is not an assertion", false},
		{"still runs after the failures above", true},
	}
	for i, w := range want {
		if out.Tests[i].Name != w.name || out.Tests[i].Passed != w.passed {
			t.Errorf("test %d: got %+v, want name=%q passed=%v", i, out.Tests[i], w.name, w.passed)
		}
	}
	if out.Tests[1].Error != "expected 200 to be 404" {
		t.Errorf("failed test should carry the assertion message, got %q", out.Tests[1].Error)
	}
	if out.Tests[0].Error != "" {
		t.Errorf("a passing test must carry no error, got %q", out.Tests[0].Error)
	}
	if out.Tests[2].Error == "" {
		t.Error("a test that threw a non-assertion error should still report why")
	}
}

// TestRunPostResponse_Matchers is the message-quality suite: every matcher,
// its negation, and the exact text a user reads in CI when it fails. The
// message IS the feature, so it is asserted verbatim.
func TestRunPostResponse_Matchers(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		passed  bool
		message string
	}{
		{"toBe pass", `expect(200).toBe(200)`, true, ""},
		{"toBe fail", `expect(404).toBe(200)`, false, "expected 404 to be 200"},
		{"toBe fail strings", `expect("a").toBe("b")`, false, `expected "a" to be "b"`},
		{"toBe no coercion", `expect("200").toBe(200)`, false, `expected "200" to be 200`},
		{"not.toBe pass", `expect(404).not.toBe(200)`, true, ""},
		{"not.toBe fail", `expect(200).not.toBe(200)`, false, "expected 200 not to be 200"},

		{"toEqual deep pass", `expect({a:[1,{b:2}]}).toEqual({a:[1,{b:2}]})`, true, ""},
		{"toEqual deep fail", `expect({a:1}).toEqual({a:2})`, false, `expected {"a":1} to equal {"a":2}`},
		{"toEqual array length", `expect([1,2]).toEqual([1,2,3])`, false, "expected [1,2] to equal [1,2,3]"},
		{"toEqual extra key", `expect({a:1,b:2}).toEqual({a:1})`, false, `expected {"a":1,"b":2} to equal {"a":1}`},
		{"not.toEqual pass", `expect({a:1}).not.toEqual({a:2})`, true, ""},
		{"not.toEqual fail", `expect({a:1}).not.toEqual({a:1})`, false, `expected {"a":1} not to equal {"a":1}`},

		{"toBeTruthy pass", `expect("x").toBeTruthy()`, true, ""},
		{"toBeTruthy fail", `expect(0).toBeTruthy()`, false, "expected 0 to be truthy"},
		{"toBeTruthy fail undefined", `expect(undefined).toBeTruthy()`, false, "expected undefined to be truthy"},
		{"not.toBeTruthy pass", `expect("").not.toBeTruthy()`, true, ""},
		{"not.toBeTruthy fail", `expect(1).not.toBeTruthy()`, false, "expected 1 not to be truthy"},

		{"toBeFalsy pass", `expect(null).toBeFalsy()`, true, ""},
		{"toBeFalsy fail", `expect("x").toBeFalsy()`, false, `expected "x" to be falsy`},
		{"not.toBeFalsy pass", `expect(1).not.toBeFalsy()`, true, ""},
		{"not.toBeFalsy fail", `expect(0).not.toBeFalsy()`, false, "expected 0 not to be falsy"},

		{"toContain string pass", `expect("hello world").toContain("world")`, true, ""},
		{"toContain string fail", `expect("hello").toContain("bye")`, false, `expected "hello" to contain "bye"`},
		{"toContain array pass", `expect([1,2,3]).toContain(2)`, true, ""},
		{"toContain array deep pass", `expect([{a:1}]).toContain({a:1})`, true, ""},
		{"toContain array fail", `expect([1,2]).toContain(9)`, false, "expected [1,2] to contain 9"},
		{"not.toContain pass", `expect("hello").not.toContain("bye")`, true, ""},
		{"not.toContain fail", `expect("hello").not.toContain("ell")`, false, `expected "hello" not to contain "ell"`},
		{"toContain wrong type", `expect(3).toContain("a")`, false, "expect(...).toContain needs a string or an array, got number"},

		{"toBeGreaterThan pass", `expect(5).toBeGreaterThan(3)`, true, ""},
		{"toBeGreaterThan fail", `expect(3).toBeGreaterThan(5)`, false, "expected 3 to be greater than 5"},
		{"toBeGreaterThan equal fails", `expect(5).toBeGreaterThan(5)`, false, "expected 5 to be greater than 5"},
		{"not.toBeGreaterThan pass", `expect(3).not.toBeGreaterThan(5)`, true, ""},
		{"not.toBeGreaterThan fail", `expect(9).not.toBeGreaterThan(5)`, false, "expected 9 not to be greater than 5"},
		{"toBeGreaterThan wrong type", `expect("5").toBeGreaterThan(3)`, false, "expect(...).toBeGreaterThan needs numbers, got string and number"},

		{"toBeLessThan pass", `expect(3).toBeLessThan(5)`, true, ""},
		{"toBeLessThan fail", `expect(9).toBeLessThan(5)`, false, "expected 9 to be less than 5"},
		{"not.toBeLessThan pass", `expect(9).not.toBeLessThan(5)`, true, ""},
		{"not.toBeLessThan fail", `expect(1).not.toBeLessThan(5)`, false, "expected 1 not to be less than 5"},

		{"toMatch regexp pass", `expect("abc123").toMatch(/[0-9]+/)`, true, ""},
		{"toMatch string source pass", `expect("abc").toMatch("^a")`, true, ""},
		{"toMatch fail", `expect("abc").toMatch(/[0-9]/)`, false, `expected "abc" to match /[0-9]/`},
		{"not.toMatch pass", `expect("abc").not.toMatch(/[0-9]/)`, true, ""},
		{"not.toMatch fail", `expect("abc").not.toMatch(/b/)`, false, `expected "abc" not to match /b/`},
		{"toMatch wrong type", `expect(5).toMatch(/5/)`, false, "expect(...).toMatch needs a string, got number"},

		{"toHaveProperty pass", `expect({a:{b:1}}).toHaveProperty("a.b")`, true, ""},
		{"toHaveProperty array index pass", `expect({items:[{id:7}]}).toHaveProperty("items[0].id")`, true, ""},
		{"toHaveProperty fail", `expect({a:1}).toHaveProperty("b")`, false, `expected {"a":1} to have property "b"`},
		{"toHaveProperty with value pass", `expect({a:{b:1}}).toHaveProperty("a.b", 1)`, true, ""},
		{"toHaveProperty with value fail", `expect({a:{b:1}}).toHaveProperty("a.b", 2)`, false, `expected property "a.b" to equal 2, got 1`},
		{"toHaveProperty with value absent", `expect({a:1}).toHaveProperty("z", 2)`, false, `expected property "z" to equal 2, but it is not present`},
		{"not.toHaveProperty pass", `expect({a:1}).not.toHaveProperty("b")`, true, ""},
		{"not.toHaveProperty fail", `expect({a:1}).not.toHaveProperty("a")`, false, `expected {"a":1} not to have property "a"`},
		{"toHaveProperty wrong type", `expect(null).toHaveProperty("a")`, false, "expect(...).toHaveProperty needs an object, got null"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRunPost(t, `test("t", function () { `+tc.expr+` })`, core.PostResponseInput{Response: response(200, "{}")})
			if len(out.Tests) != 1 {
				t.Fatalf("expected exactly 1 test result, got %+v", out.Tests)
			}
			got := out.Tests[0]
			if got.Passed != tc.passed {
				t.Fatalf("passed = %v, want %v (error: %q)", got.Passed, tc.passed, got.Error)
			}
			if got.Error != tc.message {
				t.Fatalf("message =\n  %q\nwant\n  %q", got.Error, tc.message)
			}
		})
	}
}

func TestRunPostResponse_ExposesTheResponse(t *testing.T) {
	resp := response(201, `{"token":"abc","n":3,"nested":{"list":[1,2]}}`,
		model.KeyValue{Key: "Content-Type", Value: "application/json"},
		model.KeyValue{Key: "Set-Cookie", Value: "a=1"},
		model.KeyValue{Key: "Set-Cookie", Value: "b=2"},
	)
	resp.StatusText = "201 Created"

	script := `
		test("status", function () { expect(response.status).toBe(201) })
		test("statusText", function () { expect(response.statusText).toBe("201 Created") })
		test("body is the raw text", function () { expect(response.body).toContain("\"token\"") })
		test("json is parsed", function () { expect(response.json().nested.list).toEqual([1,2]) })
		test("json is stable across calls", function () { expect(response.json()).toEqual(response.json()) })
		test("timingMs", function () { expect(response.timingMs).toBe(123) })
		test("size", function () { expect(response.size).toBe(45) })
		test("headers by name", function () { expect(response.headers["Content-Type"]).toBe("application/json") })
		test("headers case-insensitive get", function () { expect(response.headers.get("CONTENT-TYPE")).toBe("application/json") })
		test("headers getAll keeps duplicates", function () { expect(response.headers.getAll("set-cookie")).toEqual(["a=1","b=2"]) })
		test("headers get misses cleanly", function () { expect(response.headers.get("nope")).toBe(undefined) })
		test("headers enumerate without the helpers", function () { expect(Object.keys(response.headers)).toEqual(["Content-Type","Set-Cookie"]) })
	`
	out := mustRunPost(t, script, core.PostResponseInput{Response: resp})
	for _, tr := range out.Tests {
		if !tr.Passed {
			t.Errorf("%s: %s", tr.Name, tr.Error)
		}
	}
}

func TestRunPostResponse_InvalidJSONThrowsAReadableError(t *testing.T) {
	out := mustRunPost(t,
		`test("parse", function () { response.json() })`,
		core.PostResponseInput{Response: response(200, "<html>nope</html>")})

	if out.Tests[0].Passed {
		t.Fatal("expected response.json() on an HTML body to fail the test")
	}
	msg := out.Tests[0].Error
	if !strings.Contains(msg, "response.json()") || !strings.Contains(msg, "not valid JSON") {
		t.Fatalf("expected a message naming response.json() and the problem, got %q", msg)
	}
	if !strings.Contains(msg, "<html>nope</html>") {
		t.Fatalf("expected the message to show what the body actually was, got %q", msg)
	}
}

func TestRunPostResponse_InvalidJSONOnEmptyBodySaysSo(t *testing.T) {
	out := mustRunPost(t, `test("parse", function () { response.json() })`, core.PostResponseInput{})
	if out.Tests[0].Passed {
		t.Fatal("expected response.json() on an empty body to fail")
	}
	if !strings.Contains(out.Tests[0].Error, "empty") {
		t.Fatalf("expected the empty-body case to say so, got %q", out.Tests[0].Error)
	}
}

func TestRunPostResponse_SyntaxErrorIsAScriptError(t *testing.T) {
	_, err := runPost(t, `this is not valid js {{{`, core.PostResponseInput{Response: response(200, "{}")})
	if err == nil {
		t.Fatal("expected a script error for invalid syntax — a script that cannot run is a failed run, not a pass")
	}
}

func TestRunPostResponse_ThrowOutsideATestIsAScriptError(t *testing.T) {
	out, err := runPost(t, `
		test("ran before the throw", function () { expect(1).toBe(1) })
		throw new Error("boom")
	`, core.PostResponseInput{Response: response(200, "{}")})

	if err == nil {
		t.Fatal("expected a throw outside test() to be reported as a script error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the thrown message in the error, got %v", err)
	}
	if len(out.Tests) != 1 || !out.Tests[0].Passed {
		t.Fatalf("tests that already ran should still be reported, got %+v", out.Tests)
	}
}

func TestRunPostResponse_TimesOutOnInfiniteLoop(t *testing.T) {
	s := Scripter{PostResponseTimeout: 300 * time.Millisecond}

	start := time.Now()
	_, err := s.RunPostResponse(context.Background(), `while (true) {}`, core.PostResponseInput{Response: response(200, "{}")})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error for an infinite loop")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected the timeout to be enforced near 300ms, took %s", elapsed)
	}
}

// A JS try/catch must not be able to swallow the interrupt and keep the VM
// spinning — otherwise the timeout would be advisory rather than a bound.
func TestRunPostResponse_TimeoutIsNotCatchableFromScript(t *testing.T) {
	s := Scripter{PostResponseTimeout: 300 * time.Millisecond}

	start := time.Now()
	_, err := s.RunPostResponse(context.Background(),
		`test("t", function () { try { while (true) {} } catch (e) { } })`,
		core.PostResponseInput{Response: response(200, "{}")})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the interrupt to survive a try/catch inside a test")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected the timeout to be enforced near 300ms, took %s", elapsed)
	}
}

func TestRunPostResponse_CancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().RunPostResponse(ctx, `while (true) {}`, core.PostResponseInput{}); err == nil {
		t.Fatal("expected a cancelled context to stop the script")
	}
}

func TestRunPostResponse_CapturesConsoleOutput(t *testing.T) {
	script := `
		console.log("plain")
		console.log("with", 2, "values", true)
		console.log({a:1,b:[2]})
		console.warn("warn is an alias")
		console.error("so is error")
	`
	out := mustRunPost(t, script, core.PostResponseInput{Response: response(200, "{}")})

	want := []string{"plain", "with 2 values true", `{"a":1,"b":[2]}`, "warn is an alias", "so is error"}
	if len(out.Logs) != len(want) {
		t.Fatalf("expected %d captured lines, got %d: %q", len(want), len(out.Logs), out.Logs)
	}
	for i, w := range want {
		if out.Logs[i] != w {
			t.Errorf("log %d = %q, want %q", i, out.Logs[i], w)
		}
	}
}

func TestRunPostResponse_ConsoleOutputIsCapped(t *testing.T) {
	out := mustRunPost(t, `for (var i = 0; i < 5000; i++) console.log("line " + i)`, core.PostResponseInput{})
	if len(out.Logs) != maxLogLines {
		t.Fatalf("expected console output to be capped at %d lines, got %d", maxLogLines, len(out.Logs))
	}
}

func TestRunPostResponse_NoSandboxEscape(t *testing.T) {
	// sobek has no require/fetch/process/fs of its own, and the runtime's
	// own low-level sinks are deleted after installation so a script cannot
	// call them directly to sidestep the checks in vars.set.
	for _, script := range []string{
		`require("fs")`,
		`fetch("https://evil.example.com")`,
		`process.exit(1)`,
		`__varSet("token", "smuggled")`,
		`__log("straight to stdout")`,
		`__recordTest("fake", true, "")`,
	} {
		if _, err := runPost(t, script, core.PostResponseInput{Response: response(200, "{}")}); err == nil {
			t.Errorf("expected %q to fail — no such global should be reachable", script)
		}
	}
}

func TestRunPostResponse_EmptyScriptProducesNothing(t *testing.T) {
	out := mustRunPost(t, ``, core.PostResponseInput{Response: response(200, "{}")})
	if len(out.Tests) != 0 || len(out.VarWrites) != 0 || len(out.Logs) != 0 {
		t.Fatalf("expected an empty script to produce nothing, got %+v", out)
	}
}

func TestRunPostResponse_DoesNotMutateTheResponse(t *testing.T) {
	in := core.PostResponseInput{Response: response(200, `{"a":1}`)}
	before := in.Response

	_, err := runPost(t, `
		response.status = 500;
		response.body = "rewritten";
		test("t", function () { expect(1).toBe(1) })
	`, in)
	if err != nil {
		t.Fatalf("RunPostResponse: %v", err)
	}
	if in.Response.Status != before.Status || in.Response.BodyBase64 != before.BodyBase64 {
		t.Fatalf("a script must not be able to change the response, got %+v", in.Response)
	}
}

// TestRunPostResponse_AsyncTestCallbackIsNotSilentGreen guards the CRITICAL
// false-green: an async test() callback returns a Promise this synchronous
// runner can never await, so a failing async assertion would have been
// recorded as PASSED. It must be recorded as a FAILURE instead.
func TestRunPostResponse_AsyncTestCallbackIsNotSilentGreen(t *testing.T) {
	// Body {} so response.json().id is undefined — the assertion WOULD fail.
	out := mustRunPost(t,
		`test("async check", async function () { expect(response.json().id).toBe(42) })`,
		core.PostResponseInput{Response: response(200, "{}")})
	if len(out.Tests) != 1 {
		t.Fatalf("want 1 test result, got %d: %+v", len(out.Tests), out.Tests)
	}
	if out.Tests[0].Passed {
		t.Error("an async test callback must NOT be recorded as passed — it can't be awaited, so a failing async assertion would be a false green")
	}
	if !strings.Contains(out.Tests[0].Error, "async") {
		t.Errorf("failure message should explain async isn't supported, got %q", out.Tests[0].Error)
	}
}

// TestRunPostResponse_AsyncScriptThrowIsNotSilentGreen guards the other half:
// an async script whose BODY throws (no test() at all) leaves an unhandled
// promise rejection that RunString doesn't surface — previously err==nil,
// tests==[], and the whole request passed. It must now be a script error.
func TestRunPostResponse_AsyncScriptThrowIsNotSilentGreen(t *testing.T) {
	script := `
		async function checks() {
			var body = response.json();   // valid JSON, fine
			throw new Error("boom from async body")
		}
		checks()
	`
	_, err := runPost(t, script, core.PostResponseInput{Response: response(200, `{"ok":true}`)})
	if err == nil {
		t.Fatal("an async script that throws must surface a script error, not a silent green run")
	}
	if !strings.Contains(err.Error(), "boom") && !strings.Contains(err.Error(), "rejection") {
		t.Errorf("error should reflect the async throw, got %q", err.Error())
	}
}

// TestRunPostResponse_ToMatchCatastrophicPatternIsRejected proves the
// sanctioned matcher can't reach the backtracking regex engine: a pattern
// RE2 can't compile (a lookahead) is a clean usage error, not a hung goroutine.
func TestRunPostResponse_ToMatchCatastrophicPatternIsRejected(t *testing.T) {
	out := mustRunPost(t,
		`test("re", function () { expect("aaaa").toMatch("(?=a)(a+)+$") })`,
		core.PostResponseInput{Response: response(200, "{}")})
	if len(out.Tests) != 1 || out.Tests[0].Passed {
		t.Fatalf("a lookahead pattern must fail as a usage error, got %+v", out.Tests)
	}
	if !strings.Contains(out.Tests[0].Error, "toMatch") {
		t.Errorf("message should name toMatch, got %q", out.Tests[0].Error)
	}
}

// A normal RE2-compatible pattern still matches correctly through the Go engine.
func TestRunPostResponse_ToMatchNormalPatternWorks(t *testing.T) {
	out := mustRunPost(t,
		`test("re", function () { expect(response.body).toMatch("^\\{.*ok.*\\}$") })`,
		core.PostResponseInput{Response: response(200, `{"ok":true}`)})
	if len(out.Tests) != 1 || !out.Tests[0].Passed {
		t.Fatalf("a normal pattern should pass, got %+v", out.Tests)
	}
}

// TestRunPostResponse_ToMatchHonorsRegexFlags guards a FALSE GREEN: routing
// .toMatch through Go RE2 dropped the JS RegExp flags, so /error/i became
// case-SENSITIVE. The negated form then PASSED on a body containing "ERROR" —
// precisely the case-insensitive error sniffing the flag exists for.
func TestRunPostResponse_ToMatchHonorsRegexFlags(t *testing.T) {
	body := `{"status":"ERROR"}`
	cases := []struct {
		name   string
		script string
		pass   bool
	}{
		{"case-insensitive matches", `test("t", function () { expect(response.body).toMatch(/error/i) })`, true},
		{"negated case-insensitive must FAIL", `test("t", function () { expect(response.body).not.toMatch(/error/i) })`, false},
		{"case-sensitive does not match", `test("t", function () { expect(response.body).toMatch(/error/) })`, false},
		{"multiline flag", `test("t", function () { expect("a\nb").toMatch(/^b$/m) })`, true},
		{"dotall flag", `test("t", function () { expect("a\nb").toMatch(/a.b/s) })`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRunPost(t, tc.script, core.PostResponseInput{Response: response(200, body)})
			if len(out.Tests) != 1 {
				t.Fatalf("want 1 test, got %+v", out.Tests)
			}
			if out.Tests[0].Passed != tc.pass {
				t.Errorf("passed = %v, want %v (err: %q)", out.Tests[0].Passed, tc.pass, out.Tests[0].Error)
			}
		})
	}
}
