// Package mcpclient lets AUK act as an MCP CLIENT to debug a developer's
// OWN MCP server — connect (stdio subprocess or Streamable HTTP), list its
// published tools with descriptions/schemas, and call one with test
// arguments. This is the mirror image of internal/mcpserver (AUK acting as
// a SERVER exposing ITS OWN tools to Claude); the two packages share no
// code because their concerns — spawning/dialing OUT vs. serving requests
// IN — don't overlap.
package mcpclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core/model"
)

const (
	clientName     = "auk"
	clientVersion  = "0.1.0"
	connectTimeout = 15 * time.Second
)

// Client wraps one live session to a target MCP server.
type Client struct {
	session *mcp.ClientSession
}

// Connect launches (stdio) or dials (HTTP) conn and completes the MCP
// initialize handshake. The returned Client owns the underlying
// subprocess/HTTP session until Close is called — the SDK has no
// finalizer, so the caller MUST call Close eventually (including on app
// shutdown), or a stdio-launched subprocess can outlive the app entirely.
func Connect(ctx context.Context, conn model.McpConnection) (*Client, error) {
	transport, err := buildTransport(conn)
	if err != nil {
		return nil, err
	}

	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %q: %w", conn.Name, err)
	}
	return &Client{session: session}, nil
}

func buildTransport(conn model.McpConnection) (mcp.Transport, error) {
	switch conn.Transport {
	case model.McpTransportStdio:
		if conn.Command == "" {
			return nil, fmt.Errorf("stdio connection %q has no command configured", conn.Name)
		}
		return &mcp.CommandTransport{Command: exec.Command(conn.Command, conn.Args...)}, nil
	case model.McpTransportHTTP:
		if conn.URL == "" {
			return nil, fmt.Errorf("http connection %q has no url configured", conn.Name)
		}
		httpClient := &http.Client{Timeout: 30 * time.Second}
		if conn.BearerToken != "" {
			httpClient.Transport = &bearerRoundTripper{token: conn.BearerToken, base: http.DefaultTransport}
		}
		return &mcp.StreamableClientTransport{Endpoint: conn.URL, HTTPClient: httpClient}, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", conn.Transport)
	}
}

// bearerRoundTripper is the client-side mirror of internal/mcpserver/
// http.go's requireBearer: the SDK's StreamableClientTransport has no
// built-in header field, so custom auth is injected the same way that
// file's server-side handler wrapping does it, just on the request path
// instead of the response path.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(req)
}

// Close ends the session — for a stdio connection this follows the MCP
// shutdown sequence (close stdin, wait, SIGTERM, wait, SIGKILL) so the
// subprocess doesn't outlive the connection.
func (c *Client) Close() error {
	return c.session.Close()
}

// ToolInfo is the plain, Wails-serializable shape of an mcp.Tool — kept
// separate from the SDK's own type so app.go and the frontend never need
// to import the SDK. Annotation hints are flattened into plain bools: a
// nil DestructiveHint defaults to true per the MCP spec, but only
// meaningful when ReadOnlyHint is false — that resolution happens here
// rather than pushing MCP-spec default rules onto the frontend.
type ToolInfo struct {
	Name            string `json:"name"`
	Title           string `json:"title,omitempty"`
	Description     string `json:"description"`
	InputSchema     any    `json:"inputSchema"`
	OutputSchema    any    `json:"outputSchema,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
}

// ListTools fetches every published tool. The SDK's Tools iterator follows
// pagination internally, so the caller always gets the full list in one
// call regardless of how many pages the server uses.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	out := []ToolInfo{}
	for tool, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		info := ToolInfo{
			Name:         tool.Name,
			Title:        tool.Title,
			Description:  tool.Description,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
		}
		if tool.Annotations != nil {
			info.ReadOnlyHint = tool.Annotations.ReadOnlyHint
			info.IdempotentHint = tool.Annotations.IdempotentHint
			info.DestructiveHint = !info.ReadOnlyHint && (tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint)
		}
		out = append(out, info)
	}
	return out, nil
}

// ContentBlock is a flattened, Wails-serializable rendering of one
// mcp.Content element from a tool call result.
type ContentBlock struct {
	Type       string `json:"type"` // "text" | "image" | "audio" | "unknown"
	Text       string `json:"text,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	DataBase64 string `json:"dataBase64,omitempty"`
}

// CallResult is the plain shape of an mcp.CallToolResult.
type CallResult struct {
	Content           []ContentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError"`
}

// CallTool invokes name with argsJSON (a JSON object string — empty or
// "{}" both mean no arguments) and returns the flattened result. A tool
// that fails on the SERVER side reports that via IsError + Content (per
// the MCP spec, so an LLM caller — or a developer here — can see and
// self-correct) rather than as a Go error; a Go error here means the CALL
// ITSELF (transport/protocol level) failed, not the tool's own logic.
func (c *Client) CallTool(ctx context.Context, name string, argsJSON string) (CallResult, error) {
	var args map[string]any
	if trimmed := strings.TrimSpace(argsJSON); trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return CallResult{}, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}

	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return CallResult{}, fmt.Errorf("call tool %q: %w", name, err)
	}

	out := CallResult{StructuredContent: res.StructuredContent, IsError: res.IsError, Content: make([]ContentBlock, 0, len(res.Content))}
	for _, block := range res.Content {
		switch v := block.(type) {
		case *mcp.TextContent:
			out.Content = append(out.Content, ContentBlock{Type: "text", Text: v.Text})
		case *mcp.ImageContent:
			out.Content = append(out.Content, ContentBlock{Type: "image", MimeType: v.MIMEType, DataBase64: base64.StdEncoding.EncodeToString(v.Data)})
		case *mcp.AudioContent:
			out.Content = append(out.Content, ContentBlock{Type: "audio", MimeType: v.MIMEType, DataBase64: base64.StdEncoding.EncodeToString(v.Data)})
		default:
			out.Content = append(out.Content, ContentBlock{Type: "unknown"})
		}
	}
	return out, nil
}
