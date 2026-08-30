// Package scripting implements core.Scripter — the JS hooks AUK runs around
// a request — via grafana/sobek, a pure-Go interpreter with no filesystem,
// network, or process access of its own. That is what keeps this safe to run
// inline in the engine:
//
//   - A PRE-REQUEST script can only reshape the ResolvedRequest it is handed
//     (add/override a header) and write variables. It can never make its own
//     HTTP call, so it cannot become a way to route a request around the
//     Dispatch policy chokepoint (docs/04-architecture-critique.md) — it can
//     only change what the SAME request looks like before that check.
//   - A POST-RESPONSE script gets a COPY of the response. It declares tests
//     and writes variables; nothing it does can alter the response that gets
//     stored, returned, or reported on.
//
// Neither can reach the OS, and both are bounded by a hard timeout enforced
// from a second goroutine via vm.Interrupt, so a runaway loop costs one
// request rather than the app.
package scripting

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/sobek"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// The script runtime, installed into a fresh VM before every user script.
// Kept as .js files rather than Go string constants so they stay readable,
// lintable, and reviewable as the security surface they are.
var (
	//go:embed runtime_common.js
	runtimeCommonJS string
	//go:embed runtime_response.js
	runtimeResponseJS string
	//go:embed runtime_expect.js
	runtimeExpectJS string
)

const (
	// DefaultPreRequestTimeout bounds a pre-request script. It sits in front
	// of the request the user is waiting on, and its whole job (compute a
	// signature, stamp a header) is arithmetic — 2s is already generous.
	DefaultPreRequestTimeout = 2 * time.Second
	// DefaultPostResponseTimeout is longer because a post-response script is
	// a test suite: it parses the body and can run dozens of assertions over
	// it. Still bounded, since the alternative is a hung request.
	DefaultPostResponseTimeout = 5 * time.Second

	// maxLogLines caps captured console output per run, so a console.log in
	// a loop costs a truncated list rather than the process's memory.
	maxLogLines = 500
)

// Scripter is the core.Scripter / core.PreRequestVarScripter /
// core.PostResponseScripter implementation. The zero value is usable (it
// falls back to the default timeouts); New() is the normal constructor.
type Scripter struct {
	PreRequestTimeout   time.Duration
	PostResponseTimeout time.Duration
}

func New() Scripter {
	return Scripter{
		PreRequestTimeout:   DefaultPreRequestTimeout,
		PostResponseTimeout: DefaultPostResponseTimeout,
	}
}

func (s Scripter) preRequestTimeout() time.Duration {
	if s.PreRequestTimeout > 0 {
		return s.PreRequestTimeout
	}
	return DefaultPreRequestTimeout
}

func (s Scripter) postResponseTimeout() time.Duration {
	if s.PostResponseTimeout > 0 {
		return s.PostResponseTimeout
	}
	return DefaultPostResponseTimeout
}

// RunPreRequest implements core.Scripter — the original variable-less entry
// point, kept for callers (and tests) that only need header shaping.
func (s Scripter) RunPreRequest(ctx context.Context, script string, resolved core.ResolvedRequest) (core.ResolvedRequest, error) {
	out, err := s.RunPreRequestWithVars(ctx, script, resolved, core.PreRequestInput{})
	return out.Resolved, err
}

// RunPreRequestWithVars implements core.PreRequestVarScripter: the same
// script and the same moment in the pipeline as RunPreRequest, with
// vars.get/set/unset and console.log wired up.
//
// Script API:
//
//	ctx.request.{method,url,headers,body}   read-only snapshot
//	ctx.setHeader(name, value)              add or override a header
//	vars.get(name) / set(name, v) / unset(name)
//	console.log(...)                        captured, never printed
//
// Each call gets a brand-new VM: no state persists between requests, or
// between a request and its chained children. On any error the request is
// returned UNCHANGED — a script that failed halfway must not send a
// half-modified request.
func (s Scripter) RunPreRequestWithVars(ctx context.Context, script string, resolved core.ResolvedRequest, in core.PreRequestInput) (core.PreRequestOutput, error) {
	out := core.PreRequestOutput{Resolved: resolved}

	box, err := newSandbox(in.Vars, in.Secrets)
	if err != nil {
		return out, err
	}

	headers := make(map[string]string, len(resolved.Headers))
	for _, h := range resolved.Headers {
		if h.Enabled {
			headers[h.Key] = h.Value
		}
	}
	bodyText := ""
	if resolved.Body != nil {
		bodyText = resolved.Body.Text
	}

	vm := box.vm
	reqObj := vm.NewObject()
	if err := errors.Join(
		reqObj.Set("method", resolved.Method),
		reqObj.Set("url", resolved.URL),
		reqObj.Set("headers", headers),
		reqObj.Set("body", bodyText),
	); err != nil {
		return out, fmt.Errorf("build script request object: %w", err)
	}

	setHeaders := map[string]string{}
	ctxObj := vm.NewObject()
	if err := errors.Join(
		ctxObj.Set("request", reqObj),
		ctxObj.Set("setHeader", func(name, value string) { setHeaders[name] = value }),
	); err != nil {
		return out, fmt.Errorf("build script ctx object: %w", err)
	}
	if err := vm.Set("ctx", ctxObj); err != nil {
		return out, fmt.Errorf("bind script ctx: %w", err)
	}

	completed, runErr := box.run(ctx, script, s.preRequestTimeout(), "pre-request")
	if !completed {
		// The script goroutine may still be unwinding; reading anything it
		// writes to would be a data race. Report only the failure.
		return out, runErr
	}

	// Everything the script managed before it threw is still a real side
	// effect, so collect first and report the error after.
	out.VarWrites, out.VarUnsets, out.Logs = box.varWrites, box.varUnsets, box.logs
	// An async pre-request script that threw leaves an unhandled rejection
	// RunString didn't surface — surface it so it isn't a silent no-op.
	if runErr == nil {
		if msg := box.unhandledRejection(); msg != "" {
			runErr = fmt.Errorf("%s (async scripts are not supported)", msg)
		}
	}
	if runErr != nil {
		return out, fmt.Errorf("script error: %w", runErr)
	}

	for name, value := range setHeaders {
		resolved.Headers = upsertHeader(resolved.Headers, name, value)
	}
	out.Resolved = resolved
	return out, nil
}

// sandbox is one script VM plus the Go-side sinks its runtime writes into.
// The JS runtime does the validation and formatting; these just collect.
type sandbox struct {
	vm        *sobek.Runtime
	logs      []string
	varWrites map[string]string
	varUnsets []string
	tests     []model.TestResult
	// rejections holds promises that were rejected and never handled. sobek
	// runs async functions but has no host event loop, so an async script
	// (`async function f(){...}; f()`) whose body throws produces an UNHANDLED
	// rejection that RunString does NOT surface — err would be nil and the
	// whole test suite would silently vanish into a false GREEN. The
	// rejection tracker (installed in newSandbox) records these so the run
	// can be failed. Keyed by the *Promise identity via a slice of messages.
	rejections map[*sobek.Promise]string
}

// unhandledRejection returns the message of the first still-unhandled promise
// rejection, or "" if there were none. Called only after a COMPLETED run.
func (b *sandbox) unhandledRejection() string {
	for _, msg := range b.rejections {
		return msg
	}
	return ""
}

// newSandbox builds a VM with the shared runtime (console + vars) installed.
// Inputs cross into JS as JSON strings rather than as wrapped Go values, so
// what a script touches is always a plain JS object with plain JS semantics
// and no live handle back into a Go map.
func newSandbox(vars map[string]string, secrets []string) (*sandbox, error) {
	box := &sandbox{vm: sobek.New(), varWrites: map[string]string{}, rejections: map[*sobek.Promise]string{}}

	// Track unhandled promise rejections so an async script that throws can't
	// report a silent green (see sandbox.rejections). On reject-without-handler
	// we remember the promise+message; if a handler is later attached we forget
	// it. Whatever remains after a completed run is a genuine unhandled throw.
	box.vm.SetPromiseRejectionTracker(func(p *sobek.Promise, op sobek.PromiseRejectionOperation) {
		switch op {
		case sobek.PromiseRejectionReject:
			msg := "unhandled promise rejection"
			if r := p.Result(); r != nil {
				msg = "unhandled promise rejection: " + r.String()
			}
			box.rejections[p] = msg
		case sobek.PromiseRejectionHandle:
			delete(box.rejections, p)
		}
	})

	varsJSON, err := json.Marshal(nonNilVars(vars))
	if err != nil {
		return nil, fmt.Errorf("encode script variables: %w", err)
	}
	secretsJSON, err := json.Marshal(nonNilNames(secrets))
	if err != nil {
		return nil, fmt.Errorf("encode script secret names: %w", err)
	}

	if err := errors.Join(
		box.vm.Set("__initialVarsJSON", string(varsJSON)),
		box.vm.Set("__secretNamesJSON", string(secretsJSON)),
		box.vm.Set("__log", func(line string) {
			if len(box.logs) < maxLogLines {
				box.logs = append(box.logs, line)
			}
		}),
		box.vm.Set("__varSet", func(name, value string) {
			box.varWrites[name] = value
			box.varUnsets = withoutName(box.varUnsets, name)
		}),
		box.vm.Set("__varUnset", func(name string) {
			delete(box.varWrites, name)
			if !containsName(box.varUnsets, name) {
				box.varUnsets = append(box.varUnsets, name)
			}
		}),
	); err != nil {
		return nil, fmt.Errorf("bind script runtime: %w", err)
	}

	if _, err := box.vm.RunString(runtimeCommonJS); err != nil {
		return nil, fmt.Errorf("install script runtime: %w", err)
	}
	return box, nil
}

// run executes the user's script with a hard timeout, reporting whether it
// COMPLETED (normally or by throwing) separately from the error.
//
// That distinction is the whole safety contract of this file: on a timeout
// or a cancellation the script goroutine is still running, and sobek VMs are
// not goroutine-safe, so the caller must not read a single thing the script
// may still be writing. completed==false means "collect nothing".
func (b *sandbox) run(ctx context.Context, script string, timeout time.Duration, label string) (bool, error) {
	done := make(chan error, 1)
	go func() {
		_, err := b.vm.RunString(script)
		done <- err
	}()

	select {
	case err := <-done:
		return true, err
	case <-time.After(timeout):
		// sobek's interrupt is not catchable from JS, so a try/catch inside
		// the script cannot swallow this and keep spinning.
		b.vm.Interrupt(label + " script timed out")
		return false, fmt.Errorf("%s script exceeded %s", label, timeout)
	case <-ctx.Done():
		b.vm.Interrupt("cancelled")
		return false, ctx.Err()
	}
}

func nonNilVars(vars map[string]string) map[string]string {
	if vars == nil {
		return map[string]string{}
	}
	return vars
}

func nonNilNames(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func withoutName(names []string, name string) []string {
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

func upsertHeader(headers []model.KeyValue, key, value string) []model.KeyValue {
	for i, h := range headers {
		if strings.EqualFold(h.Key, key) {
			headers[i].Value = value
			headers[i].Enabled = true
			return headers
		}
	}
	return append(headers, model.KeyValue{Key: key, Value: value, Enabled: true})
}
