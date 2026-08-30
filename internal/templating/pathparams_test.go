package templating

import (
	"context"
	"testing"

	"apitool/internal/core/model"
)

// A path param VALUE is an ordinary templated field: `${userId}` in the
// value resolves against the environment exactly like it would in a header
// or the URL itself. The KEY is the literal placeholder name and must be
// passed through untouched.
func TestResolvePathParamValuesAreTemplated(t *testing.T) {
	e := New(nil)
	env := &model.Environment{Variables: []model.KeyValue{
		{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
		{Key: "userId", Value: "u-42", Enabled: true},
	}}
	req := model.RequestDef{
		Protocol: model.ProtocolHTTP,
		Method:   "GET",
		URL:      "${baseUrl}/users/:id",
		PathParams: []model.KeyValue{
			{Key: "id", Value: "${userId}", Enabled: true},
		},
	}

	resolved, err := e.Resolve(context.Background(), req, env, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.URL != "https://api.example.com/users/:id" {
		t.Errorf("URL = %q; templating must expand ${baseUrl} but leave :id for the engine", resolved.URL)
	}
	if len(resolved.PathParams) != 1 {
		t.Fatalf("PathParams = %v, want 1 row", resolved.PathParams)
	}
	if got := resolved.PathParams[0]; got.Key != "id" || got.Value != "u-42" {
		t.Errorf("PathParams[0] = %+v, want {Key:id Value:u-42}", got)
	}
}

// An unresolvable variable inside a path param value surfaces as an error
// from Resolve, the same as one anywhere else — it isn't silently dropped.
func TestResolvePathParamUnknownVariableErrors(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{
		Protocol:   model.ProtocolHTTP,
		URL:        "https://api.example.com/users/:id",
		PathParams: []model.KeyValue{{Key: "id", Value: "${nope}", Enabled: true}},
	}
	if _, err := e.Resolve(context.Background(), req, nil, nil); err == nil {
		t.Fatal("expected an error for an unresolved variable in a path param value")
	}
}

// No PathParams rows must not fabricate an empty slice difference — a
// request without them resolves exactly as before.
func TestResolveWithoutPathParams(t *testing.T) {
	e := New(nil)
	req := model.RequestDef{Protocol: model.ProtocolHTTP, URL: "https://api.example.com/ping"}
	resolved, err := e.Resolve(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PathParams != nil {
		t.Errorf("PathParams = %v, want nil", resolved.PathParams)
	}
}
