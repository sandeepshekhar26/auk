package exporter

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"apitool/internal/core/model"
)

// fakeStore is an in-memory WorkspaceSource for testing the OpenAPI export
// without a real FileStore. ListEnvironmentsRaw returns environments exactly
// as set — the test plants a real secret VALUE in Variables (with the name
// listed in Secrets) precisely to prove the export never surfaces it.
type fakeStore struct {
	workspaces   []model.Workspace
	folders      []model.Folder
	requests     []model.RequestDef
	environments []model.Environment
}

func (f *fakeStore) ListWorkspaces() []model.Workspace              { return f.workspaces }
func (f *fakeStore) ListFolders(string) []model.Folder              { return f.folders }
func (f *fakeStore) ListRequests(string) []model.RequestDef         { return f.requests }
func (f *fakeStore) ListEnvironmentsRaw(string) []model.Environment { return f.environments }

// Planted secret values that MUST never appear anywhere in the export.
const (
	secretBearer   = "SECRET-BEARER-TOKEN-DoNotLeak"
	secretAPIKey   = "SECRET-APIKEY-VALUE-DoNotLeak"
	secretBasicPw  = "SECRET-BASIC-PW-DoNotLeak"
	secretClientID = "SECRET-OAUTH2-CLIENTID-DoNotLeak"
	secretClientSc = "SECRET-OAUTH2-CLIENTSECRET-DoNotLeak"
	secretDigestPw = "SECRET-DIGEST-PW-DoNotLeak"
	secretHeader   = "SECRET-HEADER-AUTH-DoNotLeak"
	secretEnvValue = "SECRET-ENV-VALUE-DoNotLeak"
	// Userinfo planted in a raw request URL and in an environment value that
	// stands in for a host — `https://user:pass@host/path` is a legal request
	// URL people really do paste in.
	secretURLUser  = "SECRET-URL-USER-DoNotLeak"
	secretURLPw    = "SECRET-URL-PASSWORD-DoNotLeak"
	secretEnvURLPw = "SECRET-ENVURL-PASSWORD-DoNotLeak"
)

func ptr(s string) *string { return &s }

func demoStore() *fakeStore {
	const fUsers, fAuth = "folder-users", "folder-auth"
	return &fakeStore{
		workspaces: []model.Workspace{{ID: "ws1", Name: "Acme API"}},
		folders: []model.Folder{
			{ID: fUsers, WorkspaceID: "ws1", Name: "Users"},
			{ID: fAuth, WorkspaceID: "ws1", Name: "Auth"},
		},
		requests: []model.RequestDef{
			{
				ID: "r1", WorkspaceID: "ws1", FolderID: ptr(fUsers),
				Name:        "Get User",
				Description: "Fetch a single user by id.",
				Protocol:    model.ProtocolHTTP, Method: "GET",
				URL: "${baseUrl}/users/:id",
				Params: []model.KeyValue{
					{Key: "expand", Value: "profile", Enabled: true},
				},
				Headers: []model.KeyValue{
					{Key: "X-Request-Id", Value: "abc", Enabled: true},
					// Authorization must be dropped AND its value must not leak.
					{Key: "Authorization", Value: "Bearer " + secretHeader, Enabled: true},
					{Key: "Connection", Value: "keep-alive", Enabled: true}, // hop-by-hop
				},
				Auth: &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: secretBearer}},
			},
			{
				ID: "r2", WorkspaceID: "ws1", FolderID: ptr(fUsers),
				Name:     "List Users",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL: "${baseUrl}/users",
				Params: []model.KeyValue{
					{Key: "limit", Value: "10", Enabled: true},
					{Key: "api_key", Value: "", Enabled: true}, // covered by apiKey security -> skipped
				},
				Auth: &model.AuthConfig{Kind: model.AuthAPIKey, APIKey: &model.APIKeyAuth{
					Key: "api_key", Value: secretAPIKey, In: model.APIKeyInQuery,
				}},
			},
			{
				ID: "r3", WorkspaceID: "ws1", FolderID: ptr(fUsers),
				Name:     "Create User",
				Protocol: model.ProtocolHTTP, Method: "POST",
				URL:  "${baseUrl}/users",
				Body: &model.RequestBody{Kind: model.BodyJSON, Text: `{"name":"Ada","age":36,"admin":true,"tags":["x","y"]}`},
				Auth: &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{Username: "admin", Password: secretBasicPw}},
			},
			{
				ID: "r4", WorkspaceID: "ws1", FolderID: ptr(fAuth),
				Name:     "Login",
				Protocol: model.ProtocolHTTP, Method: "POST",
				URL: "${baseUrl}/auth/login",
				Body: &model.RequestBody{Kind: model.BodyForm, FormFields: []model.KeyValue{
					{Key: "username", Value: "ada", Enabled: true},
					{Key: "password", Value: "hunter2", Enabled: true},
				}},
			},
			{
				ID: "r5", WorkspaceID: "ws1", FolderID: ptr(fAuth),
				Name:     "Get Token",
				Protocol: model.ProtocolHTTP, Method: "POST",
				URL: "${baseUrl}/auth/token",
				Auth: &model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: &model.OAuth2Auth{
					ClientID: secretClientID, ClientSecret: secretClientSc,
					TokenURL: "https://auth.example.com/oauth/token",
					Scopes:   []string{"read", "write"},
				}},
			},
			{
				ID: "r6", WorkspaceID: "ws1", FolderID: ptr(fAuth),
				Name:     "Admin Ping",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL:  "${baseUrl}/admin/ping",
				Auth: &model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{Username: "root", Password: secretDigestPw}},
			},
			{
				// gRPC request must be skipped entirely (no REST path shape).
				ID: "r7", WorkspaceID: "ws1", FolderID: ptr(fAuth),
				Name:     "Grpc Thing",
				Protocol: model.ProtocolGRPC, Method: "POST",
				URL: "grpc.example.com:443",
			},
		},
		environments: []model.Environment{{
			ID: "e1", WorkspaceID: "ws1", Name: "Production",
			Variables: []model.KeyValue{
				{Key: "baseUrl", Value: "https://api.example.com", Enabled: true},
				// A real secret value planted via the RAW path — must never
				// reach the spec.
				{Key: "token", Value: secretEnvValue, Enabled: true},
			},
			Secrets: []string{"token"},
		}},
	}
}

// allSecrets is every string that must be absent from the exported document.
func allSecrets() []string {
	return []string{
		secretBearer, secretAPIKey, secretBasicPw, secretClientID,
		secretClientSc, secretDigestPw, secretHeader, secretEnvValue,
		secretURLUser, secretURLPw, secretEnvURLPw,
	}
}

// credentialURLStore is a workspace whose credentials live in the URL itself
// rather than in an Auth block — the shape that used to walk straight into
// `servers[].url`. Kept separate from demoStore so the single-server
// assertions there stay meaningful.
func credentialURLStore() *fakeStore {
	return &fakeStore{
		workspaces: []model.Workspace{{ID: "ws1", Name: "Creds In URL"}},
		requests: []model.RequestDef{
			{
				ID: "c1", WorkspaceID: "ws1", Name: "Get Things",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL: "https://" + secretURLUser + ":" + secretURLPw + "@api.example.com/v1/things",
			},
			{
				// Password-less userinfo, and a non-default port, both of which
				// must survive the strip in the right way.
				ID: "c2", WorkspaceID: "ws1", Name: "Get Widgets",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL: "https://" + secretURLUser + "@svc.example.com:8443/v1/widgets",
			},
			{
				// Scheme-relative form.
				ID: "c3", WorkspaceID: "ws1", Name: "Get Gadgets",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL: "//" + secretURLUser + ":" + secretURLPw + "@rel.example.com/v1/gadgets",
			},
			{
				// A templated host whose environment value carries userinfo.
				ID: "c4", WorkspaceID: "ws1", Name: "Get Via Env",
				Protocol: model.ProtocolHTTP, Method: "GET",
				URL: "${apiBase}/v1/env",
			},
		},
		environments: []model.Environment{{
			ID: "e1", WorkspaceID: "ws1", Name: "Production",
			Variables: []model.KeyValue{
				{Key: "apiBase", Value: "https://envuser:" + secretEnvURLPw + "@env.example.com", Enabled: true},
			},
		}},
	}
}

func TestExportOpenAPI_ParsesAsJSONAndYAML(t *testing.T) {
	store := demoStore()

	jsonBytes, err := ExportOpenAPI(store, "ws1")
	if err != nil {
		t.Fatalf("ExportOpenAPI: %v", err)
	}
	var jdoc map[string]any
	if err := json.Unmarshal(jsonBytes, &jdoc); err != nil {
		t.Fatalf("JSON export is not valid JSON: %v", err)
	}
	if jdoc["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %v, want 3.1.0", jdoc["openapi"])
	}

	yamlBytes, err := ExportOpenAPIYAML(store, "ws1")
	if err != nil {
		t.Fatalf("ExportOpenAPIYAML: %v", err)
	}
	var ydoc map[string]any
	if err := yaml.Unmarshal(yamlBytes, &ydoc); err != nil {
		t.Fatalf("YAML export is not valid YAML: %v", err)
	}
	if ydoc["openapi"] != "3.1.0" {
		t.Fatalf("yaml openapi version = %v, want 3.1.0", ydoc["openapi"])
	}

	info, _ := jdoc["info"].(map[string]any)
	if info["title"] != "Acme API" {
		t.Fatalf("info.title = %v, want Acme API", info["title"])
	}
	if info["version"] == nil || info["version"] == "" {
		t.Fatalf("info.version is required and must be non-empty")
	}
}

func TestExportOpenAPI_PathsAndParameters(t *testing.T) {
	doc := parseJSONExport(t, demoStore())
	paths, _ := doc["paths"].(map[string]any)

	for _, want := range []string{"/users/{id}", "/users", "/auth/login", "/auth/token", "/admin/ping"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("missing path %q; got paths: %v", want, keys(paths))
		}
	}
	// gRPC request must NOT have produced a path.
	if _, ok := paths["grpc.example.com:443"]; ok {
		t.Fatalf("gRPC request leaked into paths")
	}

	// /users/{id} must carry a path-level {id} parameter, required, in path.
	item := paths["/users/{id}"].(map[string]any)
	pparams, _ := item["parameters"].([]any)
	if len(pparams) != 1 {
		t.Fatalf("/users/{id} path params = %v, want exactly [id]", pparams)
	}
	p0 := pparams[0].(map[string]any)
	if p0["name"] != "id" || p0["in"] != "path" || p0["required"] != true {
		t.Fatalf("path param wrong: %v", p0)
	}

	// GET /users/{id}: query param expand, header X-Request-Id; Authorization
	// and Connection dropped.
	get := item["get"].(map[string]any)
	opParams, _ := get["parameters"].([]any)
	var hasExpandQuery, hasReqIDHeader bool
	for _, raw := range opParams {
		p := raw.(map[string]any)
		switch {
		case p["in"] == "query" && p["name"] == "expand":
			hasExpandQuery = true
		case p["in"] == "header" && p["name"] == "X-Request-Id":
			hasReqIDHeader = true
		case p["name"] == "Authorization":
			t.Fatalf("Authorization must not be emitted as a parameter")
		case p["name"] == "Connection":
			t.Fatalf("hop-by-hop Connection must not be emitted as a parameter")
		}
	}
	if !hasExpandQuery {
		t.Fatalf("missing query param 'expand'; got %v", opParams)
	}
	if !hasReqIDHeader {
		t.Fatalf("missing header param 'X-Request-Id'; got %v", opParams)
	}

	// Operation metadata from the request.
	if get["operationId"] != "GetUser" {
		t.Fatalf("operationId = %v, want GetUser", get["operationId"])
	}
	if get["summary"] != "Get User" {
		t.Fatalf("summary = %v, want 'Get User'", get["summary"])
	}
	if get["description"] != "Fetch a single user by id." {
		t.Fatalf("description = %v", get["description"])
	}
	tags, _ := get["tags"].([]any)
	if len(tags) != 1 || tags[0] != "Users" {
		t.Fatalf("tags = %v, want [Users]", tags)
	}

	// apiKey-in-query name must be skipped from the plain query parameter list
	// on GET /users (it is covered by the security scheme instead).
	usersGet := paths["/users"].(map[string]any)["get"].(map[string]any)
	for _, raw := range asSlice(usersGet["parameters"]) {
		p := raw.(map[string]any)
		if p["name"] == "api_key" {
			t.Fatalf("api_key must be covered by security, not emitted as a query param")
		}
	}
}

func TestExportOpenAPI_RequestBodies(t *testing.T) {
	doc := parseJSONExport(t, demoStore())
	paths := doc["paths"].(map[string]any)

	// POST /users -> application/json with a schema inferring top-level types.
	post := paths["/users"].(map[string]any)["post"].(map[string]any)
	rb := post["requestBody"].(map[string]any)
	content := rb["content"].(map[string]any)
	jm, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("POST /users missing application/json body; got %v", keys(content))
	}
	schema := jm["schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("json body schema type = %v, want object", schema["type"])
	}
	props := schema["properties"].(map[string]any)
	if props["name"].(map[string]any)["type"] != "string" {
		t.Fatalf("field name should infer string")
	}
	if props["age"].(map[string]any)["type"] != "integer" {
		t.Fatalf("field age should infer integer, got %v", props["age"])
	}
	if props["admin"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("field admin should infer boolean")
	}
	if props["tags"].(map[string]any)["type"] != "array" {
		t.Fatalf("field tags should infer array")
	}
	// The example must be present and carry the actual values.
	ex := jm["example"].(map[string]any)
	if ex["name"] != "Ada" {
		t.Fatalf("example name = %v, want Ada", ex["name"])
	}

	// POST /auth/login -> form-urlencoded.
	login := paths["/auth/login"].(map[string]any)["post"].(map[string]any)
	lc := login["requestBody"].(map[string]any)["content"].(map[string]any)
	if _, ok := lc["application/x-www-form-urlencoded"]; !ok {
		t.Fatalf("login body should be form-urlencoded; got %v", keys(lc))
	}
}

func TestExportOpenAPI_SecuritySchemes(t *testing.T) {
	doc := parseJSONExport(t, demoStore())
	comps := doc["components"].(map[string]any)
	schemes := comps["securitySchemes"].(map[string]any)

	// Find the schemes by their shape (names are derived but content is fixed).
	var sawBasic, sawBearer, sawDigest, sawAPIKey, sawOAuth2 bool
	for _, raw := range schemes {
		s := raw.(map[string]any)
		switch {
		case s["type"] == "http" && s["scheme"] == "basic":
			sawBasic = true
		case s["type"] == "http" && s["scheme"] == "bearer":
			sawBearer = true
		case s["type"] == "http" && s["scheme"] == "digest":
			sawDigest = true
		case s["type"] == "apiKey":
			sawAPIKey = true
			if s["in"] != "query" || s["name"] != "api_key" {
				t.Fatalf("apiKey scheme wrong: %v", s)
			}
		case s["type"] == "oauth2":
			sawOAuth2 = true
			flows := s["flows"].(map[string]any)
			cc := flows["clientCredentials"].(map[string]any)
			if cc["tokenUrl"] != "https://auth.example.com/oauth/token" {
				t.Fatalf("oauth2 tokenUrl wrong: %v", cc["tokenUrl"])
			}
			sc := cc["scopes"].(map[string]any)
			if _, ok := sc["read"]; !ok {
				t.Fatalf("oauth2 scopes missing 'read': %v", sc)
			}
		}
	}
	if !(sawBasic && sawBearer && sawDigest && sawAPIKey && sawOAuth2) {
		t.Fatalf("missing a security scheme: basic=%v bearer=%v digest=%v apikey=%v oauth2=%v",
			sawBasic, sawBearer, sawDigest, sawAPIKey, sawOAuth2)
	}

	// Per-operation security references must be present.
	get := doc["paths"].(map[string]any)["/users/{id}"].(map[string]any)["get"].(map[string]any)
	sec := asSlice(get["security"])
	if len(sec) == 0 {
		t.Fatalf("GET /users/{id} has no security requirement")
	}
	req0 := sec[0].(map[string]any)
	if len(req0) != 1 {
		t.Fatalf("security requirement should reference exactly one scheme: %v", req0)
	}
	for name := range req0 {
		if _, ok := schemes[name]; !ok {
			t.Fatalf("security references undefined scheme %q", name)
		}
	}
}

func TestExportOpenAPI_ServersFromTemplatedURL(t *testing.T) {
	doc := parseJSONExport(t, demoStore())
	servers := asSlice(doc["servers"])
	if len(servers) != 1 {
		t.Fatalf("servers = %v, want exactly one ({baseUrl})", servers)
	}
	s0 := servers[0].(map[string]any)
	if s0["url"] != "{baseUrl}" {
		t.Fatalf("server url = %v, want {baseUrl}", s0["url"])
	}
	vars := s0["variables"].(map[string]any)
	bv := vars["baseUrl"].(map[string]any)
	// The default is drawn from the NON-secret env var, which round-trips with
	// the importer's servers[0] -> baseUrl behavior.
	if bv["default"] != "https://api.example.com" {
		t.Fatalf("baseUrl default = %v, want https://api.example.com", bv["default"])
	}
}

func TestExportOpenAPI_Tags(t *testing.T) {
	doc := parseJSONExport(t, demoStore())
	tags := asSlice(doc["tags"])
	got := map[string]bool{}
	for _, raw := range tags {
		got[raw.(map[string]any)["name"].(string)] = true
	}
	if !got["Users"] || !got["Auth"] {
		t.Fatalf("tags = %v, want to include Users and Auth", got)
	}
}

// TestExportOpenAPI_NeverLeaksAnyCredential is the security-critical test:
// NONE of the planted credential/secret strings may appear anywhere in the
// exported document, in either serialization.
func TestExportOpenAPI_NeverLeaksAnyCredential(t *testing.T) {
	stores := map[string]*fakeStore{
		"auth blocks":        demoStore(),
		"credentials in URL": credentialURLStore(),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			jsonBytes, err := ExportOpenAPI(store, "ws1")
			if err != nil {
				t.Fatalf("ExportOpenAPI: %v", err)
			}
			yamlBytes, err := ExportOpenAPIYAML(store, "ws1")
			if err != nil {
				t.Fatalf("ExportOpenAPIYAML: %v", err)
			}

			for _, secret := range allSecrets() {
				if strings.Contains(string(jsonBytes), secret) {
					t.Fatalf("JSON export leaked secret %q\n%s", secret, jsonBytes)
				}
				if strings.Contains(string(yamlBytes), secret) {
					t.Fatalf("YAML export leaked secret %q\n%s", secret, yamlBytes)
				}
			}
		})
	}
}

// TestExportOpenAPI_StripsURLUserinfo is the positive half of the leak test:
// the credential is gone, and the host/port/scheme it was attached to is
// still there, so the exported spec still points somewhere real.
func TestExportOpenAPI_StripsURLUserinfo(t *testing.T) {
	doc := parseJSONExport(t, credentialURLStore())

	gotServers := map[string]bool{}
	for _, raw := range asSlice(doc["servers"]) {
		gotServers[raw.(map[string]any)["url"].(string)] = true
	}
	for _, want := range []string{
		"https://api.example.com",
		"https://svc.example.com:8443",
		"//rel.example.com",
		"{apiBase}",
	} {
		if !gotServers[want] {
			t.Errorf("servers = %v, want it to include %q", gotServers, want)
		}
	}

	// Paths are unaffected by the strip.
	paths := doc["paths"].(map[string]any)
	for _, want := range []string{"/v1/things", "/v1/widgets", "/v1/gadgets", "/v1/env"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("paths missing %q (got %v)", want, keys(paths))
		}
	}

	// The templated server's default is the env value MINUS its userinfo.
	for _, raw := range asSlice(doc["servers"]) {
		srv := raw.(map[string]any)
		if srv["url"] != "{apiBase}" {
			continue
		}
		vars := srv["variables"].(map[string]any)
		def := vars["apiBase"].(map[string]any)["default"]
		if def != "https://env.example.com" {
			t.Errorf("apiBase default = %v, want the env value with userinfo stripped", def)
		}
	}
}

// TestStripUserinfo covers the predicate directly, including the shapes where
// stripping would be WRONG.
func TestStripUserinfo(t *testing.T) {
	cases := map[string]string{
		"https://admin:hunter2@api.example.com": "https://api.example.com",
		"https://admin@api.example.com":         "https://api.example.com",
		"https://api.example.com":               "https://api.example.com",
		"https://admin:p@ss@api.example.com":    "https://api.example.com", // '@' inside the password
		"http://admin:pw@localhost:8080":        "http://localhost:8080",
		"//admin:pw@api.example.com":            "//api.example.com",
		"${baseUrl}":                            "${baseUrl}",
		"":                                      "",
		// Scheme-less: a credential shape is stripped, an email is not.
		"admin:hunter2@api.example.com": "api.example.com",
		"ada@example.com":               "ada@example.com",
	}
	for in, want := range cases {
		if got := stripUserinfo(in); got != want {
			t.Errorf("stripUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExportOpenAPI_RedactsEnvironmentSecret proves the specific env-secret
// redaction guarantee: a secret value planted in Variables via the raw path
// (name also listed in Secrets) never becomes a server-variable default or
// appears anywhere else. The non-secret baseUrl in the same environment still
// surfaces as the server default.
func TestExportOpenAPI_RedactsEnvironmentSecret(t *testing.T) {
	store := demoStore()
	out, err := ExportOpenAPIYAML(store, "ws1")
	if err != nil {
		t.Fatalf("ExportOpenAPIYAML: %v", err)
	}
	if strings.Contains(string(out), secretEnvValue) {
		t.Fatalf("environment secret value leaked into spec")
	}
	if !strings.Contains(string(out), "https://api.example.com") {
		t.Fatalf("non-secret baseUrl should still surface as the server default")
	}
}

func TestExportOpenAPI_EmptyWorkspace(t *testing.T) {
	store := &fakeStore{workspaces: []model.Workspace{{ID: "ws1", Name: ""}}}
	b, err := ExportOpenAPI(store, "ws1")
	if err != nil {
		t.Fatalf("ExportOpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("empty export not valid JSON: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v", doc["openapi"])
	}
	// info.title must fall back to a non-empty default.
	if doc["info"].(map[string]any)["title"] == "" {
		t.Fatalf("empty workspace name should fall back to a default title")
	}
	// paths must still be present (an empty object) so the doc stays valid.
	if _, ok := doc["paths"]; !ok {
		t.Fatalf("paths key must always be present")
	}
}

// --- helpers ---------------------------------------------------------------

func parseJSONExport(t *testing.T, store *fakeStore) map[string]any {
	t.Helper()
	b, err := ExportOpenAPI(store, "ws1")
	if err != nil {
		t.Fatalf("ExportOpenAPI: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("export not valid JSON: %v", err)
	}
	return doc
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
