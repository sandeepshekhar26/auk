package mockserver

import (
	"testing"
)

func TestExtractPath(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"absolute", "https://api.example.com/users", "/users"},
		{"absolute with port", "http://localhost:3000/api/v1/users", "/api/v1/users"},
		{"query stripped", "https://api.example.com/search?q=hello&page=2", "/search"},
		{"fragment stripped", "https://api.example.com/docs#section", "/docs"},
		{"host only", "https://api.example.com", "/"},
		{"host only trailing slash", "https://api.example.com/", "/"},
		{"scheme relative", "//api.example.com/users/1", "/users/1"},
		{"already a path", "/users/1", "/users/1"},
		{"bare host", "api.example.com/users", "/users"},
		{"host port no scheme", "localhost:8080/health", "/health"},
		// The dominant convention: the base URL lives in a variable.
		{"baseUrl variable", "${baseUrl}/users", "/users"},
		{"baseUrl variable nested", "${baseUrl}/v1/users/${userId}", "/v1/users/${userId}"},
		{"baseUrl variable alone", "${baseUrl}", "/"},
		{"host in variable", "https://${host}/users", "/users"},
		{"path param", "https://api.example.com/users/:id/posts", "/users/:id/posts"},
		{"trailing slash trimmed", "https://api.example.com/users/", "/users"},
		{"empty", "", "/"},
		{"whitespace", "   ", "/"},
		{"query only after host", "https://api.example.com?x=1", "/"},
		// A "://" that is not a scheme separator must not eat the path.
		{"colon slash slash inside path", "/proxy/http://example.com", "/proxy/http://example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractPath(tc.url); got != tc.want {
				t.Errorf("ExtractPath(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// route builds a Route for matcher tests without going through a store.
func route(method, path string) Route {
	segs := compileSegments(path)
	w := 0
	for _, s := range segs {
		if s.wildcard {
			w++
		}
	}
	return Route{Method: method, Path: path, RequestID: method + " " + path, segments: segs, wildcards: w}
}

func TestMatchWildcards(t *testing.T) {
	routes := []Route{
		route("GET", "/users"),
		route("GET", "/users/:id"),
		route("GET", "/users/me"),
		route("GET", "/v1/${tenant}/orders"),
		route("POST", "/users"),
		route("GET", "/a/:x/c/:y"),
	}

	cases := []struct {
		name    string
		method  string
		path    string
		wantOK  bool
		wantReq string
	}{
		{"literal", "GET", "/users", true, "GET /users"},
		{"path param wildcard", "GET", "/users/42", true, "GET /users/:id"},
		// Specificity: a fully literal route beats the wildcard one.
		{"most specific wins", "GET", "/users/me", true, "GET /users/me"},
		{"template var wildcard", "GET", "/v1/acme/orders", true, "GET /v1/${tenant}/orders"},
		{"method routing", "POST", "/users", true, "POST /users"},
		{"trailing slash normalized", "GET", "/users/", true, "GET /users"},
		{"multi wildcard", "GET", "/a/1/c/2", true, "GET /a/:x/c/:y"},
		// A wildcard matches exactly one segment, never several.
		{"wildcard is single segment", "GET", "/users/42/posts", false, ""},
		{"depth mismatch", "GET", "/a/1/c", false, ""},
		{"unknown path", "GET", "/nope", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, _ := Match(routes, tc.method, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("Match(%s %s) ok = %v, want %v", tc.method, tc.path, ok, tc.wantOK)
			}
			if ok && got.RequestID != tc.wantReq {
				t.Errorf("Match(%s %s) picked %q, want %q", tc.method, tc.path, got.RequestID, tc.wantReq)
			}
		})
	}
}

func TestMatchQueryStringIgnored(t *testing.T) {
	// Routing never sees a query string (net/http hands ServeHTTP a path-only
	// URL.Path), and the recorded URL's query is dropped at derivation time —
	// so two saved requests differing only in query are the same route.
	routes := []Route{route("GET", "/search")}
	if _, ok, _ := Match(routes, "GET", "/search"); !ok {
		t.Fatal("expected /search to match")
	}
	if ExtractPath("https://x/search?q=a") != ExtractPath("https://x/search?q=b") {
		t.Error("query string must not affect the derived route path")
	}
}

func TestMatchLeftmostLiteralBreaksTies(t *testing.T) {
	// Same wildcard count; the route that pins the EARLIER segment is more
	// specific and must win regardless of listing order.
	routes := []Route{route("GET", "/:a/fixed"), route("GET", "/fixed/:b")}
	got, ok, _ := Match(routes, "GET", "/fixed/fixed")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.RequestID != "GET /fixed/:b" {
		t.Errorf("picked %q, want the leftmost-literal route", got.RequestID)
	}
	// And the same answer with the slice reversed — no order dependence.
	got, _, _ = Match([]Route{routes[1], routes[0]}, "GET", "/fixed/fixed")
	if got.RequestID != "GET /fixed/:b" {
		t.Errorf("reversed order picked %q, want the leftmost-literal route", got.RequestID)
	}
}

func TestMatchReportsAllowedMethods(t *testing.T) {
	routes := []Route{route("POST", "/users"), route("DELETE", "/users"), route("GET", "/other")}
	_, ok, allow := Match(routes, "GET", "/users")
	if ok {
		t.Fatal("GET /users should not match")
	}
	if len(allow) != 2 || allow[0] != "DELETE" || allow[1] != "POST" {
		t.Errorf("allow = %v, want sorted [DELETE POST]", allow)
	}
}

func TestMatchPercentEncodedLiteral(t *testing.T) {
	// net/http hands ServeHTTP a DECODED URL.Path, so a route literal that was
	// written percent-encoded must be decoded at compile time to line up.
	routes := []Route{route("GET", "/files/my%20file")}
	if _, ok, _ := Match(routes, "GET", "/files/my file"); !ok {
		t.Error("percent-encoded route literal should match the decoded request path")
	}
}
