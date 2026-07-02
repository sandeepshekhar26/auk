package core

import (
	"context"
	"sync"
)

type ID = string

// Session is the one abstraction every protocol (HTTP, WS, gRPC, SSE, k6)
// implements: an id, a cancellation scope, and a sink to report progress to.
// Cancellation semantics are honest, not uniform — see docs/02-architecture.md
// §9 for the per-kind latency table (HTTP abort is immediate; a script
// interrupt waits for the next VM tick; k6 SIGINT waits for handleSummary).
type Session struct {
	ID     ID
	ctx    context.Context
	cancel context.CancelFunc
	Sink   EventSink
}

func NewSession(id ID, parent context.Context, sink EventSink) *Session {
	ctx, cancel := context.WithCancel(parent)
	if sink == nil {
		sink = NoopSink{}
	}
	return &Session{ID: id, ctx: ctx, cancel: cancel, Sink: sink}
}

func (s *Session) Context() context.Context { return s.ctx }
func (s *Session) Cancel()                  { s.cancel() }

// Registry tracks live sessions so a Cancel button (or an MCP "cancel"
// tool call) can find and stop an in-flight run by id.
type Registry struct {
	mu       sync.Mutex
	sessions map[ID]*Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[ID]*Session)}
}

func (r *Registry) Put(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
}

func (r *Registry) Remove(id ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

func (r *Registry) Cancel(id ID) bool {
	r.mu.Lock()
	s, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	s.Cancel()
	return true
}
