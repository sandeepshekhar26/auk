package graphql

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// echoHandler records the last received envelope and echoes it back as
// {"data": {"query": ..., "variables": ...}} so tests can assert on exactly
// what was sent over the wire.
func echoHandler(t *testing.T) (http.Handler, *graphqlEnvelope) {
	t.Helper()
	got := &graphqlEnvelope{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"query":     got.Query,
				"variables": got.Variables,
			},
		})
	})
	return handler, got
}

func TestExecute_SendsQueryAndVariables(t *testing.T) {
	handler, got := echoHandler(t)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := New()
	req := model.RequestDef{ID: "req-1", Protocol: model.ProtocolGraphQL}
	resolved := core.ResolvedRequest{
		URL:    srv.URL,
		Method: http.MethodPost,
		Body: &model.RequestBody{
			Kind:             model.BodyGraphQL,
			Text:             `query Hello($name: String!) { hello(name: $name) }`,
			GraphQLVariables: `{"name":"world"}`,
		},
	}

	resp, err := client.Execute(t.Context(), nil, req, resolved)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got.Query != resolved.Body.Text {
		t.Errorf("received query = %q, want %q", got.Query, resolved.Body.Text)
	}
	if string(got.Variables) != resolved.Body.GraphQLVariables {
		t.Errorf("received variables = %s, want %s", got.Variables, resolved.Body.GraphQLVariables)
	}

	if resp.Status != http.StatusOK {
		t.Errorf("resp.Status = %d, want 200", resp.Status)
	}
	if resp.RequestID != req.ID {
		t.Errorf("resp.RequestID = %q, want %q", resp.RequestID, req.ID)
	}

	bodyBytes, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		t.Fatalf("decode BodyBase64: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
}

func TestExecute_NoVariables(t *testing.T) {
	handler, got := echoHandler(t)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := New()
	req := model.RequestDef{ID: "req-2", Protocol: model.ProtocolGraphQL}
	resolved := core.ResolvedRequest{
		URL:    srv.URL,
		Method: http.MethodPost,
		Body: &model.RequestBody{
			Kind: model.BodyGraphQL,
			Text: `query { hello }`,
		},
	}

	if _, err := client.Execute(t.Context(), nil, req, resolved); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got.Query != resolved.Body.Text {
		t.Errorf("received query = %q, want %q", got.Query, resolved.Body.Text)
	}
	if len(got.Variables) != 0 {
		t.Errorf("received variables = %s, want empty", got.Variables)
	}
}

func TestExecute_WhitespaceOnlyVariablesTreatedAsAbsent(t *testing.T) {
	handler, got := echoHandler(t)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := New()
	req := model.RequestDef{ID: "req-3", Protocol: model.ProtocolGraphQL}
	resolved := core.ResolvedRequest{
		URL:    srv.URL,
		Method: http.MethodPost,
		Body: &model.RequestBody{
			Kind:             model.BodyGraphQL,
			Text:             `query { hello }`,
			GraphQLVariables: "   \n\t",
		},
	}

	if _, err := client.Execute(t.Context(), nil, req, resolved); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(got.Variables) != 0 {
		t.Errorf("received variables = %s, want empty", got.Variables)
	}
}

func TestExecute_InvalidVariablesJSON(t *testing.T) {
	client := New()
	req := model.RequestDef{ID: "req-4", Protocol: model.ProtocolGraphQL}
	resolved := core.ResolvedRequest{
		URL:    "http://example.invalid/graphql",
		Method: http.MethodPost,
		Body: &model.RequestBody{
			Kind:             model.BodyGraphQL,
			Text:             `query { hello }`,
			GraphQLVariables: `{not valid json`,
		},
	}

	_, err := client.Execute(t.Context(), nil, req, resolved)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid variables JSON")
	}
}

func TestExecute_SetsHeaders(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	client := New()
	req := model.RequestDef{ID: "req-5", Protocol: model.ProtocolGraphQL}
	resolved := core.ResolvedRequest{
		URL:    srv.URL,
		Method: http.MethodPost,
		Headers: []model.KeyValue{
			{Key: "Authorization", Value: "Bearer abc123", Enabled: true},
			{Key: "X-Disabled", Value: "should-not-send", Enabled: false},
		},
		Body: &model.RequestBody{Kind: model.BodyGraphQL, Text: `query { hello }`},
	}

	if _, err := client.Execute(t.Context(), nil, req, resolved); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer abc123")
	}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != model.ProtocolGraphQL {
		t.Errorf("Kind() = %q, want %q", got, model.ProtocolGraphQL)
	}
}

func TestBuildEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		body    *model.RequestBody
		want    string
		wantErr bool
	}{
		{
			name: "nil body",
			body: nil,
			want: `{"query":""}`,
		},
		{
			name: "query only",
			body: &model.RequestBody{Text: "{ hello }"},
			want: `{"query":"{ hello }"}`,
		},
		{
			name: "query and variables",
			body: &model.RequestBody{Text: "{ hello }", GraphQLVariables: `{"a":1}`},
			want: `{"query":"{ hello }","variables":{"a":1}}`,
		},
		{
			name:    "invalid variables json",
			body:    &model.RequestBody{Text: "{ hello }", GraphQLVariables: "nope"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildEnvelope(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildEnvelope() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if string(got) != tt.want {
				t.Errorf("buildEnvelope() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIntrospect(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env graphqlEnvelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Fatalf("decode introspection request: %v", err)
		}
		gotQuery = env.Query
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"__schema":{"queryType":{"name":"Query"}}}}`))
	}))
	defer srv.Close()

	raw, err := Introspect(t.Context(), srv.URL)
	if err != nil {
		t.Fatalf("Introspect() error = %v", err)
	}

	if gotQuery == "" {
		t.Fatal("introspection request carried an empty query")
	}
	if !json.Valid(raw) {
		t.Fatalf("Introspect() returned invalid JSON: %s", raw)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal introspection response: %v", err)
	}
	if _, ok := decoded["data"]; !ok {
		t.Errorf("introspection response missing 'data' key: %s", raw)
	}
}

func TestIntrospect_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	_, err := Introspect(t.Context(), srv.URL)
	if err == nil {
		t.Fatal("Introspect() error = nil, want error for 500 response")
	}
}
