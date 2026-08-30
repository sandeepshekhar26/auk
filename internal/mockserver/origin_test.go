package mockserver

import (
	"net/http"
	"strings"
	"testing"
)

// These tests pin the ONE thing standing between a workspace full of recorded
// access tokens and any page the developer has open in another tab: which
// origins the mock hands an Access-Control-Allow-Origin to. See the package
// doc for the policy.

// doOrigin is do() plus control over the Host header, which net/http takes
// from Request.Host rather than the header map.
func doOrigin(t *testing.T, srv *Server, path, origin, host string, extra map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL()+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if host != "" {
		req.Host = host
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func seededServer(t *testing.T) *Server {
	t.Helper()
	fs := newStore(t)
	seed(t, fs, "r-token", "POST", "https://api.example.com/oauth/token",
		jsonResp(200, `{"access_token":"super-secret-token"}`))
	seed(t, fs, "r-me", "GET", "https://api.example.com/me",
		jsonResp(200, `{"email":"ada@example.com"}`))
	return startServer(t, fs)
}

// A local dev server — the actual use case — must keep working end to end.
func TestLoopbackOriginsAreReflected(t *testing.T) {
	srv := seededServer(t)
	for _, origin := range []string{
		"http://localhost:5173",  // Vite
		"http://localhost:3000",  // CRA / Next
		"http://127.0.0.1:8080",  // explicit v4 loopback
		"http://127.0.0.1",       // no port
		"http://[::1]:5173",      // v6 loopback
		"https://localhost:5173", // vite --https
		"http://127.0.0.2:4000",  // rest of 127.0.0.0/8
	} {
		resp := doOrigin(t, srv, "/me", origin, "", nil)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("origin %q: Allow-Origin = %q, want it reflected", origin, got)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("origin %q: status = %d, want 200", origin, resp.StatusCode)
		}
	}
}

// The finding: any page in any tab reading recorded tokens off the mock.
func TestNonLoopbackOriginGetsNoCORSHeaders(t *testing.T) {
	srv := seededServer(t)
	for _, origin := range []string{
		"https://evil.test",
		"http://evil.test",
		"https://evil.test:5173",            // a port that LOOKS like a dev server
		"null",                              // sandboxed iframe / file://
		"http://192.168.1.20:5173",          // a page on the LAN
		"http://localhost.evil.test:5173",   // prefix trick
		"http://127.0.0.1.evil.test",        // suffix trick
		"http://evil.test#http://localhost", // fragment trick
	} {
		resp := doOrigin(t, srv, "/oauth/token", origin, "", nil)
		for _, h := range []string{
			"Access-Control-Allow-Origin",
			"Access-Control-Allow-Methods",
			"Access-Control-Allow-Headers",
			"Access-Control-Expose-Headers",
			"Access-Control-Allow-Credentials",
		} {
			if v := resp.Header.Get(h); v != "" {
				t.Errorf("origin %q: %s = %q, want absent", origin, h, v)
			}
		}
	}
}

// A preflight is where an attacker would most like a slip, since a permissive
// answer there clears the real request.
func TestPreflightDoesNotLeakAllowToDisallowedOrigin(t *testing.T) {
	srv := seededServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.URL()+"/oauth/token", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "https://evil.test")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()

	for h := range resp.Header {
		if strings.HasPrefix(strings.ToLower(h), "access-control-allow") {
			t.Errorf("preflight leaked %s: %q", h, resp.Header.Get(h))
		}
	}
	if resp.Header.Get("Vary") == "" {
		t.Error("preflight missing Vary")
	}
}

func TestVaryOriginAlwaysPresent(t *testing.T) {
	srv := seededServer(t)
	for _, origin := range []string{"", "http://localhost:5173", "https://evil.test"} {
		resp := doOrigin(t, srv, "/me", origin, "", nil)
		var found bool
		for _, v := range resp.Header.Values("Vary") {
			for _, part := range strings.Split(v, ",") {
				if strings.EqualFold(strings.TrimSpace(part), "Origin") {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("origin %q: Vary = %v, want it to include Origin", origin, resp.Header.Values("Vary"))
		}
	}
}

// Expose-Headers: * made every replayed header script-readable. It must now be
// a short explicit list, and only for an allowed origin.
func TestExposeHeadersIsNotWildcard(t *testing.T) {
	srv := seededServer(t)
	resp := doOrigin(t, srv, "/me", "http://localhost:5173", "", nil)
	got := resp.Header.Get("Access-Control-Expose-Headers")
	if got == "*" || got == "" {
		t.Fatalf("Expose-Headers = %q, want an explicit conservative list", got)
	}
	if !strings.Contains(got, MockHeader) {
		t.Errorf("Expose-Headers = %q, want it to include %s", got, MockHeader)
	}
}

// Credentials are never allowed, so a page cannot ride the developer's cookies
// into the mock even from an allowed loopback origin.
func TestAllowCredentialsNeverSet(t *testing.T) {
	srv := seededServer(t)
	resp := doOrigin(t, srv, "/me", "http://localhost:5173", "", nil)
	if v := resp.Header.Get("Access-Control-Allow-Credentials"); v != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want absent", v)
	}
}

// DNS rebinding: a name the attacker controls, resolved to 127.0.0.1, would
// otherwise reach the mock as a same-origin request with no CORS check to fail.
func TestNonLoopbackHostIsRejected(t *testing.T) {
	srv := seededServer(t)
	for _, host := range []string{
		"mock.evil.test",
		"mock.evil.test:8725",
		"192.168.1.20:8725",
		"evil.test",
	} {
		resp := doOrigin(t, srv, "/oauth/token", "", host, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q: status = %d, want 403", host, resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("Host %q: rejected request still carried an allow header", host)
		}
	}
}

func TestLoopbackHostsAreAccepted(t *testing.T) {
	srv := seededServer(t)
	for _, host := range []string{
		"127.0.0.1:8725",
		"localhost:8725",
		"localhost",
		"[::1]:8725",
	} {
		resp := doOrigin(t, srv, "/me", "", host, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q: status = %d, want 200", host, resp.StatusCode)
		}
	}
}

// The rejected-host path must not double as a path-enumeration oracle: a
// recorded path and an unrecorded one answer identically.
func TestRejectedHostGivesNoRoutingSignal(t *testing.T) {
	srv := seededServer(t)
	known := doOrigin(t, srv, "/oauth/token", "", "mock.evil.test", nil)
	unknown := doOrigin(t, srv, "/definitely-not-recorded", "", "mock.evil.test", nil)
	if known.StatusCode != unknown.StatusCode {
		t.Errorf("recorded path = %d, unrecorded = %d: host rejection leaks routing",
			known.StatusCode, unknown.StatusCode)
	}
	if known.Header.Get("Allow") != "" {
		t.Error("rejected host still got an Allow header")
	}
}

// Unit-level coverage of the predicates, so a future edit that widens them
// fails here rather than in a browser.
func TestIsLoopbackOrigin(t *testing.T) {
	allow := []string{
		"http://localhost:5173", "https://localhost", "http://127.0.0.1:1",
		"http://[::1]:3000", "https://[::1]", "http://127.255.255.254:9999",
	}
	deny := []string{
		"", "null", "*", "https://evil.test", "http://localhost.evil.test",
		"http://evil.test:5173", "file://", "ws://localhost:5173",
		"http://user:pass@localhost:5173", "http://localhost:5173/path",
		"http://10.0.0.5:5173", "http://[::2]:5173", "http://0.0.0.0:5173",
		"http://sub.localhost:5173", "localhost:5173",
	}
	for _, o := range allow {
		if !isLoopbackOrigin(o) {
			t.Errorf("isLoopbackOrigin(%q) = false, want true", o)
		}
	}
	for _, o := range deny {
		if isLoopbackOrigin(o) {
			t.Errorf("isLoopbackOrigin(%q) = true, want false", o)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	for _, h := range []string{"", "127.0.0.1:8725", "localhost", "[::1]:8725", "::1", "127.0.0.1"} {
		if !hostAllowed(h) {
			t.Errorf("hostAllowed(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"evil.test", "evil.test:8725", "192.168.0.1:8725", "localhost.evil.test"} {
		if hostAllowed(h) {
			t.Errorf("hostAllowed(%q) = true, want false", h)
		}
	}
}
