package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// postmanCollection is the subset of a Postman Collection v2/v2.1 we read.
type postmanCollection struct {
	Info     postmanInfo   `json:"info"`
	Item     []postmanItem `json:"item"`
	Variable []postmanVar  `json:"variable"`
}

type postmanInfo struct {
	Name string `json:"name"`
}

type postmanVar struct {
	Key string `json:"key"`
	// Postman's v2.1 schema types variable[].value as `any`, and real
	// collections (especially tool-generated or converted ones) routinely
	// carry numbers and booleans, not just strings. Decoding into a `string`
	// makes the ENCLOSING object fail to unmarshal on the first numeric
	// value — which silently blanked the whole URL (and, for collection-level
	// variables, could take the whole collection with it). RawMessage accepts
	// any JSON, and scalarString renders it.
	Value json.RawMessage `json:"value"`
}

// scalarString renders a Postman variable value as the plain string AUK
// stores. Strings are unquoted; numbers/booleans are rendered literally
// (42, true) as the user would have typed them; null and JSON objects/arrays
// (never meaningful as a path-variable value) become "".
func (v postmanVar) scalarString() string {
	raw := bytes.TrimSpace(v.Value)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	switch raw[0] {
	case '{', '[':
		return "" // structured values aren't usable as a variable value
	default:
		return string(raw) // number, true/false — render literally
	}
}

// postmanItem is either a folder (has nested Item) or a request (has Request).
type postmanItem struct {
	Name    string          `json:"name"`
	Item    []postmanItem   `json:"item"`
	Request *postmanRequest `json:"request"`
}

type postmanRequest struct {
	Method string          `json:"method"`
	Header []postmanHeader `json:"header"`
	URL    json.RawMessage `json:"url"` // string OR object
	Body   *postmanBody    `json:"body"`
	Auth   *postmanAuth    `json:"auth"`
}

type postmanHeader struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

type postmanBody struct {
	Mode       string             `json:"mode"` // raw | urlencoded | formdata
	Raw        string             `json:"raw"`
	URLEncoded []postmanFormField `json:"urlencoded"`
	FormData   []postmanFormField `json:"formdata"`
}

type postmanFormField struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

type postmanAuth struct {
	Type   string          `json:"type"`
	Bearer []postmanAuthKV `json:"bearer"`
	Basic  []postmanAuthKV `json:"basic"`
	APIKey []postmanAuthKV `json:"apikey"`
}

type postmanAuthKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type postmanURLObject struct {
	Raw string `json:"raw"`
	// Variable carries the values for the `:name` path placeholders in Raw
	// (Postman's model for path variables — e.g. Raw "/users/:id" with a
	// Variable entry {id, 42}). Mapped to RequestDef.PathParams so the
	// imported request lands with its Path row pre-filled.
	Variable []postmanVar `json:"variable"`
}

// ParsePostman converts a Postman Collection v2/v2.1 into an ImportResult:
// the item tree becomes folders + requests, collection variables become a
// "Default" environment.
func ParsePostman(data []byte) (ImportResult, error) {
	var col postmanCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return ImportResult{}, fmt.Errorf("parse Postman collection: %w", err)
	}
	name := col.Info.Name
	if name == "" {
		name = "Imported Collection"
	}
	result := ImportResult{WorkspaceName: name, Format: FormatPostman}

	if len(col.Variable) > 0 {
		env := model.Environment{ID: uuid.NewString(), Name: "Default"}
		for _, v := range col.Variable {
			env.Variables = append(env.Variables, model.KeyValue{Key: v.Key, Value: v.scalarString(), Enabled: true})
		}
		result.Environments = append(result.Environments, env)
	}

	order := 0
	// Never ends in '0' (append "1" when it would): a key ending in the
	// alphabet's lowest digit can never have a sibling inserted directly
	// before it (see internal/storage/orderkey.go).
	nextOrder := func() string {
		order++
		k := fmt.Sprintf("%06d", order)
		if strings.HasSuffix(k, "0") {
			k += "1"
		}
		return k
	}

	var walk func(items []postmanItem, parentID *string)
	walk = func(items []postmanItem, parentID *string) {
		for _, it := range items {
			if it.Request == nil && len(it.Item) > 0 {
				// Folder node.
				fid := uuid.NewString()
				result.Folders = append(result.Folders, model.Folder{
					ID: fid, ParentID: parentID, Name: it.Name, OrderKey: nextOrder(),
				})
				walk(it.Item, &fid)
				continue
			}
			if it.Request == nil {
				continue // empty item, skip
			}
			result.Requests = append(result.Requests, postmanToRequest(it, parentID, nextOrder()))
		}
	}
	walk(col.Item, nil)

	if len(result.Requests) == 0 {
		return ImportResult{}, fmt.Errorf("Postman collection has no requests")
	}
	return result, nil
}

func postmanToRequest(it postmanItem, folderID *string, orderKey string) model.RequestDef {
	r := it.Request
	urlStr, pathParams := parsePostmanURL(r.URL)
	req := model.RequestDef{
		ID:         uuid.NewString(),
		FolderID:   folderID,
		Name:       it.Name,
		Protocol:   model.ProtocolHTTP,
		Method:     strings.ToUpper(orDefault(r.Method, "GET")),
		URL:        urlStr,
		PathParams: pathParams,
		OrderKey:   orderKey,
	}
	for _, h := range r.Header {
		req.Headers = append(req.Headers, model.KeyValue{Key: h.Key, Value: h.Value, Enabled: !h.Disabled})
	}
	applyPostmanBody(&req, r.Body)
	applyPostmanAuth(&req, r.Auth)
	return req
}

// parsePostmanURL handles both the string and object forms of a Postman url
// field. It returns the URL string with any `:name` path tokens preserved,
// plus the path-variable VALUES Postman recorded in the object form's
// `variable` array, mapped to AUK PathParams.
//
// AUK's request editor derives the `:name` Path rows from the URL itself, so
// the URL and the PathParams have to agree: we keep Raw (which carries the
// `:id` tokens) verbatim and only supply the values, so an imported
// `/users/:id` with variable id=42 lands with its Path row pre-filled.
func parsePostmanURL(raw json.RawMessage) (string, []model.KeyValue) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// String form carries no separate variable list — the editor derives
		// empty Path rows from any `:name` tokens in the URL string itself.
		return convertPostmanVars(s), nil
	}
	var obj postmanURLObject
	if err := json.Unmarshal(raw, &obj); err == nil {
		var pathParams []model.KeyValue
		for _, v := range obj.Variable {
			if v.Key == "" {
				continue
			}
			pathParams = append(pathParams, model.KeyValue{Key: v.Key, Value: v.scalarString(), Enabled: true})
		}
		return convertPostmanVars(obj.Raw), pathParams
	}
	return "", nil
}

// convertPostmanVars rewrites Postman's {{var}} syntax — which happens to
// match ours — unchanged; kept as a seam in case the syntaxes ever diverge.
func convertPostmanVars(s string) string { return s }

func applyPostmanBody(req *model.RequestDef, body *postmanBody) {
	if body == nil {
		return
	}
	switch body.Mode {
	case "raw":
		if body.Raw == "" {
			return
		}
		kind := model.BodyText
		if looksJSON(body.Raw) {
			kind = model.BodyJSON
		}
		req.Body = &model.RequestBody{Kind: kind, Text: body.Raw}
	case "urlencoded", "formdata":
		fields := body.URLEncoded
		if body.Mode == "formdata" {
			fields = body.FormData
		}
		var kvs []model.KeyValue
		for _, f := range fields {
			kvs = append(kvs, model.KeyValue{Key: f.Key, Value: f.Value, Enabled: !f.Disabled})
		}
		req.Body = &model.RequestBody{Kind: model.BodyForm, FormFields: kvs}
	}
}

func applyPostmanAuth(req *model.RequestDef, auth *postmanAuth) {
	if auth == nil {
		return
	}
	switch auth.Type {
	case "bearer":
		req.Auth = &model.AuthConfig{Kind: model.AuthBearer, Bearer: &model.BearerAuth{Token: authKV(auth.Bearer, "token")}}
	case "basic":
		req.Auth = &model.AuthConfig{Kind: model.AuthBasic, Basic: &model.BasicAuth{
			Username: authKV(auth.Basic, "username"),
			Password: authKV(auth.Basic, "password"),
		}}
	case "apikey":
		loc := authKV(auth.APIKey, "in")
		if loc != "query" {
			loc = "header"
		}
		req.Auth = &model.AuthConfig{Kind: model.AuthAPIKey, APIKey: &model.APIKeyAuth{
			Key:   authKV(auth.APIKey, "key"),
			Value: authKV(auth.APIKey, "value"),
			In:    model.APIKeyLocation(loc),
		}}
	}
}

func authKV(kvs []postmanAuthKV, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func looksJSON(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}
