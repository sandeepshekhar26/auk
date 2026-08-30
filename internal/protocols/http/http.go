// Package http implements the core.Protocol for HTTP/REST — the highest
// traffic protocol and the one every other feature (chaining, k6 script
// generation, MCP run_request) leans on first.
package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

type Client struct {
	http *http.Client
}

// New builds an HTTP protocol client. TLS/proxy/mTLS configuration is
// injected via opts so every environment can carry its own cert pool
// (docs/03-tech-stack.md — crypto/tls is the single TLS backend for every
// protocol; this is the seam future auth/mTLS work plugs into). A cookie
// jar is attached by default so Set-Cookie responses persist across
// requests made through the same Client instance (docs/01-feature-roadmap.md
// "Cookie jar per workspace" — callers that need a workspace-scoped jar
// should build one with cookiejar.New and pass it via WithCookieJar).
//
// The transport is always wrapped in tracingTransport (below), which is
// how the request debugger's timing breakdown and redirect chain get
// populated — it runs regardless of whether a caller supplies a custom
// Transport via WithTransport, since that option only picks what tracing
// wraps, not whether tracing happens.
func New(opts ...Option) *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{http: &http.Client{Timeout: 60 * time.Second, Jar: jar}}
	for _, opt := range opts {
		opt(c)
	}
	base := c.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c.http.Transport = &tracingTransport{base: base}
	return c
}

type Option func(*Client)

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

func WithTransport(t http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = t }
}

// hop is one RoundTrip's worth of timing/outcome data — richer than
// model.RedirectHop internally so tracingTransport only needs one
// collector; Execute distills it into model.RedirectHop / TimingBreakdown
// once the whole chain is done.
type hop struct {
	method                          string
	url                             string
	status                          int
	dnsMs, connectMs, tlsMs, ttfbMs int64
	totalMs                         int64
}

// hopCollectorKey is the context key tracingTransport uses to find the
// per-Execute-call hop slice. Scoping the collector to the request's own
// context (rather than a field on the shared, concurrently-used Client/
// Transport) is what keeps concurrent requests through the same Client
// from corrupting each other's timing data.
type hopCollectorKey struct{}

func withHopCollector(ctx context.Context, hops *[]hop) context.Context {
	return context.WithValue(ctx, hopCollectorKey{}, hops)
}

// tracingTransport wraps a base RoundTripper to capture per-hop DNS/
// connect/TLS/TTFB timing via httptrace and append each hop (method, URL,
// status, duration) to the collector found on the request's context. Go's
// http.Client calls RoundTrip once per redirect hop, so this naturally
// covers the whole chain, not just the final response.
type tracingTransport struct {
	base http.RoundTripper
}

// hopTiming collects one hop's phase timings. httptrace fires its callbacks
// on WHICHEVER GOROUTINE does the work — Go dials candidate addresses
// concurrently (happy eyeballs), so DNSDone/ConnectDone/TLSHandshakeDone can
// run on background goroutines while RoundTrip's own goroutine reads the
// results afterwards. Plain locals here were a real data race (caught by
// `go test -race` with concurrent sends); the mutex makes the handoff safe.
type hopTiming struct {
	mu                               sync.Mutex
	dnsStart, connectStart, tlsStart time.Time
	dnsMs, connectMs, tlsMs, ttfbMs  int64
}

func (h *hopTiming) markStart(field *time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*field = time.Now()
}

func (h *hopTiming) markSince(start *time.Time, out *int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if start.IsZero() {
		return
	}
	*out = time.Since(*start).Milliseconds()
}

// snapshot returns the collected timings under the lock, for the reader.
func (h *hopTiming) snapshot() (dns, connect, tls, ttfb int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dnsMs, h.connectMs, h.tlsMs, h.ttfbMs
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hopStart := time.Now()
	ht := &hopTiming{}

	trace := &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { ht.markStart(&ht.dnsStart) },
		DNSDone:           func(httptrace.DNSDoneInfo) { ht.markSince(&ht.dnsStart, &ht.dnsMs) },
		ConnectStart:      func(string, string) { ht.markStart(&ht.connectStart) },
		ConnectDone:       func(string, string, error) { ht.markSince(&ht.connectStart, &ht.connectMs) },
		TLSHandshakeStart: func() { ht.markStart(&ht.tlsStart) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { ht.markSince(&ht.tlsStart, &ht.tlsMs) },
		GotFirstResponseByte: func() {
			ht.mu.Lock()
			defer ht.mu.Unlock()
			ht.ttfbMs = time.Since(hopStart).Milliseconds()
		},
	}
	tracedReq := req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := t.base.RoundTrip(tracedReq)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if hops, ok := req.Context().Value(hopCollectorKey{}).(*[]hop); ok {
		dnsMs, connectMs, tlsMs, ttfbMs := ht.snapshot()
		*hops = append(*hops, hop{
			method: req.Method, url: req.URL.String(), status: status,
			dnsMs: dnsMs, connectMs: connectMs, tlsMs: tlsMs, ttfbMs: ttfbMs,
			totalMs: time.Since(hopStart).Milliseconds(),
		})
	}

	return resp, err
}

func (c *Client) Kind() model.ProtocolKind { return model.ProtocolHTTP }

func (c *Client) Execute(ctx context.Context, sess *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	start := time.Now()

	fullURL, err := buildURL(resolved.URL, resolved.Params)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}

	var bodyReader io.Reader
	if resolved.Body != nil && resolved.Body.Kind != model.BodyNone {
		bodyReader = bytes.NewReader([]byte(resolved.Body.Text))
	}

	var hops []hop
	httpReq, err := http.NewRequestWithContext(withHopCollector(ctx, &hops), resolved.Method, fullURL, bodyReader)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}
	for _, h := range resolved.Headers {
		// An empty key is an incomplete row the user hasn't filled in yet,
		// not a real header to send — net/http rejects it outright
		// ("invalid header field name"), which surfaced as a cryptic
		// instant failure the first time a stray blank row (e.g. left
		// behind after removing a filled-in one) made it to Send.
		if h.Enabled && h.Key != "" {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "http", Direction: "sent", Payload: []byte(resolved.Method + " " + fullURL)})
	}

	var proxyURL string
	if req.ProxyURL != nil {
		proxyURL = *req.ProxyURL
	}
	httpClient, err := c.clientFor(req.TLS, proxyURL)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}
	// Digest is challenge-response, so unlike every other auth kind it can't
	// have been turned into a header by internal/auth before we got here —
	// it needs the 401 + WWW-Authenticate and a re-send. See digest.go.
	// httpReq.URL pins the origin those challenges may be answered at.
	if creds := digestCredentials(req, resolved); creds != nil {
		httpClient = clientWithDigestAuth(httpClient, *creds, httpReq.URL)
	}
	httpResp, err := httpClient.Do(httpReq)
	timing := time.Since(start).Milliseconds()
	if err != nil {
		return model.ResponseData{
			RequestID:     req.ID,
			TimingMs:      timing,
			Timestamp:     start.UTC().Format(time.RFC3339),
			Error:         err.Error(),
			RedirectChain: redirectChain(hops),
		}, err
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}

	headers := make([]model.KeyValue, 0, len(httpResp.Header))
	for k, vs := range httpResp.Header {
		for _, v := range vs {
			headers = append(headers, model.KeyValue{Key: k, Value: v, Enabled: true})
		}
	}

	resp := model.ResponseData{
		RequestID:     req.ID,
		Status:        httpResp.StatusCode,
		StatusText:    model.ReasonPhrase(httpResp.Status),
		Headers:       headers,
		BodyBase64:    base64.StdEncoding.EncodeToString(bodyBytes),
		BodySize:      len(bodyBytes),
		TimingMs:      timing,
		Timestamp:     start.UTC().Format(time.RFC3339),
		Timing:        finalHopTiming(hops),
		RedirectChain: redirectChain(hops),
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "http", Direction: "received", Payload: bodyBytes})
	}

	return resp, nil
}

// digestCredentials picks the Digest credentials to authenticate with, or nil
// when this request isn't a Digest one.
//
// resolved.Auth WINS: it is the templated copy the engine produced, so a
// password written as `${digestPassword}`, as a keychain-backed secret, or as
// a 1Password `op://` reference is the real credential by the time it reaches
// here. req.Auth is the RAW stored definition, where those are still literal
// `${...}` text that would be hashed verbatim and rejected by every server; it
// is consulted only as a fallback, for a caller that assembled a
// core.ResolvedRequest by hand and so has no resolved auth to offer.
func digestCredentials(req model.RequestDef, resolved core.ResolvedRequest) *model.DigestAuth {
	cfg := resolved.Auth
	if cfg == nil {
		cfg = req.Auth
	}
	if cfg == nil || cfg.Kind != model.AuthDigest || cfg.Digest == nil {
		return nil
	}
	return cfg.Digest
}

// finalHopTiming is the DNS/connect/TLS/TTFB breakdown for the last hop
// actually sent (the one whose body the caller reads) — nil if, somehow,
// no hop was recorded.
func finalHopTiming(hops []hop) *model.TimingBreakdown {
	if len(hops) == 0 {
		return nil
	}
	h := hops[len(hops)-1]
	return &model.TimingBreakdown{DNSMs: h.dnsMs, ConnectMs: h.connectMs, TLSMs: h.tlsMs, TTFBMs: h.ttfbMs, TotalMs: h.totalMs}
}

// redirectChain is empty unless the request actually followed one or more
// redirects — a single hop isn't a "chain", it's just the request.
func redirectChain(hops []hop) []model.RedirectHop {
	if len(hops) <= 1 {
		return nil
	}
	chain := make([]model.RedirectHop, len(hops))
	for i, h := range hops {
		chain[i] = model.RedirectHop{Method: h.method, URL: h.url, Status: h.status, TimingMs: h.totalMs}
	}
	return chain
}

func buildURL(raw string, params []model.KeyValue) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for _, p := range params {
		// See the matching check in Execute for headers: an empty key is an
		// incomplete row, not a real query param.
		if p.Enabled && p.Key != "" {
			q.Set(p.Key, p.Value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
