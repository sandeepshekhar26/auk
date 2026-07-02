package sse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// recordingSink collects emitted events in order, safe for concurrent use
// even though Execute only emits from one goroutine.
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

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventName, data string) {
	if eventName != "" {
		fmt.Fprintf(w, "event: %s\n", eventName)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func TestExecute_ParsesEventSequence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("expected Accept: text/event-stream, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		writeSSEEvent(w, flusher, "message", "hello")
		time.Sleep(10 * time.Millisecond)
		writeSSEEvent(w, flusher, "message", "world")
		time.Sleep(10 * time.Millisecond)
		// Multi-line data field: two "data:" lines join with "\n" per spec.
		fmt.Fprintf(w, "id: 3\n")
		fmt.Fprintf(w, "data: line1\n")
		fmt.Fprintf(w, "data: line2\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := New()
	sink := &recordingSink{}
	sess := core.NewSession("test-session", context.Background(), sink)

	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolSSE, Method: "GET"}
	resolved := core.ResolvedRequest{URL: srv.URL, Method: "GET"}

	resp, err := client.Execute(sess.Context(), sess, req, resolved)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
	if resp.BodySize != 3 {
		t.Errorf("expected 3 events counted, got %d", resp.BodySize)
	}

	events := sink.snapshot()

	var received []core.Event
	for _, e := range events {
		if e.Direction == "received" {
			received = append(received, e)
		}
	}
	if len(received) != 3 {
		t.Fatalf("expected 3 received events, got %d: %+v", len(received), received)
	}
	if string(received[0].Payload) != "hello" {
		t.Errorf("event[0] payload = %q, want %q", received[0].Payload, "hello")
	}
	if string(received[1].Payload) != "world" {
		t.Errorf("event[1] payload = %q, want %q", received[1].Payload, "world")
	}
	if string(received[2].Payload) != "line1\nline2" {
		t.Errorf("event[2] payload = %q, want %q", received[2].Payload, "line1\nline2")
	}
	for _, e := range received {
		if e.Kind != "sse" {
			t.Errorf("event.Kind = %q, want %q", e.Kind, "sse")
		}
		if e.SessionID != sess.ID {
			t.Errorf("event.SessionID = %q, want %q", e.SessionID, sess.ID)
		}
	}

	var sawSent, sawDone bool
	for _, e := range events {
		if e.Direction == "sent" {
			sawSent = true
		}
		if e.Direction == "done" {
			sawDone = true
		}
	}
	if !sawSent {
		t.Error("expected a 'sent' event before streaming")
	}
	if !sawDone {
		t.Error("expected a 'done' event after streaming completes")
	}
}

func TestExecute_ContextCancellationStopsPromptly(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		close(started)

		// Emit a slow, effectively-unbounded stream so the test can prove
		// cancellation ends the read loop rather than the server closing it.
		for i := 0; i < 1000; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			writeSSEEvent(w, flusher, "tick", fmt.Sprintf("%d", i))
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	client := New()
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	sess := core.NewSession("cancel-session", ctx, sink)

	req := model.RequestDef{ID: "req-2", Protocol: model.ProtocolSSE, Method: "GET"}
	resolved := core.ResolvedRequest{URL: srv.URL, Method: "GET"}

	done := make(chan struct {
		resp model.ResponseData
		err  error
	})
	go func() {
		resp, err := client.Execute(sess.Context(), sess, req, resolved)
		done <- struct {
			resp model.ResponseData
			err  error
		}{resp, err}
	}()

	<-started
	time.Sleep(50 * time.Millisecond) // let a couple of events flow first
	cancelStart := time.Now()
	cancel()

	select {
	case result := <-done:
		elapsed := time.Since(cancelStart)
		if elapsed > 2*time.Second {
			t.Errorf("Execute took %v to return after cancellation, want prompt return", elapsed)
		}
		if result.err == nil {
			t.Error("expected a context-cancellation error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return within 5s of context cancellation")
	}

	events := sink.snapshot()
	var receivedBeforeCancel int
	for _, e := range events {
		if e.Direction == "received" {
			receivedBeforeCancel++
		}
	}
	if receivedBeforeCancel == 0 {
		t.Error("expected at least one event to have been received before cancellation")
	}
}
