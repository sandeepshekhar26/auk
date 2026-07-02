// Package importer converts external formats (cURL, OpenAPI, Postman) into
// the app's own request model. Single-request formats use ParseCurl;
// collection formats (OpenAPI, Postman) return an ImportResult — a whole
// workspace of folders + requests + environments — via Import, which
// auto-detects the format.
package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"apitool/internal/core/model"
)

// ImportResult is a self-contained bundle the app persists as a new
// workspace: a name plus the folders, requests, and environments parsed
// from the source document. IDs are freshly generated and internally
// consistent (folder parentId / request folderId / request workspaceId all
// refer to the ids in this bundle), but WorkspaceID is left blank for the
// caller to stamp once it mints the real workspace id.
type ImportResult struct {
	WorkspaceName string              `json:"workspaceName"`
	Folders       []model.Folder      `json:"folders"`
	Requests      []model.RequestDef  `json:"requests"`
	Environments  []model.Environment `json:"environments"`
	Format        string              `json:"format"` // "openapi" | "postman" | "curl"
}

// Format identifiers.
const (
	FormatCurl    = "curl"
	FormatOpenAPI = "openapi"
	FormatPostman = "postman"
)

// Import auto-detects the format of content and parses it into an
// ImportResult. cURL yields a single-request bundle; OpenAPI and Postman
// yield full collections.
func Import(content string) (ImportResult, error) {
	switch Detect(content) {
	case FormatCurl:
		req, err := ParseCurl(content)
		if err != nil {
			return ImportResult{}, err
		}
		return ImportResult{
			WorkspaceName: "Imported (cURL)",
			Requests:      []model.RequestDef{req},
			Format:        FormatCurl,
		}, nil
	case FormatOpenAPI:
		return ParseOpenAPI([]byte(content))
	case FormatPostman:
		return ParsePostman([]byte(content))
	default:
		return ImportResult{}, fmt.Errorf("could not detect import format (expected a curl command, OpenAPI spec, or Postman collection)")
	}
}

// Detect classifies content by cheap structural signals, without a full
// parse.
func Detect(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "curl ") || strings.HasPrefix(trimmed, "curl\t") {
		return FormatCurl
	}

	// JSON or YAML object: peek at the decoded top-level keys.
	m, err := decodeToMap([]byte(trimmed))
	if err != nil {
		return ""
	}
	if _, ok := m["openapi"]; ok {
		return FormatOpenAPI
	}
	if _, ok := m["swagger"]; ok {
		return FormatOpenAPI
	}
	if info, ok := m["info"].(map[string]any); ok {
		// Postman collections carry info._postman_id or a schema URL.
		if _, ok := info["_postman_id"]; ok {
			return FormatPostman
		}
		if schema, ok := info["schema"].(string); ok && strings.Contains(schema, "getpostman.com") {
			return FormatPostman
		}
	}
	if _, ok := m["item"]; ok {
		if _, hasInfo := m["info"]; hasInfo {
			return FormatPostman
		}
	}
	return ""
}

// decodeToMap decodes JSON or YAML into a generic map. YAML is a superset of
// JSON, so a single yaml.Unmarshal handles both — but we normalize the
// map[interface{}]interface{} that yaml.v3 can produce for nested maps.
func decodeToMap(data []byte) (map[string]any, error) {
	// Prefer strict JSON first (fast, and avoids YAML's surprising coercions
	// for JSON documents).
	dec := json.NewDecoder(bytes.NewReader(data))
	var jm map[string]any
	if err := dec.Decode(&jm); err == nil {
		return jm, nil
	}
	var ym map[string]any
	if err := yaml.Unmarshal(data, &ym); err != nil {
		return nil, err
	}
	return normalizeYAML(ym).(map[string]any), nil
}

// normalizeYAML converts yaml.v3's map[string]interface{} tree (with nested
// values possibly typed loosely) into JSON-compatible types, so downstream
// code can treat OpenAPI-from-YAML identically to OpenAPI-from-JSON.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAML(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeYAML(val)
		}
		return out
	default:
		return v
	}
}
