// Package templating resolves `${...}` references in a RequestDef against
// an environment (plain variables) and template functions (uuid, timestamps,
// etc.), producing the core.ResolvedRequest that protocols execute.
//
// This is the MVP subset (variable substitution + a handful of core
// functions). The full function library from docs/01-feature-roadmap.md
// (hash.*, encode.*, cookie, fs.read, json/xml/regex, prompt,
// request.*/response.* chaining refs) registers additional Funcs here.
package templating

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

var refPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Func is a template function invocable as ${name(args)} or ${name} with no args.
type Func func(args []string) (string, error)

type Engine struct {
	funcs map[string]Func
}

func New() *Engine {
	e := &Engine{funcs: make(map[string]Func)}
	e.registerBuiltins()
	return e
}

func (e *Engine) Register(name string, fn Func) {
	e.funcs[name] = fn
}

func (e *Engine) registerBuiltins() {
	e.funcs["uuid"] = func([]string) (string, error) { return uuid.NewString(), nil }
	e.funcs["timestamp.unix"] = func([]string) (string, error) {
		return strconv.FormatInt(time.Now().Unix(), 10), nil
	}
	e.funcs["timestamp.unixMillis"] = func([]string) (string, error) {
		return strconv.FormatInt(time.Now().UnixMilli(), 10), nil
	}
	e.funcs["timestamp.iso8601"] = func([]string) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	}
	e.funcs["hash.md5"] = hashFunc(md5.New)
	e.funcs["hash.sha1"] = hashFunc(sha1.New)
	e.funcs["hash.sha256"] = hashFunc(sha256.New)
	e.funcs["encode.base64"] = func(args []string) (string, error) {
		if len(args) < 1 {
			return "", fmt.Errorf("encode.base64 requires 1 argument")
		}
		return base64.StdEncoding.EncodeToString([]byte(args[0])), nil
	}
	e.funcs["encode.base64url"] = func(args []string) (string, error) {
		if len(args) < 1 {
			return "", fmt.Errorf("encode.base64url requires 1 argument")
		}
		return base64.URLEncoding.EncodeToString([]byte(args[0])), nil
	}
}

func hashFunc(newHash func() hash.Hash) Func {
	return func(args []string) (string, error) {
		if len(args) < 1 {
			return "", fmt.Errorf("hash requires 1 argument")
		}
		h := newHash()
		h.Write([]byte(args[0]))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}

// Resolve implements core.Templater.
func (e *Engine) Resolve(_ context.Context, req model.RequestDef, env *model.Environment, history core.ResponseLookup) (core.ResolvedRequest, error) {
	vars := map[string]string{}
	if env != nil {
		for _, kv := range env.Variables {
			if kv.Enabled {
				vars[kv.Key] = kv.Value
			}
		}
	}

	var firstErr error
	resolve := func(s string) string {
		return refPattern.ReplaceAllStringFunc(s, func(match string) string {
			expr := strings.TrimSpace(match[2 : len(match)-1])
			out, err := e.eval(expr, vars, history)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			return out
		})
	}

	resolved := core.ResolvedRequest{
		URL:    resolve(req.URL),
		Method: req.Method,
	}
	for _, h := range req.Headers {
		resolved.Headers = append(resolved.Headers, model.KeyValue{Key: resolve(h.Key), Value: resolve(h.Value), Enabled: h.Enabled})
	}
	for _, p := range req.Params {
		resolved.Params = append(resolved.Params, model.KeyValue{Key: resolve(p.Key), Value: resolve(p.Value), Enabled: p.Enabled})
	}
	if req.Body != nil {
		resolvedBody := *req.Body
		resolvedBody.Text = resolve(req.Body.Text)
		resolved.Body = &resolvedBody
	}

	if firstErr != nil {
		return resolved, firstErr
	}
	return resolved, nil
}

// eval resolves one `${...}` expression: a bare variable name, a
// `func(args)` call, or a `response('ReqName').body` chaining reference.
func (e *Engine) eval(expr string, vars map[string]string, history core.ResponseLookup) (string, error) {
	if v, ok := vars[expr]; ok {
		return v, nil
	}

	if idx := strings.Index(expr, "("); idx > 0 && strings.HasSuffix(expr, ")") {
		name := expr[:idx]
		argsRaw := expr[idx+1 : len(expr)-1]
		var args []string
		if strings.TrimSpace(argsRaw) != "" {
			for _, a := range strings.Split(argsRaw, ",") {
				args = append(args, strings.Trim(strings.TrimSpace(a), `'"`))
			}
		}
		if fn, ok := e.funcs[name]; ok {
			return fn(args)
		}
		return "", fmt.Errorf("unknown template function %q", name)
	}

	// Undefined bare variable: leave the placeholder as-is rather than
	// silently emitting an empty string, so a typo'd variable name is
	// visible in the resolved request instead of vanishing.
	return "", fmt.Errorf("unresolved variable %q", expr)
}
