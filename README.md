# AUK

A desktop API client — Postman/Insomnia/Yaak territory — built around
keyboard-first UX and git-friendly on-disk storage, with a few things most
API clients don't have: built-in **k6 load testing**, an **embedded MCP
server** (Claude Code can drive the app directly), and an **MCP client
debugger** (inspect any MCP server's tools from inside AUK).

Go backend (Wails v2), SolidJS frontend.

![AUK](docs/screenshots/hero-native.jpg)

See [docs/FEATURES.md](docs/FEATURES.md) for the full tour, with screenshots
of every feature.

## Prerequisites

- Go 1.25+
- Node 20+ / npm
- [Wails v2 CLI](https://wails.io/docs/gettingstarted/installation)
- (optional, for load testing) a `k6` binary — see below

## Live development

```
wails dev
```

Runs the Go backend plus a Vite dev server with hot reload for the
frontend. The app also serves a plain-browser dev endpoint at
http://localhost:34115 with the same Go bindings available from devtools.

## Building

```
wails build
```

Produces a redistributable app under `build/bin/`. Configure product/build
metadata in `wails.json` — see the
[Wails project config reference](https://wails.io/docs/reference/project-config).

## k6 sidecar (load testing)

k6 is AGPL-3.0 licensed, so AUK never links or embeds it — it's invoked as an
arm's-length CLI subprocess, shipped unmodified. The binary isn't committed;
fetch the pinned version before building or running load tests:

```
build/sidecars/download-k6.sh macos-arm64
```

(Other targets: `macos-amd64`, `linux-amd64`, `linux-arm64`, `windows-amd64`.)
See [build/sidecars/README.md](build/sidecars/README.md) for the licensing
detail.

## Headless CLI

The same engine the GUI uses is also reachable headlessly, useful as a CI
smoke test:

```
apitool-cli run <requestID> --workspace-dir=DIR [--env=ENVIRONMENT_ID]
```

Assertions on the request (if any) determine the exit code.

## MCP server

AUK can expose itself as an MCP server (Settings → MCP Server) so Claude
Code or another MCP client can list workspaces/requests and run them
directly — see [docs/FEATURES.md](docs/FEATURES.md#embedded-mcp-server--let-claude-code-drive-the-app)
for details on the approval gating for mutating requests.
