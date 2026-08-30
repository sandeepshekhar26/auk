package scripting

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// responseEnvelope is the JSON shape handed to runtime_response.js. Headers
// travel as ordered pairs rather than an object so duplicates survive (a
// response with three Set-Cookie headers has three, and response.headers
// .getAll('set-cookie') must be able to return all of them).
type responseEnvelope struct {
	Status     int         `json:"status"`
	StatusText string      `json:"statusText"`
	Headers    [][2]string `json:"headers"`
	TimingMs   int64       `json:"timingMs"`
	Size       int         `json:"size"`
}

// RunPostResponse implements core.PostResponseScripter: the test-and-chain
// half of scripting, run after the response has arrived and after the
// declarative assertions have been scored.
//
// Script API:
//
//	response.status                number
//	response.statusText            string
//	response.headers               object of the headers as sent, plus
//	                               case-insensitive .get(name)/.getAll(name)
//	response.body                  raw text
//	response.json()                parsed body; throws a readable error when
//	                               the body is not JSON
//	response.timingMs, response.size
//	test(name, fn)                 records one TestResult; catches PER TEST
//	expect(actual)                 .toBe .toEqual .toBeTruthy .toBeFalsy
//	                               .toContain .toBeGreaterThan .toBeLessThan
//	                               .toMatch .toHaveProperty, each negatable
//	                               through .not
//	vars.get/set/unset             variable writes — the chaining unlock
//	console.log(...)               captured, never printed
//
// The returned error is the SCRIPT's own failure (syntax error, timeout, or
// a throw outside a test) — the engine turns it into ResponseData.ScriptError,
// a failed run. A test that merely fails is not an error: it comes back in
// Tests, and any tests and variable writes that happened before a later
// throw come back too.
func (s Scripter) RunPostResponse(ctx context.Context, script string, in core.PostResponseInput) (core.PostResponseOutput, error) {
	out := core.PostResponseOutput{Vars: nonNilVars(copyVars(in.Vars)), VarWrites: map[string]string{}}

	box, err := newSandbox(in.Vars, in.Secrets)
	if err != nil {
		return out, err
	}

	body := decodeBody(in.Response.BodyBase64)
	size := in.Response.BodySize
	if size == 0 {
		size = len(body)
	}
	headers := make([][2]string, 0, len(in.Response.Headers))
	for _, h := range in.Response.Headers {
		headers = append(headers, [2]string{h.Key, h.Value})
	}
	envelope, err := json.Marshal(responseEnvelope{
		Status:     in.Response.Status,
		StatusText: in.Response.StatusText,
		Headers:    headers,
		TimingMs:   in.Response.TimingMs,
		Size:       size,
	})
	if err != nil {
		return out, fmt.Errorf("encode response for script: %w", err)
	}

	if err := errors.Join(
		box.vm.Set("__responseJSON", string(envelope)),
		// The body crosses as a plain string rather than inside the JSON
		// envelope: it can be megabytes, and there is no reason to encode it
		// twice.
		box.vm.Set("__responseBody", body),
		box.vm.Set("__recordTest", func(name string, passed bool, message string) {
			box.tests = append(box.tests, model.TestResult{Name: name, Passed: passed, Error: message})
		}),
		// __reMatch runs .toMatch through Go's RE2 (linear time) rather than the
		// JS regex engine, whose regexp2 backtracking fallback ignores the VM
		// interrupt and can peg a core forever. RE2-incompatible patterns are
		// reported as a usage error instead of running on the unsafe engine.
		box.vm.Set("__reMatch", func(pattern, str string) string {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "!" + err.Error()
			}
			if re.MatchString(str) {
				return "1"
			}
			return "0"
		}),
	); err != nil {
		return out, fmt.Errorf("bind response runtime: %w", err)
	}

	if _, err := box.vm.RunString(runtimeResponseJS); err != nil {
		return out, fmt.Errorf("install response runtime: %w", err)
	}
	if _, err := box.vm.RunString(runtimeExpectJS); err != nil {
		return out, fmt.Errorf("install test runtime: %w", err)
	}

	completed, runErr := box.run(ctx, script, s.postResponseTimeout(), "post-response")
	if !completed {
		// Timed out or cancelled: the script goroutine may still be running,
		// and reading its sinks now would be a data race. Nothing collected.
		return out, runErr
	}

	// An async script whose body threw leaves an unhandled promise rejection
	// that RunString never surfaced — treat it as a script error rather than
	// letting the (now-empty) suite report a false green.
	if runErr == nil {
		if msg := box.unhandledRejection(); msg != "" {
			runErr = fmt.Errorf("%s (async test callbacks are not supported — assert synchronously)", msg)
		}
	}

	out.Tests = box.tests
	out.VarWrites = box.varWrites
	out.VarUnsets = box.varUnsets
	out.Logs = box.logs
	for name, value := range box.varWrites {
		out.Vars[name] = value
	}
	for _, name := range box.varUnsets {
		delete(out.Vars, name)
	}

	if runErr != nil {
		return out, fmt.Errorf("script error: %w", runErr)
	}
	return out, nil
}

// decodeBody turns the stored base64 body back into text, falling back to
// the raw field if it was never base64 in the first place — a script should
// see whatever the body actually was, not an empty string, and this is a
// reporting path where a decode failure must not become a hard error.
func decodeBody(bodyBase64 string) string {
	if bodyBase64 == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(bodyBase64)
	if err != nil {
		return bodyBase64
	}
	return string(decoded)
}

func copyVars(vars map[string]string) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out
}
