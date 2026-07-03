package model

// AppSettings holds app-level (not workspace-level) preferences. Persisted
// to ~/.apitool/settings.yaml — deliberately outside any git-synced
// workspace directory, since UI preferences are per-machine, not shareable
// project data.
type AppSettings struct {
	// Theme is "system", "dark", or "light". Empty means "system".
	Theme string `yaml:"theme" json:"theme"`
	// MCPEnabled starts the embedded Streamable-HTTP MCP server on launch so
	// MCP clients (Claude Code) can drive the live app.
	MCPEnabled bool `yaml:"mcpEnabled" json:"mcpEnabled"`
	// MCPPort is the fixed loopback port for the embedded MCP server (fixed,
	// not ephemeral, so a saved .mcp.json config stays valid across
	// restarts). 0 means the default.
	MCPPort int `yaml:"mcpPort,omitempty" json:"mcpPort"`
}
