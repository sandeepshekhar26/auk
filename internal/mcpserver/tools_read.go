package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core/model"
)

// The read half of the MCP surface. Every tool here answers a question an
// agent must be able to ask before it can do anything useful:
//
//	get_request          what does this request actually DO?
//	search_requests      which request is the one they mean?
//	list_environments    what can I run it against?
//	resolve_variables    what will ${baseUrl} become?
//	get_last_response    what happened last time?
//
// Before v2 an agent could list names and URLs and nothing else — it could see
// that "Refund charge" existed but not its headers, body, auth or assertions,
// which made "fix the failing request" impossible to even attempt.

// ---- get_request --------------------------------------------------------

type getRequestIn struct {
	RequestID string `json:"requestId" jsonschema:"the id of the saved request to inspect"`
}

type keyValueInfo struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type assertionInfo struct {
	Description string `json:"description"`
}

type getRequestOut struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspaceId"`
	FolderID         string         `json:"folderId,omitempty"`
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Method           string         `json:"method"`
	URL              string         `json:"url"`
	Protocol         string         `json:"protocol"`
	Headers          []keyValueInfo `json:"headers,omitempty"`
	Params           []keyValueInfo `json:"params,omitempty"`
	PathParams       []keyValueInfo `json:"pathParams,omitempty"`
	BodyKind         string         `json:"bodyKind,omitempty"`
	Body             string         `json:"body,omitempty"`
	// AuthKind names the scheme only. The CREDENTIALS are deliberately never
	// returned: an agent needs to know a request is Bearer-authenticated in
	// order to reason about a 401, and never needs the token itself. See the
	// note on redaction in resolve_variables below.
	AuthKind         string          `json:"authKind,omitempty"`
	Assertions       []assertionInfo `json:"assertions,omitempty"`
	PreRequestScript string          `json:"preRequestScript,omitempty"`
	PostResponseScript string        `json:"postResponseScript,omitempty"`
}

func toKVInfo(kvs []model.KeyValue) []keyValueInfo {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]keyValueInfo, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, keyValueInfo{Key: kv.Key, Value: kv.Value, Enabled: kv.Enabled})
	}
	return out
}

func (h *handlers) getRequest(_ context.Context, _ *mcp.CallToolRequest, in getRequestIn) (*mcp.CallToolResult, getRequestOut, error) {
	if in.RequestID == "" {
		return nil, getRequestOut{}, fmt.Errorf("requestId is required")
	}
	r, err := h.store.GetRequest(in.RequestID)
	if err != nil {
		return nil, getRequestOut{}, err
	}
	out := getRequestOut{
		ID: string(r.ID), WorkspaceID: string(r.WorkspaceID), FolderID: idPtr(r.FolderID), Name: r.Name,
		Description: r.Description, Method: r.Method, URL: r.URL, Protocol: string(r.Protocol),
		Headers: toKVInfo(r.Headers), Params: toKVInfo(r.Params), PathParams: toKVInfo(r.PathParams),
		PreRequestScript: r.PreRequestScript, PostResponseScript: r.PostResponseScript,
	}
	if r.Body != nil {
		out.BodyKind = string(r.Body.Kind)
		out.Body = truncate(r.Body.Text, 20_000)
	}
	if r.Auth != nil {
		out.AuthKind = string(r.Auth.Kind)
	}
	for _, a := range r.Assertions {
		out.Assertions = append(out.Assertions, assertionInfo{Description: assertionLabel(a)})
	}
	return nil, out, nil
}

// ---- search_requests ----------------------------------------------------

type searchRequestsIn struct {
	Query       string `json:"query" jsonschema:"case-insensitive text matched against request name, URL and method"`
	WorkspaceID string `json:"workspaceId,omitempty" jsonschema:"optional workspace to search; omit to search all workspaces"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum results to return; default 25"`
}

type searchHit struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Method      string `json:"method"`
	URL         string `json:"url"`
}

type searchRequestsOut struct {
	Hits []searchHit `json:"hits"`
	// Truncated says the result set was CUT, not that it was empty beyond
	// this point — so an agent knows to narrow its query rather than
	// concluding it has seen everything.
	Truncated bool `json:"truncated"`
}

func (h *handlers) searchRequests(_ context.Context, _ *mcp.CallToolRequest, in searchRequestsIn) (*mcp.CallToolResult, searchRequestsOut, error) {
	q := strings.ToLower(strings.TrimSpace(in.Query))
	if q == "" {
		return nil, searchRequestsOut{}, fmt.Errorf("query is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}

	workspaces := h.store.ListWorkspaces()
	out := searchRequestsOut{Hits: []searchHit{}}
	for _, w := range workspaces {
		if in.WorkspaceID != "" && w.ID != in.WorkspaceID {
			continue
		}
		for _, r := range h.store.ListRequests(w.ID) {
			hay := strings.ToLower(r.Name + " " + r.URL + " " + r.Method)
			if !strings.Contains(hay, q) {
				continue
			}
			if len(out.Hits) >= limit {
				out.Truncated = true
				return nil, out, nil
			}
			out.Hits = append(out.Hits, searchHit{
				ID: string(r.ID), WorkspaceID: string(w.ID), Name: r.Name, Method: r.Method, URL: r.URL,
			})
		}
	}
	sort.Slice(out.Hits, func(i, j int) bool { return out.Hits[i].Name < out.Hits[j].Name })
	return nil, out, nil
}

// ---- list_environments --------------------------------------------------

type listEnvironmentsIn struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"the workspace whose environments to list"`
}

type environmentInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Variables are NAMES ONLY — values require resolve_variables, which
	// redacts secrets. Listing environments is a navigation act, not a read
	// of their contents.
	Variables []string `json:"variables"`
	// Secrets names the variables whose values live in the OS keychain, so an
	// agent can see that `apiKey` exists and is secret without ever being
	// able to read it.
	Secrets []string `json:"secrets,omitempty"`
}

type listEnvironmentsOut struct {
	Environments []environmentInfo `json:"environments"`
}

func (h *handlers) listEnvironments(_ context.Context, _ *mcp.CallToolRequest, in listEnvironmentsIn) (*mcp.CallToolResult, listEnvironmentsOut, error) {
	if in.WorkspaceID == "" {
		return nil, listEnvironmentsOut{}, fmt.Errorf("workspaceId is required")
	}
	envs := h.store.ListEnvironmentsRaw(in.WorkspaceID)
	out := listEnvironmentsOut{Environments: make([]environmentInfo, 0, len(envs))}
	for _, e := range envs {
		info := environmentInfo{ID: string(e.ID), Name: e.Name, Variables: []string{}, Secrets: e.Secrets}
		for _, kv := range e.Variables {
			info.Variables = append(info.Variables, kv.Key)
		}
		out.Environments = append(out.Environments, info)
	}
	sort.Slice(out.Environments, func(i, j int) bool { return out.Environments[i].Name < out.Environments[j].Name })
	return nil, out, nil
}

// ---- resolve_variables --------------------------------------------------

type resolveVariablesIn struct {
	WorkspaceID   string `json:"workspaceId" jsonschema:"the workspace whose variables to resolve"`
	EnvironmentID string `json:"environmentId,omitempty" jsonschema:"optional environment; omit for workspace-level variables only"`
}

type resolvedVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Secret marks a value that was withheld. The name is still returned so
	// an agent can explain "this resolves from your keychain" instead of
	// reporting the variable as missing and inventing a value for it.
	Secret bool `json:"secret"`
}

type resolveVariablesOut struct {
	Variables []resolvedVar `json:"variables"`
}

// resolveVariables answers "what will ${baseUrl} actually be?" — the question
// an agent must answer before it can reason about a URL at all.
//
// SECRETS ARE NEVER RETURNED. The value of a keychain-backed variable is
// replaced with a placeholder, exactly as the script sandbox does
// (core.redactResolved). An MCP tool that returned resolved secrets would be
// a cleaner exfiltration channel than any of the ones the script redaction
// was built to close: one tool call, no script, straight out over the wire to
// whatever model is driving.
func (h *handlers) resolveVariables(_ context.Context, _ *mcp.CallToolRequest, in resolveVariablesIn) (*mcp.CallToolResult, resolveVariablesOut, error) {
	if in.WorkspaceID == "" {
		return nil, resolveVariablesOut{}, fmt.Errorf("workspaceId is required")
	}

	out := resolveVariablesOut{Variables: []resolvedVar{}}
	// Raw (not GetEnvironment): the raw listing carries variable NAMES with
	// keychain values unresolved, which is precisely what should be exposed.
	// Reading the resolved environment would pull real secret values into
	// this process for no reason.
	for _, e := range h.store.ListEnvironmentsRaw(in.WorkspaceID) {
		if in.EnvironmentID != "" && string(e.ID) != in.EnvironmentID {
			continue
		}
		secret := make(map[string]bool, len(e.Secrets))
		for _, n := range e.Secrets {
			secret[n] = true
		}
		for _, kv := range e.Variables {
			if !kv.Enabled {
				continue
			}
			if secret[kv.Key] {
				out.Variables = append(out.Variables, resolvedVar{Name: kv.Key, Value: "[secret:" + kv.Key + "]", Secret: true})
				continue
			}
			out.Variables = append(out.Variables, resolvedVar{Name: kv.Key, Value: kv.Value})
		}
	}
	sort.Slice(out.Variables, func(i, j int) bool { return out.Variables[i].Name < out.Variables[j].Name })
	return nil, out, nil
}

// ---- get_last_response --------------------------------------------------

type getLastResponseIn struct {
	RequestID string `json:"requestId" jsonschema:"the id of the request whose last response to fetch"`
}

type getLastResponseOut struct {
	Found      bool       `json:"found"`
	Status     int        `json:"status,omitempty"`
	StatusText string     `json:"statusText,omitempty"`
	Headers    []headerKV `json:"headers,omitempty"`
	Body       string     `json:"body,omitempty"`
	TimingMs   int64      `json:"timingMs,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// getLastResponse returns the most recent response WITHOUT re-sending.
//
// This is the tool that makes an agent safe to debug with: asked "why did that
// fail?", an agent whose only option is run_request will re-fire the request —
// and if it was a POST, it will do it again. Being able to READ the last
// outcome removes the incentive to re-execute a side-effecting call just to
// look at it.
func (h *handlers) getLastResponse(_ context.Context, _ *mcp.CallToolRequest, in getLastResponseIn) (*mcp.CallToolResult, getLastResponseOut, error) {
	if in.RequestID == "" {
		return nil, getLastResponseOut{}, fmt.Errorf("requestId is required")
	}
	lookup, ok := h.store.(interface {
		LastResponse(model.ID) (model.ResponseData, bool)
	})
	if !ok {
		return nil, getLastResponseOut{}, fmt.Errorf("this server's store does not keep responses")
	}
	resp, found := lookup.LastResponse(in.RequestID)
	if !found {
		return nil, getLastResponseOut{Found: false}, nil
	}
	body, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		body = []byte(resp.BodyBase64)
	}
	out := getLastResponseOut{
		Found: true, Status: resp.Status, StatusText: resp.StatusText,
		Body: truncate(string(body), 100_000), TimingMs: resp.TimingMs, Error: resp.Error,
	}
	for _, hh := range resp.Headers {
		out.Headers = append(out.Headers, headerKV{Key: hh.Key, Value: hh.Value})
	}
	return nil, out, nil
}

// ---- list_folders -------------------------------------------------------

type listFoldersIn struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"the workspace whose folders to list"`
}

type folderInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parentId,omitempty"`
}

type listFoldersOut struct {
	Folders []folderInfo `json:"folders"`
}

func (h *handlers) listFolders(_ context.Context, _ *mcp.CallToolRequest, in listFoldersIn) (*mcp.CallToolResult, listFoldersOut, error) {
	if in.WorkspaceID == "" {
		return nil, listFoldersOut{}, fmt.Errorf("workspaceId is required")
	}
	fs := h.store.ListFolders(in.WorkspaceID)
	out := listFoldersOut{Folders: make([]folderInfo, 0, len(fs))}
	for _, f := range fs {
		out.Folders = append(out.Folders, folderInfo{ID: string(f.ID), Name: f.Name, ParentID: idPtr(f.ParentID)})
	}
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].Name < out.Folders[j].Name })
	return nil, out, nil
}

// idPtr renders an optional model.ID (folder/parent references are pointers so
// "no folder" is distinguishable from "the empty id") as a plain string for the
// wire, where omitempty already carries that distinction.
func idPtr(id *model.ID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}
