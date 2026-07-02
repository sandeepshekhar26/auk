package importer

import (
	"strings"
	"testing"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

func TestParseCurl(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantMethod string
		wantURL    string
		wantErr    bool
		check      func(t *testing.T, req model.RequestDef)
	}{
		{
			name:       "simple GET",
			command:    `curl https://api.example.com/users`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/users",
		},
		{
			name:       "method flag with headers",
			command:    `curl -X POST https://api.example.com/users -H "Content-Type: application/json" -H "Accept: application/json"`,
			wantMethod: "POST",
			wantURL:    "https://api.example.com/users",
			check: func(t *testing.T, req model.RequestDef) {
				wantHeaders := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
				if len(req.Headers) != 2 {
					t.Fatalf("expected 2 headers, got %d: %+v", len(req.Headers), req.Headers)
				}
				for _, h := range req.Headers {
					if wantHeaders[h.Key] != h.Value {
						t.Fatalf("unexpected header %s=%s", h.Key, h.Value)
					}
				}
			},
		},
		{
			name:       "data implies POST",
			command:    `curl https://api.example.com/users --data '{"name":"alice"}'`,
			wantMethod: "POST",
			wantURL:    "https://api.example.com/users",
			check: func(t *testing.T, req model.RequestDef) {
				if req.Body == nil || req.Body.Kind != model.BodyJSON {
					t.Fatalf("expected JSON body, got %+v", req.Body)
				}
				if req.Body.Text != `{"name":"alice"}` {
					t.Fatalf("unexpected body text: %q", req.Body.Text)
				}
			},
		},
		{
			name:       "form data",
			command:    `curl -X POST https://api.example.com/login -d 'user=alice' -d 'pass=secret'`,
			wantMethod: "POST",
			wantURL:    "https://api.example.com/login",
			check: func(t *testing.T, req model.RequestDef) {
				if req.Body == nil || req.Body.Kind != model.BodyForm {
					t.Fatalf("expected form body, got %+v", req.Body)
				}
				if req.Body.Text != "user=alice&pass=secret" {
					t.Fatalf("unexpected body text: %q", req.Body.Text)
				}
				if len(req.Body.FormFields) != 2 {
					t.Fatalf("expected 2 form fields, got %+v", req.Body.FormFields)
				}
			},
		},
		{
			name:       "basic auth via -u",
			command:    `curl -u alice:s3cr3t https://api.example.com/secure`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/secure",
			check: func(t *testing.T, req model.RequestDef) {
				if req.Auth == nil || req.Auth.Kind != model.AuthBasic {
					t.Fatalf("expected basic auth, got %+v", req.Auth)
				}
				if req.Auth.Basic.Username != "alice" || req.Auth.Basic.Password != "s3cr3t" {
					t.Fatalf("unexpected basic auth creds: %+v", req.Auth.Basic)
				}
			},
		},
		{
			name:       "cookie flag",
			command:    `curl -b "session=abc123; theme=dark" https://api.example.com/me`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/me",
			check: func(t *testing.T, req model.RequestDef) {
				found := false
				for _, h := range req.Headers {
					if h.Key == "Cookie" && h.Value == "session=abc123; theme=dark" {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected Cookie header, got %+v", req.Headers)
				}
			},
		},
		{
			name:       "-G turns data into query params",
			command:    `curl -G https://api.example.com/search --data 'q=golang' --data 'page=2'`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/search?q=golang&page=2",
		},
		{
			name: "Chrome DevTools style copy as cURL",
			command: `curl 'https://api.example.com/graphql' ` +
				`-H 'authority: api.example.com' ` +
				`-H 'accept: */*' ` +
				`-H 'accept-language: en-US,en;q=0.9' ` +
				`-H 'content-type: application/json' ` +
				`-H 'origin: https://example.com' ` +
				`-H 'referer: https://example.com/' ` +
				`-H 'sec-ch-ua: "Chromium";v="120"' ` +
				`-H 'sec-fetch-mode: cors' ` +
				`-H 'user-agent: Mozilla/5.0' ` +
				`--data-raw '{"query":"{ viewer { id } }"}' ` +
				`--compressed`,
			wantMethod: "POST",
			wantURL:    "https://api.example.com/graphql",
			check: func(t *testing.T, req model.RequestDef) {
				if req.Body == nil || req.Body.Kind != model.BodyJSON {
					t.Fatalf("expected JSON body, got %+v", req.Body)
				}
				if req.Body.Text != `{"query":"{ viewer { id } }"}` {
					t.Fatalf("unexpected body: %q", req.Body.Text)
				}
				wantCT := false
				for _, h := range req.Headers {
					if h.Key == "content-type" && h.Value == "application/json" {
						wantCT = true
					}
				}
				if !wantCT {
					t.Fatalf("expected content-type header preserved, got %+v", req.Headers)
				}
				if len(req.Headers) != 9 {
					t.Fatalf("expected 9 headers, got %d: %+v", len(req.Headers), req.Headers)
				}
			},
		},
		{
			name: "multiline with backslash continuations",
			command: "curl --location 'https://api.example.com/orders' \\\n" +
				"--header 'Authorization: Bearer abc.def.ghi' \\\n" +
				"--header 'Content-Type: application/json' \\\n" +
				"--data '{\"id\":42}'",
			wantMethod: "POST",
			wantURL:    "https://api.example.com/orders",
			check: func(t *testing.T, req model.RequestDef) {
				found := false
				for _, h := range req.Headers {
					if h.Key == "Authorization" && h.Value == "Bearer abc.def.ghi" {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected Authorization header, got %+v", req.Headers)
				}
			},
		},
		{
			name:       "long-form --request and --header= style",
			command:    `curl --request PUT --header=Content-Type:text/plain https://api.example.com/note --data-binary 'hello world'`,
			wantMethod: "PUT",
			wantURL:    "https://api.example.com/note",
			check: func(t *testing.T, req model.RequestDef) {
				if req.Body == nil || req.Body.Kind != model.BodyBinary {
					t.Fatalf("expected binary body, got %+v", req.Body)
				}
				if req.Body.Text != "hello world" {
					t.Fatalf("unexpected body: %q", req.Body.Text)
				}
			},
		},
		{
			name:    "no URL is an error",
			command: `curl -X POST -H "Content-Type: application/json"`,
			wantErr: true,
		},
		{
			name:    "unterminated quote is an error",
			command: `curl https://api.example.com -H "Content-Type: application/json`,
			wantErr: true,
		},
		{
			name:       "unknown flag is skipped without consuming the URL",
			command:    `curl --fail --silent-fail https://api.example.com/ping`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/ping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := ParseCurl(tt.command)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none (req=%+v)", req)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Method != tt.wantMethod {
				t.Fatalf("method: got %q, want %q", req.Method, tt.wantMethod)
			}
			if req.URL != tt.wantURL {
				t.Fatalf("url: got %q, want %q", req.URL, tt.wantURL)
			}
			if req.Protocol != model.ProtocolHTTP {
				t.Fatalf("protocol: got %q, want %q", req.Protocol, model.ProtocolHTTP)
			}
			if tt.check != nil {
				tt.check(t, req)
			}
		})
	}
}

func TestToCurl(t *testing.T) {
	req := model.RequestDef{Method: "POST", URL: "https://api.example.com/users"}
	resolved := core.ResolvedRequest{
		Method: "POST",
		URL:    "https://api.example.com/users",
		Headers: []model.KeyValue{
			{Key: "Content-Type", Value: "application/json", Enabled: true},
			{Key: "X-Disabled", Value: "nope", Enabled: false},
		},
		Body: &model.RequestBody{Kind: model.BodyJSON, Text: `{"name":"alice"}`},
	}

	got := ToCurl(req, resolved)

	if !strings.HasPrefix(got, "curl -X POST ") {
		t.Fatalf("expected curl -X POST prefix, got %q", got)
	}
	if !strings.Contains(got, "https://api.example.com/users") {
		t.Fatalf("expected URL present, got %q", got)
	}
	if !strings.Contains(got, "-H 'Content-Type: application/json'") {
		t.Fatalf("expected Content-Type header quoted, got %q", got)
	}
	if strings.Contains(got, "X-Disabled") {
		t.Fatalf("disabled header should not appear, got %q", got)
	}
	if !strings.Contains(got, `-d '{"name":"alice"}'`) {
		t.Fatalf("expected -d body, got %q", got)
	}
}

func TestToCurl_GetOmitsMethodFlag(t *testing.T) {
	req := model.RequestDef{Method: "GET", URL: "https://api.example.com/ping"}
	resolved := core.ResolvedRequest{Method: "GET", URL: "https://api.example.com/ping"}

	got := ToCurl(req, resolved)

	if strings.Contains(got, "-X") {
		t.Fatalf("expected no -X flag for GET, got %q", got)
	}
	want := "curl https://api.example.com/ping"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestToCurl_QueryParamsAppended(t *testing.T) {
	req := model.RequestDef{Method: "GET", URL: "https://api.example.com/search"}
	resolved := core.ResolvedRequest{
		Method: "GET",
		URL:    "https://api.example.com/search",
		Params: []model.KeyValue{
			{Key: "q", Value: "golang", Enabled: true},
			{Key: "page", Value: "2", Enabled: true},
			{Key: "debug", Value: "1", Enabled: false},
		},
	}

	got := ToCurl(req, resolved)

	if !strings.Contains(got, "q=golang") || !strings.Contains(got, "page=2") {
		t.Fatalf("expected query params in URL, got %q", got)
	}
	if strings.Contains(got, "debug=1") {
		t.Fatalf("disabled param should not appear, got %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "GET with headers",
			command: `curl -X GET https://api.example.com/users -H 'Accept: application/json' -H 'X-Trace-Id: abc-123'`,
		},
		{
			name:    "POST with JSON body",
			command: `curl -X POST https://api.example.com/orders -H 'Content-Type: application/json' -d '{"id":42,"item":"widget"}'`,
		},
		{
			name:    "PUT with special characters requiring quoting",
			command: `curl -X PUT https://api.example.com/note -H 'X-Note: has space & ampersand' -d 'raw body with spaces'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := ParseCurl(tt.command)
			if err != nil {
				t.Fatalf("first ParseCurl failed: %v", err)
			}

			resolved := core.ResolvedRequest{
				Method:  first.Method,
				URL:     first.URL,
				Headers: first.Headers,
				Body:    first.Body,
			}

			rendered := ToCurl(first, resolved)

			second, err := ParseCurl(rendered)
			if err != nil {
				t.Fatalf("second ParseCurl failed on rendered command %q: %v", rendered, err)
			}

			if first.Method != second.Method {
				t.Fatalf("method mismatch after round trip: %q vs %q", first.Method, second.Method)
			}
			if first.URL != second.URL {
				t.Fatalf("url mismatch after round trip: %q vs %q", first.URL, second.URL)
			}
			if len(first.Headers) != len(second.Headers) {
				t.Fatalf("header count mismatch after round trip: %+v vs %+v", first.Headers, second.Headers)
			}
			for _, h := range first.Headers {
				matched := false
				for _, h2 := range second.Headers {
					if h.Key == h2.Key && h.Value == h2.Value {
						matched = true
					}
				}
				if !matched {
					t.Fatalf("header %+v missing after round trip; second=%+v", h, second.Headers)
				}
			}
			if (first.Body == nil) != (second.Body == nil) {
				t.Fatalf("body presence mismatch after round trip: %+v vs %+v", first.Body, second.Body)
			}
			if first.Body != nil && second.Body != nil && first.Body.Text != second.Body.Text {
				t.Fatalf("body text mismatch after round trip: %q vs %q", first.Body.Text, second.Body.Text)
			}
		})
	}
}
