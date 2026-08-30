package importer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// insomnia* mirror the subset of an Insomnia v4 export (`_type: "export"`,
// `__export_format: 4`) we read. The document is a FLAT `resources` array of
// heterogeneous nodes (workspace / request_group / request / environment)
// linked by `parentId`; ParseInsomnia rebuilds the tree from those links.
//
// Deliberately deferred: the newer v5 `.insomnia`/YAML "collection" files
// (one file per resource, database-per-directory). Their `.yaml` decodes
// through the same generic mapper, but the on-disk SHAPE differs enough
// (nested `meta`, per-file resources) that it's a follow-up — v4 JSON, which
// is still what "Export Data → Insomnia v4 (JSON)" produces and what the
// migration wave carries, is imported fully.
type insomniaExport struct {
	Type         string             `json:"_type"`
	ExportFormat int                `json:"__export_format"`
	Resources    []insomniaResource `json:"resources"`
}

type insomniaResource struct {
	ID       string `json:"_id"`
	Type     string `json:"_type"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`

	// request fields
	Method         string         `json:"method"`
	URL            string         `json:"url"`
	Headers        []insomniaNV   `json:"headers"`
	Parameters     []insomniaNV   `json:"parameters"`
	Body           *insomniaBody  `json:"body"`
	Authentication map[string]any `json:"authentication"`

	// environment field
	Data map[string]any `json:"data"`
}

type insomniaNV struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

type insomniaBody struct {
	MimeType string       `json:"mimeType"`
	Text     string       `json:"text"`
	Params   []insomniaNV `json:"params"`
}

// insomniaVarRe converts Insomnia template tags of the form `{{ _.name }}`
// (v4+) and the older bare `{{ name }}` to AUK's `${name}`. It deliberately
// matches ONLY a simple (optionally `_.`-prefixed) identifier, so nunjucks
// function tags like `{{ uuid() }}` / `{{ now() }}` and `{% ... %}` blocks are
// left verbatim for the user to port by hand (documented).
var insomniaVarRe = regexp.MustCompile(`\{\{\s*(?:_\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

func convertInsomniaTemplate(s string) string {
	if s == "" {
		return s
	}
	return insomniaVarRe.ReplaceAllString(s, `${$1}`)
}

// ParseInsomnia converts an Insomnia v4 export into an ImportResult: the
// request_group tree becomes folders, requests become RequestDefs, and each
// environment resource becomes an AUK Environment.
func ParseInsomnia(data []byte) (ImportResult, error) {
	var exp insomniaExport
	if err := json.Unmarshal(data, &exp); err != nil {
		return ImportResult{}, fmt.Errorf("parse Insomnia export: %w", err)
	}
	if len(exp.Resources) == 0 {
		return ImportResult{}, fmt.Errorf("Insomnia export has no resources")
	}

	// Index resources and note which ids are workspaces (the tree roots): a
	// folder/request parented directly to a workspace sits at top level.
	byID := make(map[string]insomniaResource, len(exp.Resources))
	isWorkspace := map[string]bool{}
	wsName := ""
	for _, r := range exp.Resources {
		byID[r.ID] = r
		if r.Type == "workspace" {
			isWorkspace[r.ID] = true
			if wsName == "" {
				wsName = r.Name
			}
		}
	}
	if wsName == "" {
		wsName = "Imported (Insomnia)"
	}

	result := ImportResult{WorkspaceName: wsName, Format: FormatInsomnia}

	// Pass 1: mint a folder UUID for every request_group so parent links can
	// be resolved regardless of resource order.
	folderUUID := map[string]string{}
	for _, r := range exp.Resources {
		if r.Type == "request_group" {
			folderUUID[r.ID] = uuid.NewString()
		}
	}

	// parentFolderID maps an Insomnia parentId to the AUK folder id it lands
	// under: a request_group parent → that folder; a workspace (or unknown)
	// parent → nil (top level).
	parentFolderID := func(parentID string) *string {
		if fid, ok := folderUUID[parentID]; ok {
			return &fid
		}
		return nil
	}

	nextOrder := newOrderMinter()

	// Pass 2: walk resources in file order so folders and requests keep their
	// relative ordering, emitting folders, requests, and environments.
	for _, r := range exp.Resources {
		switch r.Type {
		case "request_group":
			result.Folders = append(result.Folders, model.Folder{
				ID:       folderUUID[r.ID],
				ParentID: parentFolderID(r.ParentID),
				Name:     orDefault(r.Name, "Folder"),
				OrderKey: nextOrder(),
			})
		case "request":
			result.Requests = append(result.Requests, insomniaToRequest(r, parentFolderID(r.ParentID), nextOrder()))
		case "environment":
			if env, ok := insomniaToEnvironment(r); ok {
				result.Environments = append(result.Environments, env)
			}
		}
	}

	if len(result.Requests) == 0 {
		return ImportResult{}, fmt.Errorf("Insomnia export has no requests")
	}
	return result, nil
}

func insomniaToRequest(r insomniaResource, folderID *string, orderKey string) model.RequestDef {
	req := model.RequestDef{
		ID:       uuid.NewString(),
		FolderID: folderID,
		Name:     orDefault(r.Name, strings.ToUpper(orDefault(r.Method, "GET"))+" "+r.URL),
		Protocol: model.ProtocolHTTP,
		Method:   strings.ToUpper(orDefault(r.Method, "GET")),
		URL:      convertInsomniaTemplate(r.URL),
		OrderKey: orderKey,
	}
	for _, h := range r.Headers {
		req.Headers = append(req.Headers, model.KeyValue{
			Key: h.Name, Value: convertInsomniaTemplate(h.Value), Enabled: !h.Disabled,
		})
	}
	for _, p := range r.Parameters {
		req.Params = append(req.Params, model.KeyValue{
			Key: p.Name, Value: convertInsomniaTemplate(p.Value), Enabled: !p.Disabled,
		})
	}
	applyInsomniaBody(&req, r.Body)
	applyInsomniaAuth(&req, r.Authentication)
	return req
}

func applyInsomniaBody(req *model.RequestDef, body *insomniaBody) {
	if body == nil {
		return
	}
	mime := strings.ToLower(strings.TrimSpace(body.MimeType))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case strings.Contains(mime, "graphql"):
		// Insomnia stores a GraphQL body as JSON: {"query": "...",
		// "variables": {...}}. Split it into AUK's separate query/variables
		// fields; fall back to raw text if it isn't the expected shape.
		var g struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}
		if json.Unmarshal([]byte(body.Text), &g) == nil && g.Query != "" {
			vars := ""
			if len(g.Variables) > 0 && string(g.Variables) != "null" {
				vars = string(g.Variables)
			}
			req.Body = &model.RequestBody{
				Kind:             model.BodyGraphQL,
				Text:             convertInsomniaTemplate(g.Query),
				GraphQLVariables: convertInsomniaTemplate(vars),
			}
			return
		}
		req.Body = &model.RequestBody{Kind: model.BodyText, Text: convertInsomniaTemplate(body.Text)}
	case strings.Contains(mime, "json"):
		if body.Text == "" {
			return
		}
		req.Body = &model.RequestBody{Kind: model.BodyJSON, Text: convertInsomniaTemplate(body.Text)}
	case strings.Contains(mime, "x-www-form-urlencoded"), strings.Contains(mime, "form-data"):
		var fields []model.KeyValue
		for _, p := range body.Params {
			fields = append(fields, model.KeyValue{
				Key: p.Name, Value: convertInsomniaTemplate(p.Value), Enabled: !p.Disabled,
			})
		}
		req.Body = &model.RequestBody{Kind: model.BodyForm, FormFields: fields}
	default:
		if body.Text != "" {
			req.Body = &model.RequestBody{Kind: model.BodyText, Text: convertInsomniaTemplate(body.Text)}
		}
	}
}

func applyInsomniaAuth(req *model.RequestDef, auth map[string]any) {
	if len(auth) == 0 {
		return
	}
	if disabled, _ := auth["disabled"].(bool); disabled {
		return
	}
	get := func(key string) string {
		return convertInsomniaTemplate(anyToString(auth[key]))
	}
	switch strings.ToLower(anyToString(auth["type"])) {
	case "basic":
		req.Auth = &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{
			Username: get("username"), Password: get("password"),
		}}
	case "bearer":
		req.Auth = &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: get("token")}}
	case "apikey":
		in := model.APIKeyInHeader
		if strings.Contains(strings.ToLower(anyToString(auth["addTo"])), "quer") {
			in = model.APIKeyInQuery
		}
		req.Auth = &model.AuthConfig{Kind: model.AuthAPIKey, APIKey: &model.APIKeyAuth{
			Key: get("key"), Value: get("value"), In: in,
		}}
	case "oauth2":
		req.Auth = &model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: &model.OAuth2Auth{
			ClientID:     get("clientId"),
			ClientSecret: get("clientSecret"),
			TokenURL:     get("accessTokenUrl"),
			Scopes:       splitScopes(get("scope")),
		}}
	case "oauth1":
		req.Auth = &model.AuthConfig{Kind: model.AuthOAuth1, OAuth1: &model.OAuth1Auth{
			ConsumerKey:    get("consumerKey"),
			ConsumerSecret: get("consumerSecret"),
			Token:          get("tokenKey"),
			TokenSecret:    get("tokenSecret"),
		}}
	case "digest":
		req.Auth = &model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{
			Username: get("username"), Password: get("password"),
		}}
	case "iam": // Insomnia's AWS IAM auth
		req.Auth = &model.AuthConfig{Kind: model.AuthAWSSigV4, AWSSigV4: &model.AWSSigV4Auth{
			AccessKeyID:     get("accessKeyId"),
			SecretAccessKey: get("secretAccessKey"),
			SessionToken:    get("sessionToken"),
			Region:          get("region"),
			Service:         get("service"),
		}}
	}
}

func insomniaToEnvironment(r insomniaResource) (model.Environment, bool) {
	if len(r.Data) == 0 {
		return model.Environment{}, false
	}
	env := model.Environment{
		ID:   uuid.NewString(),
		Name: orDefault(r.Name, "Environment"),
	}
	// Sort keys for a deterministic import (map iteration is random).
	keys := make([]string, 0, len(r.Data))
	for k := range r.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env.Variables = append(env.Variables, model.KeyValue{
			Key: k, Value: convertInsomniaTemplate(anyToString(r.Data[k])), Enabled: true,
		})
	}
	return env, true
}

// splitScopes turns Insomnia's space-separated OAuth2 scope string into the
// slice AUK stores.
func splitScopes(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		out = append(out, f)
	}
	return out
}

// anyToString renders a decoded-JSON scalar the way a user would have typed
// it (matching Postman's scalarString): strings verbatim, whole numbers
// without a trailing ".0", booleans literal, null as "", and any
// structured object/array as compact JSON (so nested env values aren't lost).
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return ""
	}
}
