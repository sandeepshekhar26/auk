package importer

import (
	"testing"

	"apitool/internal/core/model"
)

// postmanPathVarCol is a real-shape Postman Collection v2.1 export whose URL
// is the object form with a `variable` array — how Postman records values for
// `:name` path placeholders (Send tab → "Path Variables").
const postmanPathVarCol = `{
  "info": {
    "_postman_id": "b8f0c2d1-0000-4a11-9c3e-abcdef012345",
    "name": "Path Vars API",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "item": [
    {
      "name": "Get User Post",
      "request": {
        "method": "GET",
        "header": [],
        "url": {
          "raw": "https://api.example.com/users/:id/posts/:postId",
          "protocol": "https",
          "host": ["api", "example", "com"],
          "path": ["users", ":id", "posts", ":postId"],
          "variable": [
            { "key": "id", "value": "42" },
            { "key": "postId", "value": "99" }
          ]
        }
      }
    },
    {
      "name": "List Users",
      "request": {
        "method": "GET",
        "url": "https://api.example.com/users"
      }
    }
  ]
}`

func TestParsePostmanPathVariables(t *testing.T) {
	res, err := ParsePostman([]byte(postmanPathVarCol))
	if err != nil {
		t.Fatalf("ParsePostman: %v", err)
	}
	if len(res.Requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(res.Requests))
	}

	var getPost, listUsers *model.RequestDef
	for i := range res.Requests {
		switch res.Requests[i].Name {
		case "Get User Post":
			getPost = &res.Requests[i]
		case "List Users":
			listUsers = &res.Requests[i]
		}
	}
	if getPost == nil {
		t.Fatal("Get User Post request not found")
	}

	// The URL must keep the :name tokens — AUK's editor derives the Path rows
	// from these, and they have to line up with the PathParams values below.
	const wantURL = "https://api.example.com/users/:id/posts/:postId"
	if getPost.URL != wantURL {
		t.Errorf("URL = %q, want %q", getPost.URL, wantURL)
	}

	// Both path variables become pre-filled PathParams rows, in order, enabled.
	want := []model.KeyValue{
		{Key: "id", Value: "42", Enabled: true},
		{Key: "postId", Value: "99", Enabled: true},
	}
	if len(getPost.PathParams) != len(want) {
		t.Fatalf("PathParams = %+v, want %d rows", getPost.PathParams, len(want))
	}
	for i, w := range want {
		got := getPost.PathParams[i]
		if got.Key != w.Key || got.Value != w.Value || got.Enabled != w.Enabled {
			t.Errorf("PathParams[%d] = %+v, want %+v", i, got, w)
		}
	}

	// A plain string URL (no object/variable array) carries no PathParams —
	// the editor derives empty Path rows from any :name tokens in the string.
	if listUsers == nil {
		t.Fatal("List Users request not found")
	}
	if len(listUsers.PathParams) != 0 {
		t.Errorf("string-form URL should yield no PathParams, got %+v", listUsers.PathParams)
	}
}

// TestParsePostmanStringURLWithColonToken guards the "both must agree" contract
// from the other direction: a string-form URL that itself contains a :name
// token is preserved verbatim (tokens intact), leaving the editor to derive
// the Path row — we don't invent values Postman never supplied.
func TestParsePostmanStringURLWithColonToken(t *testing.T) {
	const col = `{
      "info": { "name": "Str", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
      "item": [
        { "name": "Get One", "request": { "method": "GET", "url": "https://api.example.com/widgets/:widgetId" } }
      ]
    }`
	res, err := ParsePostman([]byte(col))
	if err != nil {
		t.Fatalf("ParsePostman: %v", err)
	}
	if len(res.Requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(res.Requests))
	}
	r := res.Requests[0]
	if r.URL != "https://api.example.com/widgets/:widgetId" {
		t.Errorf("URL = %q, want the :widgetId token preserved", r.URL)
	}
	if len(r.PathParams) != 0 {
		t.Errorf("no variable array → no PathParams, got %+v", r.PathParams)
	}
}

// TestParsePostmanNonStringPathVariable is the regression test for a silent
// URL-loss bug: Postman's schema types variable[].value as `any`, and a
// collection carrying a NUMBER (common in tool-generated exports) made the
// whole url-object unmarshal fail — so the request imported with a BLANK URL
// and no path params, with no error shown to the user.
func TestParsePostmanNonStringPathVariable(t *testing.T) {
	const collection = `{
      "info": {"name": "Numeric Vars"},
      "item": [{
        "name": "Get user",
        "request": {
          "method": "GET",
          "url": {
            "raw": "https://api.example.com/users/:id/flags/:on",
            "variable": [
              {"key": "id", "value": 42},
              {"key": "on", "value": true}
            ]
          }
        }
      }]
    }`

	res, err := Import(collection)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Requests) != 1 {
		t.Fatalf("want 1 request, got %d", len(res.Requests))
	}
	req := res.Requests[0]
	if req.URL != "https://api.example.com/users/:id/flags/:on" {
		t.Errorf("URL was lost or mangled: %q", req.URL)
	}
	got := map[string]string{}
	for _, p := range req.PathParams {
		got[p.Key] = p.Value
	}
	if got["id"] != "42" {
		t.Errorf("numeric path variable id = %q, want \"42\"", got["id"])
	}
	if got["on"] != "true" {
		t.Errorf("boolean path variable on = %q, want \"true\"", got["on"])
	}
}

// A numeric COLLECTION-level variable must likewise not break the import.
func TestParsePostmanNonStringCollectionVariable(t *testing.T) {
	const collection = `{
      "info": {"name": "Numeric Collection Var"},
      "variable": [{"key": "port", "value": 8080}, {"key": "host", "value": "localhost"}],
      "item": [{"name": "Ping", "request": {"method": "GET", "url": "http://{{host}}:{{port}}/ping"}}]
    }`

	res, err := Import(collection)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Requests) != 1 || res.Requests[0].URL == "" {
		t.Fatalf("request/URL lost: %+v", res.Requests)
	}
	if len(res.Environments) != 1 {
		t.Fatalf("want 1 environment, got %d", len(res.Environments))
	}
	vars := map[string]string{}
	for _, v := range res.Environments[0].Variables {
		vars[v.Key] = v.Value
	}
	if vars["port"] != "8080" {
		t.Errorf("numeric collection variable port = %q, want \"8080\"", vars["port"])
	}
	if vars["host"] != "localhost" {
		t.Errorf("string collection variable host = %q", vars["host"])
	}
}
