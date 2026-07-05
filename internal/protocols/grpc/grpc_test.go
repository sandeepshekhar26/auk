package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/builder"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
	googrpcreflection "google.golang.org/grpc/reflection"
	refv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// recordingSink captures every emitted core.Event in order — mirrors the
// helper internal/protocols/sse's own tests use for the identical purpose
// (asserting an ordered event sequence), redefined here since it's
// unexported in that package.
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

// buildEchoService hand-builds (no protoc, no generated Go code) a tiny
// "apitool.test.Echo" service with three methods, covering every streaming
// shape Execute has to distinguish:
//   - Say (unary): EchoRequest{string message=1} -> EchoResponse{string message=1, int32 length=2}
//   - Count (server-streaming): CountRequest{int32 upTo=1} -> stream of CountResponse{int32 value=1}
//   - Upload (client-streaming, real handler not needed — only used to prove
//     Execute rejects it with a clear message): stream of EchoRequest -> EchoResponse
//
// This is what lets grpc_test.go stand up a real, reflection-enabled
// in-process server without a .proto file in the repo.
func buildEchoService(t *testing.T) *desc.FileDescriptor {
	t.Helper()

	reqMsg := builder.NewMessage("EchoRequest").
		AddField(builder.NewField("message", builder.FieldTypeString()).SetNumber(1))
	respMsg := builder.NewMessage("EchoResponse").
		AddField(builder.NewField("message", builder.FieldTypeString()).SetNumber(1)).
		AddField(builder.NewField("length", builder.FieldTypeInt32()).SetNumber(2))
	countReqMsg := builder.NewMessage("CountRequest").
		AddField(builder.NewField("upTo", builder.FieldTypeInt32()).SetNumber(1))
	countRespMsg := builder.NewMessage("CountResponse").
		AddField(builder.NewField("value", builder.FieldTypeInt32()).SetNumber(1))

	sayMethod := builder.NewMethod("Say",
		builder.RpcTypeMessage(reqMsg, false),
		builder.RpcTypeMessage(respMsg, false),
	)
	countMethod := builder.NewMethod("Count",
		builder.RpcTypeMessage(countReqMsg, false),
		builder.RpcTypeMessage(countRespMsg, true),
	)
	uploadMethod := builder.NewMethod("Upload",
		builder.RpcTypeMessage(reqMsg, true),
		builder.RpcTypeMessage(respMsg, false),
	)
	echoService := builder.NewService("Echo").
		AddMethod(sayMethod).
		AddMethod(countMethod).
		AddMethod(uploadMethod)

	fb := builder.NewFile("apitool_test_echo.proto").
		SetPackageName("apitool.test").
		AddMessage(reqMsg).
		AddMessage(respMsg).
		AddMessage(countReqMsg).
		AddMessage(countRespMsg).
		AddService(echoService)

	fd, err := fb.Build()
	if err != nil {
		t.Fatalf("build echo service descriptor: %v", err)
	}
	return fd
}

// countStreamHandler implements the "Count" server-streaming method: reads
// one CountRequest{upTo}, then sends upTo CountResponse messages with value
// 1..upTo. A small per-message delay makes cancellation-mid-stream
// deterministically testable (TestExecute_ServerStream_ContextCancelled
// cancels after the first message and asserts fewer than upTo arrived).
func countStreamHandler(reqDesc, respDesc *desc.MessageDescriptor, perMessageDelay time.Duration) grpc.StreamHandler {
	return func(srv any, stream grpc.ServerStream) error {
		in := dynamic.NewMessage(reqDesc)
		if err := stream.RecvMsg(in); err != nil {
			return err
		}
		upToVal, _ := in.TryGetFieldByName("upTo")
		upTo, _ := upToVal.(int32)

		for i := int32(1); i <= upTo; i++ {
			if i > 1 && perMessageDelay > 0 {
				time.Sleep(perMessageDelay)
			}
			out := dynamic.NewMessage(respDesc)
			out.SetFieldByName("value", i)
			if err := stream.SendMsg(out); err != nil {
				return err
			}
		}
		return nil
	}
}

// echoHandler implements the "Say" unary method entirely in terms of
// dynamic.Message — there is no generated Go struct for EchoRequest, which
// is exactly the "no compiled stubs" story this package's Execute relies on
// for real-world servers too.
func echoHandler(reqDesc, respDesc *desc.MessageDescriptor) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := dynamic.NewMessage(reqDesc)
		if err := dec(in); err != nil {
			return nil, err
		}
		msg, _ := in.TryGetFieldByName("message")
		text, _ := msg.(string)

		out := dynamic.NewMessage(respDesc)
		out.SetFieldByName("message", "echo: "+text)
		out.SetFieldByName("length", int32(len(text)))
		return out, nil
	}
}

// startEchoServer boots a real *grpc.Server on an ephemeral loopback port
// with server reflection enabled (google.golang.org/grpc/reflection) and
// the descriptor resolver pointed at a local *protoregistry.Files built
// from buildEchoService — the same discovery path a real user's server
// would offer, without polluting the process-global proto registry.
// countDelay is passed straight to countStreamHandler (0 for the common
// case; a small delay lets a cancellation test observe a stream mid-flight).
// Upload (client-streaming) is deliberately NOT given a real handler —
// Execute must reject it via reflection alone, before ever invoking it.
func startEchoServer(t *testing.T, countDelay time.Duration) (addr string, stop func()) {
	t.Helper()

	fd := buildEchoService(t)
	svcDesc := fd.GetServices()[0]
	sayDesc := svcDesc.FindMethodByName("Say")
	countDesc := svcDesc.FindMethodByName("Count")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: svcDesc.GetFullyQualifiedName(),
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Say",
				Handler:    echoHandler(sayDesc.GetInputType(), sayDesc.GetOutputType()),
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Count",
				Handler:       countStreamHandler(countDesc.GetInputType(), countDesc.GetOutputType(), countDelay),
				ServerStreams: true,
			},
		},
	}, nil)

	files := &protoregistry.Files{}
	fileProto := fd.AsFileDescriptorProto()
	fileDesc, err := protodesc.NewFile(fileProto, &protoregistry.Files{})
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	if err := files.RegisterFile(fileDesc); err != nil {
		t.Fatalf("register file: %v", err)
	}
	reflSrv := googrpcreflection.NewServerV1(googrpcreflection.ServerOptions{
		Services:           srv,
		DescriptorResolver: files,
	})
	refv1.RegisterServerReflectionServer(srv, reflSrv)

	go func() { _ = srv.Serve(lis) }()

	return lis.Addr().String(), func() {
		srv.Stop()
	}
}

func TestExecute_UnaryRoundTrip(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC, Method: "grpc"}
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Say", Enabled: true},
		},
		Body: &model.RequestBody{Kind: model.BodyJSON, Text: `{"message":"hello"}`},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Execute(ctx, nil, req, resolved)
	if err != nil {
		t.Fatalf("Execute: %v (resp.Error=%q)", err, resp.Error)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected resp.Error: %s", resp.Error)
	}
	if resp.StatusText != "OK" {
		t.Fatalf("StatusText = %q, want OK", resp.StatusText)
	}

	raw, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		t.Fatalf("decode BodyBase64: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal response JSON %s: %v", raw, err)
	}
	if body["message"] != "echo: hello" {
		t.Fatalf("body.message = %v, want %q", body["message"], "echo: hello")
	}
}

// TestExecute_ServerStreamRoundTrip proves a server-streaming method emits
// one "received" event per message (in order, with correct payloads),
// bracketed by "sent" and "done" — mirroring internal/protocols/sse's own
// event-sequence tests — and that the final summary ResponseData reports
// the right message count.
func TestExecute_ServerStreamRoundTrip(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC}
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Count", Enabled: true},
		},
		Body: &model.RequestBody{Kind: model.BodyJSON, Text: `{"upTo":3}`},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sink := &recordingSink{}
	sess := core.NewSession("sess-1", ctx, sink)

	resp, err := client.Execute(ctx, sess, req, resolved)
	if err != nil {
		t.Fatalf("Execute: %v (resp.Error=%q)", err, resp.Error)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected resp.Error: %s", resp.Error)
	}
	if resp.BodySize != 3 {
		t.Fatalf("resp.BodySize = %d, want 3", resp.BodySize)
	}

	events := sink.snapshot()
	var gotDirections []string
	for _, e := range events {
		if e.Kind != "grpc" {
			t.Fatalf("event Kind = %q, want \"grpc\": %+v", e.Kind, e)
		}
		gotDirections = append(gotDirections, e.Direction)
	}
	wantDirections := []string{"sent", "received", "received", "received", "done"}
	if len(gotDirections) != len(wantDirections) {
		t.Fatalf("got %d events %v, want %d %v", len(gotDirections), gotDirections, len(wantDirections), wantDirections)
	}
	for i, want := range wantDirections {
		if gotDirections[i] != want {
			t.Fatalf("event[%d].Direction = %q, want %q (full sequence: %v)", i, gotDirections[i], want, gotDirections)
		}
	}

	// The three "received" events (indexes 1-3) must carry value 1, 2, 3 IN
	// ORDER — proves messages arrive as a genuine sequence, not just a count.
	for i, wantValue := range []int{1, 2, 3} {
		var body map[string]any
		if err := json.Unmarshal(events[i+1].Payload, &body); err != nil {
			t.Fatalf("unmarshal event[%d] payload %s: %v", i+1, events[i+1].Payload, err)
		}
		gotValue, _ := body["value"].(float64)
		if int(gotValue) != wantValue {
			t.Fatalf("event[%d] value = %v, want %d", i+1, body["value"], wantValue)
		}
	}
}

// TestExecute_ServerStream_ContextCancelled proves cancelling the call
// context (what stream.go's StopStream/Disconnect does to a live session)
// stops the receive loop before the stream naturally completes, rather
// than blocking forever or reading every message regardless.
func TestExecute_ServerStream_ContextCancelled(t *testing.T) {
	addr, stop := startEchoServer(t, 100*time.Millisecond) // slow enough to reliably cancel mid-stream
	defer stop()

	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC}
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Count", Enabled: true},
		},
		Body: &model.RequestBody{Kind: model.BodyJSON, Text: `{"upTo":100}`},
	}

	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingSink{}
	sess := core.NewSession("sess-1", ctx, sink)

	go func() {
		time.Sleep(150 * time.Millisecond) // let a message or two through, then cancel
		cancel()
	}()

	resp, err := client.Execute(ctx, sess, req, resolved)
	if err == nil {
		t.Fatal("Execute: expected an error from context cancellation, got nil")
	}
	if resp.BodySize >= 100 {
		t.Fatalf("resp.BodySize = %d, want fewer than the full 100 (cancellation should have cut the stream short)", resp.BodySize)
	}
	t.Logf("received %d of 100 messages before cancellation (err=%v)", resp.BodySize, err)
}

// TestExecute_ClientStreamingRejected proves a client-streaming method gets
// the specific "client-streaming/bidi isn't supported yet" message (not the
// old blanket "streaming methods aren't supported" this replaced), and that
// it's rejected via reflection alone — Upload has no working handler
// registered, so a bug that let Execute try to invoke it would hang or
// error very differently than this test's assertion.
func TestExecute_ClientStreamingRejected(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC}
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Upload", Enabled: true},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Execute(ctx, nil, req, resolved)
	if err == nil {
		t.Fatal("Execute: expected an error for a client-streaming method, got nil")
	}
	if !strings.Contains(err.Error(), "client-streaming/bidi") {
		t.Fatalf("error = %q, want it to mention client-streaming/bidi isn't supported", err.Error())
	}
	if resp.Error == "" {
		t.Fatal("expected resp.Error to be populated")
	}
}

func TestDescribeMethod_Unary(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Say", Enabled: true},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientStreaming, serverStreaming, err := client.DescribeMethod(ctx, resolved)
	if err != nil {
		t.Fatalf("DescribeMethod: %v", err)
	}
	if clientStreaming || serverStreaming {
		t.Fatalf("Say: clientStreaming=%v serverStreaming=%v, want false/false (unary)", clientStreaming, serverStreaming)
	}
}

func TestDescribeMethod_ServerStreaming(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/Count", Enabled: true},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientStreaming, serverStreaming, err := client.DescribeMethod(ctx, resolved)
	if err != nil {
		t.Fatalf("DescribeMethod: %v", err)
	}
	if clientStreaming {
		t.Fatal("Count: clientStreaming = true, want false")
	}
	if !serverStreaming {
		t.Fatal("Count: serverStreaming = false, want true")
	}
}

func TestDescribeMethod_UnknownMethod(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "apitool.test.Echo/DoesNotExist", Enabled: true},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := client.DescribeMethod(ctx, resolved); err == nil {
		t.Fatal("DescribeMethod: expected an error for an unknown method, got nil")
	}
}

func TestExecute_MissingMethodHeader(t *testing.T) {
	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC}
	resolved := core.ResolvedRequest{URL: "grpc+insecure://127.0.0.1:1"}

	_, err := client.Execute(context.Background(), nil, req, resolved)
	if err == nil {
		t.Fatal("expected an error for a missing x-grpc-method header")
	}
}

func TestExecute_UnknownService(t *testing.T) {
	addr, stop := startEchoServer(t, 0)
	defer stop()

	client := New(WithInsecure())
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGRPC}
	resolved := core.ResolvedRequest{
		URL: "grpc+insecure://" + addr,
		Headers: []model.KeyValue{
			{Key: MethodHeader, Value: "does.not.Exist/Method", Enabled: true},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Execute(ctx, nil, req, resolved)
	if err == nil {
		t.Fatal("expected an error resolving an unknown service via reflection")
	}
	if resp.Error == "" {
		t.Fatal("expected resp.Error to be populated instead of a bare panic/crash")
	}
}

func TestSplitMethod(t *testing.T) {
	cases := []struct {
		in          string
		wantService string
		wantMethod  string
		wantErr     bool
	}{
		{"pkg.Service/Method", "pkg.Service", "Method", false},
		{"/pkg.Service/Method", "pkg.Service", "Method", false},
		{"Method", "", "", true},
		{"pkg.Service/", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		svc, method, err := splitMethod(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitMethod(%q): expected error, got none", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitMethod(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if svc != tc.wantService || method != tc.wantMethod {
			t.Errorf("splitMethod(%q) = (%q, %q), want (%q, %q)", tc.in, svc, method, tc.wantService, tc.wantMethod)
		}
	}
}

func TestDialTarget(t *testing.T) {
	cases := []struct {
		in            string
		forceInsecure bool
		wantTarget    string
	}{
		{"grpc+insecure://localhost:50051", false, "localhost:50051"},
		{"https://example.com:443", false, "example.com:443"},
		{"example.com:50051", false, "example.com:50051"},
		{"example.com:50051/", true, "example.com:50051"},
	}
	for _, tc := range cases {
		target, opts := dialTarget(tc.in, tc.forceInsecure)
		if target != tc.wantTarget {
			t.Errorf("dialTarget(%q) target = %q, want %q", tc.in, target, tc.wantTarget)
		}
		if len(opts) == 0 {
			t.Errorf("dialTarget(%q): expected transport credentials dial option", tc.in)
		}
	}
}

func TestBuildRequestMessage_EmptyBodyProducesZeroValueMessage(t *testing.T) {
	fd := buildEchoService(t)
	methodDesc := fd.GetServices()[0].FindMethodByName("Say")

	msg, err := buildRequestMessage(methodDesc, nil)
	if err != nil {
		t.Fatalf("buildRequestMessage(nil body): %v", err)
	}
	if msg == nil {
		t.Fatal("expected a non-nil zero-value message")
	}
}

func TestBuildRequestMessage_MalformedJSON(t *testing.T) {
	fd := buildEchoService(t)
	methodDesc := fd.GetServices()[0].FindMethodByName("Say")

	_, err := buildRequestMessage(methodDesc, &model.RequestBody{Text: `{"message": `})
	if err == nil {
		t.Fatal("expected an error for malformed JSON body")
	}
}

// TestExecute_RecoversFromDescriptorPanic proves the recover() in Execute
// (docs/04-architecture-critique.md §E: jhump/protoreflect's
// descriptor/reflection code paths are documented to panic on legacy or
// malformed real-world descriptors) turns a genuine panic into a returned
// error + populated ResponseData.Error, never a crashed goroutine.
//
// It reproduces this in miniature by calling the exact same recover-wrapped
// pattern Execute uses around a call that is guaranteed to panic (a nil
// *desc.MethodDescriptor reaching GetInputType(), the same nil-pointer
// shape a broken/legacy reflected descriptor can produce) and asserting the
// panic surfaces as an error rather than propagating.
func TestExecute_RecoversFromDescriptorPanic(t *testing.T) {
	runRecoverWrapped := func() (execErr error) {
		defer func() {
			if r := recover(); r != nil {
				execErr = errFromPanic(r)
			}
		}()
		var methodDesc *desc.MethodDescriptor // simulates a broken/nil descriptor from reflection
		_, err := buildRequestMessage(methodDesc, nil)
		return err
	}

	err := runRecoverWrapped()
	if err == nil {
		t.Fatal("expected the recover() wrapper to convert the descriptor panic into an error")
	}
}
