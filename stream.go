package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// streamBufferCap bounds how many frames a session retains for the pull-based
// drain. A firehose stream that outruns the UI drops its OLDEST frames (the
// live console shows recent activity); durable full-stream capture is a
// separate future concern (docs/02-architecture.md §6).
const streamBufferCap = 2000

// A streamSession is one live streaming connection (WebSocket or SSE) started
// from the GUI. Unlike a request/response Send, these stay open and buffer
// frames for the frontend to pull. WebSocket sessions are interactive
// (SendStreamMessage writes a frame); SSE sessions are receive-only.
//
// Frames are NOT pushed through Wails EventsEmit — per the streaming contract
// in frontend/src/lib/wails.ts, EventsEmit is a lossy wake-up signal only, so
// each new frame just emits "stream:<id>" (no payload) and the frontend pulls
// the authoritative, ordered batch via DrainStream.
type streamSession struct {
	id      string
	kind    string // "ws" | "sse"
	ctx     context.Context
	cancel  context.CancelFunc
	ws      *websocket.Conn // non-nil only for WebSocket
	writeMu sync.Mutex      // serializes concurrent SendStreamMessage writes on ws

	mu      sync.Mutex
	frames  []model.StreamEvent
	dropped int // frames evicted from the front of the ring (for cursor math)
	closed  bool
}

// wailsStreamSink adapts core.Events emitted by an engine streaming protocol
// (the SSE handler) into a session's frame buffer, so the SSE path and the
// interactive WebSocket path feed the frontend through one uniform mechanism.
type wailsStreamSink struct {
	app  *App
	sess *streamSession
}

func (s wailsStreamSink) Emit(e core.Event) {
	s.app.pushFrame(s.sess, e.Kind, e.Direction, e.Payload)
}

// pushFrame appends one frame to the session ring and emits a wake-up. Payloads
// that aren't valid UTF-8 become a short placeholder rather than corrupting the
// JSON frame (a debug console is text-first; binary frames are rare here).
func (a *App) pushFrame(sess *streamSession, kind, direction string, payload []byte) {
	text := string(payload)
	if !utf8.Valid(payload) {
		text = fmt.Sprintf("<%d bytes of binary data>", len(payload))
	}
	ev := model.StreamEvent{
		SessionID: sess.id,
		Kind:      kind,
		Direction: direction,
		Payload:   text,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	sess.mu.Lock()
	sess.frames = append(sess.frames, ev)
	if over := len(sess.frames) - streamBufferCap; over > 0 {
		sess.frames = sess.frames[over:]
		sess.dropped += over
	}
	sess.mu.Unlock()
	wailsruntime.EventsEmit(a.ctx, "stream:"+sess.id) // wake-up only, no payload
}

// markClosed flags a session done and wakes the frontend for its final drain.
// The session is retained (not unregistered) so those last frames + the closed
// flag are still drainable; StopStream / shutdown do the actual removal.
func (a *App) markClosed(sess *streamSession) {
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	wailsruntime.EventsEmit(a.ctx, "stream:"+sess.id)
}

// StartStream opens a streaming session for a WebSocket or SSE request and
// returns its session id. The frontend subscribes to "stream:<id>" wake-ups and
// pulls frames via DrainStream. Non-streaming protocols are rejected — callers
// use SendRequest for those. The request is resolved through the same
// template + auth + policy path as a normal send (origin "gui").
func (a *App) StartStream(requestID string, environmentID string) (string, error) {
	req, resolved, err := a.engine.ResolveForExecution(a.ctx, requestID, environmentID, "gui")
	if err != nil {
		return "", err
	}

	sessionID := uuid.NewString()
	ctx, cancel := context.WithCancel(a.ctx)

	switch req.Protocol {
	case model.ProtocolWebSocket:
		if err := a.startWebSocket(ctx, cancel, sessionID, resolved); err != nil {
			cancel()
			return "", err
		}
	case model.ProtocolSSE:
		a.startSSE(ctx, cancel, sessionID, req, resolved)
	default:
		cancel()
		return "", fmt.Errorf("protocol %q is not a streaming protocol (use Send)", req.Protocol)
	}
	return sessionID, nil
}

// startWebSocket dials the resolved URL, registers the session, sends an
// optional seed frame from the request body, and starts the receive loop. It
// returns as soon as the connection is established so the binding is
// non-blocking; the session then lives in its read goroutine.
func (a *App) startWebSocket(ctx context.Context, cancel context.CancelFunc, sessionID string, resolved core.ResolvedRequest) error {
	header := make(http.Header)
	for _, h := range resolved.Headers {
		if h.Enabled {
			header.Add(h.Key, h.Value)
		}
	}

	conn, _, err := websocket.Dial(ctx, resolved.URL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(1 << 20) // 1 MiB — generous for a debug console

	sess := &streamSession{id: sessionID, kind: "ws", ctx: ctx, cancel: cancel, ws: conn}
	a.streamMu.Lock()
	a.streamSessions[sessionID] = sess
	a.streamMu.Unlock()

	a.pushFrame(sess, "ws", "meta", []byte("connected to "+resolved.URL))

	if resolved.Body != nil && resolved.Body.Text != "" {
		if err := conn.Write(ctx, websocket.MessageText, []byte(resolved.Body.Text)); err == nil {
			a.pushFrame(sess, "ws", "sent", []byte(resolved.Body.Text))
		}
	}

	go a.readWebSocket(sess)
	return nil
}

func (a *App) readWebSocket(sess *streamSession) {
	defer func() {
		sess.ws.CloseNow()
		a.markClosed(sess)
	}()
	for {
		_, data, err := sess.ws.Read(sess.ctx)
		if err != nil {
			if sess.ctx.Err() != nil {
				a.pushFrame(sess, "ws", "meta", []byte("disconnected"))
			} else {
				a.pushFrame(sess, "ws", "meta", []byte("closed: "+err.Error()))
			}
			return
		}
		a.pushFrame(sess, "ws", "received", data)
	}
}

// startSSE runs the engine's SSE protocol handler in a goroutine, feeding its
// core.Events into the session buffer via a wailsStreamSink. The handler blocks
// until the stream ends or ctx is cancelled (StopStream), so it runs in its own
// goroutine and the binding returns immediately.
func (a *App) startSSE(ctx context.Context, cancel context.CancelFunc, sessionID string, req model.RequestDef, resolved core.ResolvedRequest) {
	sess := &streamSession{id: sessionID, kind: "sse", ctx: ctx, cancel: cancel}
	a.streamMu.Lock()
	a.streamSessions[sessionID] = sess
	a.streamMu.Unlock()

	proto, ok := a.engine.Protocols[model.ProtocolSSE]
	if !ok {
		a.pushFrame(sess, "sse", "meta", []byte("SSE protocol not registered"))
		a.markClosed(sess)
		cancel()
		return
	}

	a.pushFrame(sess, "sse", "meta", []byte("connecting to "+resolved.URL))
	coreSession := core.NewSession(sessionID, ctx, wailsStreamSink{app: a, sess: sess})
	go func() {
		defer func() {
			a.markClosed(sess)
			cancel()
		}()
		resp, err := proto.Execute(ctx, coreSession, req, resolved)
		if err != nil {
			a.pushFrame(sess, "sse", "meta", []byte("closed: "+err.Error()))
			return
		}
		a.pushFrame(sess, "sse", "meta", []byte(fmt.Sprintf("stream ended (%d event(s))", resp.BodySize)))
	}()
}

// DrainStream returns the frames a session has buffered since `cursor`, the
// cursor to pass next time, and whether the session has closed. A missing
// session (already cleaned up) reports Closed so the frontend stops polling.
func (a *App) DrainStream(sessionID string, cursor int) model.StreamDrain {
	a.streamMu.Lock()
	sess := a.streamSessions[sessionID]
	a.streamMu.Unlock()
	if sess == nil {
		return model.StreamDrain{Cursor: cursor, Closed: true}
	}
	sess.mu.Lock()
	start := cursor - sess.dropped
	if start < 0 {
		start = 0
	}
	if start > len(sess.frames) {
		start = len(sess.frames)
	}
	out := make([]model.StreamEvent, len(sess.frames)-start)
	copy(out, sess.frames[start:])
	nextCursor := sess.dropped + len(sess.frames)
	closed := sess.closed
	sess.mu.Unlock()

	if closed {
		// Final drain: the consumer now holds every remaining frame (a closed
		// session buffers nothing more), so release the retained session. This
		// is the single cleanup point — StopStream only cancels — so the last
		// "disconnected"/"closed" frame is never lost, whether the peer closed
		// or the user hit Disconnect.
		a.streamMu.Lock()
		delete(a.streamSessions, sessionID)
		a.streamMu.Unlock()
	}
	return model.StreamDrain{Frames: out, Cursor: nextCursor, Closed: closed}
}

// SendStreamMessage writes a text frame on an open WebSocket session.
func (a *App) SendStreamMessage(sessionID string, text string) error {
	a.streamMu.Lock()
	sess := a.streamSessions[sessionID]
	a.streamMu.Unlock()
	if sess == nil || sess.ws == nil {
		return fmt.Errorf("no open websocket session %q", sessionID)
	}
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	if err := sess.ws.Write(sess.ctx, websocket.MessageText, []byte(text)); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	a.pushFrame(sess, "ws", "sent", []byte(text))
	return nil
}

// StopStream cancels a live session (Disconnect). It only cancels — the session
// stays registered so its read goroutine can still push a final "disconnected"
// frame and mark closed; DrainStream then releases it on the last drain.
// Idempotent: safe on an already-closed/absent session.
func (a *App) StopStream(sessionID string) {
	a.streamMu.Lock()
	sess := a.streamSessions[sessionID]
	a.streamMu.Unlock()
	if sess != nil {
		sess.cancel()
	}
}

// stopAllStreams cancels every live streaming session — called on app shutdown
// so a WebSocket/SSE connection can't outlive the window.
func (a *App) stopAllStreams() {
	a.streamMu.Lock()
	sessions := make([]*streamSession, 0, len(a.streamSessions))
	for _, s := range a.streamSessions {
		sessions = append(sessions, s)
	}
	a.streamSessions = map[string]*streamSession{}
	a.streamMu.Unlock()
	for _, s := range sessions {
		s.cancel()
	}
}
