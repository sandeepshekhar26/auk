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
//
// The capability scope is chosen with -scope (or APITOOL_MCP_SCOPE):
//
//	read-only   inspect the workspace; nothing is sent or written
//	run         (default) read + execute requests, folders and load tests
//	write       + author the workspace: create/update/delete requests
//
// `write` is opt-in because this binary is headless: there is nobody to
// prompt, so starting it with that flag IS the consent. Prefer running the
// GUI's embedded server for agent authoring, where each change is approved
// in-app — and either way, every edit lands as plain YAML you review in a
// git diff.
package main

import (
	"context"
	"flag"
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
	scopeFlag := flag.String("scope", os.Getenv("APITOOL_MCP_SCOPE"),
		"capability scope: read-only, run (default), or write")
	flag.Parse()

	scope, err := mcpserver.ParseScope(*scopeFlag)
	if err != nil {
		return err
	}

	dir := appcore.DefaultWorkspaceDir()
	if v := os.Getenv("APITOOL_WORKSPACE_DIR"); v != "" {
		dir = v
	}

	engine, store, err := appcore.NewEngine(dir)
	if err != nil {
		return fmt.Errorf("open workspace %q: %w", dir, err)
	}

	// AllowAllWrites, not a prompt: this process has no UI. The human
	// consented by launching it with -scope=write, and the review surface is
	// the git diff the edits land in.
	srv, err := mcpserver.NewWithOptions(engine, store, mcpserver.Options{
		Scope:  scope,
		Writes: mcpserver.AllowAllWrites{},
	})
	if err != nil {
		return err
	}
	// StdioTransport is what Claude Code's .mcp.json launches by default:
	// the client speaks JSON-RPC over this process's stdin/stdout.
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}
