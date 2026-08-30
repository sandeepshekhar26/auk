package mockserver

import (
	"os/exec"
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// TestEndToEndWithRealCurl is the proof that a real HTTP client — not Go's
// in-process one — sees what a frontend would see: a seeded workspace, the
// real server on a real socket, and `curl` on the other end. Skipped (not
// failed) where curl isn't installed, so it never becomes a portability
// hazard for the rest of the suite.
func TestEndToEndWithRealCurl(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not installed")
	}

	fs := newStore(t)
	seed(t, fs, "r-users", "GET", "${baseUrl}/api/users/:id", jsonResp(200,
		`{"id":42,"name":"Ada Lovelace","role":"admin"}`,
		model.KeyValue{Key: "X-RateLimit-Remaining", Value: "99", Enabled: true},
	))
	seed(t, fs, "r-create", "POST", "${baseUrl}/api/users", jsonResp(201, `{"id":43,"created":true}`))
	srv := startServer(t, fs)

	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(curl, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("curl %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// 1. A wildcard route replays the recorded body and headers, and a
	// loopback dev-server origin is reflected back so the browser will let
	// the frontend read them.
	got := run("-sS", "-i", "-H", "Origin: http://localhost:5173", srv.URL()+"/api/users/42")
	t.Logf("curl -i %s/api/users/42\n%s", srv.URL(), got)
	for _, want := range []string{
		"HTTP/1.1 200 OK",
		"X-Auk-Mock: 1",
		"X-Ratelimit-Remaining: 99",
		"Access-Control-Allow-Origin: http://localhost:5173",
		"Vary: Origin",
		`{"id":42,"name":"Ada Lovelace","role":"admin"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("replay missing %q", want)
		}
	}
	if strings.Contains(got, "Access-Control-Allow-Origin: *") {
		t.Error("replay wildcarded Allow-Origin")
	}

	// 1b. The same request from a page on the open internet gets the body over
	// the wire (curl is not a browser) but NO allow header, so a real browser
	// refuses to hand it to the calling script.
	evil := run("-sS", "-i", "-H", "Origin: https://evil.test", srv.URL()+"/api/users/42")
	t.Logf("curl -i (evil origin) %s/api/users/42\n%s", srv.URL(), evil)
	if strings.Contains(evil, "Access-Control-Allow-Origin") {
		t.Errorf("non-loopback origin got an allow header:\n%s", evil)
	}

	// 1c. A rebound DNS name pointing at 127.0.0.1 is refused outright.
	rebound := run("-sS", "-i", "-H", "Host: mock.evil.test", srv.URL()+"/api/users/42")
	t.Logf("curl -i (rebound Host) %s/api/users/42\n%s", srv.URL(), rebound)
	if !strings.Contains(rebound, "HTTP/1.1 403 Forbidden") ||
		strings.Contains(rebound, "Ada Lovelace") {
		t.Errorf("non-loopback Host was served:\n%s", rebound)
	}

	// 2. A browser preflight from a Vite dev server passes.
	pre := run("-sS", "-i", "-X", "OPTIONS",
		"-H", "Origin: http://localhost:5173",
		"-H", "Access-Control-Request-Method: POST",
		"-H", "Access-Control-Request-Headers: content-type",
		srv.URL()+"/api/users")
	t.Logf("curl -i -X OPTIONS (preflight) %s/api/users\n%s", srv.URL(), pre)
	for _, want := range []string{
		"HTTP/1.1 204 No Content",
		"Access-Control-Allow-Origin: http://localhost:5173",
		"Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS",
		"Access-Control-Allow-Headers: content-type",
	} {
		if !strings.Contains(pre, want) {
			t.Errorf("preflight missing %q", want)
		}
	}

	// 2b. The same preflight from a hostile origin must not leak an allow.
	evilPre := run("-sS", "-i", "-X", "OPTIONS",
		"-H", "Origin: https://evil.test",
		"-H", "Access-Control-Request-Method: POST",
		"-H", "Access-Control-Request-Headers: content-type",
		srv.URL()+"/api/users")
	t.Logf("curl -i -X OPTIONS (evil preflight) %s/api/users\n%s", srv.URL(), evilPre)
	if strings.Contains(evilPre, "Access-Control-Allow-") {
		t.Errorf("preflight leaked an allow to a non-loopback origin:\n%s", evilPre)
	}

	// 3. The POST the preflight cleared then replays its 201.
	post := run("-sS", "-i", "-X", "POST", "-H", "Content-Type: application/json",
		"-d", `{"name":"Grace"}`, srv.URL()+"/api/users")
	t.Logf("curl -i -X POST %s/api/users\n%s", srv.URL(), post)
	if !strings.Contains(post, "HTTP/1.1 201 Created") || !strings.Contains(post, `{"id":43,"created":true}`) {
		t.Errorf("POST replay unexpected:\n%s", post)
	}

	// 4. A path with no recording explains itself in JSON.
	miss := run("-sS", "-i", srv.URL()+"/api/nothing")
	t.Logf("curl -i %s/api/nothing\n%s", srv.URL(), miss)
	if !strings.Contains(miss, "HTTP/1.1 404 Not Found") ||
		!strings.Contains(miss, `"hint":"send the request once in AUK to record a mock"`) {
		t.Errorf("404 unexpected:\n%s", miss)
	}

	// 5. A known path under an unrecorded method 405s with Allow.
	notAllowed := run("-sS", "-i", "-X", "DELETE", srv.URL()+"/api/users/42")
	t.Logf("curl -i -X DELETE %s/api/users/42\n%s", srv.URL(), notAllowed)
	if !strings.Contains(notAllowed, "HTTP/1.1 405 Method Not Allowed") || !strings.Contains(notAllowed, "Allow: GET") {
		t.Errorf("405 unexpected:\n%s", notAllowed)
	}
}
