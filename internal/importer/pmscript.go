package importer

// Postman script translation — the part of a migration a switcher cannot do
// by hand in an afternoon.
//
// A Postman collection carries `event[]` entries (`listen: "prerequest"` and
// `listen: "test"`) whose `script.exec` is an array of JavaScript lines
// written against Postman's `pm.*` API. AUK's script API is a different,
// smaller surface (docs/08-scripting.md): `test`/`expect`, a frozen
// `response`, `vars`, `ctx.setHeader`, `console`. This file rewrites the
// common Postman vocabulary into it.
//
// Three rules shape the whole design:
//
//  1. **Nothing is silently lost.** A line the translator does not understand
//     is emitted COMMENTED OUT under a TODO marker, in its original form, and
//     is reported as a warning. A user must never discover in CI that a test
//     quietly stopped existing.
//  2. **The result must parse.** Translation is line-oriented and
//     best-effort, so a commented-out line that opened a block would orphan
//     its closing brace. Untranslatable blocks are therefore commented out
//     WHOLE, and the finished script is compiled (grafana/sobek — the same
//     interpreter that will run it) before being handed back; if it does not
//     parse, the entire original is kept as comments with a header explaining
//     the manual port. A migrated request never fails with a syntax error.
//  3. **The sandbox is not negotiable.** `pm.sendRequest` has no translation
//     and never will: AUK scripts cannot make HTTP calls, because every
//     outbound request goes through one policy chokepoint and a script that
//     could dial out would be a way around it.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/grafana/sobek"
)

// ScriptKind selects which AUK hook a Postman script is being translated for.
// It changes what is legal: a pre-request script has no response to assert
// against, and a post-response script can no longer shape the request.
type ScriptKind string

const (
	// ScriptPreRequest maps to Postman's `listen: "prerequest"` and AUK's
	// RequestDef.PreRequestScript.
	ScriptPreRequest ScriptKind = "prerequest"
	// ScriptPostResponse maps to Postman's `listen: "test"` and AUK's
	// RequestDef.PostResponseScript.
	ScriptPostResponse ScriptKind = "test"
)

// migrateTODO marks every line the translator could not handle. Grep-able on
// purpose: "TODO(auk-migrate)" in the Script tab is the user's worklist.
const migrateTODO = "// TODO(auk-migrate):"

// TranslatedScript is the outcome of translating ONE Postman script.
type TranslatedScript struct {
	// Text is the AUK script to store on the request. Empty only when the
	// Postman source was empty.
	Text string
	// Untranslated holds the original Postman lines that were commented out.
	Untranslated []string
	// Reasons holds the distinct human-readable explanations for those lines,
	// in first-seen order, for the migration report.
	Reasons []string
	// UsesSendRequest is true when the script called pm.sendRequest — the one
	// Postman feature AUK deliberately cannot offer, worth its own warning.
	UsesSendRequest bool
	// FullyCommented is true when the translation was abandoned and the whole
	// original was preserved as comments (it did not parse).
	FullyCommented bool
}

// Partial reports whether anything was left for the user to port by hand.
func (t TranslatedScript) Partial() bool { return len(t.Untranslated) > 0 }

// Empty reports whether there is nothing to store on the request.
func (t TranslatedScript) Empty() bool { return strings.TrimSpace(t.Text) == "" }

// Reason strings, named so the report and the in-script TODO agree word for
// word — the user reads both.
const (
	reasonSendRequest = "pm.sendRequest cannot be translated: AUK scripts cannot make HTTP calls (every request goes through one policy chokepoint). Model this as its own request and chain it with vars.set/${var}"
	reasonAsync       = "async/await (and done-callback tests) are not supported by AUK's script runtime"
	reasonRequire     = "require() is not available: AUK's sandbox has no module loader"
	reasonTimers      = "setTimeout/setInterval are not available in AUK's script sandbox"
	reasonNextRequest = "setNextRequest has no AUK equivalent: run the folder, which sends requests in tree order"
	reasonPreNoResp   = "a pre-request script has no response to assert against in AUK (its API is ctx.setHeader/vars/console)"
	reasonPostNoReq   = "the request can no longer be changed after the response arrives; move this to the request's pre-request script"
	reasonUnknownPM   = "no AUK equivalent for this Postman API"
	reasonUnknownChai = "no AUK matcher for this chai assertion (see docs/08-scripting.md for the supported matchers)"
	reasonBlockTail   = "part of the untranslatable block above"
)

// TranslatePostmanScript rewrites one Postman script into AUK's script API.
func TranslatePostmanScript(src string, kind ScriptKind) TranslatedScript {
	var out TranslatedScript
	if strings.TrimSpace(src) == "" {
		return out
	}
	original := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	var lines []string
	// pendingReasons collects the reasons for the current run of commented
	// lines so consecutive failures share one TODO header instead of
	// producing a wall of them.
	var pending []string
	var pendingReasons []string
	suppress := 0 // >0 while inside a block whose opening line was commented out

	flush := func() {
		if len(pending) == 0 {
			return
		}
		lines = append(lines, migrateTODO+" "+strings.Join(pendingReasons, "; ")+".")
		lines = append(lines, pending...)
		pending = nil
		pendingReasons = nil
	}
	reject := func(line, reason string) {
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		pending = append(pending, indent+"// "+strings.TrimSpace(line))
		out.Untranslated = append(out.Untranslated, strings.TrimSpace(line))
		if reason != reasonBlockTail && !contains(pendingReasons, reason) {
			pendingReasons = append(pendingReasons, reason)
		}
		if !contains(out.Reasons, reason) && reason != reasonBlockTail {
			out.Reasons = append(out.Reasons, reason)
		}
	}

	for _, line := range original {
		if suppress > 0 {
			delta, _ := bracketScan(line)
			suppress += delta
			reject(line, reasonBlockTail)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			flush()
			lines = append(lines, line)
			continue
		}
		translated, reason := translatePostmanLine(line, kind)
		if reason == "" {
			flush()
			lines = append(lines, translated)
			continue
		}
		if strings.Contains(line, "pm.sendRequest") {
			out.UsesSendRequest = true
		}
		delta, minDepth := bracketScan(line)
		switch {
		case minDepth < 0:
			// The line CLOSES a block something above it opened (`} else if
			// (pm.info…) {`). Commenting it out in place would silently splice
			// the else-branch into the if-branch — a change in behaviour that
			// still parses, which is far worse than a visible failure. Bail
			// out and preserve the whole script as comments instead.
			return fullyCommented(original, out, "it mixes Postman API calls into control flow the translator cannot rewrite line by line")
		case delta > 0:
			// The line OPENS a block; take the whole block into the comment
			// with it so its closing brace is not left orphaned.
			suppress = delta
		}
		reject(line, reason)
	}
	flush()

	out.Text = strings.TrimRight(strings.Join(lines, "\n"), "\n \t")

	// Last line of defence: the script AUK will actually run must parse. A
	// translation that does not compile is worse than no translation, so fall
	// back to preserving the original in full as comments. The check runs the
	// SAME interpreter (grafana/sobek) that will execute the script.
	if _, err := sobek.Compile("migrated.js", out.Text, false); err != nil {
		return fullyCommented(original, out, "the translated script did not parse ("+firstLine(err.Error())+")")
	}
	return out
}

// fullyCommented preserves the entire Postman source as comments. Used when a
// line-by-line translation cannot be spliced together safely — the script is
// then inert rather than wrong, and every original line is still in front of
// the user in the Script tab.
func fullyCommented(original []string, prev TranslatedScript, cause string) TranslatedScript {
	out := TranslatedScript{
		FullyCommented:  true,
		UsesSendRequest: prev.UsesSendRequest,
		Reasons:         prev.Reasons,
	}
	lines := []string{
		migrateTODO + " this Postman script needs a manual port:",
		"// " + cause + ".",
		"// Nothing was lost — the original Postman source is preserved below,",
		"// and AUK's script API is documented in docs/08-scripting.md.",
	}
	for _, l := range original {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			lines = append(lines, "//")
			continue
		}
		lines = append(lines, "// "+trimmed)
		out.Untranslated = append(out.Untranslated, trimmed)
	}
	if len(out.Reasons) == 0 {
		out.Reasons = append(out.Reasons, cause)
	}
	out.Text = strings.Join(lines, "\n")
	return out
}

// translatePostmanLine rewrites one line. A non-empty second return value is
// the reason the line could NOT be translated (and the first return value is
// then meaningless).
func translatePostmanLine(line string, kind ScriptKind) (string, string) {
	if reason := blockedReason(line, kind); reason != "" {
		return "", reason
	}

	work := line
	// Postman's legacy (v1) `tests["name"] = expr` sugar, still common in old
	// collections, becomes a real test() before anything else runs — the
	// expression inside it is then translated by the ordinary rules.
	if m := reLegacyTests.FindStringSubmatch(work); m != nil {
		work = fmt.Sprintf("%stest(%s, () => { expect(%s).toBeTruthy() })", m[1], m[2], strings.TrimSpace(m[3]))
	}
	if kind == ScriptPreRequest {
		work = translateHeaderAdd(work)
	}
	// chai lets `.not` sit on either side of `.to`; normalize so one set of
	// matcher rules covers both spellings.
	work = strings.ReplaceAll(work, ".to.not.", ".not.to.")
	for _, r := range pmRewrites {
		work = r.re.ReplaceAllString(work, r.repl)
	}
	work = reAnonFunc.ReplaceAllString(work, "() => ")

	if leftover := leftoverAPI(work); leftover != "" {
		return "", leftover
	}
	return strings.TrimRight(work, " \t"), ""
}

// blockedReason names the Postman constructs that have no translation at all,
// checked against the ORIGINAL line before any rewriting.
func blockedReason(line string, kind ScriptKind) string {
	switch {
	case strings.Contains(line, "pm.sendRequest"):
		return reasonSendRequest
	case reAsync.MatchString(line):
		return reasonAsync
	case looksAsyncTest(line):
		return reasonAsync
	case strings.Contains(line, "require("):
		return reasonRequire
	case strings.Contains(line, "setTimeout(") || strings.Contains(line, "setInterval("):
		return reasonTimers
	case strings.Contains(line, "setNextRequest"):
		return reasonNextRequest
	}
	if kind == ScriptPreRequest {
		// Nothing that reaches for a response — or for test()/expect(), which
		// AUK deliberately does not expose before a request is sent — can be
		// translated into a pre-request script.
		if strings.Contains(line, "pm.response") || strings.Contains(line, "pm.test(") || strings.Contains(line, "pm.expect(") {
			return reasonPreNoResp
		}
		if reLegacyTests.MatchString(line) || strings.Contains(line, "responseCode") || strings.Contains(line, "responseBody") {
			return reasonPreNoResp
		}
	}
	if kind == ScriptPostResponse && strings.Contains(line, "pm.request.headers") {
		return reasonPostNoReq
	}
	return ""
}

// leftoverAPI reports whether a rewritten line still references something the
// translator did not convert. A line is only accepted when NO Postman or chai
// residue survives — a half-rewritten line would run and fail at runtime with
// a confusing message, which is exactly the silent breakage this feature
// exists to prevent.
func leftoverAPI(line string) string {
	code := stripStringLiterals(line)
	switch {
	case strings.Contains(code, "pm."), strings.Contains(code, "postman."):
		return reasonUnknownPM
	case strings.Contains(code, ".to."), strings.Contains(code, ".should."):
		return reasonUnknownChai
	case strings.Contains(code, "responseCode"), strings.Contains(code, "responseBody"), strings.Contains(code, "responseHeaders"):
		return reasonUnknownPM
	}
	return ""
}

// ---------------------------------------------------------------------------
// Rewrite rules
// ---------------------------------------------------------------------------

type pmRewrite struct {
	re   *regexp.Regexp
	repl string
}

// rw is shorthand for one rule.
func rw(pattern, repl string) pmRewrite {
	return pmRewrite{re: regexp.MustCompile(pattern), repl: repl}
}

// pmRewrites is the full Postman → AUK mapping, applied in order. Response
// rules come before the generic `pm.response.*` accessors so the longer chain
// wins; `pm.expect(`/`pm.test(` come before the chai matcher rules so those
// rules see a plain `expect(...)` chain.
var pmRewrites = []pmRewrite{
	// ---- assertions on the response object -------------------------------
	rw(`\bpm\.response\.to\.not\.have\.status\(`, `expect(response.status).not.toBe(`),
	rw(`\bpm\.response\.to\.have\.status\(`, `expect(response.status).toBe(`),
	rw(`\bpm\.response\.to\.have\.header\(([^()]*)\)`, `expect(response.headers.get($1)).toBeTruthy()`),
	rw(`\bpm\.response\.to\.have\.body\(([^()]*)\)`, `expect(response.body).toBe($1)`),

	// ---- response accessors ----------------------------------------------
	rw(`\bpm\.response\.code\b`, `response.status`),
	rw(`\bpm\.response\.status\b`, `response.statusText`),
	rw(`\bpm\.response\.json\(`, `response.json(`),
	rw(`\bpm\.response\.text\(\s*\)`, `response.body`),
	rw(`\bpm\.response\.responseTime\b`, `response.timingMs`),
	rw(`\bpm\.response\.responseSize\b`, `response.size`),
	rw(`\bpm\.response\.headers\.get\(`, `response.headers.get(`),

	// ---- variables (every Postman scope collapses onto AUK's one set) ----
	rw(`\bpm\.(?:environment|collectionVariables|globals|variables)\.set\(`, `vars.set(`),
	rw(`\bpm\.(?:environment|collectionVariables|globals|variables)\.get\(`, `vars.get(`),
	rw(`\bpm\.(?:environment|collectionVariables|globals|variables)\.unset\(`, `vars.unset(`),

	// ---- legacy (Postman v1) API -----------------------------------------
	rw(`\bpostman\.setEnvironmentVariable\(`, `vars.set(`),
	rw(`\bpostman\.setGlobalVariable\(`, `vars.set(`),
	rw(`\bpostman\.getEnvironmentVariable\(`, `vars.get(`),
	rw(`\bpostman\.getGlobalVariable\(`, `vars.get(`),
	rw(`\bpostman\.clearEnvironmentVariable\(`, `vars.unset(`),
	rw(`\bpostman\.clearGlobalVariable\(`, `vars.unset(`),
	rw(`\bresponseCode\.code\b`, `response.status`),
	rw(`\bresponseBody\b`, `response.body`),
	rw(`\bresponseTime\b`, `response.timingMs`),

	// ---- test / expect entry points --------------------------------------
	rw(`\bpm\.test\(`, `test(`),
	rw(`\bpm\.expect\(`, `expect(`),

	// ---- chai chains → AUK matchers (an optional leading `.not` is kept) --
	rw(`(\.not)?\.to\.deep\.equal\(`, `${1}.toEqual(`),
	rw(`(\.not)?\.to\.eql\(`, `${1}.toEqual(`),
	rw(`(\.not)?\.to\.(?:be\.)?equal\(`, `${1}.toBe(`),
	rw(`(\.not)?\.to\.be\.true\b`, `${1}.toBeTruthy()`),
	rw(`(\.not)?\.to\.be\.false\b`, `${1}.toBeFalsy()`),
	rw(`(\.not)?\.to\.be\.null\b`, `${1}.toBe(null)`),
	rw(`(\.not)?\.to\.be\.undefined\b`, `${1}.toBe(undefined)`),
	rw(`(\.not)?\.to\.(?:include|contain)\(`, `${1}.toContain(`),
	rw(`(\.not)?\.to\.have\.property\(`, `${1}.toHaveProperty(`),
	rw(`(\.not)?\.to\.be\.(?:above|greaterThan|gt)\(`, `${1}.toBeGreaterThan(`),
	rw(`(\.not)?\.to\.be\.(?:below|lessThan|lt)\(`, `${1}.toBeLessThan(`),
	rw(`(\.not)?\.to\.match\(`, `${1}.toMatch(`),
}

var (
	// reAnonFunc turns an anonymous `function () {` callback into an arrow
	// function. Named declarations (`function helper()`) are untouched — the
	// pattern requires the parameter list to follow `function` directly.
	reAnonFunc = regexp.MustCompile(`\bfunction\s*\(\s*\)\s*`)
	// reLegacyTests matches Postman v1's `tests["name"] = expr;`.
	reLegacyTests = regexp.MustCompile(`^(\s*)tests\[([^\]]+)\]\s*=\s*(.+?);?\s*$`)
	// reAsync catches async functions and awaits in any position.
	reAsync = regexp.MustCompile(`\basync\b|\bawait\b`)
	// reCallbackArg matches a callback that TAKES an argument, in either
	// spelling. Combined with a pm.test( on the same line (see looksAsyncTest)
	// that is Postman's async `done` test form, which AUK records as a
	// failure — so it must never be translated silently.
	reCallbackArg = regexp.MustCompile(`function\s*\(\s*\w+|\(\s*\w+\s*\)\s*=>`)
	// reHeaderAdd matches pm.request.headers.add({key: 'X', value: 'Y'}).
	reHeaderAdd = regexp.MustCompile(`\bpm\.request\.headers\.(?:add|upsert)\(\s*\{([^{}]*)\}\s*\)`)
)

// looksAsyncTest reports whether a line declares a Postman test whose
// callback takes an argument — the `done` form, which is asynchronous.
func looksAsyncTest(line string) bool {
	return strings.Contains(line, "pm.test(") && reCallbackArg.MatchString(line)
}

// translateHeaderAdd rewrites pm.request.headers.add/upsert into AUK's
// ctx.setHeader. The object literal is parsed rather than pattern-matched so
// {value, key} order and computed values both work; anything unexpected is
// left alone, and the leftover check then routes the line to a TODO.
func translateHeaderAdd(line string) string {
	return reHeaderAdd.ReplaceAllStringFunc(line, func(match string) string {
		inner := reHeaderAdd.FindStringSubmatch(match)[1]
		var key, value string
		for _, part := range splitTopLevel(inner) {
			name, expr, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			switch strings.Trim(strings.TrimSpace(name), `'"`) {
			case "key":
				key = strings.TrimSpace(expr)
			case "value":
				value = strings.TrimSpace(expr)
			}
		}
		if key == "" || value == "" {
			return match
		}
		return "ctx.setHeader(" + key + ", " + value + ")"
	})
}

// ---------------------------------------------------------------------------
// Small JS-aware helpers
// ---------------------------------------------------------------------------

// bracketScan reports the net bracket depth a line opens (delta: +1 for a
// line ending mid-block) and the LOWEST depth it reaches (minDepth: negative
// when the line closes a block that was opened above it). Quoted strings are
// skipped so a brace inside a string literal does not throw the count off.
//
// delta is what lets an untranslatable line that OPENS a block take its whole
// block into the comment with it, instead of leaving an orphaned `});` behind
// that would not parse. minDepth is what catches the more dangerous case —
// commenting out a `} else {` would silently merge two branches into one and
// still compile.
func bracketScan(line string) (delta, minDepth int) {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return delta, minDepth // rest of the line is a comment
			}
		case '{', '(', '[':
			delta++
		case '}', ')', ']':
			delta--
			if delta < minDepth {
				minDepth = delta
			}
		}
	}
	return delta, minDepth
}

// stripStringLiterals blanks out quoted spans so the leftover check reasons
// about CODE only — a URL or a message containing ".to." or "pm." in a string
// is not a Postman API call.
func stripStringLiterals(line string) string {
	var b strings.Builder
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == quote:
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// splitTopLevel splits an object-literal body on commas that are not nested
// inside brackets or quotes.
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote && (i == 0 || s[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
