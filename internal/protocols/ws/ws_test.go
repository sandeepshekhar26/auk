package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// newEchoServer upgrades every connection and echoes back whatever text
// message it receives, closing on read error (client hangup/cancel).
func newEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, payload); err != nil {
				return
			}
		}
	}))
	return srv
}

type recordingSink struct {
	mu     sync.Mutex
	events []core.Event
}

func (s *recordingSink) Emit(e core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingSink) snapshot() []core.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.Event, len(s.events))
	copy(out, s.events)
	return out
}

func toWS(url string) string {
	if len(url) >= 5 && url[:5] == "http:" {
		return "ws:" + url[5:]
	}
	if len(url) >= 6 && url[:6] == "https:" {
		return "wss:" + url[6:]
	}
	return url
}

func TestExecute_SendReceiveRoundTrip(t *testing.T) {
	srv := newEchoServer(t)
	defer srv.Close()

	sink := &recordingSink{}
	sess := core.NewSession("sess-1", context.Background(), sink)

	client := New()
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolWebSocket}
	resolved := core.ResolvedRequest{
		URL:  toWS(srv.URL),
		Body: &model.RequestBody{Kind: model.BodyText, Text: "hello"},
	}

	// The server echoes forever, so bound the session with a short deadline
	// rather than waiting for the peer to close.
	ctx, cancel := context.WithTimeout(sess.Context(), 300*time.Millisecond)
	defer cancel()

	resp, err := client.Execute(ctx, sess, req, resolved)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if resp.Status != 101 {
		t.Errorf("Status = %d, want 101", resp.Status)
	}
	if resp.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", resp.RequestID)
	}

	events := sink.snapshot()
	var sawSent, sawReceived bool
	for _, e := range events {
		if e.Kind != "ws" {
			t.Errorf("unexpected event kind %q", e.Kind)
		}
		switch e.Direction {
		case "sent":
			sawSent = true
			if string(e.Payload) != "hello" {
				t.Errorf("sent payload = %q, want hello", e.Payload)
			}
		case "received":
			sawReceived = true
			if string(e.Payload) != "hello" {
				t.Errorf("received payload = %q, want hello (echo)", e.Payload)
			}
		}
	}
	if !sawSent {
		t.Error("expected a 'sent' event to be emitted")
	}
	if !sawReceived {
		t.Error("expected a 'received' event to be emitted (echo)")
	}

	var sentHeader, receivedHeader string
	for _, h := range resp.Headers {
		switch h.Key {
		case "X-WS-Frames-Sent":
			sentHeader = h.Value
		case "X-WS-Frames-Received":
			receivedHeader = h.Value
		}
	}
	if sentHeader != "1" {
		t.Errorf("X-WS-Frames-Sent = %q, want 1", sentHeader)
	}
	if receivedHeader != "1" {
		t.Errorf("X-WS-Frames-Received = %q, want 1", receivedHeader)
	}
}

func TestExecute_ContextCancelClosesPromptly(t *testing.T) {
	srv := newEchoServer(t)
	defer srv.Close()

	sink := &recordingSink{}
	sess := core.NewSession("sess-2", context.Background(), sink)

	client := New()
	req := model.RequestDef{ID: "req-2", Protocol: model.ProtocolWebSocket}
	resolved := core.ResolvedRequest{URL: toWS(srv.URL)}

	ctx, cancel := context.WithCancel(sess.Context())

	done := make(chan struct{})
	var resp model.ResponseData
	var execErr error
	go func() {
		resp, execErr = client.Execute(ctx, sess, req, resolved)
		close(done)
	}()

	// Give the dial/read loop a moment to establish, then cancel and assert
	// Execute returns promptly rather than hanging until some external timeout.
	time.Sleep(50 * time.Millisecond)
	cancelStart := time.Now()
	cancel()

	select {
	case <-done:
		elapsed := time.Since(cancelStart)
		if elapsed > 2*time.Second {
			t.Errorf("Execute took %v to return after cancel, want near-immediate", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return within 2s of context cancellation")
	}

	if execErr != nil {
		t.Fatalf("Execute returned error: %v", execErr)
	}
	if resp.Status != 101 {
		t.Errorf("Status = %d, want 101", resp.Status)
	}
}
