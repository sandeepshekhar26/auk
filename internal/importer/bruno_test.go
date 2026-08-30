package importer

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// bruSample is a realistic single-request .bru file with a meta block, a POST
// method block referencing its body/auth, headers (one disabled with `~`), a
// bearer auth block, a JSON body, and a query block. Templates use `{{var}}`.
const bruSample = `meta {
  name: Create User
  type: http
  seq: 1
}

post {
  url: {{baseUrl}}/users
  body: json
  auth: bearer
}

headers {
  Content-Type: application/json
  ~X-Debug: true
}

auth:bearer {
  token: {{token}}
}

body:json {
  {
    "name": "{{userName}}"
  }
}

query {
  page: 1
}
`

func TestParseBruno(t *testing.T) {
	res, err := ParseBruno(bruSample)
	if err != nil {
		t.Fatalf("ParseBruno: %v", err)
	}
	if len(res.Requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(res.Requests))
	}
	req := res.Requests[0]

	if req.Name != "Create User" {
		t.Errorf("name = %q", req.Name)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q", req.Method)
	}
	// {{baseUrl}} -> ${baseUrl}
	if req.URL != "${baseUrl}/users" {
		t.Errorf("URL = %q, want ${baseUrl}/users", req.URL)
	}

	// Headers: Content-Type enabled, X-Debug disabled via `~`.
	var ct, dbg *model.KeyValue
	for i := range req.Headers {
		switch req.Headers[i].Key {
		case "Content-Type":
			ct = &req.Headers[i]
		case "X-Debug":
			dbg = &req.Headers[i]
		}
	}
	if ct == nil || !ct.Enabled || ct.Value != "application/json" {
		t.Errorf("Content-Type header wrong: %+v", ct)
	}
	if dbg == nil || dbg.Enabled {
		t.Errorf("X-Debug should be present and disabled, got %+v", dbg)
	}

	// Bearer auth block mapped, token converted.
	if req.Auth == nil || req.Auth.Kind != model.AuthBearer || req.Auth.Bearer.Token != "${token}" {
		t.Errorf("bearer auth wrong: %+v", req.Auth)
	}

	// JSON body with converted var.
	if req.Body == nil || req.Body.Kind != model.BodyJSON || !strings.Contains(req.Body.Text, "${userName}") {
		t.Errorf("body should be JSON with ${userName}, got %+v", req.Body)
	}
	// The JSON braces must survive the block parser intact.
	if !strings.Contains(req.Body.Text, "{") || !strings.Contains(req.Body.Text, "}") {
		t.Errorf("JSON body braces lost: %q", req.Body.Text)
	}

	// Query -> params.
	if len(req.Params) != 1 || req.Params[0].Key != "page" || req.Params[0].Value != "1" {
		t.Errorf("params = %+v, want page=1", req.Params)
	}

	assertValidOrderKeys(t, res)
}

func TestDetectBruno(t *testing.T) {
	if got := Detect(bruSample); got != FormatBruno {
		t.Errorf("Detect(Bruno) = %q, want %q", got, FormatBruno)
	}
	// A bare method-block .bru (no meta) still detects.
	if got := Detect("get {\n  url: https://x.test\n}\n"); got != FormatBruno {
		t.Errorf("Detect(bare get block) = %q, want %q", got, FormatBruno)
	}
}

// TestParseBrunoBraceAwareBody proves the string-literal-aware brace matcher
// doesn't terminate a JSON body early on a `}` inside a string value.
func TestParseBrunoBraceAwareBody(t *testing.T) {
	const bru = `post {
  url: https://x.test/a
  body: json
}

body:json {
  { "tmpl": "a}b", "nested": { "k": 1 } }
}
`
	res, err := ParseBruno(bru)
	if err != nil {
		t.Fatalf("ParseBruno: %v", err)
	}
	body := res.Requests[0].Body
	if body == nil || body.Kind != model.BodyJSON {
		t.Fatalf("expected a JSON body, got %+v", body)
	}
	if !strings.Contains(body.Text, `"a}b"`) || !strings.Contains(body.Text, `"nested"`) {
		t.Errorf("body truncated at brace-in-string: %q", body.Text)
	}
}
