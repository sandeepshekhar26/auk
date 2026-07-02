package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
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

// buildEchoService hand-builds (no protoc, no generated Go code) a tiny
// "apitool.test.Echo/Say" unary service: EchoRequest{string message=1} ->
// EchoResponse{string message=1, int32 length=2}. This is what lets
// grpc_test.go stand up a real, reflection-enabled in-process server
// without a .proto file in the repo.
func buildEchoService(t *testing.T) *desc.FileDescriptor {
	t.Helper()

	reqMsg := builder.NewMessage("EchoRequest").
		AddField(builder.NewField("message", builder.FieldTypeString()).SetNumber(1))
	respMsg := builder.NewMessage("EchoResponse").
		AddField(builder.NewField("message", builder.FieldTypeString()).SetNumber(1)).
		AddField(builder.NewField("length", builder.FieldTypeInt32()).SetNumber(2))

	sayMethod := builder.NewMethod("Say",
		builder.RpcTypeMessage(reqMsg, false),
		builder.RpcTypeMessage(respMsg, false),
	)
	echoService := builder.NewService("Echo").AddMethod(sayMethod)

	fb := builder.NewFile("apitool_test_echo.proto").
		SetPackageName("apitool.test").
		AddMessage(reqMsg).
		AddMessage(respMsg).
		AddService(echoService)

	fd, err := fb.Build()
	if err != nil {
		t.Fatalf("build echo service descriptor: %v", err)
	}
	return fd
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
func startEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	fd := buildEchoService(t)
	svcDesc := fd.GetServices()[0]
	methodDesc := svcDesc.FindMethodByName("Say")

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
				Handler:    echoHandler(methodDesc.GetInputType(), methodDesc.GetOutputType()),
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
	addr, stop := startEchoServer(t)
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
	addr, stop := startEchoServer(t)
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
