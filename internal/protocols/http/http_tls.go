package http

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"

	"apitool/internal/core/model"
)

// BuildTLSConfig assembles a *tls.Config from PEM-encoded material stored
// alongside a workspace/environment: an optional custom CA pool (for
// self-signed/internal endpoints), an optional client cert+key pair (mTLS),
// and the explicit "disable verification" escape hatch — which must stay
// loud and opt-in per docs/01-feature-roadmap.md §2.5, never a silent
// default.
func BuildTLSConfig(customCAPEM []byte, clientCertPEM, clientKeyPEM []byte, insecureSkipVerify bool) (*tls.Config, error) {
	cfg := &tls.Config{InsecureSkipVerify: insecureSkipVerify}

	if len(customCAPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(customCAPEM) {
			return nil, fmt.Errorf("build tls config: custom CA PEM contained no usable certificates")
		}
		cfg.RootCAs = pool
	}

	if len(clientCertPEM) > 0 || len(clientKeyPEM) > 0 {
		cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("build tls config: load client key pair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

// WithTLSConfig installs a TLS config (custom CA / client certs for mTLS /
// InsecureSkipVerify) onto the client's transport, building a fresh
// *http.Transport if one hasn't been set via WithTransport yet.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(c *Client) {
		transportOf(c).TLSClientConfig = cfg
	}
}

// WithProxy routes outbound requests through proxyURL (e.g.
// "http://user:pass@proxyhost:8080"), the manual-proxy case from
// docs/01-feature-roadmap.md §2.6. System/env-based proxying is already
// Go's default (http.ProxyFromEnvironment) when no transport is set.
func WithProxy(proxyURL string) Option {
	return func(c *Client) {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return
		}
		transportOf(c).Proxy = http.ProxyURL(parsed)
	}
}

// WithCookieJar attaches a cookie jar to the client so Set-Cookie responses
// persist and are replayed on subsequent requests through this Client
// instance. New() calls this by default; pass a workspace-scoped jar via
// this option to override it (a per-workspace jar is wired at the app.go
// layer, not here).
func WithCookieJar(jar http.CookieJar) Option {
	return func(c *Client) { c.http.Jar = jar }
}

// WithoutCookieJar disables cookie persistence, restoring Go's normal
// no-jar behavior for callers that need per-request isolation.
func WithoutCookieJar() Option {
	return func(c *Client) { c.http.Jar = nil }
}

// WithMaxRedirects caps the number of redirects the client will follow
// before erroring out, surfacing the "redirect warning" case from
// docs/01-feature-roadmap.md §"Redirect warnings". n <= 0 restores Go's
// default CheckRedirect behavior (follow up to 10 redirects).
func WithMaxRedirects(n int) Option {
	return func(c *Client) {
		if n <= 0 {
			c.http.CheckRedirect = nil
			return
		}
		c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= n {
				return fmt.Errorf("stopped after %d redirects", n)
			}
			return nil
		}
	}
}

// clientFor returns the Client's normal shared *http.Client (the fast path —
// every request without per-request TLS settings, which is nearly all of
// them, keeps using the one long-lived client and its cookie jar), or, when
// cfg specifies a client cert / custom CA / skip-verify, a fresh one-off
// *http.Client built just for this call via New(WithTLSConfig(...)).
//
// A one-off client is necessary rather than mutating c.http in place because
// c.http is shared across every request the app sends for the lifetime of
// the process (see New's doc comment) — different requests can need
// different (or no) client certificates, so TLS config can't be a
// process-wide setting the way the cookie jar currently is. The one-off
// client still shares c.http's cookie jar, so switching a single request to
// a custom cert doesn't fragment its cookie continuity with the rest of the
// workspace.
func (c *Client) clientFor(cfg *model.RequestTLSConfig) (*http.Client, error) {
	if cfg == nil || (cfg.ClientCertPEM == "" && cfg.ClientKeyPEM == "" && cfg.CustomCAPEM == "" && !cfg.InsecureSkipVerify) {
		return c.http, nil
	}
	tlsCfg, err := BuildTLSConfig([]byte(cfg.CustomCAPEM), []byte(cfg.ClientCertPEM), []byte(cfg.ClientKeyPEM), cfg.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	return New(WithTLSConfig(tlsCfg), WithCookieJar(c.http.Jar)).http, nil
}

// transportOf returns the client's *http.Transport, creating one from a
// clone of http.DefaultTransport if the current RoundTripper isn't already
// an *http.Transport (e.g. still nil, or replaced with a custom
// RoundTripper by WithTransport — in which case TLS/proxy options are
// no-ops on that custom transport and the caller owns those concerns).
func transportOf(c *Client) *http.Transport {
	if t, ok := c.http.Transport.(*http.Transport); ok && t != nil {
		return t
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	c.http.Transport = t
	return t
}
