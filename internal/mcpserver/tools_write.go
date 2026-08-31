package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core/model"
)

// The authoring half of the MCP surface — registered ONLY under ScopeWrite.
//
// This is the differentiator the whole MCP story rests on: an agent that can
// read your OpenAPI spec or your handler code and BUILD the workspace, where
// the result is plain YAML in a git diff you review before it means anything.
// No competitor can offer the second half of that sentence, because their
// collections live in a cloud database rather than in your repository.
//
// Which is also why every tool here is small, explicit, and passes through the
// WriteGuard first. An agent editing files on someone's disk earns exactly one
// safety story: the user saw what it intended to do, and can read the diff
// afterwards.

// guard runs the WriteGuard and turns a refusal into an error the agent sees.
// Centralised so a new write tool cannot forget the check — the failure mode
// of "one tool skipped the guard" is silent and total.
func (h *handlers) guard(ctx context.Context, tool, summary, workspaceID string) error {
	g := h.writes
	if g == nil {
		g = DenyAllWrites{}
	}
	ok, reason := g.AuthorizeWrite(ctx, WriteIntent{Tool: tool, Summary: summary, WorkspaceID: workspaceID})
	if ok {
		return nil
	}
	if reason == "" {
		reason = "the user declined this change"
	}
	return fmt.Errorf("%s refused: %s", tool, reason)
}

// writeStore is the mutating subset of the store the authoring tools need.
// Kept separate from Store so a read-only or run-scoped server can be
// constructed with a store that literally cannot write.
// Method names here must match storage.FileStore EXACTLY — this is a
// structural assertion, so a near-miss (DeleteRequest vs the real
// RemoveRequest) does not fail to compile. It fails at runtime, as every write
// tool reporting "this server's store is read-only", which points at the wrong
// thing entirely. The tests below assert a real write round-trips, which is
// what caught it.
type writeStore interface {
	PutRequest(model.RequestDef) error
	RemoveRequest(model.ID) error
	PutFolder(model.Folder) error
	ListEnvironmentsRaw(model.ID) []model.Environment
	PutEnvironment(model.Environment, map[string]string) error
}

func (h *handlers) writer() (writeStore, error) {
	w, ok := h.store.(writeStore)
	if !ok {
		return nil, fmt.Errorf("this server's store is read-only")
	}
	return w, nil
}

// ---- create_request -----------------------------------------------------

type createRequestIn struct {
	WorkspaceID string         `json:"workspaceId" jsonschema:"the workspace to create the request in"`
	Name        string         `json:"name" jsonschema:"display name, e.g. 'Refund charge'"`
	Method      string         `json:"method" jsonschema:"HTTP method: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS"`
	URL         string         `json:"url" jsonschema:"request URL; may contain ${variable} references"`
	FolderID    string         `json:"folderId,omitempty" jsonschema:"optional folder to place the request in"`
	Description string         `json:"description,omitempty" jsonschema:"what this request does; shown in the app and versioned in git"`
	Headers     []keyValueIn   `json:"headers,omitempty" jsonschema:"request headers"`
	Params      []keyValueIn   `json:"params,omitempty" jsonschema:"query string parameters"`
	BodyKind    string         `json:"bodyKind,omitempty" jsonschema:"body type: none, json, text, form, or binary"`
	Body        string         `json:"body,omitempty" jsonschema:"request body text"`
}

type mutationOut struct {
	ID string `json:"id"`
	// Summary is echoed back so the agent can report what it did in the same
	// words the human saw in the approval prompt.
	Summary string `json:"summary"`
}

// keyValueIn is the INPUT shape for headers and params, deliberately separate
// from keyValueInfo (the output shape).
//
// The difference is `enabled`: as a plain bool it lands in the generated JSON
// schema as REQUIRED, so an agent sending the obvious {"key":"X","value":"Y"}
// is rejected by schema validation before the tool ever runs. A pointer makes
// it optional, and omitted means enabled — which is what an agent that took
// the trouble to specify a header meant.
type keyValueIn struct {
	Key     string `json:"key" jsonschema:"header or parameter name"`
	Value   string `json:"value" jsonschema:"its value; may contain ${variable} references"`
	Enabled *bool  `json:"enabled,omitempty" jsonschema:"set false to save the row without sending it; defaults to true"`
}

func fromKVInfo(kvs []keyValueIn) []model.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]model.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		enabled := true
		if kv.Enabled != nil {
			enabled = *kv.Enabled
		}
		out = append(out, model.KeyValue{Key: kv.Key, Value: kv.Value, Enabled: enabled})
	}
	return out
}

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
}

func normalizeMethod(m string) (string, error) {
	up := strings.ToUpper(strings.TrimSpace(m))
	if up == "" {
		return "GET", nil
	}
	if !allowedMethods[up] {
		return "", fmt.Errorf("unsupported method %q", m)
	}
	return up, nil
}

func bodyFrom(kind, text string) *model.RequestBody {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" || k == "none" {
		if text == "" {
			return nil
		}
		k = "json"
	}
	return &model.RequestBody{Kind: model.BodyKind(k), Text: text}
}

func (h *handlers) createRequest(ctx context.Context, _ *mcp.CallToolRequest, in createRequestIn) (*mcp.CallToolResult, mutationOut, error) {
	if in.WorkspaceID == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.URL) == "" {
		return nil, mutationOut{}, fmt.Errorf("workspaceId, name and url are required")
	}
	method, err := normalizeMethod(in.Method)
	if err != nil {
		return nil, mutationOut{}, err
	}
	w, err := h.writer()
	if err != nil {
		return nil, mutationOut{}, err
	}

	summary := fmt.Sprintf("Create request %q (%s %s)", in.Name, method, in.URL)
	if err := h.guard(ctx, "create_request", summary, in.WorkspaceID); err != nil {
		return nil, mutationOut{}, err
	}

	req := model.RequestDef{
		ID: model.ID(uuid.NewString()), WorkspaceID: model.ID(in.WorkspaceID), FolderID: optionalID(in.FolderID),
		Name: in.Name, Description: in.Description, Method: method, URL: in.URL,
		Protocol: model.ProtocolHTTP,
		Headers:  fromKVInfo(in.Headers), Params: fromKVInfo(in.Params),
		Body: bodyFrom(in.BodyKind, in.Body),
	}
	if err := w.PutRequest(req); err != nil {
		return nil, mutationOut{}, err
	}
	return nil, mutationOut{ID: string(req.ID), Summary: summary}, nil
}

// ---- update_request -----------------------------------------------------

type updateRequestIn struct {
	RequestID string `json:"requestId" jsonschema:"the id of the request to update"`
	// Every field is optional: an omitted field is LEFT ALONE. Pointer types
	// distinguish "not mentioned" from "set to empty" — without that, an agent
	// changing only the URL would silently wipe the request's headers, body
	// and description, and the user would find out from the git diff.
	Name        *string         `json:"name,omitempty" jsonschema:"new display name"`
	Method      *string         `json:"method,omitempty" jsonschema:"new HTTP method"`
	URL         *string         `json:"url,omitempty" jsonschema:"new URL"`
	Description *string         `json:"description,omitempty" jsonschema:"new description"`
	Headers     *[]keyValueIn   `json:"headers,omitempty" jsonschema:"replaces ALL headers when provided"`
	Params      *[]keyValueIn   `json:"params,omitempty" jsonschema:"replaces ALL query parameters when provided"`
	BodyKind    *string         `json:"bodyKind,omitempty" jsonschema:"new body type"`
	Body        *string         `json:"body,omitempty" jsonschema:"new body text"`
}

func (h *handlers) updateRequest(ctx context.Context, _ *mcp.CallToolRequest, in updateRequestIn) (*mcp.CallToolResult, mutationOut, error) {
	if in.RequestID == "" {
		return nil, mutationOut{}, fmt.Errorf("requestId is required")
	}
	w, err := h.writer()
	if err != nil {
		return nil, mutationOut{}, err
	}
	req, err := h.store.GetRequest(model.ID(in.RequestID))
	if err != nil {
		return nil, mutationOut{}, err
	}

	changed := []string{}
	if in.Name != nil {
		req.Name = *in.Name
		changed = append(changed, "name")
	}
	if in.Method != nil {
		m, mErr := normalizeMethod(*in.Method)
		if mErr != nil {
			return nil, mutationOut{}, mErr
		}
		req.Method = m
		changed = append(changed, "method")
	}
	if in.URL != nil {
		req.URL = *in.URL
		changed = append(changed, "url")
	}
	if in.Description != nil {
		req.Description = *in.Description
		changed = append(changed, "description")
	}
	if in.Headers != nil {
		req.Headers = fromKVInfo(*in.Headers)
		changed = append(changed, "headers")
	}
	if in.Params != nil {
		req.Params = fromKVInfo(*in.Params)
		changed = append(changed, "params")
	}
	if in.BodyKind != nil || in.Body != nil {
		kind, text := "", ""
		if req.Body != nil {
			kind, text = string(req.Body.Kind), req.Body.Text
		}
		if in.BodyKind != nil {
			kind = *in.BodyKind
		}
		if in.Body != nil {
			text = *in.Body
		}
		req.Body = bodyFrom(kind, text)
		changed = append(changed, "body")
	}
	if len(changed) == 0 {
		return nil, mutationOut{}, fmt.Errorf("nothing to update — supply at least one field")
	}

	summary := fmt.Sprintf("Update request %q: change %s", req.Name, strings.Join(changed, ", "))
	if err := h.guard(ctx, "update_request", summary, string(req.WorkspaceID)); err != nil {
		return nil, mutationOut{}, err
	}
	if err := w.PutRequest(req); err != nil {
		return nil, mutationOut{}, err
	}
	return nil, mutationOut{ID: string(req.ID), Summary: summary}, nil
}

// ---- delete_request -----------------------------------------------------

type deleteRequestIn struct {
	RequestID string `json:"requestId" jsonschema:"the id of the request to delete"`
}

func (h *handlers) deleteRequest(ctx context.Context, _ *mcp.CallToolRequest, in deleteRequestIn) (*mcp.CallToolResult, mutationOut, error) {
	if in.RequestID == "" {
		return nil, mutationOut{}, fmt.Errorf("requestId is required")
	}
	w, err := h.writer()
	if err != nil {
		return nil, mutationOut{}, err
	}
	// Resolved BEFORE deleting so the approval prompt (and the result) can
	// name what is being destroyed rather than quoting an opaque uuid.
	req, err := h.store.GetRequest(model.ID(in.RequestID))
	if err != nil {
		return nil, mutationOut{}, err
	}

	summary := fmt.Sprintf("DELETE request %q (%s %s)", req.Name, req.Method, req.URL)
	if err := h.guard(ctx, "delete_request", summary, string(req.WorkspaceID)); err != nil {
		return nil, mutationOut{}, err
	}
	if err := w.RemoveRequest(model.ID(in.RequestID)); err != nil {
		return nil, mutationOut{}, err
	}
	return nil, mutationOut{ID: in.RequestID, Summary: summary}, nil
}

// ---- create_folder ------------------------------------------------------

type createFolderIn struct {
	WorkspaceID string `json:"workspaceId" jsonschema:"the workspace to create the folder in"`
	Name        string `json:"name" jsonschema:"folder name"`
	ParentID    string `json:"parentId,omitempty" jsonschema:"optional parent folder id for nesting"`
}

func (h *handlers) createFolder(ctx context.Context, _ *mcp.CallToolRequest, in createFolderIn) (*mcp.CallToolResult, mutationOut, error) {
	if in.WorkspaceID == "" || strings.TrimSpace(in.Name) == "" {
		return nil, mutationOut{}, fmt.Errorf("workspaceId and name are required")
	}
	w, err := h.writer()
	if err != nil {
		return nil, mutationOut{}, err
	}
	summary := fmt.Sprintf("Create folder %q", in.Name)
	if err := h.guard(ctx, "create_folder", summary, in.WorkspaceID); err != nil {
		return nil, mutationOut{}, err
	}
	f := model.Folder{ID: model.ID(uuid.NewString()), WorkspaceID: model.ID(in.WorkspaceID), Name: in.Name, ParentID: optionalID(in.ParentID)}
	if err := w.PutFolder(f); err != nil {
		return nil, mutationOut{}, err
	}
	return nil, mutationOut{ID: string(f.ID), Summary: summary}, nil
}

// ---- set_environment_variable -------------------------------------------

type setEnvVarIn struct {
	EnvironmentID string `json:"environmentId" jsonschema:"the environment to modify"`
	Name          string `json:"name" jsonschema:"variable name"`
	Value         string `json:"value" jsonschema:"variable value"`
}

// setEnvVar writes a PLAIN variable. It deliberately cannot create or
// overwrite a SECRET.
//
// Secrets live in the OS keychain and are excluded from the git-tracked YAML
// on purpose; letting an agent write one would either put a credential into a
// versioned file or silently move a value into the keychain where the user
// cannot see what happened. Refusing by name is the only honest option — and
// the refusal says why, so the agent tells the user to set it in the app
// instead of retrying.
func (h *handlers) setEnvVar(ctx context.Context, _ *mcp.CallToolRequest, in setEnvVarIn) (*mcp.CallToolResult, mutationOut, error) {
	if in.EnvironmentID == "" || strings.TrimSpace(in.Name) == "" {
		return nil, mutationOut{}, fmt.Errorf("environmentId and name are required")
	}
	w, err := h.writer()
	if err != nil {
		return nil, mutationOut{}, err
	}

	// Find the environment across workspaces — the tool takes an id, and
	// making the caller also supply the workspace would be needless ceremony.
	var target *model.Environment
	for _, ws := range h.store.ListWorkspaces() {
		for _, e := range h.store.ListEnvironmentsRaw(ws.ID) {
			if string(e.ID) == in.EnvironmentID {
				cp := e
				target = &cp
				break
			}
		}
		if target != nil {
			break
		}
	}
	if target == nil {
		return nil, mutationOut{}, fmt.Errorf("environment %s not found", in.EnvironmentID)
	}
	for _, s := range target.Secrets {
		if s == in.Name {
			return nil, mutationOut{}, fmt.Errorf(
				"%q is a secret variable — its value lives in the OS keychain and cannot be set by an agent; ask the user to set it in AUK's environment editor", in.Name)
		}
	}

	summary := fmt.Sprintf("Set %s=%s in environment %q", in.Name, truncate(in.Value, 60), target.Name)
	if err := h.guard(ctx, "set_environment_variable", summary, string(target.WorkspaceID)); err != nil {
		return nil, mutationOut{}, err
	}

	found := false
	for i := range target.Variables {
		if target.Variables[i].Key == in.Name {
			target.Variables[i].Value = in.Value
			target.Variables[i].Enabled = true
			found = true
			break
		}
	}
	if !found {
		target.Variables = append(target.Variables, model.KeyValue{Key: in.Name, Value: in.Value, Enabled: true})
	}
	// nil secretValues: this path never touches keychain-backed values, and
	// passing an empty map would ask the store to rewrite them as blank.
	if err := w.PutEnvironment(*target, nil); err != nil {
		return nil, mutationOut{}, err
	}
	return nil, mutationOut{ID: string(target.ID), Summary: summary}, nil
}

// optionalID converts an empty string to a nil *model.ID — the model uses a
// pointer so "no folder" and "the empty id" stay distinguishable.
func optionalID(s string) *model.ID {
	if s == "" {
		return nil
	}
	id := model.ID(s)
	return &id
}
