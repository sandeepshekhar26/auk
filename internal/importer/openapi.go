package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"apitool/internal/core/model"
)

// openAPIDoc is the subset of OpenAPI 3.x / Swagger 2.0 we read. Everything
// else in a spec is ignored — this is an importer, not a validator.
type openAPIDoc struct {
	OpenAPI  string                          `json:"openapi"`
	Swagger  string                          `json:"swagger"`
	Info     openAPIInfo                     `json:"info"`
	Servers  []openAPIServer                 `json:"servers"`
	Host     string                          `json:"host"`     // swagger 2.0
	BasePath string                          `json:"basePath"` // swagger 2.0
	Schemes  []string                        `json:"schemes"`  // swagger 2.0
	Paths    map[string]map[string]openAPIOp `json:"paths"`
}

type openAPIInfo struct {
	Title string `json:"title"`
}

type openAPIServer struct {
	URL string `json:"url"`
}

type openAPIOp struct {
	OperationID string              `json:"operationId"`
	Summary     string              `json:"summary"`
	Tags        []string            `json:"tags"`
	Parameters  []openAPIParam      `json:"parameters"`
	RequestBody *openAPIRequestBody `json:"requestBody"`
}

type openAPIParam struct {
	Name string `json:"name"`
	In   string `json:"in"` // query | header | path
}

type openAPIRequestBody struct {
	Content map[string]openAPIMediaType `json:"content"`
}

type openAPIMediaType struct {
	Example any            `json:"example"`
	Schema  map[string]any `json:"schema"`
}

var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true, "trace": true,
}

// ParseOpenAPI converts an OpenAPI 3.x or Swagger 2.0 document (JSON or YAML)
// into an ImportResult: one folder per tag (or per first path segment),
// one request per path+method, and a "baseUrl" environment variable driven
// from servers[0] so every request URL is `{{baseUrl}}<path>`.
func ParseOpenAPI(data []byte) (ImportResult, error) {
	// Route through decodeToMap so YAML specs work, then re-marshal to JSON
	// for strict struct decoding.
	m, err := decodeToMap(data)
	if err != nil {
		return ImportResult{}, fmt.Errorf("parse OpenAPI: %w", err)
	}
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return ImportResult{}, err
	}
	var doc openAPIDoc
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return ImportResult{}, fmt.Errorf("parse OpenAPI: %w", err)
	}
	if len(doc.Paths) == 0 {
		return ImportResult{}, fmt.Errorf("OpenAPI spec has no paths")
	}

	name := doc.Info.Title
	if name == "" {
		name = "Imported API"
	}
	result := ImportResult{WorkspaceName: name, Format: FormatOpenAPI}

	baseURL := openAPIBaseURL(doc)
	if baseURL != "" {
		result.Environments = []model.Environment{{
			ID:        uuid.NewString(),
			Name:      "Default",
			Variables: []model.KeyValue{{Key: "baseUrl", Value: baseURL, Enabled: true}},
		}}
	}

	// Folders keyed by tag/segment, created lazily and ordered deterministically.
	folderIDByName := map[string]string{}
	folderName := func(op openAPIOp, path string) string {
		if len(op.Tags) > 0 && op.Tags[0] != "" {
			return op.Tags[0]
		}
		seg := strings.Trim(path, "/")
		if i := strings.IndexByte(seg, '/'); i > 0 {
			seg = seg[:i]
		}
		if seg == "" {
			return "default"
		}
		return seg
	}

	// Deterministic iteration: sort paths, then methods.
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	order := 0
	nextOrder := func() string { order++; return fmt.Sprintf("%06d", order) }

	for _, path := range paths {
		ops := doc.Paths[path]
		methods := make([]string, 0, len(ops))
		for mth := range ops {
			if httpMethods[strings.ToLower(mth)] {
				methods = append(methods, mth)
			}
		}
		sort.Strings(methods)

		for _, mth := range methods {
			op := ops[mth]
			fname := folderName(op, path)
			fid, ok := folderIDByName[fname]
			if !ok {
				fid = uuid.NewString()
				folderIDByName[fname] = fid
				result.Folders = append(result.Folders, model.Folder{
					ID: fid, Name: fname, OrderKey: nextOrder(),
				})
			}

			req := model.RequestDef{
				ID:       uuid.NewString(),
				FolderID: &fid,
				Name:     openAPIRequestName(op, mth, path),
				Protocol: model.ProtocolHTTP,
				Method:   strings.ToUpper(mth),
				URL:      openAPIRequestURL(baseURL, path),
				OrderKey: nextOrder(),
			}
			applyOpenAPIParams(&req, op.Parameters)
			applyOpenAPIBody(&req, op.RequestBody)
			result.Requests = append(result.Requests, req)
		}
	}

	return result, nil
}

func openAPIBaseURL(doc openAPIDoc) string {
	if len(doc.Servers) > 0 && doc.Servers[0].URL != "" {
		return strings.TrimRight(doc.Servers[0].URL, "/")
	}
	// Swagger 2.0 assembles it from scheme + host + basePath.
	if doc.Host != "" {
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		return strings.TrimRight(fmt.Sprintf("%s://%s%s", scheme, doc.Host, doc.BasePath), "/")
	}
	return ""
}

// openAPIRequestURL uses a {{baseUrl}} template when a base URL exists so the
// user can swap environments; otherwise the raw path (relative, to be edited).
func openAPIRequestURL(baseURL, path string) string {
	if baseURL != "" {
		return "{{baseUrl}}" + path
	}
	return path
}

func openAPIRequestName(op openAPIOp, method, path string) string {
	if op.Summary != "" {
		return op.Summary
	}
	if op.OperationID != "" {
		return op.OperationID
	}
	return strings.ToUpper(method) + " " + path
}

func applyOpenAPIParams(req *model.RequestDef, params []openAPIParam) {
	for _, p := range params {
		switch p.In {
		case "query":
			req.Params = append(req.Params, model.KeyValue{Key: p.Name, Value: "", Enabled: false})
		case "header":
			req.Headers = append(req.Headers, model.KeyValue{Key: p.Name, Value: "", Enabled: false})
		}
	}
}

func applyOpenAPIBody(req *model.RequestDef, body *openAPIRequestBody) {
	if body == nil {
		return
	}
	mt, ok := body.Content["application/json"]
	if !ok {
		return
	}
	var text string
	if mt.Example != nil {
		if b, err := json.MarshalIndent(mt.Example, "", "  "); err == nil {
			text = string(b)
		}
	} else if mt.Schema != nil {
		if b, err := json.MarshalIndent(schemaSkeleton(mt.Schema), "", "  "); err == nil {
			text = string(b)
		}
	}
	req.Headers = append(req.Headers, model.KeyValue{Key: "Content-Type", Value: "application/json", Enabled: true})
	req.Body = &model.RequestBody{Kind: model.BodyJSON, Text: text}
}

// schemaSkeleton builds a minimal example object from a JSON-schema-ish map:
// object → {field: <placeholder per type>}. Best-effort, one level deep-ish.
func schemaSkeleton(schema map[string]any) any {
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		props, _ := schema["properties"].(map[string]any)
		out := map[string]any{}
		for name, raw := range props {
			if ps, ok := raw.(map[string]any); ok {
				out[name] = schemaSkeleton(ps)
			} else {
				out[name] = nil
			}
		}
		return out
	case "array":
		if items, ok := schema["items"].(map[string]any); ok {
			return []any{schemaSkeleton(items)}
		}
		return []any{}
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "string":
		return ""
	default:
		return nil
	}
}
