# API Tool — Overview

A cross-platform-capable API client (REST/GraphQL/gRPC/WebSocket/SSE) built on **Wails** — Go backend, web frontend — that ships three things no incumbent combines: **k6-powered load testing with live reporting**, a **first-class embedded MCP server** so Claude Code can drive the app, and **git-friendly plain-file storage**. macOS first; Windows/Linux when it earns the audience.

> **The wedge is UX.** Features get matched; feel does not. The bet behind this product is that a *dramatically* better user experience — instant startup, sub-frame interactions, keyboard-first everything, zero-config, no clutter — is what converts users, not the feature checklist. Every feature below is necessary but not sufficient; the UX is the moat. See [05-ux-north-star.md](05-ux-north-star.md) for the standard every screen is held to.

## Vision & positioning

The API-client market splits into two camps: cloud-first tools that trade lock-in for collaboration (Postman, Insomnia), and local-first tools that trade collaboration for git-friendliness and speed (Yaak, Bruno). This product stays firmly in the local-first camp — offline, no accounts, no telemetry, files as the source of truth — and then adds the capabilities that camp is missing.

- **vs. Yaak** — Yaak is the closest reference (also a native-webview desktop app with YAML-on-disk git sync). But Yaak has *no load/perf testing*, *no JS pre/post-request scripting or test assertions*, *no collection runner with reporting*, and it **deprecated its app-exposed MCP server in favor of a CLI**. Every one of those is a headline feature here. Where Yaak vacated the "app is a first-class MCP server" position, this product plants a flag.
- **vs. Postman** — no mandatory cloud, no account wall, no proprietary sync. Collaboration is git — reviewable, diffable, mergeable plain text — not a hosted workspace. Postman's strengths worth borrowing (layered environment inheritance, snippet codegen, agent-mode authoring) are adopted without its cloud gravity.
- **vs. Bruno** — Bruno nails the git-native, CLI-runnable, assertion-driven workflow, but is HTTP/GraphQL-centric and has no perf testing or embedded MCP. This product keeps Bruno's runner/assertion ergonomics (declarative assertions, non-zero CI exit, JUnit/HTML reports) and extends to gRPC/WS/SSE, load testing, and agent control.

The through-line: **one headless execution engine** — request + chaining + scripting + variable model — reused identically by the GUI, the MCP server, and the k6 perf orchestrator. A request authored once is runnable in the UI, callable by Claude over MCP, and compilable to a k6 load test, because all three consume the same core.

## Feasibility summary

Every pillar is grounded in a mature, verified Go module or binary — this is integration work, not research.

- **Multi-protocol backend** — `net/http` (HTTP/1.1+2) with `quic-go/quic-go/http3` as a pluggable `RoundTripper` for HTTP/3, `coder/websocket` (WebSocket; the `nhooyr` successor — `gorilla/websocket` is archived), `google.golang.org/grpc` + `jhump/protoreflect` + `bufbuild/protocompile` + `dynamicpb` (fully dynamic gRPC via reflection or imported protos, no build-time stubs — the same stack k6 itself uses), and a hand-rolled `text/event-stream` parser on `net/http` for SSE (backpressure straight into the persistence sink). GraphQL is HTTP + a parser, not a new transport. All five unify under one Go "session" abstraction (id + `context.Context` cancel + typed channel to the webview), giving one honest cancellation idiom across every protocol.
- **Load testing** — k6 is a single static Go binary, staged into the app bundle as an arm's-length CLI **sidecar** and driven via `os/exec`. Live metrics stream via `--out json=-` (NDJSON on stdout, aggregated Go-side); the end-of-test report comes from `handleSummary`. Pass/fail is the k6 exit code (99 = thresholds failed). No linking, no `go get go.k6.io/k6`, no `xk6` — the CLI process boundary is deliberate and load-bearing (see licensing).
- **Embedded MCP server** — the official `modelcontextprotocol/go-sdk` (`NewStreamableHTTPHandler`) running as a goroutine on the Wails process's runtime, bound to `127.0.0.1`, bearer-token gated via the SDK's `auth.RequireBearerToken` middleware. Tools call directly into the live core engine — the same code path the GUI uses.
- **Chaining & scripting** — declarative `{{ response('Req').body.token }}` refs that build an auto-resolving dependency DAG (Yaak's model), plus an imperative `ctx`-style scripting API on **`grafana/sobek`** (the actively maintained goja fork k6 itself uses) — pure Go (no CGo), native capability sandbox (no ambient `fs`/`require`), interrupt-driven timeout, and cheap per-run contexts.
- **Storage & secrets** — YAML one-file-per-resource via `go.yaml.in/yaml/v3` (the spec-org successor to the archived `gopkg.in/yaml.v3`; deterministic key ordering) as source of truth; `modernc.org/sqlite` (pure-Go, WAL + FTS5 via `-tags sqlite_fts5`) as a disposable cache for history/responses; two-tier XChaCha20-Poly1305 secret encryption (`golang.org/x/crypto/chacha20poly1305`) rooted in the OS keychain via `zalando/go-keyring`. In-app git via pure-Go `go-git/go-git` v5, falling back to the user's `git` for exotic remote/auth cases.

The only material de-risking items: smoke-test the macOS WKWebView host process early for the known Wails idle-memory-leak reports, validate the Wails event bridge under sustained k6/stream load (raw `EventsEmit` has no backpressure — treat it as a wake-up signal and pull payloads via bindings), and confirm macOS codesigning/notarization of the bundled k6 sidecar (each nested Mach-O signed individually, with its own hardened-runtime + JIT entitlements).

## Locked decisions

These are settled; downstream design assumes them.

1. **Wails v2 (Go backend + web frontend).** Fixed. Build on v2 (stable/GA) now, keep the Wails binding layer thin, and architect for a later v2→v3 migration once v3 exits alpha (its native multi-window + built-in updater are attractive but not worth the alpha tax for v1). Not re-litigating Electron/Tauri/Swift.
2. **Git-friendly plain-file storage.** YAML, one resource per file, deterministic serialization, a fractional/lexicographic `orderKey` for merge-safe ordering (touch one file per reorder), IDs (not paths) for hierarchy. The user's git repo is the primary sync mechanism; the SQLite DB is a rebuildable cache and is git-ignored.
3. **k6 as a bundled sidecar, invoked via CLI.** Stock, unmodified, version-pinned per target arch, staged into the bundle by the packaging step — **never** `go:embed`-ed into the Go binary (the Wails-idiomatic embed pattern is the *wrong* answer here) and never linked. **The k6-AGPL trap is sharper on Wails than it was on Tauri:** both the app and k6 are Go, so `import "go.k6.io/k6/..."` compiles cleanly, autocompletes, and looks like an ordinary dependency add — and doing so statically links AGPLv3 code into a single combined binary, forcing the *entire* app (engine, MCP server, GUI glue) under AGPLv3 with a Corresponding Source obligation. The CLI child-process boundary is the only escape valve. We keep it, and we ship k6's exact matching source per AGPL §6/§13 alongside each release (a durable, resolvable source archive, not just a GitHub pointer).
4. **Embedded MCP server (Streamable HTTP over loopback), first-class.** In-process on the Wails runtime; a thin stdio→HTTP bridge sidecar is offered for zero-config `.mcp.json` setups. Both terminate at one tool implementation and one policy engine.
5. **`crypto/tls` as the single TLS backend**, so one cert/mTLS/custom-CA config (custom CA pools, `GetClientCertificate` for keychain-backed client certs, an explicit loud `InsecureSkipVerify` toggle) feeds all protocols.

## Non-goals

- **No cloud sync, accounts, or team server (v1, likely ever as a requirement).** Collaboration is git/file-based. Optional cloud may layer on the same file format later — never as a dependency.
- **No distributed/multi-node load generation in-app.** k6 runs single-process, multi-scenario, locally. Kubernetes/`k6-operator` or Grafana Cloud is out of scope; at most a future "export to k6-operator manifest."
- **No custom `xk6` builds, no importing/vendoring k6's Go packages, no embedding k6 as a library** — it changes the licensing posture (whole-app AGPL) and the bundling story.
- **No running the load test itself in the app's JS engine.** sobek handles *this app's* pre/post scripts; k6 owns load generation in its own runtime.
- **No telemetry, no mandatory network calls, no proprietary lock-in.**
- **Windows/Linux are not v1 targets** — the stack (pure-Go modules, no CGo except the webview) is chosen to keep them cheap, but macOS ships first. The Windows WebView2 large-payload IPC latency cliff is a known, flagged cross-platform risk, not a v1 concern.

## High-level feature pillars

1. **Multi-protocol client** — HTTP/1.1, HTTP/2, HTTP/3 (opt-in), GraphQL (with introspection tooling), gRPC (unary + all three streaming modes, via reflection or imported protos), WebSocket, and SSE — all sharing one streaming/cancellation session model and one TLS config.
2. **Performance/load testing (k6)** — generate a k6 script from any saved request/collection, run it as a sidecar, stream live charts (throughput, p95/p99, error rate, VUs), gate on threshold-based SLAs, and persist git-friendly run reports with regression/drift tracking against a baseline.
3. **Request chaining & scripting** — a two-layer model: declarative response references (auto-resolving DAG) for the 80% case, and a sandboxed `ctx`/`pm`-shaped scripting API (with a Postman/Bruno compat shim) for conditional logic, computed signatures, and assertions.
4. **App-exposed MCP server** — Claude Code (and any MCP client) drives the running app: `run_request`, `run_collection`, `run_perf_test`, `get_last_response`, authoring tools, and more — with structured results, live progress notifications, and a server-side policy engine (per-action approval, production-environment gates, secret redaction, audit log).
5. **Git-friendly local storage** — YAML files as source of truth, in-app git UI (commit/branch/diff/push), keychain-rooted encrypted-but-shareable secrets, and a disposable SQLite cache.
6. **Fast, keyboard-first GUI — the primary differentiator.** Command palette, full shortcut coverage, instant startup, streaming that *feels* live, and a lightweight footprint — treated as the product's core thesis, not an afterthought. This is the pillar the others are judged against: a feature that degrades the UX doesn't ship until it doesn't. Governed by [05-ux-north-star.md](05-ux-north-star.md).

Beyond the required differentiators, the near-term roadmap steals the highest-leverage competitor wins Yaak lacks: a **headless CLI runner** with non-zero-exit + JUnit/HTML/JSON reporters (the same engine the MCP and perf tools call), a **declarative assertion engine** (jsonBody + JSON Schema for lightweight contract testing), **code-snippet generation**, **layered environment inheritance**, and **response diffing** (under-served everywhere, and a natural fit for git-backed storage).

## Why this can win

The reference project already proved the local-first, git-friendly, native-webview API client is desirable — and then walked away from exactly the capabilities that make such a tool indispensable to a modern developer: it has no load testing, no scripting or assertions, no runner, and it retreated from being an agent-drivable MCP server. This product occupies that vacated ground with a single architectural bet — one headless execution engine behind the GUI, an embedded MCP server, and a k6 orchestrator — so that "run it yourself," "let Claude run it," "run it in CI," and "load-test it" are all the same code path with guaranteed-consistent behavior. For a solo dev who lives in git and increasingly works alongside an AI agent, that combination — lightweight, offline, git-native, load-testing-capable, and Claude-drivable — doesn't exist yet in one tool.

But the durable advantage isn't the feature list — competitors can copy features. It's **the experience of using it**: the fastest, most keyboard-fluent, least-cluttered API client on the market. Features are the reason to try it; UX is the reason to stay. That is the winning bet, and everything downstream is held to it (see [05-ux-north-star.md](05-ux-north-star.md)).
