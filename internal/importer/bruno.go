package importer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// Bruno stores each request as a `.bru` file — a small block-structured text
// DSL, e.g.:
//
//	meta {
//	  name: Create user
//	  type: http
//	}
//	post {
//	  url: {{baseUrl}}/users
//	  body: json
//	  auth: bearer
//	}
//	headers {
//	  Content-Type: application/json
//	  ~X-Disabled: off
//	}
//	auth:bearer {
//	  token: {{token}}
//	}
//	body:json {
//	  { "name": "{{name}}" }
//	}
//
// ParseBruno parses ONE `.bru` request faithfully (method/url/headers/query/
// body/auth), which is the unit the string-based Import entry point receives.
// A whole-collection folder walk is deferred: it needs a directory of `.bru`
// files (Import takes a single content string), so it belongs behind a
// filesystem-aware binding, not here.

// bruBlock is one top-level `name { ... }` section of a .bru file. name may
// contain colons ("body:json", "auth:bearer", "vars:pre-request").
type bruBlock struct {
	name string
	body string
}

// bruHeaderRe matches a leading Bruno block header — a `meta {` or an HTTP
// method block — used only for format detection.
var bruHeaderRe = regexp.MustCompile(`^(?i)(meta|get|post|put|delete|patch|head|options|connect|trace)\s*\{`)

// brunoVarRe converts Bruno's `{{var}}` interpolation (which also covers
// `{{process.env.X}}` etc.) to AUK's `${var}`. The inner expression is copied
// through verbatim, so a bare `{{token}}` becomes `${token}` and a dotted
// `{{process.env.TOKEN}}` becomes `${process.env.TOKEN}` (documented — AUK
// resolves plain names; exotic expressions are left for the user to adjust).
var brunoVarRe = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)

func convertBrunoTemplate(s string) string {
	if s == "" {
		return s
	}
	return brunoVarRe.ReplaceAllString(s, `${$1}`)
}

// looksBruno reports whether content is (the start of) a .bru file: after
// trimming, its first block header is `meta {` or an HTTP method block. Used
// by Detect BEFORE any JSON/YAML decode, so JSON formats (which begin with
// '{') never reach here.
func looksBruno(trimmed string) bool {
	return bruHeaderRe.MatchString(trimmed)
}

var brunoMethods = map[string]bool{
	"get": true, "post": true, "put": true, "delete": true, "patch": true,
	"head": true, "options": true, "connect": true, "trace": true,
}

// ParseBruno converts a single .bru request into an ImportResult carrying one
// RequestDef (plus an environment if the file declares `vars`).
func ParseBruno(content string) (ImportResult, error) {
	blocks := parseBruBlocks(content)
	if len(blocks) == 0 {
		return ImportResult{}, fmt.Errorf("parse Bruno .bru: no blocks found")
	}

	// Index by block name (first occurrence wins).
	byName := map[string]string{}
	for _, b := range blocks {
		if _, ok := byName[b.name]; !ok {
			byName[b.name] = b.body
		}
	}

	// The HTTP method block names the verb and references which body/auth
	// blocks are active.
	var methodName, methodBody string
	for _, b := range blocks {
		if brunoMethods[strings.ToLower(b.name)] {
			methodName = strings.ToLower(b.name)
			methodBody = b.body
			break
		}
	}
	if methodName == "" {
		return ImportResult{}, fmt.Errorf("parse Bruno .bru: no request method block (get/post/...)")
	}
	methodDict := parseBruDictMap(methodBody)

	name := "Imported (Bruno)"
	if meta, ok := byName["meta"]; ok {
		if n := parseBruDictMap(meta)["name"]; n != "" {
			name = n
		}
	}

	nextOrder := newOrderMinter()
	req := model.RequestDef{
		ID:       uuid.NewString(),
		Name:     name,
		Protocol: model.ProtocolHTTP,
		Method:   strings.ToUpper(methodName),
		URL:      convertBrunoTemplate(strings.TrimSpace(methodDict["url"])),
		OrderKey: nextOrder(),
	}

	// Headers.
	if hb, ok := byName["headers"]; ok {
		for _, kv := range parseBruDict(hb) {
			req.Headers = append(req.Headers, model.KeyValue{
				Key: kv.Key, Value: convertBrunoTemplate(kv.Value), Enabled: kv.Enabled,
			})
		}
	}

	// Query params: `query { }` (classic) or `params:query { }` (newer).
	for _, qn := range []string{"query", "params:query"} {
		if qb, ok := byName[qn]; ok {
			for _, kv := range parseBruDict(qb) {
				req.Params = append(req.Params, model.KeyValue{
					Key: kv.Key, Value: convertBrunoTemplate(kv.Value), Enabled: kv.Enabled,
				})
			}
		}
	}
	// Path params: newer `params:path { }`.
	if pb, ok := byName["params:path"]; ok {
		for _, kv := range parseBruDict(pb) {
			req.PathParams = append(req.PathParams, model.KeyValue{
				Key: kv.Key, Value: convertBrunoTemplate(kv.Value), Enabled: true,
			})
		}
	}

	applyBrunoBody(&req, strings.TrimSpace(methodDict["body"]), byName, blocks)
	applyBrunoAuth(&req, strings.TrimSpace(methodDict["auth"]), byName, blocks)

	result := ImportResult{
		WorkspaceName: name,
		Requests:      []model.RequestDef{req},
		Format:        FormatBruno,
	}

	// Request-level vars become a best-effort environment so they aren't lost
	// (Bruno's runtime `vars` have no exact RequestDef equivalent).
	var envVars []model.KeyValue
	for _, b := range blocks {
		if b.name == "vars" || b.name == "vars:pre-request" {
			for _, kv := range parseBruDict(b.body) {
				envVars = append(envVars, model.KeyValue{
					Key: kv.Key, Value: convertBrunoTemplate(kv.Value), Enabled: kv.Enabled,
				})
			}
		}
	}
	if len(envVars) > 0 {
		result.Environments = append(result.Environments, model.Environment{
			ID: uuid.NewString(), Name: "Bruno Vars", Variables: envVars,
		})
	}

	return result, nil
}

func applyBrunoBody(req *model.RequestDef, bodyKind string, byName map[string]string, blocks []bruBlock) {
	kind := strings.ToLower(bodyKind)
	var raw string
	ok := false
	if kind != "" && kind != "none" {
		raw, ok = byName["body:"+kind]
	}
	if !ok {
		// Fall back to whichever body:* block exists (slice order for
		// determinism), ignoring the graphql vars sidecar block.
		for _, b := range blocks {
			if strings.HasPrefix(b.name, "body:") && b.name != "body:graphql:vars" {
				kind = strings.TrimPrefix(b.name, "body:")
				raw = b.body
				ok = true
				break
			}
		}
	}
	if !ok {
		return
	}
	switch kind {
	case "json":
		req.Body = &model.RequestBody{Kind: model.BodyJSON, Text: convertBrunoTemplate(strings.TrimSpace(raw))}
	case "form-urlencoded", "multipart-form":
		var fields []model.KeyValue
		for _, kv := range parseBruDict(raw) {
			fields = append(fields, model.KeyValue{
				Key: kv.Key, Value: convertBrunoTemplate(kv.Value), Enabled: kv.Enabled,
			})
		}
		req.Body = &model.RequestBody{Kind: model.BodyForm, FormFields: fields}
	case "graphql":
		body := &model.RequestBody{Kind: model.BodyGraphQL, Text: convertBrunoTemplate(strings.TrimSpace(raw))}
		if vars, ok := byName["body:graphql:vars"]; ok {
			body.GraphQLVariables = convertBrunoTemplate(strings.TrimSpace(vars))
		}
		req.Body = body
	default: // text, xml, sparql, and anything unrecognized
		req.Body = &model.RequestBody{Kind: model.BodyText, Text: convertBrunoTemplate(strings.TrimSpace(raw))}
	}
}

func applyBrunoAuth(req *model.RequestDef, authKind string, byName map[string]string, blocks []bruBlock) {
	kind := strings.ToLower(authKind)
	if kind == "none" || kind == "inherit" {
		return
	}
	if kind == "" {
		// No explicit auth reference: adopt an auth:* block if one is present.
		for _, b := range blocks {
			if strings.HasPrefix(b.name, "auth:") {
				kind = strings.TrimPrefix(b.name, "auth:")
				break
			}
		}
		if kind == "" {
			return
		}
	}
	dict := parseBruDictMap(byName["auth:"+kind])
	get := func(key string) string { return convertBrunoTemplate(dict[key]) }
	switch kind {
	case "bearer":
		req.Auth = &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: get("token")}}
	case "basic":
		req.Auth = &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{
			Username: get("username"), Password: get("password"),
		}}
	case "apikey":
		in := model.APIKeyInHeader
		placement := strings.ToLower(dict["placement"] + dict["in"])
		if strings.Contains(placement, "quer") {
			in = model.APIKeyInQuery
		}
		req.Auth = &model.AuthConfig{Kind: model.AuthAPIKey, APIKey: &model.APIKeyAuth{
			Key: get("key"), Value: get("value"), In: in,
		}}
	case "oauth2":
		req.Auth = &model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: &model.OAuth2Auth{
			ClientID:     get("client_id"),
			ClientSecret: get("client_secret"),
			TokenURL:     get("access_token_url"),
			Scopes:       splitScopes(get("scope")),
		}}
	case "awsv4":
		req.Auth = &model.AuthConfig{Kind: model.AuthAWSSigV4, AWSSigV4: &model.AWSSigV4Auth{
			AccessKeyID:     get("accessKeyId"),
			SecretAccessKey: get("secretAccessKey"),
			SessionToken:    get("sessionToken"),
			Region:          get("region"),
			Service:         get("service"),
		}}
	case "digest":
		req.Auth = &model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{
			Username: get("username"), Password: get("password"),
		}}
	}
}

// parseBruBlocks splits a .bru file into its top-level `name { ... }` blocks.
// Brace matching is string-literal-aware so a JSON body containing `}` inside
// a string (e.g. `{ "a": "x}y" }`) doesn't terminate the block early.
func parseBruBlocks(s string) []bruBlock {
	var blocks []bruBlock
	n := len(s)
	i := 0
	for i < n {
		// Skip leading whitespace between blocks.
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= n {
			break
		}
		// Read the header up to the block's opening brace. A newline before a
		// brace means this line isn't a block header — skip it.
		start := i
		for i < n && s[i] != '{' && s[i] != '\n' {
			i++
		}
		if i >= n {
			break
		}
		if s[i] == '\n' {
			i++
			continue
		}
		header := strings.TrimSpace(s[start:i])

		// Walk from the opening brace to its match, tracking string literals.
		bodyStart := i + 1
		depth := 0
		inStr := false
		esc := false
		j := i
		for j < n {
			c := s[j]
			if inStr {
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					inStr = false
				}
			} else {
				switch c {
				case '"':
					inStr = true
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						goto closed
					}
				}
			}
			j++
		}
	closed:
		if j >= n { // unbalanced: take the rest as the body
			if header != "" {
				blocks = append(blocks, bruBlock{name: header, body: s[bodyStart:]})
			}
			break
		}
		if header != "" {
			blocks = append(blocks, bruBlock{name: header, body: s[bodyStart:j]})
		}
		i = j + 1
	}
	return blocks
}

// parseBruDict parses a dictionary-style block body ("key: value" lines) into
// KeyValues, honoring the `~` disabled-row prefix.
func parseBruDict(body string) []model.KeyValue {
	var kvs []model.KeyValue
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		enabled := true
		if strings.HasPrefix(line, "~") {
			enabled = false
			line = strings.TrimSpace(line[1:])
		}
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		kvs = append(kvs, model.KeyValue{
			Key: strings.TrimSpace(key), Value: strings.TrimSpace(val), Enabled: enabled,
		})
	}
	return kvs
}

// parseBruDictMap is parseBruDict keyed by name (last value wins), for blocks
// read by field rather than iterated (the method block, auth blocks, meta).
func parseBruDictMap(body string) map[string]string {
	out := map[string]string{}
	for _, kv := range parseBruDict(body) {
		out[kv.Key] = kv.Value
	}
	return out
}
