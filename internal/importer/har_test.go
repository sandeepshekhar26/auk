package importer

import (
	"strings"
	"testing"

	"apitool/internal/core/model"
)

// harSample is a realistic HAR 1.2 recording: a page, an HTTP/2 pseudo-header,
// a Bearer Authorization header, cookies, a query string, a DUPLICATE GET
// (the same URL is hit twice — HARs are full of these), and a POST with a
// JSON body.
const harSample = `{
  "log": {
    "version": "1.2",
    "creator": { "name": "Firefox", "version": "128.0" },
    "pages": [{ "id": "page_1", "title": "Example App" }],
    "entries": [
      {
        "pageref": "page_1",
        "request": {
          "method": "GET",
          "url": "https://api.example.com/users?limit=10",
          "headers": [
            { "name": ":authority", "value": "api.example.com" },
            { "name": "Authorization", "value": "Bearer abc123" },
            { "name": "Accept", "value": "application/json" }
          ],
          "queryString": [{ "name": "limit", "value": "10" }],
          "cookies": [{ "name": "sid", "value": "xyz" }]
        }
      },
      {
        "pageref": "page_1",
        "request": {
          "method": "GET",
          "url": "https://api.example.com/users?limit=10",
          "headers": [{ "name": "Accept", "value": "application/json" }],
          "queryString": [{ "name": "limit", "value": "10" }]
        }
      },
      {
        "pageref": "page_1",
        "request": {
          "method": "POST",
          "url": "https://api.example.com/users",
          "headers": [{ "name": "Content-Type", "value": "application/json" }],
          "postData": { "mimeType": "application/json", "text": "{\"name\":\"Ada\"}" }
        }
      }
    ]
  }
}`

func TestParseHAR(t *testing.T) {
	res, err := ParseHAR([]byte(harSample))
	if err != nil {
		t.Fatalf("ParseHAR: %v", err)
	}

	if res.WorkspaceName != "api.example.com (HAR)" {
		t.Errorf("workspace name = %q", res.WorkspaceName)
	}
	// Two identical GETs collapse: 2 requests (deduped GET + POST).
	if len(res.Requests) != 2 {
		t.Fatalf("expected 2 requests after dedupe, got %d", len(res.Requests))
	}
	// One folder from the HAR page, owning both requests.
	if len(res.Folders) != 1 || res.Folders[0].Name != "Example App" {
		t.Fatalf("expected one 'Example App' folder, got %+v", res.Folders)
	}
	for _, r := range res.Requests {
		if r.FolderID == nil || *r.FolderID != res.Folders[0].ID {
			t.Errorf("request %q not in the page folder", r.Name)
		}
	}

	var get, post *model.RequestDef
	for i := range res.Requests {
		switch res.Requests[i].Method {
		case "GET":
			get = &res.Requests[i]
		case "POST":
			post = &res.Requests[i]
		}
	}
	if get == nil || post == nil {
		t.Fatalf("expected a GET and a POST, got %+v", res.Requests)
	}

	// Query string moved to Params; URL stripped of its query.
	if get.URL != "https://api.example.com/users" {
		t.Errorf("GET url = %q, want query stripped", get.URL)
	}
	if len(get.Params) != 1 || get.Params[0].Key != "limit" || get.Params[0].Value != "10" {
		t.Errorf("GET params = %+v, want limit=10", get.Params)
	}
	// Bearer Authorization mapped to an auth kind and removed from headers.
	if get.Auth == nil || get.Auth.Kind != model.AuthBearer || get.Auth.Bearer.Token != "abc123" {
		t.Errorf("GET bearer auth wrong: %+v", get.Auth)
	}
	for _, h := range get.Headers {
		if strings.EqualFold(h.Key, "authorization") {
			t.Errorf("Authorization should have been mapped to auth, still a header: %+v", h)
		}
		if strings.HasPrefix(h.Key, ":") {
			t.Errorf("HTTP/2 pseudo-header leaked into headers: %+v", h)
		}
	}
	// Cookie synthesized from the cookies array.
	if got := headerValue(get.Headers, "Cookie"); got != "sid=xyz" {
		t.Errorf("Cookie header = %q, want sid=xyz", got)
	}
	// Dedup note recorded.
	if !strings.Contains(get.Description, "2 identical") {
		t.Errorf("expected a dedupe note in Description, got %q", get.Description)
	}

	// POST body imported as JSON.
	if post.Body == nil || post.Body.Kind != model.BodyJSON || !strings.Contains(post.Body.Text, "Ada") {
		t.Errorf("POST body wrong: %+v", post.Body)
	}

	assertValidOrderKeys(t, res)
}

func TestDetectHAR(t *testing.T) {
	if got := Detect(harSample); got != FormatHAR {
		t.Errorf("Detect(HAR) = %q, want %q", got, FormatHAR)
	}
}

// headerValue returns the value of the first header whose key case-insensitively
// matches name, or "".
func headerValue(hs []model.KeyValue, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Key, name) {
			return h.Value
		}
	}
	return ""
}

// assertValidOrderKeys checks every folder/request order key is non-empty and
// never ends in the alphabet's lowest digit '0' (the merge-safety guard the
// importers share — see newOrderMinter).
func assertValidOrderKeys(t *testing.T, res ImportResult) {
	t.Helper()
	check := func(what, key string) {
		if key == "" {
			t.Errorf("%s has an empty order key", what)
		}
		if strings.HasSuffix(key, "0") {
			t.Errorf("%s order key %q ends in '0' (not merge-safe)", what, key)
		}
	}
	for _, f := range res.Folders {
		check("folder "+f.Name, f.OrderKey)
	}
	for _, r := range res.Requests {
		check("request "+r.Name, r.OrderKey)
	}
}
