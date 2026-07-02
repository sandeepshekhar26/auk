package importer

import (
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
	Key   string `json:"key"`
	Value string `json:"value"`
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
			env.Variables = append(env.Variables, model.KeyValue{Key: v.Key, Value: v.Value, Enabled: true})
		}
		result.Environments = append(result.Environments, env)
	}

	order := 0
	nextOrder := func() string { order++; return fmt.Sprintf("%06d", order) }

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
	req := model.RequestDef{
		ID:       uuid.NewString(),
		FolderID: folderID,
		Name:     it.Name,
		Protocol: model.ProtocolHTTP,
		Method:   strings.ToUpper(orDefault(r.Method, "GET")),
		URL:      postmanURL(r.URL),
		OrderKey: orderKey,
	}
	for _, h := range r.Header {
		req.Headers = append(req.Headers, model.KeyValue{Key: h.Key, Value: h.Value, Enabled: !h.Disabled})
	}
	applyPostmanBody(&req, r.Body)
	applyPostmanAuth(&req, r.Auth)
	return req
}

// postmanURL handles both the string and object forms of a Postman url field.
func postmanURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return convertPostmanVars(s)
	}
	var obj postmanURLObject
	if err := json.Unmarshal(raw, &obj); err == nil {
		return convertPostmanVars(obj.Raw)
	}
	return ""
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
