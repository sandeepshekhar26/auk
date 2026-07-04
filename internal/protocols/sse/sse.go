// Package sse implements the core.Protocol for Server-Sent Events: a GET
// request that upgrades to a long-lived text/event-stream body, parsed
// line-by-line per the SSE spec and re-emitted as core.Events so the GUI can
// render them as they arrive rather than waiting for the connection to close.
package sse

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

type Client struct {
	http *http.Client
}

// New builds an SSE protocol client. No response Timeout is set on the
// underlying http.Client — an SSE connection is expected to stay open
// indefinitely; the caller's ctx (Session cancellation) is the only thing
// that ends the stream, matching the semantics documented in session.go.
func New(opts ...Option) *Client {
	c := &Client{http: &http.Client{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Option func(*Client)

func WithTransport(t http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = t }
}

func (c *Client) Kind() model.ProtocolKind { return model.ProtocolSSE }

// event accumulates the fields of one SSE event as they're parsed across
// consecutive lines, per the "event stream interpretation" algorithm in the
// SSE spec (https://html.spec.whatwg.org/multipage/server-sent-events.html).
type event struct {
	id, name string
	data     []string
}

func (e event) payload() []byte {
	return []byte(strings.Join(e.data, "\n"))
}

func (e event) empty() bool {
	return len(e.data) == 0
}

func (c *Client) Execute(ctx context.Context, sess *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	start := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.URL, nil)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}
	for _, h := range resolved.Headers {
		if h.Enabled {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "sse", Direction: "sent", Payload: []byte(http.MethodGet + " " + resolved.URL)})
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return model.ResponseData{
			RequestID: req.ID,
			TimingMs:  time.Since(start).Milliseconds(),
			Timestamp: start.UTC().Format(time.RFC3339),
			Error:     err.Error(),
		}, err
	}
	defer httpResp.Body.Close()

	headers := make([]model.KeyValue, 0, len(httpResp.Header))
	for k, vs := range httpResp.Header {
		for _, v := range vs {
			headers = append(headers, model.KeyValue{Key: k, Value: v, Enabled: true})
		}
	}

	eventCount, readErr := streamEvents(ctx, sess, httpResp)

	resp := model.ResponseData{
		RequestID:  req.ID,
		Status:     httpResp.StatusCode,
		StatusText: model.ReasonPhrase(httpResp.Status),
		Headers:    headers,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d SSE event(s) received", eventCount))),
		BodySize:   eventCount,
		TimingMs:   time.Since(start).Milliseconds(),
		Timestamp:  start.UTC().Format(time.RFC3339),
	}
	if readErr != nil {
		resp.Error = readErr.Error()
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "sse", Direction: "done", Payload: []byte(strconv.Itoa(eventCount) + " event(s)")})
	}

	// A clean server-closed stream (io.EOF, surfaced as a nil error from
	// streamEvents) is a normal completion, not a failure — only propagate
	// readErr (e.g. ctx cancellation) as the function's error so RunRequest
	// still persists the summary response for the common case.
	return resp, readErr
}

// streamEvents reads httpResp.Body line by line, parsing SSE fields and
// emitting one core.Event per completed event (a blank line, per spec,
// terminates/dispatches the event being accumulated). It stops when ctx is
// cancelled, the body reaches EOF (server closed the connection), or a
// scanner error occurs. It returns the number of events dispatched.
func streamEvents(ctx context.Context, sess *core.Session, httpResp *http.Response) (int, error) {
	lines := make(chan string)
	scanErr := make(chan error, 1)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		scanErr <- scanner.Err()
	}()

	count := 0
	cur := event{}

	dispatch := func() {
		if cur.empty() {
			return
		}
		if sess != nil && sess.Sink != nil {
			sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "sse", Direction: "received", Payload: cur.payload()})
		}
		count++
		cur = event{}
	}

	for {
		select {
		case <-ctx.Done():
			return count, ctx.Err()
		case line, ok := <-lines:
			if !ok {
				dispatch()
				return count, <-scanErr
			}
			parseLine(line, &cur)
			if line == "" {
				dispatch()
			}
		}
	}
}

// parseLine applies one line of the SSE wire format to the in-progress
// event. Lines starting with ':' are comments and ignored; "field: value" or
// "field:value" set the field; a bare blank line is handled by the caller
// (it triggers dispatch, not a field update).
func parseLine(line string, cur *event) {
	if line == "" {
		return
	}
	if strings.HasPrefix(line, ":") {
		return
	}

	field, value := line, ""
	if idx := strings.Index(line, ":"); idx >= 0 {
		field = line[:idx]
		value = line[idx+1:]
		value = strings.TrimPrefix(value, " ")
	}

	switch field {
	case "event":
		cur.name = value
	case "data":
		cur.data = append(cur.data, value)
	case "id":
		cur.id = value
	case "retry":
		// Reconnection hint; not applicable to a one-shot Execute call.
	}
}
