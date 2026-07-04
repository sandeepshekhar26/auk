// Package grpc implements core.Protocol for gRPC unary calls resolved
// entirely via server reflection — no compiled .proto stubs required, the
// same "dynamic" story grpcurl and k6 rely on
// (docs/02-architecture.md §"gRPC dynamic").
//
// Scope for v1 (docs/01-feature-roadmap.md 2.9/2.12): unary calls only.
// Client-stream / server-stream / bidi are explicitly deferred — Execute
// returns a clear error if the resolved method is not unary so callers get
// an honest message instead of a silent hang.
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

	// A unary gRPC call should never hang forever: reflection + invoke run
	// against the caller's ctx, which for a GUI send has no deadline, so an
	// unreachable server (wrong host/port, blocked network) would block the UI
	// on "Sending…" indefinitely. Bound it if the caller didn't. grpc.NewClient
	// is lazy, so this covers connect, reflection, and the RPC together.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
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

	refClient := grpcreflect.NewClientAuto(ctx, conn)
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
	if methodDesc.IsClientStreaming() || methodDesc.IsServerStreaming() {
		err = fmt.Errorf("grpc: %s is a streaming method (client=%v server=%v); only unary calls are supported in this build", fullMethod, methodDesc.IsClientStreaming(), methodDesc.IsServerStreaming())
		return errResponse(req.ID, start, err), err
	}

	reqMsg, err := buildRequestMessage(methodDesc, resolved.Body)
	if err != nil {
		return errResponse(req.ID, start, err), err
	}

	callCtx := ctx
	if len(metaHeaders) > 0 {
		callCtx = metadata.NewOutgoingContext(ctx, metadata.New(metaHeaders))
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "grpc", Direction: "sent", Payload: []byte(fullMethod)})
	}

	stub := grpcdynamic.NewStub(conn)
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
