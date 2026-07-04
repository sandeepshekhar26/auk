# Feature Roadmap

> Cross-platform API client (Wails: Go backend + web frontend). macOS first; Windows/Linux later. Local, git-friendly file storage; no mandatory cloud. This roadmap sequences (1) parity features inherited from Yaak, (2) the owner's differentiating features, and (3) recommended additions, then phases everything into shippable milestones.
>
> **Legend:** ★ = headline differentiator vs. Yaak/Postman/Insomnia/Bruno.
>
> **Status (2026-07-05):** checkboxes below reflect actual shipped state (this doc had never been updated as work landed — the `[ ]`s were stale, not accurate). Roughly: **v0.1 MVP is done**, **v0.5 is mostly done** (biggest gaps: OAuth1/NTLM/1Password auth, code-snippet gen beyond cURL, auto-update mechanics), **v1.0 is partial** (gRPC is unary-only — no streaming/`.proto` import; MCP Resources and full capability-grant UI are missing; no plugin runtime), **v2.0 is not started** (no Windows/Linux, no mock server, no HTTP/3, no cloud sync). See `api-tool-build-progress` session memory for the full shipped-feature list with dates.

---

## Features inherited from Yaak

Full parity checklist. These are table-stakes an established Yaak user expects; none are differentiators on their own. (Yaak stack for reference: Tauri + Rust + React/TypeScript, MIT/source-available, offline-first, no telemetry.)

### Workspaces, collections & organization
- [x] Workspaces (top-level containers for requests, environments, settings)
- [x] Arbitrarily nested folders (model + sidebar tree support `parentId`; **folder creation UI + folder-scoped variables were still missing — see 2026-07-05 additions below**)
- [ ] Folder/request settings inheritance (auth, headers, settings cascade down the tree)
- [x] Folder-scoped variables (2026-07-05)
- [x] Live sidebar filtering / request-tree search
- [ ] Multiple OS windows (Wails v2 is single-window — see 6.6; multi-window is a v3 feature, so v1 ships one main window with in-app panels/tabs) — **deliberately deferred to a v3 port, not a gap**
- [x] Multiple request tabs
- [x] Command palette (fuzzy launcher for navigation + actions)

### Request types (protocols)
- [x] HTTP / REST (redirects, custom methods)
- [x] GraphQL (query editor, variables; **schema doc explorer/introspection still missing**)
- [x] gRPC (unary only — server reflection; **streaming and `.proto`-file import still missing**)
- [x] WebSocket (bidirectional, interactive send/receive console)
- [x] Server-Sent Events (SSE) streaming request type
- [ ] Batch send (fire multiple requests at once)

### Environments & variable resolution
- [x] Named environments (dev/staging/prod)
- [ ] Sub-environments with fallback to base workspace environment
- [x] Environment color-coding (avoid prod mistakes) (2026-07-05)
- [x] `{{ functionName(args) }}` reference syntax (own grammar, not literal `${}`) — **autocomplete-while-typing in text inputs still missing**
- [x] Encrypted secrets via OS keychain (safe to commit whole workspace to git) — `internal/storage/secrets.go`, real Keychain/Credential-Manager/Secret-Service backing via `go-keyring`, values never touch the synced YAML

### Templating & dynamic values (template functions)
- [x] `{{ functionName(args) }}` syntax; nested functions
- [ ] Preview-vs-send render context (skip side effects during preview)
- [x] `uuid` (v4)
- [x] `timestamp.unix` / `unixMillis` / `iso8601`
- [x] `timestamp.format(unixSeconds, goLayout)` / `timestamp.offset(unixSeconds, duration)`
- [x] `hash.md5` / `sha1` / `sha256`
- [x] `encode.base64` / `base64url` / `url`
- [x] `cookie(name)` (read from cookie jar) (2026-07-05 — was a stub returning "not wired yet"; now backed by a real per-workspace jar)
- [x] `fs.read` (read file contents) — **note: this reads arbitrary local files at send time and is a different trust boundary than the pre-request script sandbox** (`internal/scripting` deliberately has zero ambient filesystem authority; this template function does not carry that same restriction). Flagging as a known tradeoff worth being aware of for a public repo, not something changed in this pass.
- [ ] `json` / `xml` / `regex` manipulation
- [ ] `prompt` (ask user for input at send time)
- [ ] `request.body` / `request.header` / `request.param` / `request.name`
- [x] `response.body` / `response.header` (pull from another request's last response) — response-chaining via `response('Name').path`
- [ ] Plugin-provided functions (Faker, TOTP, JWT generate/decode, shell, secret managers, date-add) — blocked on the plugin runtime (not started)

### Request chaining (piping values between requests)
- [x] `response.*()` chaining via JSONPath (JSON) — XPath/XML not built (no XML body support yet)
- [x] Auto-send dependency (referenced request with no cached response is sent first)
- [ ] Chaining via environment variables (store a `response()` tag in an env var, reuse widely)

### Authentication (built-in)
- [x] Basic Auth
- [x] Bearer Token
- [x] API Key (header or query param)
- [ ] OAuth 1.0
- [x] OAuth 2.0 (client-credentials grant only — no multi-grant/external-browser flow yet)
- [x] JWT auth
- [x] AWS Signature v4 (2026-07-05) — verified against AWS's own published SigV4 test suite (7 test cases) plus a real signed request against httpbin.org
- [ ] NTLM
- [x] Client certificates (mTLS) — backend + tests existed already; request-level UI added 2026-07-05
- [ ] 1Password integration (pull secrets) — needs the user's own 1Password CLI/account; not attempted without that decision
- [ ] Custom auth via plugins (e.g. RFC 9421 HTTP Message Signatures) — blocked on the plugin runtime
- [ ] Auth inheritance across workspace/folder/request levels

### Plugins & SDK
- [ ] Plugin runtime (TypeScript, Node.js runtime → npm ecosystem)
- [ ] `definePlugin` single-entry API
- [ ] Extension points: template functions, auth methods, importers, exporters, context-menu actions, workspace/folder actions, themes, response extensions
- [ ] Plugin CLI (`generate`, `dev`, `build`, `publish`, `auth login`)
- [ ] Public plugin directory / registry (install from settings)

**None of Plugins & SDK is started.** This is a large, self-contained milestone (embedding a Node/npm-ecosystem runtime, a extension-point API surface, a CLI, a registry) — sizing it honestly as its own future project rather than folding a partial slice into a general sweep.

### Response viewing
- [x] JSON pretty (syntax-highlighted) + raw modes
- [x] Response filtering via JSONPath + plain-text filter for large bodies (JSONPath filter added 2026-07-05; XPath not built — no XML support)
- [ ] Rich previews (HTML, images, binary)
- [x] Dedicated response headers tab
- [x] Timeline / debug tab (redirects, cookies, payload, headers, timing)
- [x] Streaming responses (SSE / WebSocket live view; gRPC is unary-only, nothing to stream yet)
- [x] Code-snippet generation from a request ("Copy as" cURL, Python `requests`, JS `fetch`, Go `net/http` added 2026-07-05)

### History, cookies & debugging
- [x] Per-request response history
- [ ] Cookie jar per workspace with manual cookie editing (an internal per-send `net/http/cookiejar` exists; no persistent workspace jar or edit UI)
- [ ] Custom proxy support
- [x] Redirect warnings (cross-origin / insecure downgrade flagged in the redirect chain) (2026-07-05)

### Import / export
- [x] Import: OpenAPI 3.0/3.1, Swagger 2.0, Postman Collection v2/v2.1, cURL — Insomnia v4+ not built
- [x] Copy as cURL / Paste cURL (round-trip individual requests)
- [x] Export workspace as single JSON file (2026-07-05)
- [ ] OpenAPI sync (keep workspace synced to a spec)

### Git sync & file storage
- [x] Local directory sync to plain-text YAML (one resource per file)
- [x] Built-in visual git client (init, status, commit, push, log) — branch create/switch and a diff viewer not built (today's slice: status+log+commit+push)
- [x] Secrets excluded from sync by default; keychain-encrypted secrets safely committable
- [x] Works with Dropbox / any file-based sync (plain YAML) — true by construction, no code needed

### UX, themes & keyboard
- [x] Command palette + autocomplete everywhere *(command palette + fuzzy nav yes; "autocomplete everywhere" in every text field is not built)* + fully customizable hotkeys *(shortcuts are fixed today, not user-rebindable)*
- [x] Built-in + community themes; build-your-own themes — System/Light/Dark shipped; the CSS-variable token system makes a new theme data-only, but no theme picker/import UI for custom ones yet
- [ ] Adjustable panes / sidebar customization
- [x] One-click send ("Run") affordance
- [ ] Auto-generated Markdown request docs

---

## Owner-requested features

The differentiating set. Each broken into concrete sub-tasks. Backend module stack per the design briefs: Go's native goroutine/`context.Context` concurrency model, a shared **Session** abstraction (`{id, context.Context+cancel, event sink}`) that every streaming protocol implements, and a single headless **ExecutionEngine** (one Go core-engine package) consumed identically by the Wails GUI, the MCP server, and the perf runner. Every outbound request passes through one policy chokepoint at `engine.Dispatch`.

### ★ 1. Performance / load testing with k6 (live + end-of-test reporting)

Bundle stock, unmodified, version-pinned k6 as an arm's-length **CLI sidecar**, invoke via `os/exec` child process only (AGPLv3 boundary — never `import "go.k6.io/k6/..."`, never vendor its source, never `xk6`-compile it into the app binary). **Sharper trap on Wails than on Tauri:** both the app and k6 are Go, so `go get go.k6.io/k6` "just works" and would silently make the *entire app* AGPL — do NOT do it. Wails has no first-class sidecar feature and its maintainer-recommended `go:embed` pattern is the *wrong* answer here; stage k6 into the `.app` bundle via a build step, as a separate file the user can swap.

| # | Sub-task | Notes |
|---|----------|-------|
| 1.1 | `download-k6.sh` build/Taskfile step → pinned k6 per target triple into the `.app` bundle `Contents/Resources/` (NOT `go:embed`-ed into the Go binary), checksum-verified | `k6-darwin-arm64`, etc.; hand-rolled per-arch staging (no Wails equivalent to Tauri `externalBin`) |
| 1.2 | Stage + individually codesign the k6 sidecar binary (bottom-up), then notarize the whole bundle (macOS Gatekeeper) | k6 JITs its sobek JS VM → likely needs `com.apple.security.cs.allow-jit` (+ maybe unsigned-exec-memory); entitlements are NOT inherited by the spawned process — sign k6 with its own entitlements |
| 1.3 | `ScriptGen` (in the core engine): saved request/collection → deterministic, readable k6 JS from a `text/template` template (inject user values as JSON-encoded JS literals, not raw concat) | secrets via `__ENV`; per-request `name` tags + custom `Trend`s; `discardResponseBodies: true` |
| 1.4 | Map chaining extractors to generated JS (`res.json()`, headers, regex) → scenario-scoped vars; treat as a tested translation layer (k6's sobek VM ≠ the in-app goja VM) | login → chained authenticated call |
| 1.5 | Go `Runner`: `exec.CommandContext` spawn with `--out json=-`, separate stdout (NDJSON) / stderr (console) | SIGINT for graceful stop (still runs `handleSummary`), SIGKILL fallback |
| 1.6 | NDJSON stream parser (`bufio.Scanner`, raise max-line buffer) + 1 s bucket aggregator (t-digest for live percentiles) → **one batched Wails event per bucket** | never emit per-line; Wails events have no backpressure + known drop-under-load issues — webview will choke at 50–500 VUs |
| 1.7 | Live charts: throughput (`http_reqs`), latency (`http_req_duration`), error rate (`http_req_failed`), VUs | via batched `runtime.EventsEmit` used as a wake-up signal; frontend pulls full series from a binding, not the event stream |
| 1.8 | End-of-test: `handleSummary()` writes `summary.json` (proper percentiles) + HTML report; Go ingests after exit | do NOT compute p95 from the raw NDJSON |
| 1.9 | Threshold → SLA gate builder UI: (metric, aggregation, operator, value) → `options.thresholds` string | p95<800, error rate<1%, rps≥N, per-request SLA, `abortOnFail` |
| 1.10 | Pass/fail verdict from **exit code** (99 = thresholds failed, 0 = pass); per-gate green/red from summary | deterministic, no parsing; read the numeric contract, don't import `errext/exitcodes` |
| 1.11 | Scenario/executor presets: Smoke, Load (`ramping-vus`), Stress / Spike (`ramping-arrival-rate`), Soak | open vs. closed model; warn on "insufficient VUs" |
| 1.12 | Multi-scenario concurrent runs (single process, multiple `scenarios` keys) | true k8s distribution is out of scope → later "export to k6-operator" |
| 1.13 | Run history: `perf/<collection>/runs/<ts-uuid>/` (script.js + summary.json + meta.json) git-friendly; SQLite index (gitignored) for querying | |
| 1.14 | Regression tracking: baseline run per collection; flag Δp95/Δp99/Δerror/Δrps beyond tolerance; trend sparklines | hard SLA (threshold) vs. soft drift (baseline) |
| 1.15 | Third-party-licenses screen: AGPLv3 text + k6 copyright + durable Corresponding Source (exact shipped tag/commit tarball on infra you control, not just a GitHub link) | required for redistribution; owner to confirm with counsel |

### ★ 2. WebSocket, gRPC, and HTTP support

Single `crypto/tls` config feeds all protocols; uniform `context.Context` cancellation; buffer HTTP bodies in Go (don't push raw bytes over the Wails bridge — it base64-encodes binary, ~33% inflation + no zero-copy path), route large/binary bodies via a loopback HTTP endpoint the webview `fetch()`es; emit typed Wails events only for genuinely incremental streams with Go-side coalescing/backpressure.

| # | Sub-task | Module / notes |
|---|----------|---------------|
| 2.1 | Session abstraction first (`{id, context.Context+cancel, event sink}`) — every streaming protocol implements it | `context.Context` is the one cancel idiom across all five protocols |
| 2.2 | HTTP/1.1 + HTTP/2 client (redirects, proxy, timeouts, gzip/brotli/zstd, cookie jar, streaming bodies) | `net/http` stdlib (own the `Transport`/`Client`); HTTP/2 auto-negotiated |
| 2.3 | Surface negotiated protocol (h1/h2/h3) in the UI | ALPN; `httptrace` for wire-level detail |
| 2.4 | HTTP/3 as opt-in per-request toggle, pluggable `RoundTripper` | `github.com/quic-go/quic-go/http3` (`http3.Transport` implements `http.RoundTripper`) — the only viable Go HTTP/3 lib |
| 2.5 | TLS / mTLS / custom CA / "disable verification" toggle (per-request, loudly labeled) | `crypto/tls` + `crypto/x509` (`RootCAs`, `Certificates`, `GetClientCertificate` for keychain-backed certs); `InsecureSkipVerify` as explicit, logged opt-in |
| 2.6 | Proxy: system, per-scheme, manual host:port:auth, `NO_PROXY` | `net/http` `Transport.Proxy`; SOCKS5 via `golang.org/x/net/proxy` |
| 2.7 | Connect / overall / idle timeouts; uniform cancel across all protocols | one `context.Context` per request scope |
| 2.8 | WebSocket: connect (custom headers, subprotocols, auth), send/receive log (↑/↓ + timestamp), Text/Binary composer, manual Ping, close code+reason chip | `github.com/coder/websocket` (ex-`nhooyr.io/websocket`, Coder-maintained) — context-native, concurrent-write-safe; **not** archived `gorilla/websocket` |
| 2.9 | gRPC schema acquisition — **both** server reflection and imported `.proto` files | `jhump/protoreflect/grpcreflect` (handles v1 + v1alpha); cache descriptors per endpoint; same machinery grpcurl uses |
| 2.10 | Runtime `.proto` compile with pure-Go `github.com/bufbuild/protocompile` (no vendored `protoc`, unlike Yaak) → `FileDescriptorSet` | Buf-maintained successor to deprecated `protoparse`; fallback: vendored protoc only if it can't parse |
| 2.11 | Dynamic message encode/decode via `google.golang.org/protobuf/types/dynamicpb` ↔ canonical protobuf JSON | `dynamicpb` is first-party in the official protobuf runtime; frontend editor is just a JSON editor |
| 2.12 | gRPC call routing from `MethodDescriptor` streaming flags → unary / server-stream / client-stream / bidi | `jhump/protoreflect/dynamic/grpcdynamic.Stub` over `google.golang.org/grpc` — no compiled stubs; same stack k6 uses in production |
| 2.13 | gRPC UI: metadata (headers), deadlines, TLS/mTLS + h2c toggle, bidi two-column log + half-close control | `google.golang.org/grpc` (canonical grpc-go); isolate behind an internal interface |
| 2.14 | GraphQL as HTTP layer (not a new transport): POST `{query,variables,operationName}`, GET, and WS subscriptions | reuse `net/http`; `github.com/hasura/go-graphql-client` for the `graphql-transport-ws` subscription handshake only |
| 2.15 | SSE request type reusing the HTTP client config (spec reconnection with `Last-Event-ID`, backoff) | hand-rolled `text/event-stream` parser on the `net/http` body (natural backpressure into the persistence sink) or `github.com/r3labs/sse/v2` |

### ★ 3. Request chaining (pipe values between requests)

Two layers on **one** resolver + headless engine. Build the script engine first; template functions are thin wrappers over it.

| # | Sub-task | Notes |
|---|----------|-------|
| 3.1 | Embed `github.com/grafana/sobek` (goja fork, k6's own engine) behind a `ScriptEngine` interface (vanilla `dop251/goja` disqualified — single-maintainer bottleneck; v8go/otto disqualified — CGo/ES5) | **100% pure Go — no CGo, no per-target C toolchain; cross-compile "just works." Prior rquickjs MSVC-cross-compile risk is REMOVED.** |
| 3.2 | Sandbox: fresh `Runtime` per run, zero ambient authority (no fs/net/process bound), `SetMaxCallStackSize` + `Interrupt()`/`ClearInterrupt()` watchdog for gas/timeout; `GOMEMLIMIT` as process-level memory backstop | untrusted, git-shared scripts; note: goja/sobek has no native per-script memory cap (documented limitation) |
| 3.3 | Single `{{ … }}` interpolation grammar in every field (vars, response refs, template functions) | |
| 3.4 | Declarative response refs building an auto-resolving dependency **DAG** (Yaak model): `{{ response('LoginReq').body.token }}`, `.jsonpath()`, `.header()`, `.status`, `.regex()`, `{index}` | address by stable slug/ID, not display name |
| 3.5 | Cache keyed by `(requestId, resolved-inputs-hash)` + refresh policy (always / if-missing / TTL) | avoid re-login on every keystroke |
| 3.6 | `ctx` scripting global (Postman/Bruno-shaped): `ctx.request`, `ctx.response.json()/jsonpath()`, `ctx.vars/env/collection`, `await ctx.sendRequest()/fetch()`, `ctx.test()/expect()`, crypto/uuid/base64 helpers | `sendRequest`/`fetch` bind ONLY to `engine.Dispatch` (never `net/http`) so script-originated calls can't bypass the policy gate |
| 3.7 | `pm.*` → `ctx.*` and `bru.*` → `ctx.*` compat shim (painless Postman/Bruno import); load Chai-style assertion + CryptoJS as pure-JS polyfills into each runtime; deliberately do NOT expose Node `fs`/`require` (the vm2 footgun) | adoption accelerant |
| 3.8 | In-memory vars default; explicit `{persist}` writes to env file; secrets → keychain, never git | git-friendliness lever |
| 3.9 | Explicit **Runner/Flow**: ordered steps, per-step capture rules, `runIf`/retries/delay/continue-on-error, data-driven CSV/JSON iteration | stored as plain YAML/JSON (diffable) |
| 3.10 | Headless `ExecutionEngine` emitting structured events (StepStarted, RequestSent, ResponseReceived, Captured, Assertion, Error) | GUI, MCP, perf runner are all thin consumers |

### ★ 4. MCP support — the app EXPOSES an MCP server

Yaak deprecated its app-exposed MCP server in favor of a CLI — this is a vacated differentiator. Spec revision 2025-11-25; official Go SDK `github.com/modelcontextprotocol/go-sdk` v1.6.x (v1.6.1 current as of 2026-07; GitHub's own MCP server migrated to it from mark3labs/mcp-go to track the spec). Treat long-running ops as streaming progress notifications, not the still-maturing Tasks primitive.

| # | Sub-task | Notes |
|---|----------|-------|
| 4.1 | Embedded Streamable-HTTP MCP server as a goroutine in the Wails Go process, bound to `127.0.0.1`, random high port | `mcp.NewStreamableHTTPHandler` on a `net/http` mux; shares the same core engine, keychain, and cache — no second process to re-establish access |
| 4.2 | Thin stdio→HTTP bridge sidecar for zero-config `.mcp.json` users (same tool code, different transport) | ~20-line forwarder injecting the bearer token; optional fallback, not the primary path |
| 4.3 | Bearer pairing token (minted by app, OS-keychain stored, rotatable) via `auth.RequireBearerToken` middleware + loopback-only bind (anti DNS-rebinding) | constant-time compare; reject on missing/wrong token |
| 4.4 | Read/discovery tools (`readOnlyHint`): `list_workspaces`, `list_environments`/`resolve_env`, `list_requests`, `get_request`, `get_last_response`, `get_collection_run`/`get_perf_run` | secrets redacted by default (resolve_env returns references only) |
| 4.5 | Execution tools: `run_request`, `run_collection`, `run_perf_test` (`stream:true` → `notifications/progress`), `open_websocket`/`send_websocket_message`, `call_grpc`, `cancel_run` | `openWorldHint:true`; never `readOnlyHint` even for GET |
| 4.6 | Authoring tools (`destructiveHint` where relevant): `create/update_request`, `create_environment`/`set_env_var`, `delete_request` | route through approval unless full-auto |
| 4.7 | MCP Resources for read-only browsing (`apiclient://workspace/{id}/request/{id}` `resource_link`s + subscribe) | avoid tool round-trips |
| 4.8 | Capability scoping / grants: Read-only (default) / Run-ask-before-each / Full-auto; narrowable to workspaces/envs | revoke invalidates token |
| 4.9 | Server-side policy engine at the **engine dispatch chokepoint** (not the MCP boundary): hold non-GET or production-env calls for in-app approval modal; allow/deny rules; `create/delete` gating; audit log | tag origin as `OriginMCP`; a click, a chained-script call, and an MCP call are indistinguishable to the gate — none can bypass it; native Wails modal is the real approval UI, not client-relayed elicitation |
| 4.10 | Structured tool results: `structuredContent` + `outputSchema` on `run_request`/`run_perf_test`/`resolve_env`/`get_last_response`; truncate large bodies + `resource_link` to full | powers agentic chaining (`extracted.token` → next call) |
| 4.11 | "Connect to Claude Code" button: generate/rotate token, write `claude mcp add api-tool --transport http http://127.0.0.1:PORT/mcp --header "Authorization: Bearer <token>"` / `.mcp.json`, detect Cursor/Claude Desktop, copy-paste fallback | don't make a solo dev hand-edit JSON |

### 5. Lightweight

| # | Sub-task | Notes |
|---|----------|-------|
| 5.1 | Prefer pure-Go / no-CGo deps: `grafana/sobek` (no V8/QuickJS C toolchain), `bufbuild/protocompile` over vendored `protoc`, `modernc.org/sqlite` over CGo `mattn/go-sqlite3`, `crypto/tls` over OpenSSL | each choice defends the "lightweight" thesis AND keeps cross-compile/notarize CI toolchain-free |
| 5.2 | Treat SQLite as a disposable derived cache (`modernc.org/sqlite`, WAL, FTS5 via `-tags sqlite_fts5`); definitions live in files | blow away + rebuild from files on migration failure |
| 5.3 | Buffer HTTP bodies in Go; keep binary bodies off the Wails bridge (loopback HTTP `fetch()` / save-to-disk / hex-on-demand) | Wails base64-encodes binary + no zero-copy path; the bridge is the main perf trap (worse on Windows WebView2) |
| 5.4 | Single static-binary bundle (no bundled Chromium/Node runtime — rides the OS webview); measure installer + memory footprint as a release gate | budget ~80–250MB for a featureful app, not the vendor "~10MB"; smoke-test the macOS WKWebView memory-leak reports early |

### 6. Fast GUI

| # | Sub-task | Notes |
|---|----------|-------|
| 6.1 | Binding + event IPC model (never request/response over sockets from JS); frontend calls generated Go bindings, backend pushes via `runtime.EventsEmit` | Go struct fields need `json` tags to generate TS types |
| 6.2 | Go-side coalescing/backpressure so a fast WS/gRPC/k6 stream never makes the webview the bottleneck; persist the stream server-side as system-of-record (no lossy ring buffer), treat Wails events as a wake-up signal + pull history via a binding | Wails events have NO documented backpressure + known drop-under-load issues — this is a build-it-yourself layer |
| 6.3 | Keyboard-first: command palette, full shortcut coverage, autocomplete everywhere | HTTPie Desktop is the model |
| 6.4 | Virtualized request tree + response viewer for large workspaces/bodies | |
| 6.5 | Instant startup / no telemetry / offline-first as measured non-functional requirements | fast startup is architecturally credible (no runtime to boot) but unverified — measure your actual bundle |
| 6.6 | **Wails version decision: build on v2 (stable), architect for a later v3 port.** Keep the Wails binding layer thin so the v2→v3 migration is cheap; multi-window (item under "Workspaces") and the built-in updater are v3-only | v3 is alpha with no committed GA date; the core-engine bet already isolates Wails-specific code |

---

## Added features (recommended)

High-value additions beyond Yaak, from the competitor analysis. One-line rationale each; ordered by value × fit.

| Feature | Best-in-class | Rationale (one line) |
|---------|---------------|----------------------|
| ★ Headless CLI runner (`run` with non-zero exit + JUnit/HTML/JSON reporters) | Bruno CLI (`bru run`) | Unblocks CI/CD and is the **same execution core** the MCP `run_collection` and k6 tools already call — highest leverage. |
| Assertion engine (declarative jsonBody + JSON Schema, optional JS) | Bruno | Yaak's flagship gap and a prerequisite for the runner/MCP to be useful; JSON Schema gives contract testing for free. |
| Data-driven iteration (CSV/JSON feeds the runner) | Bruno / Newman | Run one request over N rows without loops; core to smoke/contract suites and shared with the Flow runner and k6. |
| Tag-filtered runs (`--tags smoke,release-gate`) | Bruno CLI | Same collection runs a smoke subset vs. full regression across different CI stages. |
| Official Docker image + first-party CI action | Bruno | Zero-boilerplate CI adoption; step outputs (passed/failed/total/duration) for gating. |
| Code-snippet generation (curl + 10–12 languages) | Insomnia | Low effort, high daily value; expected by every dev switching from Insomnia/Postman. |
| Layered environment inheritance (global → collection → env → local, explicit precedence) | Postman | Real multi-env projects need base + per-env overrides; Yaak's model is comparatively flat. |
| ★ Response diffing (two responses / history / cross-env) | under-served everywhere | Genuinely unmet across all competitors and synergizes with git-friendly storage — strong "beyond Yaak" differentiator. |
| Environment-comparison / regression runs (same suite vs. two envs) | Insomnia runner | Diff local vs. dev/prod responses to catch drift; pairs with response diffing. |
| ★ Bundled OpenAPI mock server (spawn Prism/Microcks or built-in) | Prism / Microcks | "Mock + load-test before the backend exists" pairs directly with the k6 story — a clear leap past Yaak. |
| GraphQL introspection + typed autocomplete/validation (auto-fetched) | Hoppscotch / HTTPie | Makes GraphQL genuinely usable; Yaak has GraphQL but weaker introspection UX. |
| Keyboard-first command palette + full shortcut coverage | HTTPie Desktop | Cheap-ish, directly reinforces the "lightweight/fast GUI" positioning. |
| Two-tier secrets vault (master key in keychain → per-workspace key → XChaCha20-Poly1305 values) | Yaak model | Encrypted-but-shareable environments safely committable to git; keychain holds exactly one small secret. |
| AI-assisted assertion/request scaffolding (from OpenAPI spec or observed traffic) | Postman Agent Mode / Insomnia AI | Cuts test-authoring time; **defer** — the MCP-server bet covers "Claude drives the app," so build deterministic primitives first. |
| HAR import/export | (Yaak lacks it) | Small, commonly-requested interop gap. |

---

## Phased delivery plan

Four milestones. Each lists rough scope; ★ marks the differentiators that justify the product's existence. Principle: build the **headless ExecutionEngine + Session abstraction + file storage** first, because every later feature (GUI, CLI, MCP, k6) is a thin consumer of them.

### v0.1 — MVP (prove the core, macOS only)
Goal: a fast HTTP client with git-friendly storage that a solo dev would actually use daily.

| Area | Scope |
|------|-------|
| Foundation | Session abstraction; headless `ExecutionEngine` + `Dispatch` policy chokepoint; Wails v2 binding+event IPC; `grafana/sobek` behind `ScriptEngine` interface (pure Go — no cross-compile de-risking needed) |
| Storage | YAML one-file-per-resource (`go.yaml.in/yaml/v3`, deterministic key-sort + golden-file diff test; explicit `orderKey` field, fractional index) + canonical writer; `modernc.org/sqlite` cache (WAL, FTS5); on-disk layout; stable IDs |
| Secrets | Two-tier keychain-rooted vault (`github.com/zalando/go-keyring` + `golang.org/x/crypto/chacha20poly1305` XChaCha20); env excluded from git by default; auto `.gitignore` |
| Protocols | HTTP/1.1 + HTTP/2 (`net/http`): methods, redirects, proxy, timeouts, TLS/mTLS/custom-CA, cookie jar, decompression |
| Core UX | Workspaces, nested folders, environments (+ colors), request tabs, sidebar filter, response viewer (JSON pretty/raw, headers, timeline), history |
| Templating | `{{ }}` grammar + core template functions (uuid, timestamp, hash, encode, prompt) |
| Chaining ★ | Declarative `response()` refs + auto-resolving DAG (basic) |
| Auth | Basic, Bearer, API Key, OAuth 2.0, JWT |
| Import/export | OpenAPI, Postman, Insomnia, cURL import; Copy/Paste as cURL |
| Git | In-app git via `github.com/go-git/go-git/v5` (commit/pull/push/diff; shell out to `git` for exotic auth/remote cases) |

### v0.5 — Differentiators land
Goal: ship the features that make this "beyond Yaak."

| Area | Scope |
|------|-------|
| Protocols ★ | WebSocket (`coder/websocket`), SSE (hand-rolled parser or `r3labs/sse`), GraphQL (HTTP + introspection + WS subscriptions via `hasura/go-graphql-client`) |
| Chaining ★ | Full `ctx` scripting API + sandbox (`Interrupt` watchdog / `SetMaxCallStackSize` / `GOMEMLIMIT`); `pm`/`bru` compat shim; explicit Flow runner (capture rules, control flow, data-driven iteration) |
| Assertions | Declarative jsonBody + JSON Schema assertions (optional JS) |
| CLI runner ★ | Headless `run` with non-zero exit + JUnit/HTML/JSON reporters; tag-filtered runs |
| k6 perf ★ | k6 CLI sidecar bundled/signed; ScriptGen; NDJSON live parse + batched charts; `handleSummary` end-of-test report; threshold SLA gates + exit-code verdict; executor presets |
| Codegen | Code-snippet generation (curl + 10–12 languages) |
| Auth | Add OAuth 1.0, AWS SigV4, NTLM, client certs, 1Password |
| Distribution ★ | **Auto-update mechanics** — Wails v2 ships no updater; wire `github.com/minio/selfupdate` + self-hosted manifest, swapping a fully codesigned+notarized new bundle (binary-patch-then-hope invalidates the Gatekeeper signature). New engineering vs. Tauri's first-party updater — sized as a real milestone, not a bolt-on. |

### v1.0 — Agent-drivable + polished
Goal: full protocol coverage, the MCP differentiator, and competitive polish.

| Area | Scope |
|------|-------|
| Protocols ★ | gRPC full (reflection via `jhump/protoreflect/grpcreflect` + `.proto` via `bufbuild/protocompile`; `dynamicpb`/`grpcdynamic` dynamic messages; unary/server/client/bidi over `google.golang.org/grpc`); batch send |
| MCP server ★ | Embedded Streamable-HTTP server (`modelcontextprotocol/go-sdk`) as a goroutine + stdio bridge; full tool surface (read/execute/authoring); bearer token (`auth.RequireBearerToken`) + loopback bind; capability grants; policy engine at dispatch chokepoint + approval modal + audit log; structured results/outputSchema; "Connect to Claude Code" button |
| k6 perf ★ | Run history (git-friendly) + SQLite index; baseline/regression tracking + trend sparklines; AGPLv3 third-party-licenses screen + durable Corresponding Source |
| Response diff ★ | Two-response / history / cross-env diffing + environment-comparison runs |
| Environments | Layered inheritance (global → collection → env → local) with explicit precedence |
| GraphQL | Full introspection UX (typed autocomplete, inline docs, schema explorer) |
| Plugins | Plugin runtime + SDK + `definePlugin` extension points; plugin CLI |
| UX | Full customizable hotkeys, themes (+ community), auto-generated request docs, rich response previews |

### v2.0 — Ecosystem & scale
Goal: broaden platforms, mock/test ecosystem, and optional collaboration.

| Area | Scope |
|------|-------|
| Platforms | Windows + Linux release (keychain via Credential Manager / Secret Service through `go-keyring`; graceful fallback where no keyring daemon; CRLF-safe `.gitattributes`; flag Windows WebView2 large-payload IPC latency) |
| Mock server ★ | Bundled OpenAPI mock (spawn Prism/Microcks or built-in) — "mock + load-test before backend exists" |
| CI ecosystem | Official Docker image + first-party CI action with step outputs |
| Perf at scale | "Export to k6-operator" manifest for distributed/k8s runs |
| Interop | HAR import/export; OpenAPI sync |
| HTTP/3 ★ | Opt-in per-request h3 toggle (`quic-go/quic-go/http3` pluggable `RoundTripper`) |
| AI-assist | AI-generated assertions / request scaffolding from OpenAPI spec or observed traffic |
| Sync | Optional cloud sync layered on the same file format (git remote wrapper first; CRDT later) — strictly additive, never required |
| Plugin directory | Public registry + install-from-settings |
| Multi-window ★ | Revisit Wails v3 (once GA): native multi-window (detached response viewer, floating env inspector, separate k6 dashboard) + v3's built-in bsdiff updater — a real v2→v3 port, not a recompile |

---

### Differentiator summary (the "why this over Yaak" list)

0. ★★ **UX is the winning bet** — the fastest, most keyboard-fluent, least-cluttered API client on the market. Features get copied; feel does not. This is the primary differentiator and a constraint on every milestone, not a feature in one. Governed by [05-ux-north-star.md](05-ux-north-star.md).
1. ★ **k6 performance/load testing** with live + end-of-test reporting — Yaak has nothing like it.
2. ★ **First-class app-exposed MCP server** — Yaak deprecated its own in favor of a CLI; this space is vacated.
3. ★ **Full multi-protocol + strong request chaining on one headless engine** — GUI, CLI, MCP, and k6 all consume identical execution logic.
4. ★ **Headless CLI runner + assertion engine** — Yaak's two biggest documented gaps; unblocks CI and makes the MCP/perf tools useful.
5. ★ **Response diffing** and ★ **bundled OpenAPI mock server** — under-served across all competitors; both synergize with git-friendly storage and the k6 story.
