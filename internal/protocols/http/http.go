// Package http implements the core.Protocol for HTTP/REST — the highest
// traffic protocol and the one every other feature (chaining, k6 script
// generation, MCP run_request) leans on first.
package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
func New(opts ...Option) *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{http: &http.Client{Timeout: 60 * time.Second, Jar: jar}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Option func(*Client)

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

func WithTransport(t http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = t }
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

	httpReq, err := http.NewRequestWithContext(ctx, resolved.Method, fullURL, bodyReader)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}
	for _, h := range resolved.Headers {
		if h.Enabled {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "http", Direction: "sent", Payload: []byte(resolved.Method + " " + fullURL)})
	}

	httpResp, err := c.http.Do(httpReq)
	timing := time.Since(start).Milliseconds()
	if err != nil {
		return model.ResponseData{
			RequestID: req.ID,
			TimingMs:  timing,
			Timestamp: start.UTC().Format(time.RFC3339),
			Error:     err.Error(),
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
		RequestID:  req.ID,
		Status:     httpResp.StatusCode,
		StatusText: httpResp.Status,
		Headers:    headers,
		BodyBase64: base64.StdEncoding.EncodeToString(bodyBytes),
		BodySize:   len(bodyBytes),
		TimingMs:   timing,
		Timestamp:  start.UTC().Format(time.RFC3339),
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "http", Direction: "received", Payload: bodyBytes})
	}

	return resp, nil
}

func buildURL(raw string, params []model.KeyValue) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for _, p := range params {
		if p.Enabled {
			q.Set(p.Key, p.Value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
