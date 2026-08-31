package model

// AppSettings holds app-level (not workspace-level) preferences. Persisted
// to ~/.auk/settings.yaml — deliberately outside any git-synced
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
	// MCPScope is the capability grant the embedded MCP server runs at:
	// "read-only", "run", or "write". Empty means the run default, which is
	// what every existing settings.yaml written before this field means.
	//
	// The GUI defaults to "write" when the user turns the server on from
	// Settings, because the GUI is the one host that can ASK before each edit
	// — every authoring call goes through the in-app approval modal. Someone
	// who wants an agent that can look but never touch sets "read-only" here.
	MCPScope string `yaml:"mcpScope,omitempty" json:"mcpScope,omitempty"`
	// SidebarMode is "docked" (persistent left tree, the default — empty
	// string means docked) or "overlay" (the original slide-over drawer).
	SidebarMode string `yaml:"sidebarMode,omitempty" json:"sidebarMode"`
	// MockPort is the loopback port the mock server binds (Settings → Mock
	// Server; see internal/mockserver and docs/10-mock-server.md). Remembered
	// so a frontend's `.env` pointing at 127.0.0.1:<port> keeps working
	// across restarts. 0 means "use mockserver.DefaultPort". Only the PORT is
	// persisted, not whether the mock was running — unlike MCPEnabled, the
	// mock never auto-starts on launch.
	MockPort int `yaml:"mockPort,omitempty" json:"mockPort"`
}
