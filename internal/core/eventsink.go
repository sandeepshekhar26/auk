package core

// Event is a unit of progress/streaming data emitted by a running session
// (an HTTP response arriving, a WS frame, an SSE event, a k6 metric point).
type Event struct {
	SessionID ID
	Kind      string // "http", "ws", "sse", "grpc", "perf"
	Direction string // "sent", "received", "meta", "error", "done"
	Payload   []byte
}

// EventSink is the ONLY way engine code may emit progress to the outside
// world. GUI, CLI, and MCP adapters each provide their own implementation;
// the engine never touches a GUI-specific type (docs/02-architecture.md §6/§F —
// no context-value smuggling, no direct Wails runtime calls from core).
//
// Implementations MUST NOT block the caller indefinitely — a slow consumer
// degrades (coalesce/drop), it never stalls the session goroutine.
type EventSink interface {
	Emit(Event)
}

// NoopSink discards events; used by the CLI runner and in tests where no
// consumer is listening.
type NoopSink struct{}

func (NoopSink) Emit(Event) {}

// ChannelSink fans events into a buffered Go channel. The GUI adapter reads
// from this channel and is responsible for coalescing before it notifies the
// webview (Wails EventsEmit is a wake-up signal only, never the data pipe).
type ChannelSink struct {
	ch chan Event
}

func NewChannelSink(buffer int) *ChannelSink {
	return &ChannelSink{ch: make(chan Event, buffer)}
}

func (s *ChannelSink) Emit(e Event) {
	select {
	case s.ch <- e:
	default:
		// Buffer full: drop rather than block the session goroutine. Callers
		// that need "no lossy ring buffer" semantics (WS/SSE debugging) must
		// also persist the full stream via storage; this channel is only the
		// live-UI notification path.
	}
}

func (s *ChannelSink) C() <-chan Event {
	return s.ch
}
