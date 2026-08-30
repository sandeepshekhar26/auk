package mockserver

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"apitool/internal/core/model"
)

// Store is the read-only subset of storage the mock server needs: the
// workspace's saved requests (which give the ROUTES) and each request's last
// recorded response (which gives the BODY to replay). An interface, not
// *storage.FileStore, so tests can drive it with anything and so the mock
// server stays a pure reader — it never writes to the store.
type Store interface {
	ListRequests(workspaceID model.ID) []model.RequestDef
	LastResponse(requestID model.ID) (model.ResponseData, bool)
}

// templateRef matches a `${...}` templating reference, using the same shape
// as internal/templating's refPattern. A path segment containing one can't be
// matched literally (its value depends on the environment), so it becomes a
// wildcard.
var templateRef = regexp.MustCompile(`\$\{[^}]+\}`)

// pathParamSeg matches a whole-segment `:name` placeholder — the SAME rule
// core.applyPathParams and the URL bar in RequestEditor.tsx use, so the rows
// the user sees as path params are exactly the segments that become
// wildcards here. A partial match inside a longer segment (`v:1`) is not a
// placeholder.
var pathParamSeg = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)

// segment is one compiled path component of a route.
type segment struct {
	wildcard bool
	// lit is the percent-DECODED literal text, compared against the
	// equally-decoded segment of an incoming request path. Empty for a
	// wildcard.
	lit string
}

// Route is one mock endpoint: an HTTP method plus a path pattern derived from
// a saved request's URL, backed by that request's recorded last response.
// Exported (and JSON-tagged) because the Settings UI lists these.
type Route struct {
	Method string `json:"method"`
	// Path is the DISPLAY form — the path exactly as it appears in the saved
	// request's URL, wildcards included (`/users/:id`, `/v1/${tenant}/orders`)
	// — so the listing in Settings lines up with what the user typed. Matching
	// runs off the compiled segments below, never off this string.
	Path        string `json:"path"`
	RequestID   string `json:"requestId"`
	RequestName string `json:"requestName"`
	// Status is the recorded response's status code, shown in the listing so
	// "this route mocks a 201" is visible without sending anything.
	Status int `json:"status"`

	segments  []segment
	wildcards int
}

// DeriveRoutes reads the workspace's requests LIVE from the store and returns
// the mock route table. Called on every incoming request rather than
// snapshotted at start: re-sending a request in AUK updates its recorded
// response, and the very next hit on the mock serves the new one. That's the
// feature — an always-current mock — not an accident.
//
// A request contributes a route only if all of the following hold:
//
//   - its protocol is http or graphql (a WebSocket/SSE/gRPC endpoint has no
//     replayable request/response pair over plain HTTP);
//   - it has a recorded last response with a real status code (a failed send
//     records Status 0 plus an Error — that is not a mock, it's a failure).
func DeriveRoutes(store Store, workspaceID string) []Route {
	if store == nil || workspaceID == "" {
		return nil
	}
	reqs := store.ListRequests(workspaceID)
	routes := make([]Route, 0, len(reqs))
	for _, r := range reqs {
		if !mockableProtocol(r.Protocol) {
			continue
		}
		resp, ok := store.LastResponse(r.ID)
		if !ok || resp.Status <= 0 {
			continue
		}
		path := ExtractPath(r.URL)
		segs := compileSegments(path)
		wildcards := 0
		for _, s := range segs {
			if s.wildcard {
				wildcards++
			}
		}
		routes = append(routes, Route{
			Method:      normalizeMethod(r.Method, r.Protocol),
			Path:        path,
			RequestID:   r.ID,
			RequestName: r.Name,
			Status:      resp.Status,
			segments:    segs,
			wildcards:   wildcards,
		})
	}
	// Deterministic order so the Settings listing doesn't reshuffle between
	// refreshes (map iteration inside the store gives no ordering guarantee).
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].RequestID < routes[j].RequestID
	})
	return routes
}

func mockableProtocol(p model.ProtocolKind) bool {
	// An empty protocol is the pre-protocols default, i.e. plain HTTP.
	return p == "" || p == model.ProtocolHTTP || p == model.ProtocolGraphQL
}

// normalizeMethod upper-cases the saved method and fills in the protocol's
// natural default when it's blank (GraphQL is POST; everything else is GET).
func normalizeMethod(method string, protocol model.ProtocolKind) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m != "" {
		return m
	}
	if protocol == model.ProtocolGraphQL {
		return http.MethodPost
	}
	return http.MethodGet
}

// ExtractPath reduces a saved request's URL to just its path, which is all a
// local mock can route on. Rules:
//
//   - the fragment and the query string are dropped — routing ignores the
//     query entirely, so `/search?q=a` and `/search?q=b` are ONE route (they
//     are one saved request, with one recorded response);
//   - `scheme://host/path` and `//host/path` lose their authority;
//   - a URL already starting with `/` is taken as-is;
//   - anything else has its FIRST component treated as an authority, which
//     is what makes the overwhelmingly common `${baseUrl}/users` resolve to
//     `/users` (and `localhost:3000/users` to `/users`). The trade-off: a
//     variable that holds a path PREFIX rather than a base URL loses its
//     first segment. Base-URL-in-a-variable is the dominant convention and
//     the one worth optimizing for;
//   - a trailing slash is trimmed (except on the root), so `/users/` and
//     `/users` are the same route.
//
// The result always starts with `/`.
func ExtractPath(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "/"
	}

	switch {
	// `scheme://host/...`. The "://" must come before any '/' or it's part of
	// a path, not a scheme separator.
	case schemeSep(s) >= 0:
		rest := s[schemeSep(s)+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return "/"
		}
		s = rest[j:]
	// `//host/...` (scheme-relative).
	case strings.HasPrefix(s, "//"):
		rest := s[2:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return "/"
		}
		s = rest[j:]
	case strings.HasPrefix(s, "/"):
		// Already a path.
	default:
		// `host/path`, `host:port/path`, `${baseUrl}/path`.
		j := strings.IndexByte(s, '/')
		if j < 0 {
			return "/"
		}
		s = s[j:]
	}

	return normalizePath(s)
}

// schemeSep returns the index of the "://" that separates a scheme from an
// authority, or -1 when there isn't one (a later "://" inside a path doesn't
// count).
func schemeSep(s string) int {
	i := strings.Index(s, "://")
	if i < 0 || strings.IndexByte(s[:i], '/') >= 0 {
		return -1
	}
	return i
}

// normalizePath guarantees a leading slash and drops a trailing one.
func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}

// splitPath returns the path's segments. The root path has none.
func splitPath(p string) []string {
	p = normalizePath(p)
	if p == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

func compileSegments(path string) []segment {
	parts := splitPath(path)
	segs := make([]segment, 0, len(parts))
	for _, part := range parts {
		if templateRef.MatchString(part) || pathParamSeg.MatchString(part) {
			segs = append(segs, segment{wildcard: true})
			continue
		}
		lit := part
		if dec, err := url.PathUnescape(part); err == nil {
			lit = dec
		}
		segs = append(segs, segment{lit: lit})
	}
	return segs
}

// matches reports whether the route's pattern covers the given already-decoded
// request segments. Wildcards match exactly ONE segment, so a pattern only
// ever matches a path of the same depth.
func (r Route) matches(reqSegs []string) bool {
	if len(r.segments) != len(reqSegs) {
		return false
	}
	for i, s := range r.segments {
		if s.wildcard {
			continue
		}
		if s.lit != reqSegs[i] {
			return false
		}
	}
	return true
}

// Match picks the route to serve for an incoming method+path.
//
// Specificity: the candidate with the FEWEST wildcards wins, so a recorded
// `/users/me` beats `/users/:id` for a request to /users/me. Ties break on
// the leftmost literal (a route that pins the earlier segment is the more
// specific one), then on the sorted order DeriveRoutes already imposed, so
// the choice is deterministic rather than map-iteration-dependent.
//
// allow lists the distinct methods recorded for a path that matched but under
// a different method — exactly what a 405 must report in its Allow header. It
// is only populated when ok is false.
func Match(routes []Route, method, path string) (best Route, ok bool, allow []string) {
	// r.URL.Path arrives percent-DECODED from net/http, and route literals
	// were decoded at compile time, so the two sides compare like for like.
	reqSegs := splitPath(path)
	method = strings.ToUpper(method)

	allowSet := map[string]struct{}{}
	found := false
	for _, rt := range routes {
		if !rt.matches(reqSegs) {
			continue
		}
		if rt.Method != method {
			allowSet[rt.Method] = struct{}{}
			continue
		}
		if !found || moreSpecific(rt, best) {
			best, found = rt, true
		}
	}
	if found {
		return best, true, nil
	}

	allow = make([]string, 0, len(allowSet))
	for m := range allowSet {
		allow = append(allow, m)
	}
	sort.Strings(allow)
	return Route{}, false, allow
}

// moreSpecific reports whether a should beat b.
func moreSpecific(a, b Route) bool {
	if a.wildcards != b.wildcards {
		return a.wildcards < b.wildcards
	}
	// Same wildcard count: whichever pins the earlier segment wins.
	for i := range a.segments {
		if i >= len(b.segments) {
			break
		}
		if a.segments[i].wildcard != b.segments[i].wildcard {
			return !a.segments[i].wildcard
		}
	}
	return false
}
