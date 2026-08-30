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
	"apitool/internal/onepassword"
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
	e.registerRandom()
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

// ListCookies, SetCookie, and DeleteCookie expose the same per-workspace jar
// CaptureCookies writes into, for the GUI's Cookies panel (view/edit/clear
// what's been captured) — app.go type-asserts these off Templater the same
// way core.Engine.RunRequest already does for CaptureCookies.
func (e *Engine) ListCookies(workspaceID model.ID) []model.KeyValue {
	return e.cookies.List(workspaceID)
}

func (e *Engine) SetCookie(workspaceID model.ID, name, value string) {
	e.cookies.Set(workspaceID, name, value)
}

func (e *Engine) DeleteCookie(workspaceID model.ID, name string) {
	e.cookies.Delete(workspaceID, name)
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
	// Path param VALUES are templated like any other field (so /users/:id
	// with id = ${userId} works), but their KEYS are not: a key is the
	// literal placeholder name parsed out of the URL, and running it
	// through the resolver would only ever mangle it. The `:name` -> value
	// substitution into URL itself happens one layer up, in
	// core.Engine.resolveAndAuthorize, which is where the request's
	// protocol gates whether path placeholders apply at all.
	for _, p := range req.PathParams {
		resolved.PathParams = append(resolved.PathParams, model.KeyValue{Key: p.Key, Value: resolve(p.Value), Enabled: p.Enabled})
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

// ResolveAuth returns a DEEP COPY of auth with every credential string field
// `${...}`-templated against the same variables Resolve uses. Auth config
// fields were historically NOT templated (only URL/headers/params/body were),
// so `Bearer ${token}` in the Auth tab stayed literal — surprising, and it
// blocked the natural auth-chaining flow (a post-response script stores a
// token, the next request's Auth→Bearer field references it). This closes
// that for every auth kind.
//
// It is a COPY: the input *AuthConfig is a pointer straight into the store,
// so resolving in place would rewrite the user's saved credentials with a
// one-shot resolved value.
func (e *Engine) ResolveAuth(ctx context.Context, req model.RequestDef, env *model.Environment, history core.ResponseLookup, auth *model.AuthConfig) (*model.AuthConfig, error) {
	if auth == nil {
		return nil, nil
	}
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

	// Every nested credential struct is deep-copied so the returned config
	// never aliases the store's, but ONLY the one matching auth.Kind is
	// templated. The others are carried through verbatim.
	//
	// That distinction is load-bearing, not tidiness. The Auth tab
	// deliberately PRESERVES the sub-objects of kinds you switched away from
	// (so flipping Bearer → Basic → Bearer doesn't lose your typing), so a
	// request that once used Basic still carries a Basic block referencing
	// `${legacyPassword}` long after it moved to Bearer. Resolving that
	// inactive block sets firstErr on an unresolved variable, and the engine
	// turns any error here into a hard "resolve auth templates:" failure —
	// so deleting a variable no request still uses would abort EVERY send of
	// every request that ever referenced it. An inactive credential cannot
	// break an active one.
	out := *auth
	out.Basic = cloneBasic(auth.Basic)
	out.Bearer = cloneBearer(auth.Bearer)
	out.APIKey = cloneAPIKey(auth.APIKey)
	out.JWT = cloneJWT(auth.JWT)
	out.OAuth2 = cloneOAuth2(auth.OAuth2)
	out.AWSSigV4 = cloneAWSSigV4(auth.AWSSigV4)
	out.OAuth1 = cloneOAuth1(auth.OAuth1)
	out.Digest = cloneDigest(auth.Digest)

	switch auth.Kind {
	case model.AuthBasic:
		if b := out.Basic; b != nil {
			b.Username, b.Password = resolve(b.Username), resolve(b.Password)
		}
	case model.AuthBearer:
		if b := out.Bearer; b != nil {
			b.Token = resolve(b.Token)
		}
	case model.AuthAPIKey:
		if a := out.APIKey; a != nil {
			a.Key, a.Value = resolve(a.Key), resolve(a.Value)
		}
	case model.AuthJWT:
		if j := out.JWT; j != nil {
			j.Secret, j.Claims = resolve(j.Secret), resolve(j.Claims)
		}
	case model.AuthOAuth2:
		if o := out.OAuth2; o != nil {
			o.ClientID, o.ClientSecret, o.TokenURL = resolve(o.ClientID), resolve(o.ClientSecret), resolve(o.TokenURL)
		}
	case model.AuthAWSSigV4:
		if a := out.AWSSigV4; a != nil {
			a.AccessKeyID, a.SecretAccessKey = resolve(a.AccessKeyID), resolve(a.SecretAccessKey)
			a.Region, a.Service, a.SessionToken = resolve(a.Region), resolve(a.Service), resolve(a.SessionToken)
		}
	case model.AuthOAuth1:
		if o := out.OAuth1; o != nil {
			o.ConsumerKey, o.ConsumerSecret = resolve(o.ConsumerKey), resolve(o.ConsumerSecret)
			o.Token, o.TokenSecret = resolve(o.Token), resolve(o.TokenSecret)
		}
	case model.AuthDigest:
		if d := out.Digest; d != nil {
			d.Username, d.Password = resolve(d.Username), resolve(d.Password)
		}
	}

	if firstErr != nil {
		return &out, firstErr
	}
	return &out, nil
}

// The clone* helpers below each deep-copy one credential sub-struct (nil in,
// nil out). They exist so ResolveAuth can hand back a config that shares
// nothing with the store's, whether or not the sub-struct's kind is active.

func cloneBasic(v *model.BasicAuth) *model.BasicAuth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneBearer(v *model.BearerAuth) *model.BearerAuth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneAPIKey(v *model.APIKeyAuth) *model.APIKeyAuth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneJWT(v *model.JWTAuth) *model.JWTAuth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneOAuth2(v *model.OAuth2Auth) *model.OAuth2Auth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneAWSSigV4(v *model.AWSSigV4Auth) *model.AWSSigV4Auth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneOAuth1(v *model.OAuth1Auth) *model.OAuth1Auth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneDigest(v *model.DigestAuth) *model.DigestAuth {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// eval resolves one `${...}` expression: a bare variable name, a
// `func(args)` call, or a `response('ReqName').body` chaining reference.
func (e *Engine) eval(ctx context.Context, expr string, workspaceID model.ID, vars map[string]string, history core.ResponseLookup) (string, error) {
	if v, ok := vars[expr]; ok {
		// A variable's value (plain, or OS-keychain-backed — env.Secrets
		// resolution already happened by the time it reaches here, see
		// storage.FileStore.GetEnvironment) can ALSO be a 1Password
		// reference instead of a literal, resolved here (lazily, only for
		// variables a request ACTUALLY references) so every existing
		// ${varName} use site (headers, URL, body, params) gets 1Password
		// support for free, without auth methods needing their own
		// separate op:// handling. Lazy, not resolved up front alongside
		// every other variable in the environment, so an unrelated broken
		// op:// variable elsewhere in the same environment can't break a
		// request that never references it.
		if onepassword.IsRef(v) {
			resolved, err := onepassword.Read(ctx, v)
			if err != nil {
				return "", fmt.Errorf("variable %q: %w", expr, err)
			}
			return resolved, nil
		}
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

	// Bare `${name}` (no parens) that names a registered function: dispatch
	// it with no args. This is what makes argument-less dynamic variables
	// resolve — `${uuid}`, `${randomEmail}`, `${randomInt}` — and is exactly
	// how an imported Postman collection reaches them, since Postman's
	// `{{$random*}}` dynamic variables are always argument-less and map to
	// bare `${random*}`. Checked AFTER variables so a user-defined variable
	// of the same name still wins.
	if fn, ok := e.funcs[expr]; ok {
		return fn(nil)
	}

	// Undefined bare variable: leave the placeholder as-is rather than
	// silently emitting an empty string, so a typo'd variable name is
	// visible in the resolved request instead of vanishing.
	return "", fmt.Errorf("unresolved variable %q", expr)
}
