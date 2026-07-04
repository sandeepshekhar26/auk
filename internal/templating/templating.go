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

	"apitool/internal/cookiejar"
	"apitool/internal/core"
	"apitool/internal/core/model"
)

var refPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// responseRefPattern matches a `response('Name').path` / `response("Name")`
// chaining reference. Capture group 2 (the path suffix, if any) is whatever
// follows the closing paren and leading dot, e.g. `body.token` or `status`.
var responseRefPattern = regexp.MustCompile(`^response\(\s*['"](.+?)['"]\s*\)(?:\.(.+))?$`)

// ParseResponseRef parses a `response('ReqName').jsonpath` expression into
// its request-name and path components, WITHOUT executing anything —
// actually resolving the reference (cache lookup + possible auto-send)
// requires the engine, which templating cannot import (core -> templating
// would cycle). ok is false when expr is not a response() reference.
func ParseResponseRef(expr string) (requestName, path string, ok bool) {
	m := responseRefPattern.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Func is a template function invocable as ${name(args)} or ${name} with no args.
type Func func(args []string) (string, error)

// ChainResolver resolves a `response('Name').path` reference: it looks up
// the named request within the given workspace, returns its cached response
// — auto-sending it first if no cached response exists yet — and extracts
// path from the result. core.Engine implements this (see
// core.ChainResolver / Engine.ResolveChainRef); templating only parses the
// expression and calls back through this narrow interface so this package
// never imports core's execution machinery (avoiding the core -> templating
// -> core import cycle).
type ChainResolver interface {
	ResolveChainRef(ctx context.Context, workspaceID model.ID, requestName, path string) (string, error)
}

type Engine struct {
	funcs    map[string]Func
	resolver ChainResolver
	cookies  *cookiejar.Jar
}

// New builds a templating Engine. resolver may be nil (e.g. in tests that
// don't exercise response() refs); a nil resolver makes any response() ref
// fail with a clear error instead of panicking.
func New(resolver ChainResolver) *Engine {
	e := &Engine{funcs: make(map[string]Func), resolver: resolver, cookies: cookiejar.New()}
	e.registerBuiltins()
	e.registerExtra()
	return e
}

// CaptureCookies feeds a response's Set-Cookie headers into this Engine's
// per-workspace jar so a later ${cookie(name)} reference in the same
// workspace can read them. core.Engine calls this after every response
// (type-asserted off Templater, since the Templater interface itself doesn't
// need to know about cookies — see core.Engine.RunRequest).
func (e *Engine) CaptureCookies(workspaceID model.ID, headers []model.KeyValue) {
	e.cookies.Capture(workspaceID, headers)
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
func (e *Engine) Resolve(ctx context.Context, req model.RequestDef, env *model.Environment, history core.ResponseLookup) (core.ResolvedRequest, error) {
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
			out, err := e.eval(ctx, expr, req.WorkspaceID, vars, history)
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
func (e *Engine) eval(ctx context.Context, expr string, workspaceID model.ID, vars map[string]string, history core.ResponseLookup) (string, error) {
	if v, ok := vars[expr]; ok {
		return v, nil
	}

	if name, path, ok := ParseResponseRef(expr); ok {
		if e.resolver == nil {
			return "", fmt.Errorf("response(%q) ref: no chain resolver configured", name)
		}
		out, err := e.resolver.ResolveChainRef(ctx, workspaceID, name, path)
		if err != nil {
			return "", fmt.Errorf("response(%q) ref: %w", name, err)
		}
		return out, nil
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
		if name == "cookie" {
			if len(args) < 1 {
				return "", fmt.Errorf("cookie requires 1 argument: cookie(name)")
			}
			v, ok := e.cookies.Get(workspaceID, args[0])
			if !ok {
				return "", fmt.Errorf("cookie(%q): no such cookie captured yet in this workspace (cookie() reads Set-Cookie from earlier responses this session)", args[0])
			}
			return v, nil
		}
		if fn, ok := e.funcs[name]; ok {
			return fn(args)
		}
		return "", fmt.Errorf("unknown template function %q", name)
	}

	// history (core.ResponseLookup) is currently unused by eval directly —
	// response() refs are served by e.resolver, which does its own
	// cache-vs-auto-send decision inside the engine. It stays a parameter so
	// core.Templater's signature (and the by-id last-response cache it
	// exposes) is available to future non-chaining callers without another
	// signature change.
	_ = history

	// Undefined bare variable: leave the placeholder as-is rather than
	// silently emitting an empty string, so a typo'd variable name is
	// visible in the resolved request instead of vanishing.
	return "", fmt.Errorf("unresolved variable %q", expr)
}
