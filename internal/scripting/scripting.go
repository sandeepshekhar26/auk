// Package scripting implements core.Scripter — a pre-request script hook
// run via grafana/sobek, a pure-Go JS interpreter with no filesystem,
// network, or process access of its own. That's what keeps this safe to
// run inline in the engine: a script can only reshape the ResolvedRequest
// object it's handed (add/override a header), never make its own HTTP
// calls or reach the OS, so it cannot become a way to route a request
// around the Dispatch policy chokepoint (docs/04-architecture-critique.md)
// — it can only change what the SAME request looks like before that check.
package scripting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/sobek"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// scriptTimeout bounds a runaway script (e.g. an infinite loop) so one bad
// pre-request script can't hang a request forever.
const scriptTimeout = 2 * time.Second

type Scripter struct{}

func New() Scripter { return Scripter{} }

// RunPreRequest executes script against resolved, exposing a minimal
// `ctx` object: ctx.request.{method,url,headers,body} (read-only
// snapshot) and ctx.setHeader(name, value) (the only mutation). Each call
// gets a brand-new sobek VM — no state persists between requests or
// between a request and its chained children.
func (Scripter) RunPreRequest(ctx context.Context, script string, resolved core.ResolvedRequest) (core.ResolvedRequest, error) {
	vm := sobek.New()

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

	reqObj := vm.NewObject()
	if err := errors.Join(
		reqObj.Set("method", resolved.Method),
		reqObj.Set("url", resolved.URL),
		reqObj.Set("headers", headers),
		reqObj.Set("body", bodyText),
	); err != nil {
		return resolved, fmt.Errorf("build script request object: %w", err)
	}

	setHeaders := map[string]string{}
	ctxObj := vm.NewObject()
	if err := errors.Join(
		ctxObj.Set("request", reqObj),
		ctxObj.Set("setHeader", func(name, value string) { setHeaders[name] = value }),
	); err != nil {
		return resolved, fmt.Errorf("build script ctx object: %w", err)
	}
	if err := vm.Set("ctx", ctxObj); err != nil {
		return resolved, fmt.Errorf("bind script ctx: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := vm.RunString(script)
		done <- runErr
	}()

	select {
	case err := <-done:
		if err != nil {
			return resolved, fmt.Errorf("script error: %w", err)
		}
	case <-time.After(scriptTimeout):
		vm.Interrupt("pre-request script timed out")
		return resolved, fmt.Errorf("pre-request script exceeded %s", scriptTimeout)
	case <-ctx.Done():
		vm.Interrupt("cancelled")
		return resolved, ctx.Err()
	}

	for name, value := range setHeaders {
		resolved.Headers = upsertHeader(resolved.Headers, name, value)
	}
	return resolved, nil
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
