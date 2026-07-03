// Command apitool-mcp is the stdio Model Context Protocol server: it exposes
// the user's saved API requests as MCP tools so Claude Code (or any MCP
// client) can list and run them. It is a headless consumer of the exact same
// core.Engine the GUI drives, reading the git-friendly workspace files that
// are the source of truth. The tool surface lives in internal/mcpserver,
// shared with the GUI's embedded Streamable-HTTP server.
//
// Add to Claude Code with:
//
//	claude mcp add apitool -- /path/to/apitool-mcp
//
// or in .mcp.json:
//
//	{ "mcpServers": { "apitool": { "command": "/path/to/apitool-mcp" } } }
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/appcore"
	"apitool/internal/mcpserver"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "apitool-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := appcore.DefaultWorkspaceDir()
	if v := os.Getenv("APITOOL_WORKSPACE_DIR"); v != "" {
		dir = v
	}

	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		return fmt.Errorf("open workspace %q: %w", dir, err)
	}

	srv := mcpserver.New(engine, store)
	// StdioTransport is what Claude Code's .mcp.json launches by default:
	// the client speaks JSON-RPC over this process's stdin/stdout.
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}
