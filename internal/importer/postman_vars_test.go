package importer

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"apitool/internal/templating"
)

// These tests pin the conservative `{{var}}` -> `${var}` conversion. The
// original rewrite matched ANY `{{name}}`, which meant a collection whose body
// is a Handlebars/Mustache template imported as something that could no longer
// be SENT — Postman leaves unknown `{{...}}` literal and posts the payload
// intact, so a collection that worked there stopped working here. See
// knownVars.convert.

// handlebarsCollection POSTs a Handlebars template as its body. `items` and
// `this` are template expressions, not variables; `baseUrl` IS a collection
// variable and must still convert.
const handlebarsCollection = `{
  "info": {"name": "Templates", "_postman_id": "hb1"},
  "variable": [{"key": "baseUrl", "value": "https://api.example.com"}],
  "item": [{
    "name": "Render",
    "request": {
      "method": "POST",
      "url": {"raw": "{{baseUrl}}/render"},
      "header": [{"key": "X-Base", "value": "{{baseUrl}}"}],
      "body": {"mode": "raw", "raw": "{\"template\":\"{{#each items}}{{this}}{{/each}}\",\"base\":\"{{baseUrl}}\"}"}
    }
  }]
}`

func TestPostmanHandlebarsBodyIsNotCorrupted(t *testing.T) {
	res, err := Import(handlebarsCollection)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	req := res.Requests[0]
	if req.Body == nil {
		t.Fatal("body dropped")
	}
	body := req.Body.Text

	// The template survives byte for byte.
	for _, want := range []string{"{{#each items}}", "{{this}}", "{{/each}}"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lost %q: %s", want, body)
		}
	}
	// Specifically NOT the old corruption.
	if strings.Contains(body, "${this}") {
		t.Errorf("{{this}} was rewritten to ${this} — the payload can no longer send: %s", body)
	}
	// The collection's OWN variable still converts, in the same body.
	if !strings.Contains(body, `"base":"${baseUrl}"`) {
		t.Errorf("known collection variable not converted inside the template body: %s", body)
	}
	// And everywhere else.
	if req.URL != "${baseUrl}/render" {
		t.Errorf("URL = %q, want ${baseUrl}/render", req.URL)
	}
	if req.Headers[0].Value != "${baseUrl}" {
		t.Errorf("header = %q, want ${baseUrl}", req.Headers[0].Value)
	}
}

// The imported Handlebars body must actually RESOLVE — the proof that the
// request can still be sent, which is what the corruption broke.
func TestPostmanHandlebarsBodyStillResolves(t *testing.T) {
	res, err := Import(handlebarsCollection)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	req := res.Requests[0]
	env := res.Environments[0]

	eng := templating.New(nil)
	resolved, err := eng.Resolve(context.Background(), req, &env, nil)
	if err != nil {
		t.Fatalf("resolve imported request: %v", err)
	}
	if resolved.Body == nil {
		t.Fatal("resolved request has no body")
	}
	body := resolved.Body.Text
	if !strings.Contains(body, "{{#each items}}{{this}}{{/each}}") {
		t.Errorf("resolved body lost the template: %s", body)
	}
	if !strings.Contains(body, `"base":"https://api.example.com"`) {
		t.Errorf("resolved body did not substitute baseUrl: %s", body)
	}
}

func TestPostmanTripleStashUntouched(t *testing.T) {
	const col = `{
      "info": {"name": "Triple", "_postman_id": "t1"},
      "item": [{
        "name": "Render",
        "request": {
          "method": "POST",
          "url": {"raw": "https://api.example.com/render"},
          "body": {"mode": "raw", "raw": "{\"html\":\"{{{name}}}\"}"}
        }
      }]
    }`
	res, err := Import(col)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	body := res.Requests[0].Body.Text
	if !strings.Contains(body, "{{{name}}}") {
		t.Errorf("triple stash mangled: %s", body)
	}
	if strings.Contains(body, "${name}") {
		t.Errorf("triple stash produced a template ref: %s", body)
	}
}

// A KNOWN collection variable converts in every field, and a $-dynamic always
// converts — including in a body that is otherwise a template.
func TestPostmanKnownAndDynamicVarsAlwaysConvert(t *testing.T) {
	const col = `{
      "info": {"name": "Known", "_postman_id": "k1"},
      "variable": [{"key": "baseUrl", "value": "https://api.example.com"}],
      "item": [{
        "name": "Create",
        "request": {
          "method": "POST",
          "url": {"raw": "{{baseUrl}}/users/:id?tenant={{baseUrl}}", "variable": [{"key":"id","value":"{{baseUrl}}"}]},
          "header": [{"key": "X-Base", "value": "{{baseUrl}}"}, {"key": "X-Req", "value": "{{$randomInt}}"}],
          "auth": {"type": "bearer", "bearer": [{"key": "token", "value": "{{baseUrl}}"}]},
          "body": {"mode": "raw", "raw": "{\"a\":\"{{#each x}}{{baseUrl}}{{$guid}}{{unknownVar}}{{/each}}\"}"}
        }
      }]
    }`
	res, err := Import(col)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	req := res.Requests[0]

	if req.URL != "${baseUrl}/users/:id?tenant=${baseUrl}" {
		t.Errorf("URL = %q", req.URL)
	}
	if req.Headers[0].Value != "${baseUrl}" {
		t.Errorf("header = %q", req.Headers[0].Value)
	}
	if req.Headers[1].Value != "${randomInt}" {
		t.Errorf("dynamic header = %q, want ${randomInt}", req.Headers[1].Value)
	}
	if len(req.PathParams) != 1 || req.PathParams[0].Value != "${baseUrl}" {
		t.Errorf("path param = %+v", req.PathParams)
	}
	if req.Auth == nil || req.Auth.Bearer == nil || req.Auth.Bearer.Token != "${baseUrl}" {
		t.Errorf("auth = %+v", req.Auth)
	}
	body := req.Body.Text
	if !strings.Contains(body, "${baseUrl}") || !strings.Contains(body, "${guid}") {
		t.Errorf("known/dynamic vars not converted inside a template body: %s", body)
	}
	// ...while the unknown one beside them stays literal.
	if !strings.Contains(body, "{{unknownVar}}") {
		t.Errorf("unknown var inside a template body was rewritten: %s", body)
	}
}

// An unknown name in a string with NO template markers is still converted:
// that's a variable the user keeps in a Postman ENVIRONMENT, and the whole
// reason the conversion exists.
func TestPostmanUnknownVarStillConvertsOutsideTemplates(t *testing.T) {
	const col = `{
      "info": {"name": "EnvOnly", "_postman_id": "e1"},
      "item": [{
        "name": "Get",
        "request": {
          "method": "GET",
          "url": {"raw": "{{host}}/v1/things"},
          "header": [{"key": "Authorization", "value": "Bearer {{token}}"}],
          "body": {"mode": "raw", "raw": "{\"id\":\"{{userId}}\"}"}
        }
      }]
    }`
	res, err := Import(col)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	req := res.Requests[0]
	if req.URL != "${host}/v1/things" {
		t.Errorf("URL = %q, want ${host}/v1/things", req.URL)
	}
	if req.Headers[0].Value != "Bearer ${token}" {
		t.Errorf("header = %q", req.Headers[0].Value)
	}
	if !strings.Contains(req.Body.Text, "${userId}") {
		t.Errorf("body = %q", req.Body.Text)
	}
}

// Unit coverage of the rule table, including the shapes where converting
// would break the payload.
func TestKnownVarsConvert(t *testing.T) {
	known := knownVars{"baseUrl": true, "userId": true, "this": true}

	cases := []struct{ in, want, why string }{
		{"{{baseUrl}}/x", "${baseUrl}/x", "known variable"},
		{"{{$randomInt}}", "${randomInt}", "dynamic variable loses its $"},
		{"{{$timestamp}}", "${timestamp}", "dynamic timestamp"},
		{"{{{name}}}", "{{{name}}}", "triple stash untouched"},
		{"a {{{name}}} b", "a {{{name}}} b", "triple stash mid-string"},
		{"{{#each items}}{{unknown}}{{/each}}", "{{#each items}}{{unknown}}{{/each}}", "unknown inside a template"},
		{"{{#each items}}{{baseUrl}}{{/each}}", "{{#each items}}${baseUrl}{{/each}}", "known inside a template"},
		{"Hello {{this}}", "Hello ${this}", "a collection that really defines `this` wins"},
		{"{{unknown}}", "${unknown}", "unknown outside a template converts"},
		{`{"x":{{userId}}}`, `{"x":${userId}}`, "trailing JSON brace is not a triple stash"},
		{"{{9lives}}", "{{9lives}}", "leading digit is not a variable name"},
		{"{{ baseUrl }}", "${baseUrl}", "inner whitespace tolerated"},
		{"{% for x in y %}{{x}}{% endfor %}", "{% for x in y %}{{x}}{% endfor %}", "Liquid template"},
		{"{{! a comment }}{{unknown}}", "{{! a comment }}{{unknown}}", "Mustache comment marks a template"},
		{"no braces here", "no braces here", "untouched"},
	}
	for _, c := range cases {
		if got := known.convert(c.in); got != c.want {
			t.Errorf("convert(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}

	// With no known set, a Handlebars built-in is still left alone.
	if got := convertPostmanVars("Hello {{this}}"); got != "Hello {{this}}" {
		t.Errorf("convertPostmanVars(%q) = %q, want it untouched", "Hello {{this}}", got)
	}
}

// ---------------------------------------------------------------------------
// Postman $timestamp / $isoTimestamp (finding 4)
// ---------------------------------------------------------------------------

// The `$`-stripping conversion lands {{$timestamp}} on ${timestamp}, which
// matched no registered function — so every imported request carrying
// `X-Request-Time: {{$timestamp}}` failed to send with
// `unresolved variable "timestamp"`.
func TestPostmanTimestampImportsAndResolves(t *testing.T) {
	const col = `{
      "info": {"name": "Stamps", "_postman_id": "s1"},
      "item": [{
        "name": "Ping",
        "request": {
          "method": "GET",
          "url": {"raw": "https://api.example.com/ping"},
          "header": [
            {"key": "X-Request-Time", "value": "{{$timestamp}}"},
            {"key": "X-Request-Iso", "value": "{{$isoTimestamp}}"},
            {"key": "X-Request-Id", "value": "{{$guid}}"},
            {"key": "X-Request-Uuid", "value": "{{$randomUUID}}"},
            {"key": "X-Request-Nonce", "value": "{{$randomInt}}"}
          ]
        }
      }]
    }`
	res, err := Import(col)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	req := res.Requests[0]
	wantRefs := []string{"${timestamp}", "${isoTimestamp}", "${guid}", "${randomUUID}", "${randomInt}"}
	for i, want := range wantRefs {
		if req.Headers[i].Value != want {
			t.Errorf("header %d = %q, want %q", i, req.Headers[i].Value, want)
		}
	}

	resolved, err := templating.New(nil).Resolve(context.Background(), req, nil, nil)
	if err != nil {
		t.Fatalf("imported request does not resolve: %v", err)
	}
	byName := map[string]string{}
	for _, h := range resolved.Headers {
		byName[h.Key] = h.Value
	}

	secs, err := strconv.ParseInt(byName["X-Request-Time"], 10, 64)
	if err != nil {
		t.Fatalf("X-Request-Time = %q, want unix seconds: %v", byName["X-Request-Time"], err)
	}
	if delta := time.Since(time.Unix(secs, 0)); delta < -time.Minute || delta > time.Minute {
		t.Errorf("X-Request-Time = %d, not within a minute of now", secs)
	}
	if _, err := time.Parse(time.RFC3339, byName["X-Request-Iso"]); err != nil {
		t.Errorf("X-Request-Iso = %q, want RFC3339: %v", byName["X-Request-Iso"], err)
	}
	for _, h := range []string{"X-Request-Id", "X-Request-Uuid", "X-Request-Nonce"} {
		if v := byName[h]; v == "" || strings.Contains(v, "${") {
			t.Errorf("%s = %q, want a resolved value", h, v)
		}
	}
}
