// Package grpc implements core.Protocol for gRPC unary and server-streaming
// calls resolved entirely via server reflection — no compiled .proto stubs
// required, the same "dynamic" story grpcurl and k6 rely on
// (docs/02-architecture.md §"gRPC dynamic").
//
// Scope for v1 (docs/01-feature-roadmap.md 2.9/2.12): unary and
// server-streaming calls. Client-streaming and bidi are explicitly
// deferred — Execute returns a clear error for those so callers get an
// honest message instead of a silent hang. .proto file import is also
// deferred (reflection is the only discovery mechanism today).
//
// Request convention: the target method is addressed via a resolved header
// "x-grpc-method" with the value "package.Service/Method" (the same slash
// form grpcurl and grpc-go's method strings use). This is chosen over
// packing the method into resolved.Body.Text so Body.Text can stay pure
// JSON — it is parsed directly as the unary request message via the
// method's input type discovered through reflection. Headers other than
// "x-grpc-method" are forwarded as gRPC request metadata.
//
// Message marshaling note: grpcdynamic.Stub (the invocation helper this
// package uses per docs/01-feature-roadmap.md 2.12) is built around
// jhump/protoreflect's own dynamic.Message rather than
// google.golang.org/protobuf/types/dynamicpb — its InvokeRpc always
// allocates the response via its internal dynamic.MessageFactory, and that
// concrete type does not cleanly bridge to dynamicpb/protojson (the v1/v2
// proto shims dynamic.Message relies on assume a registered legacy
// FileDescriptor, which a purely-reflected type never has). So request and
// response messages here are dynamic.Message, and JSON conversion goes
// through its MarshalJSONPB/UnmarshalJSONPB (github.com/golang/protobuf/jsonpb
// under the hood) — the same JSON<->protobuf mapping rules as protojson,
// just reached via the API this call path actually supports.
package grpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golang/protobuf/jsonpb"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// MethodHeader is the resolved-header key that carries the fully-qualified
// "package.Service/Method" target for a gRPC request (see package doc).
const MethodHeader = "x-grpc-method"

type Client struct {
	insecureTransport bool
}

type Option func(*Client)

// WithInsecure forces a plaintext (h2c) dial regardless of the URL scheme —
// useful for local dev servers that don't terminate TLS. Default is TLS
// unless the resolved URL explicitly uses the "grpc+insecure"/"http" scheme
// (see dialTarget).
func WithInsecure() Option {
	return func(c *Client) { c.insecureTransport = true }
}

func New(opts ...Option) *Client {
	c := &Client{}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) Kind() model.ProtocolKind { return model.ProtocolGRPC }

func (c *Client) Execute(ctx context.Context, sess *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (resp model.ResponseData, execErr error) {
	start := time.Now()

	// Bound DISCOVERY (dial + reflection) so an unreachable server can't hang
	// the UI's "Sending…" state forever, regardless of whether the resolved
	// method turns out to be unary or server-streaming — grpc.NewClient is
	// lazy, so this is also what actually triggers the connect. A
	// server-streaming call's RECEIVE loop deliberately does NOT inherit this
	// deadline once discovery succeeds (see the branch below): the whole
	// point of a stream is staying open past 30 seconds, same reasoning as
	// internal/protocols/sse.Client's client having no response Timeout.
	discoveryCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		discoveryCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	// Reflection/descriptor code paths in jhump/protoreflect can panic on
	// malformed or legacy descriptors from real-world servers (docs/04-
	// architecture-critique.md §E). Degrade to a clean error, never crash
	// the session goroutine.
	defer func() {
		if r := recover(); r != nil {
			execErr = errFromPanic(r)
			resp = model.ResponseData{
				RequestID: req.ID,
				TimingMs:  time.Since(start).Milliseconds(),
				Timestamp: start.UTC().Format(time.RFC3339),
				Error:     execErr.Error(),
			}
		}
	}()

	fullMethod, metaHeaders, err := extractMethod(resolved.Headers)
	if err != nil {
		return errResponse(req.ID, start, err), err
	}
	serviceName, methodName, err := splitMethod(fullMethod)
	if err != nil {
		return errResponse(req.ID, start, err), err
	}

	target, dialOpts := dialTarget(resolved.URL, c.insecureTransport)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		err = fmt.Errorf("dial %s: %w", target, err)
		return errResponse(req.ID, start, err), err
	}
	defer conn.Close()

	refClient := grpcreflect.NewClientAuto(discoveryCtx, conn)
	defer refClient.Reset()

	svcDesc, err := refClient.ResolveService(serviceName)
	if err != nil {
		err = fmt.Errorf("resolve service %q via reflection: %w", serviceName, err)
		return errResponse(req.ID, start, err), err
	}
	methodDesc := svcDesc.FindMethodByName(methodName)
	if methodDesc == nil {
		err = fmt.Errorf("method %q not found on service %q", methodName, serviceName)
		return errResponse(req.ID, start, err), err
	}
	if methodDesc.IsClientStreaming() {
		kind := "client-streaming"
		if methodDesc.IsServerStreaming() {
			kind = "bidi-streaming"
		}
		err = fmt.Errorf("grpc: %s is a %s method — client-streaming/bidi gRPC isn't supported yet; only unary and server-streaming are", fullMethod, kind)
		return errResponse(req.ID, start, err), err
	}

	reqMsg, err := buildRequestMessage(methodDesc, resolved.Body)
	if err != nil {
		return errResponse(req.ID, start, err), err
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "sent", Payload: []byte(fullMethod)})
	}

	stub := grpcdynamic.NewStub(conn)

	if methodDesc.IsServerStreaming() {
		// Deliberately derived from the ORIGINAL, unbounded ctx (not
		// discoveryCtx) — see the comment on discoveryCtx above.
		streamCtx := ctx
		if len(metaHeaders) > 0 {
			streamCtx = metadata.NewOutgoingContext(streamCtx, metadata.New(metaHeaders))
		}
		return c.executeServerStream(streamCtx, stub, methodDesc, reqMsg, sess, req, start)
	}

	callCtx := discoveryCtx
	if len(metaHeaders) > 0 {
		callCtx = metadata.NewOutgoingContext(callCtx, metadata.New(metaHeaders))
	}

	respMsgV1, callErr := stub.InvokeRpc(callCtx, methodDesc, reqMsg)
	timing := time.Since(start).Milliseconds()

	if callErr != nil {
		st := status.Convert(callErr)
		resp = model.ResponseData{
			RequestID:  req.ID,
			Status:     int(st.Code()),
			StatusText: st.Code().String(),
			TimingMs:   timing,
			Timestamp:  start.UTC().Format(time.RFC3339),
			Error:      st.Message(),
		}
		return resp, callErr
	}

	respMsg, ok := respMsgV1.(*dynamic.Message)
	if !ok {
		err = fmt.Errorf("grpc: unexpected response message type %T", respMsgV1)
		return errResponse(req.ID, start, err), err
	}
	bodyJSON, err := respMsg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true, EmitDefaults: true})
	if err != nil {
		err = fmt.Errorf("marshal response to JSON: %w", err)
		return errResponse(req.ID, start, err), err
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "received", Payload: bodyJSON})
	}

	resp = model.ResponseData{
		RequestID:  req.ID,
		Status:     0,
		StatusText: "OK",
		BodyBase64: base64.StdEncoding.EncodeToString(bodyJSON),
		BodySize:   len(bodyJSON),
		TimingMs:   timing,
		Timestamp:  start.UTC().Format(time.RFC3339),
	}
	return resp, nil
}

// executeServerStream runs a server-streaming RPC to completion, emitting
// one "received" event per response message via receiveServerStream
// (mirroring internal/protocols/sse's streamEvents loop) rather than the
// single request/response pair a unary call produces. Blocks until the
// stream ends (EOF), streamCtx is cancelled (session Disconnect — see
// internal/core.Session and stream.go's StopStream), or the server returns
// an error — same "Execute blocks for the life of the stream" contract sse
// already relies on for its own caller in stream.go.
func (c *Client) executeServerStream(streamCtx context.Context, stub grpcdynamic.Stub, methodDesc *desc.MethodDescriptor, reqMsg *dynamic.Message, sess *core.Session, req model.RequestDef, start time.Time) (model.ResponseData, error) {
	stream, err := stub.InvokeRpcServerStream(streamCtx, methodDesc, reqMsg)
	if err != nil {
		st := status.Convert(err)
		return model.ResponseData{
			RequestID:  req.ID,
			Status:     int(st.Code()),
			StatusText: st.Code().String(),
			TimingMs:   time.Since(start).Milliseconds(),
			Timestamp:  start.UTC().Format(time.RFC3339),
			Error:      st.Message(),
		}, err
	}

	count, streamErr := receiveServerStream(streamCtx, sess, stream)
	timing := time.Since(start).Milliseconds()

	resp := model.ResponseData{
		RequestID:  req.ID,
		Status:     0,
		StatusText: "OK",
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d message(s) received", count))),
		BodySize:   count,
		TimingMs:   timing,
		Timestamp:  start.UTC().Format(time.RFC3339),
	}
	if streamErr != nil {
		resp.Error = streamErr.Error()
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "done", Payload: []byte(fmt.Sprintf("%d message(s)", count))})
	}

	// A clean server-closed stream (io.EOF, surfaced as a nil error from
	// receiveServerStream) is normal completion, not a failure — only
	// propagate streamErr as Execute's error so RunRequest still persists
	// the summary response for the common case, matching sse.Execute's
	// identical reasoning for its own readErr.
	return resp, streamErr
}

// receiveServerStream reads stream.RecvMsg() in a loop, emitting one
// core.Event per message, until the stream ends (io.EOF), streamCtx is
// cancelled, or a transport-level error occurs. It returns the number of
// messages successfully emitted.
func receiveServerStream(streamCtx context.Context, sess *core.Session, stream *grpcdynamic.ServerStream) (int, error) {
	count := 0
	for {
		respMsgV1, recvErr := stream.RecvMsg()
		if recvErr != nil {
			if recvErr == io.EOF {
				return count, nil
			}
			// RecvMsg blocks on network I/O, so it can't observe
			// streamCtx.Done() directly the way sse's select-based loop
			// does — but grpc-go's own ClientStream aborts a pending
			// RecvMsg as soon as its context is cancelled, surfacing that
			// as a non-EOF error here. Reporting ctx.Err() in that case
			// (rather than grpc's wrapped status text) keeps the
			// disconnect-vs-real-error distinction the same shape sse's
			// loop already returns.
			if ctxErr := streamCtx.Err(); ctxErr != nil {
				return count, ctxErr
			}
			return count, recvErr
		}

		if emitErr := emitServerStreamMessage(sess, respMsgV1); emitErr != nil {
			if sess != nil && sess.Sink != nil {
				sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "error", Payload: []byte(emitErr.Error())})
			}
			continue
		}
		count++
	}
}

// emitServerStreamMessage marshals one server-stream response message to
// JSON and emits it as a "received" event. Isolated in its own recover
// (jhump/protoreflect's dynamic.Message can panic on legacy/malformed
// descriptors, per Execute's own top-level recover and docs/04-architecture-
// critique.md §E) so ONE bad message in a long-running stream degrades to
// an "error" event for that message only, rather than aborting every
// message still to come the way Execute's outer recover would.
func emitServerStreamMessage(sess *core.Session, respMsgV1 any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errFromPanic(r)
		}
	}()
	respMsg, ok := respMsgV1.(*dynamic.Message)
	if !ok {
		return fmt.Errorf("grpc: unexpected response message type %T", respMsgV1)
	}
	bodyJSON, marshalErr := respMsg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true, EmitDefaults: true})
	if marshalErr != nil {
		return fmt.Errorf("marshal response to JSON: %w", marshalErr)
	}
	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "received", Payload: bodyJSON})
	}
	return nil
}

// DescribeMethod resolves resolved's target method via reflection (the same
// dial+resolve steps Execute performs) and reports its streaming shape,
// WITHOUT invoking it. The GUI calls this before deciding whether "Send"
// should start a live-stream session (server-streaming) or a normal
// one-shot send (unary) — GrpcEditor lets the method be typed freely, no
// reflection-based picker exists yet, so there's no other way to know a
// method's shape ahead of time.
func (c *Client) DescribeMethod(ctx context.Context, resolved core.ResolvedRequest) (clientStreaming, serverStreaming bool, err error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	defer func() {
		if r := recover(); r != nil {
			err = errFromPanic(r)
		}
	}()

	fullMethod, _, extractErr := extractMethod(resolved.Headers)
	if extractErr != nil {
		return false, false, extractErr
	}
	serviceName, methodName, splitErr := splitMethod(fullMethod)
	if splitErr != nil {
		return false, false, splitErr
	}

	target, dialOpts := dialTarget(resolved.URL, c.insecureTransport)
	conn, dialErr := grpc.NewClient(target, dialOpts...)
	if dialErr != nil {
		return false, false, fmt.Errorf("dial %s: %w", target, dialErr)
	}
	defer conn.Close()

	refClient := grpcreflect.NewClientAuto(ctx, conn)
	defer refClient.Reset()

	svcDesc, resolveErr := refClient.ResolveService(serviceName)
	if resolveErr != nil {
		return false, false, fmt.Errorf("resolve service %q via reflection: %w", serviceName, resolveErr)
	}
	methodDesc := svcDesc.FindMethodByName(methodName)
	if methodDesc == nil {
		return false, false, fmt.Errorf("method %q not found on service %q", methodName, serviceName)
	}
	return methodDesc.IsClientStreaming(), methodDesc.IsServerStreaming(), nil
}

// errFromPanic converts a recover() value into an error, used by Execute's
// top-level recover and exercised directly by grpc_test.go to prove that
// wrapper actually degrades a descriptor panic to an error (docs/04-
// architecture-critique.md §E) rather than merely existing unexercised.
func errFromPanic(r any) error {
	return fmt.Errorf("grpc: recovered panic: %v", r)
}

func errResponse(requestID model.ID, start time.Time, err error) model.ResponseData {
	return model.ResponseData{
		RequestID: requestID,
		TimingMs:  time.Since(start).Milliseconds(),
		Timestamp: start.UTC().Format(time.RFC3339),
		Error:     err.Error(),
	}
}

// extractMethod pulls the "x-grpc-method" resolved header (see package doc)
// and returns every other enabled header as gRPC metadata.
func extractMethod(headers []model.KeyValue) (method string, meta map[string]string, err error) {
	meta = make(map[string]string, len(headers))
	for _, h := range headers {
		if !h.Enabled {
			continue
		}
		if strings.EqualFold(h.Key, MethodHeader) {
			method = h.Value
			continue
		}
		meta[h.Key] = h.Value
	}
	if method == "" {
		return "", nil, fmt.Errorf("grpc: missing %q header identifying the target as \"package.Service/Method\"", MethodHeader)
	}
	return method, meta, nil
}

// splitMethod parses "package.Service/Method" (grpcurl/grpc-go convention,
// optionally with a leading '/') into its service and method parts.
func splitMethod(full string) (service, method string, err error) {
	full = strings.TrimPrefix(strings.TrimSpace(full), "/")
	idx := strings.LastIndex(full, "/")
	if idx <= 0 || idx == len(full)-1 {
		return "", "", fmt.Errorf("grpc: %q is not a valid \"package.Service/Method\" target", full)
	}
	return full[:idx], full[idx+1:], nil
}

// dialTarget strips any URL scheme from resolved.URL (grpc.NewClient wants
// a bare "host:port" target) and decides whether to dial with TLS or
// plaintext credentials. "grpc+insecure://", "http://", and "h2c://"
// schemes (or the explicit WithInsecure option) force plaintext; anything
// else — including no scheme at all — defaults to TLS, matching how every
// other protocol in this codebase treats an unqualified host as
// TLS-by-default.
func dialTarget(rawURL string, forceInsecure bool) (string, []grpc.DialOption) {
	target := rawURL
	insecureScheme := forceInsecure

	if idx := strings.Index(target, "://"); idx >= 0 {
		scheme := strings.ToLower(target[:idx])
		target = target[idx+3:]
		switch scheme {
		case "grpc+insecure", "http", "h2c", "grpc":
			insecureScheme = insecureScheme || scheme != "grpc"
		case "grpcs", "https":
			insecureScheme = false
		}
	}
	target = strings.TrimSuffix(target, "/")

	var creds credentials.TransportCredentials
	if insecureScheme {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(nil)
	}
	return target, []grpc.DialOption{grpc.WithTransportCredentials(creds)}
}

// buildRequestMessage parses body (expected to be a JSON object per the
// package doc) into a dynamic.Message shaped by the method's discovered
// input type. A nil/empty body produces an empty (zero-value) request
// message, which is valid for methods that take no meaningful input.
func buildRequestMessage(methodDesc *desc.MethodDescriptor, body *model.RequestBody) (*dynamic.Message, error) {
	msg := dynamic.NewMessage(methodDesc.GetInputType())

	text := ""
	if body != nil {
		text = strings.TrimSpace(body.Text)
	}
	if text == "" {
		return msg, nil
	}

	if err := msg.UnmarshalJSONPB(&jsonpb.Unmarshaler{AllowUnknownFields: false}, []byte(text)); err != nil {
		return nil, fmt.Errorf("decode request body as %s JSON: %w", methodDesc.GetInputType().GetFullyQualifiedName(), err)
	}
	return msg, nil
}
