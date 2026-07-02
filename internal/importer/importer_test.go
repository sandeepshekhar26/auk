package importer

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
)

const openAPISpec = `{
  "openapi": "3.0.0",
  "info": { "title": "Pet Store" },
  "servers": [{ "url": "https://api.pets.test/v1" }],
  "paths": {
    "/pets": {
      "get": { "summary": "List pets", "tags": ["pets"],
        "parameters": [{ "name": "limit", "in": "query" }] },
      "post": { "summary": "Create pet", "tags": ["pets"],
        "requestBody": { "content": { "application/json": {
          "schema": { "type": "object", "properties": {
            "name": { "type": "string" }, "age": { "type": "integer" } } } } } } }
    },
    "/users/{id}": {
      "get": { "summary": "Get user", "tags": ["users"] }
    }
  }
}`

func TestDetect(t *testing.T) {
	cases := map[string]string{
		`curl https://x.test`:            FormatCurl,
		openAPISpec:                      FormatOpenAPI,
		`openapi: 3.0.0` + "\npaths: {}": FormatOpenAPI,
		`{"info":{"_postman_id":"x","name":"C"},"item":[]}`: FormatPostman,
		`{"random":"object"}`:                               "",
	}
	for input, want := range cases {
		if got := Detect(input); got != want {
			t.Errorf("Detect(%.30q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseOpenAPI(t *testing.T) {
	res, err := ParseOpenAPI([]byte(openAPISpec))
	if err != nil {
		t.Fatalf("ParseOpenAPI: %v", err)
	}
	if res.WorkspaceName != "Pet Store" {
		t.Errorf("workspace name = %q", res.WorkspaceName)
	}
	if len(res.Requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(res.Requests))
	}
	// baseUrl env created, and URLs templated against it.
	if len(res.Environments) != 1 || res.Environments[0].Variables[0].Value != "https://api.pets.test/v1" {
		t.Errorf("expected a baseUrl env, got %+v", res.Environments)
	}
	var post *model.RequestDef
	for i := range res.Requests {
		if !strings.HasPrefix(res.Requests[i].URL, "{{baseUrl}}") {
			t.Errorf("request URL not templated: %q", res.Requests[i].URL)
		}
		if res.Requests[i].Method == "POST" {
			post = &res.Requests[i]
		}
	}
	// POST /pets got a JSON body skeleton from the schema.
	if post == nil || post.Body == nil || !strings.Contains(post.Body.Text, "\"name\"") {
		t.Errorf("expected POST to have a JSON body skeleton, got %+v", post)
	}
	// Two tags → two folders.
	if len(res.Folders) != 2 {
		t.Errorf("expected 2 folders (pets, users), got %d", len(res.Folders))
	}
}

const postmanCol = `{
  "info": { "_postman_id": "abc", "name": "My API",
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json" },
  "variable": [{ "key": "base", "value": "https://api.test" }],
  "item": [
    { "name": "Auth", "item": [
      { "name": "Login", "request": {
        "method": "POST",
        "header": [{ "key": "Content-Type", "value": "application/json" }],
        "url": { "raw": "{{base}}/login" },
        "body": { "mode": "raw", "raw": "{\"user\":\"a\"}" },
        "auth": { "type": "bearer", "bearer": [{ "key": "token", "value": "xyz" }] }
      }}
    ]},
    { "name": "Ping", "request": { "method": "GET", "url": "{{base}}/ping" } }
  ]
}`

func TestParsePostman(t *testing.T) {
	res, err := ParsePostman([]byte(postmanCol))
	if err != nil {
		t.Fatalf("ParsePostman: %v", err)
	}
	if res.WorkspaceName != "My API" {
		t.Errorf("name = %q", res.WorkspaceName)
	}
	if len(res.Requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(res.Requests))
	}
	if len(res.Folders) != 1 || res.Folders[0].Name != "Auth" {
		t.Errorf("expected one 'Auth' folder, got %+v", res.Folders)
	}
	// Collection variable → env.
	if len(res.Environments) != 1 || res.Environments[0].Variables[0].Key != "base" {
		t.Errorf("expected a 'base' env var, got %+v", res.Environments)
	}

	var login *model.RequestDef
	for i := range res.Requests {
		if res.Requests[i].Name == "Login" {
			login = &res.Requests[i]
		}
	}
	if login == nil {
		t.Fatal("Login request not found")
	}
	if login.Method != "POST" || login.URL != "{{base}}/login" {
		t.Errorf("Login method/url wrong: %s %s", login.Method, login.URL)
	}
	if login.Body == nil || login.Body.Kind != model.BodyJSON {
		t.Errorf("Login body should be JSON, got %+v", login.Body)
	}
	if login.Auth == nil || login.Auth.Kind != model.AuthBearer || login.Auth.Bearer.Token != "xyz" {
		t.Errorf("Login bearer auth wrong: %+v", login.Auth)
	}
	// Login belongs to the Auth folder.
	if login.FolderID == nil || *login.FolderID != res.Folders[0].ID {
		t.Errorf("Login should be in the Auth folder")
	}
}

func TestImportDispatch(t *testing.T) {
	// Import auto-detects and routes.
	if r, err := Import(openAPISpec); err != nil || r.Format != FormatOpenAPI {
		t.Errorf("Import(openapi) = %q, %v", r.Format, err)
	}
	if r, err := Import(postmanCol); err != nil || r.Format != FormatPostman {
		t.Errorf("Import(postman) = %q, %v", r.Format, err)
	}
	if r, err := Import(`curl -X GET https://x.test`); err != nil || r.Format != FormatCurl || len(r.Requests) != 1 {
		t.Errorf("Import(curl) = %q, %v", r.Format, err)
	}
	if _, err := Import(`{"nonsense":true}`); err == nil {
		t.Errorf("expected error for undetectable input")
	}
}
