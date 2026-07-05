package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

func execOnce(t *testing.T, c *Client, method, url string) model.ResponseData {
	t.Helper()
	resp, err := c.Execute(context.Background(), nil, model.RequestDef{ID: "req-1"}, core.ResolvedRequest{
		Method: method,
		URL:    url,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return resp
}

func TestRedirects_FollowedByDefault(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("landed"))
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := New()
	resp := execOnce(t, c, http.MethodGet, redirector.URL)
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200 after following redirect, got %d (err=%s)", resp.Status, resp.Error)
	}
}

func TestTiming_PopulatesBreakdownAndNoChainForDirectRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New()
	resp := execOnce(t, c, http.MethodGet, srv.URL)

	if resp.Timing == nil {
		t.Fatal("expected a non-nil Timing breakdown for a direct (non-redirected) request")
	}
	if resp.Timing.TotalMs < 0 {
		t.Fatalf("expected a non-negative TotalMs, got %d", resp.Timing.TotalMs)
	}
	if resp.RedirectChain != nil {
		t.Fatalf("expected no RedirectChain for a request with no redirects, got %+v", resp.RedirectChain)
	}
}

func TestRedirects_PopulateChain(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := New()
	resp := execOnce(t, c, http.MethodGet, redirector.URL)

	if len(resp.RedirectChain) != 2 {
		t.Fatalf("expected a 2-hop redirect chain, got %d hops: %+v", len(resp.RedirectChain), resp.RedirectChain)
	}
	if resp.RedirectChain[0].URL != redirector.URL || resp.RedirectChain[0].Status != http.StatusFound {
		t.Fatalf("expected first hop to be the redirector returning 302, got %+v", resp.RedirectChain[0])
	}
	if resp.RedirectChain[1].URL != final.URL || resp.RedirectChain[1].Status != http.StatusOK {
		t.Fatalf("expected second hop to be the final destination returning 200, got %+v", resp.RedirectChain[1])
	}
	if resp.Timing == nil {
		t.Fatal("expected Timing to reflect the FINAL hop even when redirects occurred")
	}
}

func TestWithMaxRedirects_StopsAndErrors(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()

	var redirector *httptest.Server
	hops := 0
	redirector = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, redirector.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := New(WithMaxRedirects(2))
	_, err := c.Execute(context.Background(), nil, model.RequestDef{ID: "req-1"}, core.ResolvedRequest{
		Method: http.MethodGet,
		URL:    redirector.URL,
	})
	if err == nil {
		t.Fatal("expected an error once redirect cap is exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected a redirect-related error, got: %v", err)
	}
}

func TestCookieJar_PersistsAcrossRequests(t *testing.T) {
	var sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			sawCookie = c.Value
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
	}))
	defer srv.Close()

	c := New() // cookie jar is on by default
	execOnce(t, c, http.MethodGet, srv.URL)
	execOnce(t, c, http.MethodGet, srv.URL)

	if sawCookie != "abc123" {
		t.Fatalf("expected cookie set on first request to be replayed on second, got %q", sawCookie)
	}
}

func TestWithoutCookieJar_DoesNotPersist(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("session"); err == nil {
			sawCookie = true
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
	}))
	defer srv.Close()

	c := New(WithoutCookieJar())
	execOnce(t, c, http.MethodGet, srv.URL)
	execOnce(t, c, http.MethodGet, srv.URL)

	if sawCookie {
		t.Fatal("expected no cookie jar, cookie should not have been replayed")
	}
}

func TestMTLS_ClientCertRequired(t *testing.T) {
	serverCert, _, _ := generateSelfSignedCert(t, "server")
	_, clientCertPEM, clientKeyPEM := generateSelfSignedCert(t, "client")

	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCertPEM) {
		t.Fatal("failed to add client cert to pool")
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("expected a peer certificate on the server side")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mtls-ok"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
	}
	srv.StartTLS()
	defer srv.Close()

	serverCertPEM := certToPEM(t, serverCert)

	tlsCfg, err := BuildTLSConfig(serverCertPEM, clientCertPEM, clientKeyPEM, false)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}

	c := New(WithTLSConfig(tlsCfg))
	resp := execOnce(t, c, http.MethodGet, srv.URL)
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200 over mTLS, got %d (err=%s)", resp.Status, resp.Error)
	}
}

func TestMTLS_NoClientCertRejected(t *testing.T) {
	serverCert, _, _ := generateSelfSignedCert(t, "server")
	_, clientCertPEM, _ := generateSelfSignedCert(t, "client")

	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCertPEM) {
		t.Fatal("failed to add client cert to pool")
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
	}
	srv.StartTLS()
	defer srv.Close()

	serverCertPEM := certToPEM(t, serverCert)

	// No client cert supplied: server should reject the handshake.
	tlsCfg, err := BuildTLSConfig(serverCertPEM, nil, nil, false)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}

	c := New(WithTLSConfig(tlsCfg))
	_, err = c.Execute(context.Background(), nil, model.RequestDef{ID: "req-1"}, core.ResolvedRequest{
		Method: http.MethodGet,
		URL:    srv.URL,
	})
	if err == nil {
		t.Fatal("expected TLS handshake failure without a client certificate")
	}
}

func TestBuildTLSConfig_BadCAPEM(t *testing.T) {
	_, err := BuildTLSConfig([]byte("not a real cert"), nil, nil, false)
	if err == nil {
		t.Fatal("expected an error for an unparseable CA PEM")
	}
}

// TestExecute_PerRequestClientCert proves the actual wiring this feature
// depends on: a *Client built with NO TLS options at all (New(), the plain
// shared client every request normally uses) still successfully completes an
// mTLS handshake when ONE request's model.RequestDef.TLS carries a client
// cert — i.e. Execute's clientFor is what's doing the work, not something
// baked in at client-construction time (that path is already covered by
// TestMTLS_ClientCertRequired above, via WithTLSConfig).
func TestExecute_PerRequestClientCert(t *testing.T) {
	serverCert, _, _ := generateSelfSignedCert(t, "server")
	_, clientCertPEM, clientKeyPEM := generateSelfSignedCert(t, "client")

	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(clientCertPEM) {
		t.Fatal("failed to add client cert to pool")
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("expected a peer certificate on the server side")
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCertPool,
	}
	srv.StartTLS()
	defer srv.Close()

	serverCertPEM := certToPEM(t, serverCert)

	c := New() // no TLS options at construction — the plain shared client
	resp, err := c.Execute(context.Background(), nil,
		model.RequestDef{ID: "req-1", TLS: &model.RequestTLSConfig{
			ClientCertPEM: string(clientCertPEM),
			ClientKeyPEM:  string(clientKeyPEM),
			CustomCAPEM:   string(serverCertPEM),
		}},
		core.ResolvedRequest{Method: http.MethodGet, URL: srv.URL},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200 over per-request mTLS, got %d (err=%s)", resp.Status, resp.Error)
	}
}

// TestExecute_RequestsWithoutTLSConfigUnaffected guards the fast path: a
// request with req.TLS == nil (the overwhelming common case) must keep using
// c.http directly, not silently build a one-off client every time (which
// would, among other things, defeat cookie/connection reuse across the
// workspace's other requests).
func TestExecute_RequestsWithoutTLSConfigUnaffected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New()
	client, err := c.clientFor(nil, "")
	if err != nil {
		t.Fatalf("clientFor(nil, \"\"): %v", err)
	}
	if client != c.http {
		t.Fatal("expected clientFor(nil, \"\") to return the shared client, got a different instance")
	}

	client2, err := c.clientFor(&model.RequestTLSConfig{}, "")
	if err != nil {
		t.Fatalf("clientFor(empty config, \"\"): %v", err)
	}
	if client2 != c.http {
		t.Fatal("expected clientFor(all-zero-value config, \"\") to still return the shared client")
	}
}

func TestWithProxy_RoutesThroughProxyServer(t *testing.T) {
	var proxyHit bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxy.Close()

	c := New(WithProxy(proxy.URL))
	// Target URL doesn't need to resolve to anything real; http.ProxyURL
	// sends the request to the proxy regardless of the target host.
	resp := execOnce(t, c, http.MethodGet, "http://example.invalid/some/path")
	if !proxyHit {
		t.Fatal("expected request to be routed through the proxy server")
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200 via proxy, got %d (err=%s)", resp.Status, resp.Error)
	}
}

// TestExecute_PerRequestProxy proves the same "no options baked in at
// construction time" wiring TestExecute_PerRequestClientCert proves for
// mTLS, but for RequestDef.ProxyURL: a *Client built with New() (no proxy
// option at all) still routes through the proxy when ONE request's
// RequestDef carries a ProxyURL.
func TestExecute_PerRequestProxy(t *testing.T) {
	var proxyHit bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	c := New() // no proxy option at construction — the plain shared client
	proxyURL := proxy.URL
	resp, err := c.Execute(context.Background(), nil,
		model.RequestDef{ID: "req-1", ProxyURL: &proxyURL},
		core.ResolvedRequest{Method: http.MethodGet, URL: "http://example.invalid/some/path"},
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !proxyHit {
		t.Fatal("expected the request to be routed through the per-request proxy")
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200 via per-request proxy, got %d (err=%s)", resp.Status, resp.Error)
	}
}
