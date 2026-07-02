// Package ws implements the core.Protocol for WebSocket — a bidirectional
// session rather than a single request/response, so Execute dials once,
// optionally sends a seed message from the request body, then streams every
// frame in both directions as core.Events until the context is cancelled or
// the peer closes (docs/02-architecture.md §8 — "All frames persisted").
package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

type Client struct {
	http *http.Client
}

// New builds a WebSocket protocol client. The handshake is a plain HTTP
// upgrade, so it reuses the same *http.Client (and therefore the same TLS
// config for mTLS parity) other protocols are built on.
func New(opts ...Option) *Client {
	c := &Client{http: &http.Client{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Option func(*Client)

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

func (c *Client) Kind() model.ProtocolKind { return model.ProtocolWebSocket }

// Execute implements core.Protocol. It dials resolved.URL, sends one
// outbound message if resolved.Body is non-nil, then reads incoming frames
// in a loop — emitting a core.Event for every frame in both directions —
// until ctx is cancelled or the connection closes. Cancellation is honest:
// closing the underlying net.Conn on ctx.Done() unblocks any in-flight Read
// near-immediately, unlike script/k6 cancellation (docs/02-architecture.md §9).
func (c *Client) Execute(ctx context.Context, sess *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	start := time.Now()

	header := make(http.Header)
	for _, h := range resolved.Headers {
		if h.Enabled {
			header.Add(h.Key, h.Value)
		}
	}

	conn, _, err := websocket.Dial(ctx, resolved.URL, &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: header,
	})
	if err != nil {
		return model.ResponseData{
			RequestID: req.ID,
			TimingMs:  time.Since(start).Milliseconds(),
			Timestamp: start.UTC().Format(time.RFC3339),
			Error:     fmt.Errorf("dial: %w", err).Error(),
		}, err
	}

	sent, received, closeErr := runSession(ctx, sess, conn, resolved)

	status, statusText := summarizeClose(closeErr)
	resp := model.ResponseData{
		RequestID:  req.ID,
		Status:     status,
		StatusText: statusText,
		Headers: []model.KeyValue{
			{Key: "X-WS-Frames-Sent", Value: fmt.Sprintf("%d", sent), Enabled: true},
			{Key: "X-WS-Frames-Received", Value: fmt.Sprintf("%d", received), Enabled: true},
		},
		TimingMs:  time.Since(start).Milliseconds(),
		Timestamp: start.UTC().Format(time.RFC3339),
	}
	if closeErr != nil && !isNormalClose(closeErr) && !errors.Is(closeErr, context.Canceled) {
		resp.Error = closeErr.Error()
	}
	return resp, nil
}

// runSession owns the connection for its lifetime: it optionally writes one
// seed message, then alternates reading frames until ctx is done or the
// connection closes, emitting a core.Event for every frame observed in
// either direction. It always returns with the connection closed.
func runSession(ctx context.Context, sess *core.Session, conn *websocket.Conn, resolved core.ResolvedRequest) (sent, received int, closeErr error) {
	defer conn.CloseNow()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "cancelled")
			conn.CloseNow()
		case <-done:
		}
	}()
	defer close(done)

	if resolved.Body != nil {
		if err := conn.Write(ctx, websocket.MessageText, []byte(resolved.Body.Text)); err != nil {
			return sent, received, fmt.Errorf("send: %w", err)
		}
		sent++
		emit(sess, "sent", []byte(resolved.Body.Text))
	}

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return sent, received, ctx.Err()
			}
			return sent, received, err
		}
		received++
		emit(sess, "received", payload)
	}
}

func emit(sess *core.Session, direction string, payload []byte) {
	if sess == nil || sess.Sink == nil {
		return
	}
	sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "ws", Direction: direction, Payload: payload})
}

func isNormalClose(err error) bool {
	code := websocket.CloseStatus(err)
	return code == websocket.StatusNormalClosure || code == websocket.StatusGoingAway
}

// summarizeClose maps how the session ended to the "101 Switching Protocols"
// convention this Protocol reports through, since ResponseData has no
// WS-native status concept.
func summarizeClose(err error) (int, string) {
	switch {
	case err == nil:
		return 101, "Switching Protocols"
	case errors.Is(err, context.Canceled):
		return 101, "Switching Protocols (cancelled)"
	case isNormalClose(err):
		return 101, "Switching Protocols (closed)"
	default:
		return 101, "Switching Protocols (error: " + err.Error() + ")"
	}
}
