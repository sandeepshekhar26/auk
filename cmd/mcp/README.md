# apitool-mcp

An [MCP](https://modelcontextprotocol.io) server that exposes your saved
API requests to Claude Code (or any MCP client) as tools. It's a headless
consumer of the same engine the GUI uses, reading the same git-friendly
workspace files (`~/.apitool/workspace`) — so anything you save in the app,
Claude can list and run.

## Tools

| Tool | What it does |
|---|---|
| `list_workspaces` | List your workspaces (collections of requests). |
| `list_requests` | List saved requests in a workspace (id, name, method, url). |
| `run_request` | Execute a saved request and return status, headers, body. |
| `run_perf_test` | Run a k6 load test against a request (throughput, latency percentiles, SLA pass/fail). Needs the k6 sidecar. |

Every request runs through the engine's Dispatch policy chokepoint with
origin `mcp`, the same gate the GUI and CLI pass through.

## Connect to Claude Code

Build the binary:

    go build -o apitool-mcp ./cmd/mcp

Register it:

    claude mcp add apitool -- /absolute/path/to/apitool-mcp

or add it to `.mcp.json`:

    {
      "mcpServers": {
        "apitool": { "command": "/absolute/path/to/apitool-mcp" }
      }
    }

Point it at a non-default workspace with the `APITOOL_WORKSPACE_DIR`
environment variable.

## Notes

- The server reads the on-disk workspace (the source of truth). Unsaved
  in-memory edits in a running GUI aren't visible until they auto-save.
- Transport is stdio (what `.mcp.json` launches). An embedded
  Streamable-HTTP server inside the GUI — so Claude drives *live* app
  state and mutating actions can prompt for approval — is the planned next
  step (docs/02-architecture.md §MCP).
