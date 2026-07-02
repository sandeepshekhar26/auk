# Technical Architecture: A Wails-Based Multi-Protocol API Client

**Status:** Design v2 (critique-hardened, **ported to Wails/Go**) · **Target:** macOS first, Windows/Linux later · **Stack (fixed):** **Wails v2 (Go backend + web frontend)**, tracking v3; SolidJS + TS + Vite + CodeMirror 6 + uPlot + Tailwind frontend unchanged.

This document specifies the full architecture and supersedes the Tauri/Rust design. The load-bearing decision that everything else hangs off is the **headless `core` engine package**: a UI-agnostic execution core (now a Go package, was a Rust crate) that the GUI, the MCP server, the CLI runner, the perf-test generator, and request chaining all consume as a library. Every "run" verb in the product resolves to exactly one code path — and, critically, every outbound request passes through **one policy chokepoint** inside that path.

This revision folds in the prior design review **and** ports the stack from Tauri/Rust to Wails/Go. The headline changes:

- **Ported to Wails/Go.** The Rust workspace becomes one Go module with `internal/` packages; the Tauri command/Channel/event layer becomes **Wails bindings + `runtime.EventsEmit`**; the Tokio async model becomes **goroutines + `context.Context`**. The webview frontend (SolidJS/TS/Vite/CodeMirror 6/uPlot/Tailwind) is unchanged because Wails is also webview-based.
- **Wails v2 chosen, v3 tracked.** v3 (native multi-window, first-party updater, service/DI model) is still **alpha** with no committed GA date; v2 is the stable, production line (prior art: Restmate). We build on v2 now and keep the Wails binding layer thin so a later v2→v3 migration is a port of glue, not of the engine. (§3, Known risks R24)
- **Policy enforcement stays off the MCP tool boundary and inside the engine's request-dispatch chokepoint**, so script-initiated `pm.sendRequest()`/`ctx.sendRequest()` calls can no longer bypass approval gates.
- **`EventSink` is async and backpressure-aware**, with a per-consumer shedding policy; the engine never synchronously blocks on a slow consumer. This is *more* load-bearing on Wails, whose `EventsEmit` is not just backpressure-free but has a **live emit-path data race (#2448) and inconsistent delivery under rapid emit (#2759)** — so the coalesce + pull-history-via-binding + **single dedicated emitter goroutine** pattern is load-bearing for *correctness*, not just throughput (§6, R3).
- **MCP re-baselined onto the official `github.com/modelcontextprotocol/go-sdk` (v1.x)** and MCP spec 2025-11-25 semantics, using the standardized async **Tasks** primitive (best-effort) and **elicitation** for approval — not bespoke session-id long-poll and a private event side channel.
- **Storage de-risked and simplified by the Go move**: the Rust-YAML-ecosystem crisis is gone — we use `go.yaml.in/yaml/v3` (spec-org-maintained successor to the archived `gopkg.in/yaml.v3`), keep a golden round-trip corpus, use **fractional order keys** instead of integer `sortPriority`, keep **secrets out of git entirely**, and run a **post-merge integrity pass**.
- **k6 stays an arm's-length CLI sidecar — now a SHARPER trap.** Because both Wails and k6 are Go, `import "go.k6.io/k6/..."` compiles cleanly and would make the *entire app* AGPLv3. The engine must never import k6; k6 is invoked via `os/exec` only, and we ship its corresponding source. (§11, R9)
- **Scope cuts for v1 unchanged**: in-app git UI, HTTP/3, distributed k6, and full Postman-shim parity are explicitly deferred.

---

## 1. Architectural principles

1. **The engine is a library, not a service.** All request execution, variable resolution, chaining, and scripting live in a pure-Go package with zero Wails, zero UI, zero global state. Wails-bound methods, the MCP server, and the CLI are thin adapters over it. "Run request" behaves identically — *modulo an explicit, inspectable policy* (principle 7) — whether a human clicks it, Claude calls the MCP tool, or CI runs the CLI.
2. **Go owns every socket.** The webview never opens a connection. It sends *commands* (Wails binding calls) and subscribes to *event streams*. Mandatory for streaming protocols (WS/gRPC/SSE) and for cancellation.
3. **Command + event, not request/response.** Anything long-lived returns a *handle* (session id or MCP Task id) immediately and pushes incremental data back over a typed, backpressured channel.
4. **Files are the source of truth; SQLite is a disposable cache.** Deleting the DB loses history, never a request definition.
5. **One session abstraction unifies all protocols** — but *cancellation semantics are per-kind and surfaced honestly in the UI* (principle 8). `Session = { id, context.CancelFunc, async event sink }`. HTTP-streaming, WS frames, gRPC streams, SSE events, and k6 metrics are all implementations of it.
6. **`crypto/tls` is the single TLS source of truth**, fed into every protocol client; OS-trust-store fallback is opt-in only.
7. **One policy chokepoint, not per-adapter gates.** *Every* outbound request — GUI, CLI, MCP, and script-initiated — passes through `PolicyEngine.Authorize(dispatchCtx)` at the moment of dispatch. Behavior is "identical except for an explicit policy object," and that object is the *only* place side effects (persist, prod writes, request budgets) are parameterized.
8. **Backpressure and cancellation are honest, not aspirational.** The event bus is async and can shed load per consumer; Cancel has documented, per-kind latency the UI shows ("stopping…" vs. immediate). On Wails this is *not optional* — `EventsEmit` gives us no flow control, so the engine owns batching and the frontend pulls history from a query binding rather than trusting the event stream (§6).

---

## 2. Component diagram

```mermaid
graph TB
    subgraph webview["Webview Frontend (Solid + TypeScript)"]
        UI[UI: request editor, response viewer,<br/>perf charts uPlot, WS/gRPC console]
        Store[Solid stores + TanStack Query]
        Editor[CodeMirror 6 editors]
        UI --> Store
        UI --> Editor
    end

    subgraph wailsproc["Wails Backend Process (Go, goroutine-scheduled)"]
        Cmd[gui app: Wails-bound struct methods<br/>+ runtime.EventsEmit plumbing]
        MCP[mcp: embedded go-sdk StreamableHTTPHandler<br/>127.0.0.1 + bearer token + Tasks]
        subgraph enginepkg["core (UI-agnostic library package)"]
            Exec[Engine<br/>RunRequest / RunFlow / RunPerf]
            Dispatch{{Dispatch chokepoint<br/>PolicyEngine.Authorize}}
            Vars[Variable scope chain + template resolver + unified DAG]
            Sessions[Session registry<br/>id, context.CancelFunc, async sink]
            EventBus[Backpressured event stream]
        end
        Proto[protocols: HTTP/WS/gRPC/GraphQL/SSE<br/>behind Protocol interface]
        Script[scripting: goja/sobek sandbox<br/>behind ScriptEngine interface]
        Store2[storage: canonical YAML files + modernc.org/sqlite cache]
        Perf[perf: k6 orchestrator + NDJSON parser]

        Cmd --> Exec
        MCP --> Exec
        Exec --> Vars
        Exec --> Sessions
        Exec --> EventBus
        Exec --> Dispatch
        Script -. ctx.sendRequest .-> Dispatch
        Vars -. auto-send prerequisite .-> Dispatch
        Dispatch --> Proto
        Exec --> Script
        Exec --> Store2
        Exec --> Perf
        Dispatch -.approval via elicitation/Task.-> Cmd
        Dispatch -.approval via elicitation/Task.-> MCP
    end

    subgraph sidecars["Sidecar Processes"]
        K6[k6 binary<br/>bundled in .app Resources or first-run download<br/>NEVER go:embed'd, NEVER imported]
        Bridge[apiclient-mcp-bridge<br/>stdio to HTTP shim, NO auto-launch]
    end

    subgraph clibin["Separate Binary"]
        CLI[cli: headless runner<br/>JUnit/HTML/JSON reporters, NO Wails import]
    end

    UI -- Wails binding call --> Cmd
    Cmd -- EventsEmit / loopback fetch for large bodies --> Store
    Perf -- os/exec spawn + NDJSON stdout --> K6
    Bridge -- forwards --> MCP
    ClaudeCode[Claude Code / Cursor] -- MCP over HTTP --> MCP
    ClaudeCode -. stdio .-> Bridge
    CI[CI pipeline] --> CLI
    CLI --> Exec

    Store2 -- git-friendly YAML --> Disk[(Workspace dir<br/>= git repo)]
    Store2 -- cache --> SQLite[(.app/cache.sqlite)]
```

The single most important structural property is unchanged from the Tauri design and survives the port: **`Dispatch` (the policy chokepoint) sits between the engine and the protocol layer**, and *both* script `ctx.sendRequest` and template auto-send route through it. There is no path to a socket that skips policy. The port adds two Wails-specific realities visible in the diagram: the k6 sidecar is bundled in the `.app` bundle (not `go:embed`'d — that would taint the app under AGPL, §11), and large/binary bodies leave the backend over a **loopback HTTP fetch**, not the Wails bridge (§6).

---

## 3. High-level layering: engine vs binding layer vs webview

| Layer | Runs where | Responsibility | Must NOT |
|---|---|---|---|
| **`core` + sibling `internal/` packages** | In-process Go library | All execution logic: protocol I/O, variable/template resolution, chaining DAG, scripting, storage, perf orchestration, **and the policy chokepoint**. Emits structured events over an async sink. | Import any `github.com/wailsapp/wails/*` package, touch the webview, hold UI state, or write to stdout |
| **Wails binding layer (`cmd/gui`)** | Wails backend process | Translate bound-method calls into engine calls; bridge the engine's event stream to the webview via `runtime.EventsEmit` (+ loopback HTTP for large bodies); own window/menu/tray; host the embedded MCP server goroutine; render approval prompts (fed by the engine's policy events). | Contain business logic. It is glue only. |
| **Webview frontend** | WKWebView (macOS) | Presentation, editing, virtualized lists, charts, keyboard UX. Calls bound methods, subscribes to events. | Open sockets, parse protocols, hold the source of truth |

The key inversion vs. a naive Wails app: **business logic does not live in the Wails-bound struct methods.** Those are one-line adapters. The logic lives in `core`, so the CLI (a completely separate binary with no Wails) and the MCP server reuse it verbatim. **CI enforces this** by building `./cmd/cli` and asserting no `github.com/wailsapp/wails` appears in `go list -deps ./cmd/cli` (see §16, milestone-gated).

### Wails version: v2 now, v3 tracked

Wails **v3 is alpha** (`alpha.102` at writing, no committed GA date; the maintainer has declined to commit a timeline). Its v3-only features — **native multi-window and a first-party auto-updater with `bsdiff` deltas** — are real, but not worth eating alpha instability on for a v1 ship. **Wails v2 is the stable, production line** (semver-tagged, real changelog; the Restmate REST client is direct prior art). v2's API surface (single-window, context-threaded runtime calls) differs enough from v3 (service/DI application model replacing the monolithic `wails.Run()`, `wails3`/Taskfile build system, changed runtime-call signatures) that migration is a **real project, not a port of glue** — budget it as such, especially because `cmd/gui` will accrete (it owns the loopback body server, the MCP goroutine wiring, the single event-emitter goroutine, the approval-modal plumbing, *and* the `EventsEmit`↔binding-pull dance). The isolation discipline keeps it bounded to that one package, but "bounded" is not "a weekend." **Decision: build on v2; revisit v3 at its beta.** (R24)

**Multi-window is a v1 NON-GOAL, because v2 cannot do it** (v2 is a single `wails.Run()`; multi-window is a v3-only feature). This is a *hard* constraint for a product whose differentiator is UX: **detached response viewer, floating env inspector, and a separate k6 dashboard window are OFF THE TABLE for the v1 ship.** The v1 UX must deliver its keyboard-fluent, uncluttered feel **inside a single window using panels/overlays/split-panes** — not by assuming detached windows the chosen stack can't render. Those affordances wait for the v3 migration; do not let the UX design depend on them. (R29)

---

## 4. Module & package layout

One Go module, `internal/` packages for the engine tree (unimportable outside the module — a hard Go-enforced boundary the Rust workspace approximated only by convention), and `cmd/` for the three binaries.

```
apiclient/                          # single Go module (go.mod)
├── go.mod / go.sum                 # one module; pins below in §8/§10/§16
├── .golangci.yml                   # depguard rule: internal/core... may not import wailsapp/wails
├── internal/
│   ├── core/                       # THE SPINE — UI-agnostic execution library
│   │   ├── engine.go               # Engine: RunRequest/RunFlow/RunPerf
│   │   ├── dispatch.go             # THE CHOKEPOINT: Authorize -> protocol send
│   │   ├── session.go              # Session registry, context.CancelFunc, async sink
│   │   ├── vars.go                 # scope chain (runtime>env>collection>global)
│   │   ├── template.go             # {{ }} grammar; PURE, side-effect-free resolver
│   │   ├── chaining.go             # unified dep graph (declared + script edges), cycle+budget
│   │   ├── policy.go               # Authorize(DispatchContext): approval, prod/method guards
│   │   ├── events.go               # StepStarted/RequestSent/Captured/Assertion/Error + async EventSink
│   │   └── model.go                # Workspace/Request/Environment/Flow domain types
│   ├── protocols/                  # transport implementations behind one interface
│   │   ├── traits.go               # Protocol / ProtocolSession interfaces
│   │   ├── http.go                 # net/http (h1/h2) — thin client, own the Transport
│   │   ├── ws.go                   # github.com/coder/websocket
│   │   ├── grpc.go                 # google.golang.org/grpc + protoreflect + protocompile + dynamicpb
│   │   ├── graphql.go              # net/http POST + parser + hasura/go-graphql-client (subs)
│   │   ├── sse.go                  # hand-rolled text/event-stream parser on net/http
│   │   └── tls.go                  # single crypto/tls Config builder
│   ├── storage/                    # files (source of truth) + sqlite (cache)
│   │   ├── files.go                # canonical YAML writer, one-resource-per-file
│   │   ├── yamlcodec.go            # go.yaml.in/yaml/v3 + canonicalizer + golden corpus test
│   │   ├── order.go                # fractional index keys (vendored fracdex-style)
│   │   ├── integrity.go            # post-merge integrity + repair pass
│   │   ├── db.go                   # modernc.org/sqlite (pure Go, WAL, FTS5 via build tag)
│   │   ├── secrets.go              # keychain-rooted vault; NO ciphertext in git; export/import
│   │   └── git.go                  # go-git v5 status/diff read-only view (NOT a full git UI)
│   ├── scripting/                  # sandboxed JS behind an interface
│   │   ├── engine_iface.go         # ScriptEngine interface (swappable)
│   │   ├── goja.go                 # grafana/sobek impl: interrupt watchdog + call-stack cap
│   │   └── api.go                  # `ctx`/`pm`/`bru` global (documented SUBSET) + compat matrix
│   ├── perf/                       # k6 subsystem
│   │   ├── scriptgen.go            # request/flow -> k6 JS template (text/template, JSON-escaped)
│   │   ├── runner.go               # os/exec spawn, SIGINT cancel, exit-code verdict
│   │   └── ndjson.go               # NDJSON parse + 1s bucket aggregator (t-digest)
│   └── mcp/                        # official go-sdk server: tool defs + dispatch into engine
│       ├── server.go               # StreamableHTTPHandler mount + optional stdio serve
│       ├── tools.go                # run_request/run_collection/run_perf_test/... (Tasks-aware)
│       ├── tasks.go                # async Task adapters (working/input_required/…) — best-effort
│       └── auth.go                 # bearer token (auth.RequireBearerToken), 127.0.0.1 bind, grants
├── cmd/
│   ├── cli/                        # SEPARATE BINARY (no Wails) — CI runner
│   │   └── main.go                 # `apiclient run` -> core.Engine, reporters
│   └── gui/                        # the Wails app binary
│       ├── main.go                 # wails.Run(&options.App{...}), OnStartup wires Engine
│       ├── app.go                  # bound struct: thin adapters over core.Engine
│       ├── events.go               # engine events -> runtime.EventsEmit (+ loopback body server)
│       └── mcp_task.go             # go mcp.Serve(ctx, engine, token, addr) on OnStartup
├── build/                          # Wails packaging + sidecar staging (Taskfile/Makefile)
│   ├── darwin/                     # entitlements.plist, sign+notarize scripts
│   └── bin/                        # staged sidecars per arch (NOT go:embed'd)
│       ├── k6-darwin-arm64                     # (or downloaded at first run — see §11.1)
│       └── apiclient-mcp-bridge-darwin-arm64
├── wails.json                      # Wails v2 project config
└── frontend/                       # Solid + Vite + TS + CodeMirror 6 + uPlot + Tailwind
```

**Dependency direction (strict):** `protocols`, `storage`, `scripting`, `perf` ← `core` ← {`mcp`, `cmd/cli`, `cmd/gui`}. Nothing below `core` knows Wails exists, and **no engine-tree package may import `github.com/wailsapp/wails/*`** (enforced by a `golangci-lint` `depguard` rule *and* the `go list -deps ./cmd/cli` CI check). `core` re-exports the domain model so all three adapters share types. Using `internal/` means Go's compiler itself forbids anything outside this module from importing the engine — a stronger boundary than the Rust workspace had.

---

## 5. The core execution engine and its five consumers

`core` exposes a small, event-emitting surface. The sink is **async and fallible** (backpressure) and every network egress funnels through `dispatch`:

```go
type Engine struct {
    Protocols ProtocolRegistry // http/ws/grpc/graphql/sse
    Scripts   ScriptEngine     // goja/sobek
    Store     *Storage         // files + sqlite
    Sessions  *SessionRegistry // id -> {cancel func, async sink}
    Policy    *PolicyEngine    // Authorize(DispatchContext)
    Vars      *VariableResolver// scope chain + PURE template resolver
    Graph     *ChainGraph      // unified declared+script dependency graph
}

func (e *Engine) RunRequest(ctx context.Context, req RequestRef, rc RunContext, sink EventSink) (RequestOutcome, error)
func (e *Engine) RunFlow(ctx context.Context, flow FlowRef, rc RunContext, sink EventSink) (FlowOutcome, error)
func (e *Engine) OpenSession(ctx context.Context, req RequestRef, rc RunContext, sink EventSink) (SessionHandle, error)
func (e *Engine) RunPerf(ctx context.Context, target PerfTarget, sink EventSink) (PerfOutcome, error)
func (e *Engine) Cancel(sessionID SessionID)

// THE chokepoint (unexported). Everything that touches a socket calls this,
// incl. scripts and auto-send.
func (e *Engine) dispatch(ctx context.Context, out OutboundRequest, rc *RunContext) (ProtocolResponse, error)
```

Every entrypoint takes a `context.Context` as its first argument — this is the single cancellation idiom threaded through all five protocols, replacing Tokio's mixed `select!`/cancellation-token patterns with one uniform mechanism (§6).

### 5.1 The async, backpressure-aware `EventSink`

A fire-and-forget `emit` is the wrong shape. A fast gRPC/WS stream or a 500-VU k6 run can outrun a slow consumer (a laggy MCP client, a busy webview). A synchronous `emit` either **blocks the engine goroutine** or **drops events silently**. Fixed:

```go
type EventSink interface {
    // Returns ErrSinkLagged when the consumer can't keep up; the engine
    // then applies this sink's declared ShedPolicy rather than blocking.
    Emit(ctx context.Context, ev ExecutionEvent) error
    ShedPolicy() ShedPolicy // Coalesce | DropOldest | Block(bounded)
}
```

Each consumer supplies its own sink *and* its own shedding strategy. Internally every session gets a **bounded per-session channel** (`chan ExecutionEvent`); the engine writes to it and never blocks on an unbounded/slow consumer directly. This is the seam that lets one engine serve five callers without one slow caller stalling the others — **and it is doubly load-bearing on Wails**, because `runtime.EventsEmit` has no backpressure of its own (§6, R3):

| Consumer | How it reuses the engine | Sink implementation | Shed policy |
|---|---|---|---|
| **GUI** (`cmd/gui`) | bound `RunRequest` → `engine.RunRequest(...)` | Forwards each event to `runtime.EventsEmit` → live UI; large bodies via loopback HTTP | **Coalesce** (per-frame batching; capped emit rate 10–30Hz) |
| **MCP server** (`internal/mcp`) | `run_request` tool → same call; long ops as **Tasks / progress notifications** | Buffers events into `structuredContent`; emits Task status + `NotifyProgress` | **DropOldest** for progress, buffer terminal result |
| **CLI runner** (`cmd/cli`) | `apiclient run flow.yaml` → `engine.RunFlow(...)` | Aggregates into JUnit/HTML/JSON reporters; exit code from verdict | **Block(bounded)** — CLI wants every event |
| **Perf generator** (`internal/perf`) | `RunPerf` codegens k6 from the *same* `RequestRef`/`FlowRef` | Parses k6 NDJSON back into the **same `ExecutionEvent` bus** | **Coalesce** (1s buckets) |
| **Request chaining** | Not a separate caller — it's the unified DAG resolver used *inside* `RunRequest`/`RunFlow` | inherits caller's sink | inherits |

The payoff is unchanged — a request authored once is runnable in the GUI, callable by Claude via MCP, runnable in CI, and compilable to a k6 load test — but now no consumer can stall the shared runtime, and (per §5.2) no consumer can escape policy.

### 5.2 The dispatch chokepoint (closing the script-bypass hole)

If policy sat at the *MCP tool boundary only*, a pre-request script doing `ctx.sendRequest('DeleteUser')` would re-enter the engine and reach the network **without passing that gate** — a real security hole for a product whose whole point is letting an LLM run mutating requests.

The gate lives in `Engine.dispatch`, the single function every egress path calls:

```go
func (e *Engine) dispatch(ctx context.Context, out OutboundRequest, rc *RunContext) (ProtocolResponse, error) {
    switch d := e.Policy.Authorize(ctx, NewDispatchContext(&out, rc)); d.Kind {
    case DecisionAllow:
        // proceed
    case DecisionNeedsApproval:
        if err := rc.RequestApproval(ctx, d.Prompt); err != nil { // elicitation/Task or Wails modal
            return ProtocolResponse{}, err
        }
    case DecisionDeny:
        return ProtocolResponse{}, &PolicyDeniedError{Why: d.Why}
    }
    return e.Protocols.ForScheme(&out).Execute(ctx, &out, rc)
}
```

`DispatchContext` carries origin (GUI/CLI/MCP/script), method, resolved URL, environment flags (`production`), and the caller's capability grant. Because template auto-send (§9) and script `sendRequest` both funnel here, the gate is **genuinely uniform**. The "behaves identically" principle is now precise: *identical except for the policy object*, which is explicit and inspectable.

---

## 6. Async / concurrency model

**Goroutines scheduled by the Go runtime**, not a manually-configured async runtime. The engine's methods are ordinary blocking Go calls made concurrent by the caller's goroutines; the engine is safe for concurrent use and shared as a single `*core.Engine` (held in `AppState` and passed to Wails' `OnStartup`, the MCP server goroutine, and the CLI `main`). **The engine imports nothing from Wails** — it uses `context.Context` and standard goroutines so the CLI links without Wails.

The Tokio→Go move is a genuine simplification for this app: there is no separate async runtime to configure, no `spawn_blocking` distinction (blocking DB/file I/O just runs on its own goroutine and the scheduler handles it), and `context.Context` is the *single* cancellation primitive across every protocol library we chose (§8) — replacing Tokio's mix of `select!`, `CancellationToken`, and per-crate cancellation shapes.

**The real Wails-leak vector is `context.Context` value smuggling, not an `import`.** `go list -deps` catches `import wailsapp/wails` in the engine — but there is a subtler coupling it *cannot* catch. Wails threads its runtime through the `context.Context` you get in `OnStartup` and bound methods; `runtime.EventsEmit(ctx, …)` **requires that specific Wails-flavored ctx**. The tempting shortcut is to pass that ctx down into the engine so the engine can emit directly. That imports nothing (the ctx is opaque `context.Context`) yet **couples the engine to Wails' runtime at the value level** — and the CLI's plain `context.Background()` will then silently no-op or panic on `EventsEmit`. The firewall is the **`EventSink` interface (§5.1): the engine emits *only* via `EventSink`, never reaches into `ctx` for a runtime.** Enforced by a test that runs `RunRequest`/`RunFlow` under a plain `context.Background()` with a test `EventSink`, asserting no reliance on a Wails-flavored ctx (§16 exit criteria, R19).

### Sessions and cancellation — made honest

Every long-lived operation registers a `Session`:

```go
type Session struct {
    ID     SessionID
    Cancel context.CancelFunc // cancels the per-session context
    Kind   SessionKind        // Http | Ws | Grpc | Sse | Perf | Script
}
```

A naive claim that "one Cancel button works everywhere because everything takes a context" is false in three places. The real semantics are documented and *surfaced*:

| Kind | Cancel mechanism | Latency the UI shows |
|---|---|---|
| **HTTP unary/stream** | cancel the request `context.Context`; in-flight `Do`/body read unblocks | Immediate |
| **WS / gRPC** | send clean `Close`/half-close, then cancel context (`coder/websocket` and grpc-go both take a context per call) | ~1 RTT ("closing…") |
| **SSE** | cancel context → the `net/http` body read loop unblocks | Immediate |
| **Script (goja/sobek)** | **`Runtime.Interrupt()` from a watchdog goroutine** — a synchronous `while(true)` runs pure JS bytecode with no goroutine yield, so cancelling the Go context alone can't kill it; the interpreter polls the interrupt flag between VM instructions. Must `ClearInterrupt()` before runtime reuse. | Bounded by the interrupt poll (sub-ms in practice) |
| **Perf (k6)** | `cmd.Process.Signal(os.Interrupt)` (graceful; teardown + `handleSummary` still run), `Kill()` fallback after grace | Seconds ("stopping test…") |
| **Blocking DB write** | Go/`context` cannot force-abort an in-flight `modernc.org/sqlite` statement mid-execution; capped operation size + `busy_timeout`/statement timeout so it can't outlive a cancel by seconds | Bounded by op cap |

Two Go-specific cancellation caveats we design around: (1) `Interrupt()` only fires while JS bytecode is executing — any host function we expose that can block (e.g. `pm.sendRequest`) must itself honor the request `context.Context` so it unblocks on its own, not wait for the JS-level interrupt to reach it; (2) the script timeout/interrupt budget is a *fraction* of the node's overall context deadline, threaded through pre-script → request → post-script as one deadline, so a slow pre-script can't silently make a "5s request timeout" take 15s. The UI presents Cancel as one button but shows a per-kind state ("Cancel" → "closing…"/"stopping…").

### Streaming to the webview — the critical performance detail (and Wails' honest limits)

Wails' event system is a **notification/wake-up mechanism, not a reliable high-throughput data pipe** — and the problem is **delivery correctness, not merely backpressure**. `runtime.EventsEmit(ctx, name, data...)` / `EventsOn(name, cb)` form a pub/sub layer with **no documented backpressure, queueing, coalescing, or slow-consumer signal**, *plus two verified upstream defects we cannot fix and must design around*:
  - **A live data race in the emit path** ([Wails #2448](https://github.com/wailsapp/wails/issues/2448)): concurrent `EventsEmit` (from engine goroutines) and `EventsOn` (from the webview mounting/unmounting listeners as tabs open) races `runtime.mapaccess1_faststr` inside `notifyBackend` under `-race`. This is *exactly* our access pattern — many session goroutines emitting while the frontend churns subscribers.
  - **Inconsistent delivery under rapid emit** ([Wails #2759](https://github.com/wailsapp/wails/issues/2759)): fire-per-frame emitting drops/reorders data. The "coalesce, don't fire-per-frame" rule below is therefore **mandatory for correctness, not a performance tuning knob.**

So the pull-history-via-binding pattern is **load-bearing for correctness**, not just throughput, and it *reinforces* — rather than relaxes — every constraint from the Rust design:

- **Serialize *all* `EventsEmit` calls through a single dedicated emitter goroutine** (one `ctx`, one emit call site). The per-session bounded channels (§5.1) fan **into** this one emitter, which drains them and emits. This dodges the #2448 data race (only one goroutine ever touches the emit path) since we can't patch upstream — make "one emitter goroutine" an explicit, enforced invariant, not an accident.
- **Coalesce/batch on the Go side for *every* streaming protocol, not just k6.** Emit at a **capped rate (10–30Hz)** with the engine coalescing intermediate messages server-side (per #2759, this is non-negotiable). A chatty WS at thousands of msg/s cannot go frame-per-`EventsEmit`.
- **Treat `EventsEmit` as a wake-up, and have the frontend PULL full history from a query binding** rather than trusting the event stream as the source of truth. The event says "there are N new frames since seq X"; the frontend calls a bound `GetStream(sessionID, fromSeq)` to fetch them. This is the backpressure discipline the Rust design achieved with a typed `Channel<T>`, adapted to Wails' weaker primitive. **Cost to acknowledge:** a coalesced 20–30Hz stream means 20–30 binding round-trips/sec/session, each marshalled across the same Go↔JS bridge. Harmless on macOS/WKWebView, but this pull path is *more* bridge-chatty than a push stream would be — so it lands squarely on the WebView2 latency cliff (§15) on the future Windows target. The mitigation there is the same as for bodies: batch harder / lower the emit frequency per platform.
- **Never push large or binary payloads through `EventsEmit` or bindings.** Wails **auto-base64-encodes binary data** across the Go↔JS boundary (~33% inflation + JSON string overhead; there is no zero-copy `ArrayBuffer` path, open upstream since 2024). For HTTP response bodies over a threshold, file downloads, gRPC binary payloads, and k6 report artifacts, write to a temp file or serve from a **small loopback HTTP server** the webview fetches via ordinary `fetch()`, and pass only a handle/URL through the Wails bridge.
- **The full stream is persisted Go-side as the system of record; the UI is a virtualized window over it — never a lossy ring buffer as the source of truth.** For a WS/gRPC debugging tool, the dropped frame is often *the bug*; shedding is a *rendering* concession only. Every frame is appended to SQLite/disk; the UI shows "N not rendered (scroll/expand to load)," and the data is always recoverable.
- **Windows exposure (future target):** the ~200ms large-payload IPC cliff on Windows is a **WebView2 platform problem** (`chrome.webview.postMessage` shared-memory is slow), and Wails rides the same WebView2 control with the same base64+JSON path — Wails has *no* documented equivalent to Tauri's "Raw Requests" mitigation, so it is likely worse-or-equal, not better. This is macOS-first, so it's a flagged Windows risk (§15, R15), and the loopback-HTTP-for-large-bodies pattern above is the mitigation on both platforms.

---

## 7. Data model & storage

Follows the "files are truth, SQLite is a cache" split. The prior design had four latent defects (crumbling YAML ecosystem, integer ordering collisions, "committable ciphertext" merge hell, dangling-reference silent corruption). All four are addressed here — and the Go move *dissolves* the first one rather than merely mitigating it.

### 7.1 Files (git-tracked, one resource per file, canonical YAML)

`app.<type>.<slug>.yaml` per Workspace / Request / Folder / Environment / Chain / Perf-def. Rationale: two people editing two *different* requests never touch the same file → git auto-merges.

**The YAML-ecosystem risk that dominated the Rust design is largely gone in Go** — but requires one deliberate import choice, not muscle memory. In Rust the situation was genuinely bad: `serde_yaml` archived, `serde_yml` flagged **RUSTSEC-2025-0068 (unsound, segfault-capable)**, `serde_norway`/`serde_yaml_ng` competing forks with no upstream authority. Go's situation is the *opposite structure* — a single, clean handoff:

- **`gopkg.in/yaml.v3` (the muscle-memory default) is itself archived/unmaintained** — the trap is defaulting to it.
- **The successor is `go.yaml.in/yaml/v3`**, adopted by the official YAML organization (the people who own the YAML spec), byte-identical drop-in API, frozen-legacy/security-fixes-only, with active `go.yaml.in/yaml/v4` for future feature work. **We import `go.yaml.in/yaml/v3`** (zero migration cost from the API every Go dev already knows) and evaluate v4 only after the on-disk schema stabilizes.
- No vendored-emitter gymnastics are needed (the Rust design had to *own* the emitter because no healthy dep existed). `go.yaml.in/yaml/v3`'s `Marshal` produces **deterministic sorted map-key ordering** natively — the diff-stable property we need — so we lean on it and add a **golden round-trip corpus in CI** as insurance, not as a workaround for a missing library.

Serialization discipline that makes diffs clean:
- **Deterministic key order** (yaml.v3 sorts map keys; for user-visible ordered lists — headers, requests in a folder — model order as an explicit `orderKey` field on a struct/slice, *not* map iteration or YAML key order, since Go map iteration is randomized), **block scalars** for multiline bodies/scripts/GraphQL, **defensive quoting**, **normalize-on-write** so imported files converge, **LF + `.gitattributes`** for future Windows CRLF safety.
- **Golden round-trip corpus in CI.** A fixture set of real requests/flows/envs is serialized, re-read, re-serialized, and byte-compared; **any reformat fails CI.** This pins the hard cases (Norway problem: `yes`/`no`/`on`/`off`/`~`/`null`; numeric-looking strings; folded vs literal block scalars; trailing-space in block scalars; unicode escaping) and catches the known yaml.v3 mixed string/number key-sort quirk (go-yaml #194, fixed in v3 — pin the version and assert it). Use `yaml.Node` where comments/formatting must survive hand-edits.
- **Ordering uses fractional/lexicographic index keys, not integer `sortPriority`.** Two people inserting siblings both pick the "next" integer → same value → conflict or silent reorder. Fractional keys let concurrent inserts pick distinct keys *between* neighbors with no coordination; reorder is still a one-field diff. Given maintenance uncertainty on `github.com/rocicorp/fracdex`, we **vendor a small (~150–300 LOC) fracdex-compatible implementation** (ported from the well-known `rocicorp/fractional-indexing` reference logic) rather than take a long-term external dep for something this load-bearing to the merge story.
- **Hierarchy is by ID reference** (`folderId`, `parentId`), not directory nesting → moves stay one-field diffs. **Stable ULIDs** never change on rename; chain references survive renames.

### 7.2 Post-merge integrity pass

ULID-by-reference removes rename churn but introduces a subtler failure: Person A deletes request X; Person B adds a chain step referencing X; **git auto-merges both (different files) into a referentially broken workspace with no conflict marker.** Git-friendliness here trades textual conflicts for *silent logical corruption*.

`integrity.go` runs on every workspace load (and post-`git merge`/`pull` via a hook we install): detects **dangling ULID references, duplicate/adjacent order keys, orphaned folder membership**, reports them in a "Workspace health" panel, and offers one-click repair (drop dangling step, rebalance colliding keys, reparent orphans). This turns silent corruption back into a visible, resolvable event.

### 7.3 SQLite (git-ignored under `.app/`, fully rebuildable)

Request/response **history**, **k6 run results** (time-series), response cache, **FTS5 search index**, transient run state (cookie jars, in-flight sessions), UI state, OAuth tokens (encrypted), **and the append-only stream log** that backs the WS/gRPC/SSE viewers (§6). Engine: **`modernc.org/sqlite`** — the **pure-Go** transpile of SQLite, **no CGo**, so it cross-compiles and notarizes cleanly (the whole reason to prefer it over `mattn/go-sqlite3`, whose CGo cross-compilation is exactly the toolchain fragility we're avoiding). WAL mode; **FTS5 enabled via `-tags sqlite_fts5`** (not on by default). Wrapped so blocking calls run on their own goroutines; statement/busy timeouts set. Migration table; on failed migration, **blow it away and rebuild from files.** SQLite usage is disposable-cache-only, so the ~1.5–2× raw-throughput gap versus CGo SQLite is irrelevant; if k6 live-metrics ingestion ever needs more, that one hot path can use an append-only file sink without changing the DB driver.

### 7.4 Secrets vault (keychain-rooted, NOT committed)

Committing ciphertext is a diff/merge disaster (new nonce every re-encrypt → opaque full-line diff; secret-vs-secret merges are unresolvable by humans). Decision, cleanly:

- **Secrets never enter git.** Master key (32 B) in OS keychain via **`github.com/zalando/go-keyring`** (macOS Keychain, Windows Credential Manager, Linux Secret Service) → per-workspace key → values encrypted with **XChaCha20-Poly1305** (`golang.org/x/crypto/chacha20poly1305`, the 192-bit-nonce variant so a random nonce per encryption is safe), stored **only** in the local vault / `.app/` and `*.secret.yaml`, all `.gitignore`d. A **pre-serialization guard refuses to emit any value flagged `secret` into a git-tracked file.**
- **Team sharing is an explicit encrypted export/import path**, not committed ciphertext — so the clean-diff thesis is never undermined.
- **Pin `zalando/go-keyring` with a `go.mod` comment** noting the macOS backend concern (go-keyring #110: shelling out to `/usr/bin/security` is weaker than a linked Keychain Services call); prefer a version/path that calls Security.framework directly if available. On Linux, plan a graceful encrypted-file fallback for headless environments with no Secret Service daemon; passphrase + argon2 fallback for CI.

### 7.5 On-disk layout

```
workspace/                    # = git repo root
├── .gitignore                # .app/, *.local.yaml, *.secret.yaml
├── .gitattributes            # *.yaml text eol=lf
├── app.workspace.yaml
├── environments/  app.env.*.yaml     # secret values OMITTED (kept in local vault)
├── folders/       app.folder.*.yaml  # flat; tree via IDs; fractional order keys
├── requests/      app.request.*.yaml # http/ws/grpc/graphql
├── chains/        app.chain.*.yaml
├── perf/          app.perf.*.yaml
└── .app/          cache.sqlite (+ -wal), vault/, runtime/   # GIT-IGNORED
```

Files reference secrets **by name/id only** (e.g., `Authorization: "{{secrets.prod_api_key}}"`); the value never round-trips into a git-tracked file. `.app/` is disposable/rebuildable — deleting it loses local history/index, never source-of-truth data.

---

## 8. Protocol layer: one interface, five transports

A common interface unifies execution and cancellation; GraphQL is deliberately *not* a transport (it's HTTP + tooling). `Protocol.Execute`/`Open` are reached **only via `Engine.dispatch`** (§5.2) — the protocol layer never sees an unauthorized request.

```go
type Protocol interface {
    Execute(ctx context.Context, req *ResolvedRequest, rc *RunContext) (ProtocolResponse, error)
    Open(ctx context.Context, req *ResolvedRequest, rc *RunContext, sink EventSink) (ProtocolSession, error)
}

type ProtocolSession interface {
    Send(ctx context.Context, frame OutboundFrame) error
    Close(ctx context.Context) error
}
```

| Protocol | Module(s) — **pinned verified-current (2026-07)** | Notes |
|---|---|---|
| **HTTP/1.1, HTTP/2** | **`net/http` (stdlib)** — thin client, own the `Transport` | h1/h2 built in (h2 auto-negotiated via `golang.org/x/net/http2`). We deliberately **skip `resty`** and hand-roll a thin client so we get inspector-grade access to headers, timing, TLS handshake (`httptrace`), and per-request cancellation via `context.Context`. Cookies, gzip/br/zstd, proxy (`Transport.Proxy` = `ProxyFromEnvironment` + `NO_PROXY`; SOCKS5 via `golang.org/x/net/proxy`), per-request timeouts. Surface negotiated version in UI. |
| **HTTP/3** | **Out of v1 scope** (`github.com/quic-go/quic-go/http3` if ever) | `http3.Transport` implements `http.RoundTripper`, so it *would* plug into the same `http.Client` cleanly — but it's out of v1. If ever needed, it's a per-request transport swap, not a global build flag (a real improvement over Rust's workspace-global `reqwest_unstable` cfg, which is why this risk is downgraded, R17). |
| **WebSocket** | **`github.com/coder/websocket`** (formerly `nhooyr.io/websocket`) | **`gorilla/websocket` is archived (Dec 2022) — not adopted.** `coder/websocket` is context-native for every read/write/close (matches "honest cancellation"), handles concurrent writes safely (gorilla panics on concurrent `WriteMessage`), Coder-maintained since 2024. Split read/write goroutines; session goroutine `select`s read half → events, send channel → sink, `ctx.Done()` → clean `Close`. UI: chronological frame log, Text/Binary composer, manual Ping, close-code chip. Custom headers + `Sec-WebSocket-Protocol` + mTLS. **All frames persisted (§6).** |
| **gRPC** | **`google.golang.org/grpc`** + `google.golang.org/protobuf/types/dynamicpb` + `github.com/jhump/protoreflect` (+ `grpcreflect`, `grpcdynamic`) + `github.com/bufbuild/protocompile` | Dynamic (no build-time `.proto`). Schema via **server reflection** (`grpc_reflection_v1` client, wrapped by `jhump/protoreflect/grpcreflect`, handles v1+v1alpha) *or* imported `.proto` compiled at runtime by **`bufbuild/protocompile`** (pure-Go, no `protoc` binary, successor to deprecated `protoparse`). Build/read messages with **`dynamicpb`** (first-party, in the official protobuf runtime), invoke via **`grpcdynamic.Stub`** (`InvokeRpc`/`InvokeRpcServerStream`/…). **This exact stack is what k6 itself uses in production for its gRPC module** — strong validation, and a genuine advantage of the Go move over Rust's `protox`+`prost-reflect` (two separately-maintained crates without an equivalent production consumer at this scale, R16 now moot). Supports proto2, custom options, and **Editions (2023; experimental 2024 via `AllowExperimentalEditions`)**. `DynamicMessage` ⇄ protobuf-JSON; route by descriptor `is_*_streaming`. **Harden against malformed descriptors:** a *generic* tool ingesting arbitrary user `.proto`/reflection payloads *will* hit weird-in-the-wild descriptors — custom-option resolution can **nil-panic on legacy v1-`protoc`-generated extensions** unless `RequireInterpretedOptions` is set appropriately. Set it, and **wrap every descriptor compile in a per-parse `recover()`** so a malformed proto from a user's server degrades to a clear error, not a crashed backend goroutine. (R28) |
| **GraphQL** | `net/http` (reuse) + introspection query executor + **`github.com/hasura/go-graphql-client`** (subscriptions only) | Query = HTTP POST `{query,variables,operationName}` via the same thin client. **No codegen client** (`Khan/genqlient` and typed clients assume a fixed known schema — wrong shape for a generic tool where users paste arbitrary queries). Introspection is just a GraphQL query. Subscriptions over WS (`graphql-transport-ws`) reuse `hasura/go-graphql-client` narrowly, because the subscription handshake/keepalive has real edge cases worth not hand-rolling. |
| **SSE** | **hand-rolled `text/event-stream` parser on `net/http`** (`r3labs/sse/v2` as alternative) | Reuses the HTTP client config (TLS/proxy/auth free). A `bufio.Scanner`-based parser is ~100 lines and lets us write **directly into the persistence sink with natural backpressure** — the `net/http` body read blocks the TCP connection when our consumer is slow, exactly the backpressure-to-persistence propagation the design requires. Spec reconnection with `Last-Event-ID`. **All events persisted (§6).** |

**Single TLS config** (`tls.go`): build one `*tls.Config` (roots via `x509.NewCertPool()`+`AppendCertsFromPEM`; optional client identity via `tls.LoadX509KeyPair` or `GetClientCertificate` for keychain-backed dynamic certs) and inject it into the `http.Transport`, grpc-go (`credentials.NewTLS`), `coder/websocket` (via a custom `http.Client`), and the SSE/GraphQL clients. Users configure certs/mTLS/custom-CA/"disable verification" (a distinct, loud, per-environment `InsecureSkipVerify` toggle, surfaced/logged at the same dispatch chokepoint) once per environment; every protocol honors it. `crypto/tls` covers all of this with no third-party dep — a strength to lean into.

---

## 9. Scripting & chaining

**Two mechanisms on one engine, with a hard security boundary between "pure" and "does I/O."**

### 9.1 Template references — pure and side-effect-free

Single `{{ … }}` grammar in every field: `{{ response('LoginReq').body.token }}`, `.jsonpath('$.x')`, `.header(...)`, `.regex(...)`. Address source requests by **stable ID** so renames don't break chains. **Evaluating a template never does network I/O itself.** A `response()` reference is a **declared dependency edge**; the only thing that performs I/O is explicit prerequisite auto-send, which — like everything else — goes through the dispatch chokepoint (§5.2). Cache keyed by `(requestId, resolved-inputs-hash)` with a refresh policy (always / if-missing / TTL) so a login isn't re-run on every keystroke.

### 9.2 The unified dependency graph (cycles + budget)

Auto-sending missing prerequisites, combined with scripts that call `ctx.sendRequest`, is a **re-entrancy/cycle minefield** (login → refresh → login…). DAG resolution on *declared* `response()` edges is not sufficient.

`chaining.go` maintains **one unified graph** spanning both declared `response()` edges *and* script-initiated request edges, with:
- **Cycle detection across the combined graph** (fail closed with a clear error naming the cycle).
- **An auto-send depth + total-request budget** per top-level run; exceeding it aborts with a diagnostic rather than looping/exploding.
- Every auto-send and every `sendRequest` counts against the same budget and passes the same policy gate.

### 9.3 Scripts — imperative, sandboxed, `goja`/`sobek`

**`github.com/grafana/sobek`** (a goja-API-compatible fork, maintained by the k6/Grafana team) behind a `ScriptEngine` interface. We standardize on Sobek rather than vanilla `dop251/goja` because upstream goja has a single time-constrained maintainer, and Sobek is the actively-developed lineage k6 itself switched to (v0.52.0+) — since we already shell out to k6, aligning on its engine lineage is near-free. Treat "goja" and "sobek" as interchangeable below.

- **Pure Go, zero CGo.** This is the single strongest reason over the alternatives: `go build` cross-compiles to any `GOOS/GOARCH` (Windows-from-macOS CI, universal macOS binaries) with **no C toolchain**. This directly avoids the class of pain that motivated the Rust design's week-1 rquickjs MSVC de-risk spike (`bindgen`/`cc` ABI failures on `x86_64-pc-windows-msvc`) — that entire risk (old R21/M0 spike) evaporates. `v8go` would *reintroduce* it (CGo, prebuilt static V8 per platform, no first-party Windows), so it is rejected; `otto` is ES5-only and rejected.
- **Sandboxing is native by construction.** A fresh `sobek.Runtime` has no `require`, no `fs`, no `net`, no `process` — nothing to escape from (there was never a vm2-style ambient-`require` attack surface). Everything the script can touch is explicitly bound via `runtime.Set`. **We bind no raw `net/http` to scripts** — only a thin `sendRequest` that routes through `Engine.dispatch` (§9 rule below). Pass narrow DTO/interface types (not rich domain objects) so `reflect`-exposed surface is exactly what we intend.
- **Interrupts/timeouts:** `Runtime.Interrupt(reason)` sets a flag polled between VM instructions, so `for(;;){}` is interruptible sub-ms (§6). Design contracts: a single **watchdog goroutine per run** (`time.AfterFunc` on the run's context deadline) calls `Interrupt`; **`ClearInterrupt()` on runtime checkout** if pooled (otherwise the next script dies instantly); **`SetMaxCallStackSize(n)`** caps recursion (throws catchable `*StackOverflowError`, doesn't crash the process). Stress-test concurrent interrupts in CI (goja historically had an `Interrupt` deadlock, #97) rather than trusting docs.
- **Runtime lifecycle:** **fresh `sobek.Runtime` per top-level script invocation** (pre-script, post-script) for correctness/isolation — cheap at API-client scale (dozens–hundreds of concurrent requests, not k6's tens-of-thousands VUs), and it sidesteps prototype-pollution leakage between runs and gives naturally-bounded memory (worst case = timeout × alloc rate, since each short-lived runtime is discarded and GC'd). `Compile` once per script text (cache by content hash), `RunProgram` per invocation. Reserve pooling for a future high-throughput in-process path only if profiling demands it — and even then, delegate bulk iteration to the k6 sidecar, not in-process JS.
- **Memory limit — honest gap:** goja/sobek has **no built-in per-runtime memory cap** (confirmed upstream, no `SetMemoryLimit`). Mitigations, layered and documented as a known limitation (not oversold): `SetMaxCallStackSize` for stack blowup; the wall-clock timeout catches most runaway-allocation loops as a side effect; **`GOMEMLIMIT`/`debug.SetMemoryLimit` as a process-wide backstop**; and for a future hard per-script ceiling (e.g. protecting the MCP server from a pathological Claude-generated script), run *that* execution in a subprocess we can rlimit — a v2 hardening item, not a v1 blocker.

**API surface:** one injected `ctx`/`pm`/`bru` global, Postman/Bruno-shaped — but **explicitly a documented subset, with a published compatibility matrix**, not implied full parity:
```js
pm.response.json(); pm.response.jsonpath('$.token');
pm.variables.set(k,v);                   // runtime, in-memory
pm.environment.set(k,v,{persist});       // {persist} writes to LOCAL vault; secrets -> keychain, never git
pm.sendRequest('GetUser').then(res => {  // routes through dispatch chokepoint -> SAME Go client + policy
  pm.variables.set('userId', res.json().id);
});                                       // .then() form — async/await is NOT supported (see below)
pm.test('ok', () => pm.expect(...).to.be.ok);
```
The shim is JS-source polyfills (a ported minimal Chai-style assertion lib; `crypto-js`/`atob`/`btoa` are pure JS loaded once at runtime init) + narrow Go host bindings. **`pm.sendRequest` is not a raw host binding** — it marshals into the same internal `RequestSpec` and calls the same `Engine` entrypoint, so TLS/proxy/cookies/timing/**policy** are one code path. **Node built-ins (`fs`, `child_process`, `require`) are deliberately NOT replicated** — that's the vm2/NodeVM footgun Bruno had to walk back; a script that needs filesystem/process access gets a policy-gated, audited capability, never one inherited "for free."

**`async`/`await` and generators are a hard NON-GOAL — this is a real limitation, not an oversight.** goja/sobek is fundamentally ES5.1 with partial ES6+ backfill; **native `async`/`await` and generators are not implemented** (blocked upstream on goja's inability to save/restore execution context — [goja #460](https://github.com/dop251/goja/issues/460)). goja/sobek *does* support `Promise` (`rt.NewPromise()`), so **`.then()` chains work**; a top-level `await` or an `async function` in a user script will **throw a SyntaxError**, not run. Because real-world Postman scripts increasingly use `async/await`, this is the single most likely "why does my script fail to parse?" support ticket. Two honest options, decided now: (a) **reject `async` scripts with a clear, specific error** ("async/await is not supported by this script engine; use `.then()`"), which is the v1 choice — we have no in-Go JS transpiler (no Babel) to rewrite them; or (b) ship a transpile step later. The published compat matrix (below) carries an explicit **NOT SUPPORTED** row for `async`/`await`/generators, and every `pm.*` example in the docs is `.then()`-based, so the docs and the engine never contradict.

**Performance is a rule, not a footnote: in-process scripts are for orchestration and assertions only.** Sobek is an interpreter roughly **an order of magnitude slower than V8** on heavy compute. That is *irrelevant* for per-request pre/post scripts (a script that blocks on one HTTP call is expected) but disqualifying for data-transform-heavy or large-iteration work. **Rule:** never run bulk loops or heavy compute in in-process JS — that belongs in the k6 sidecar (its own VUs) or a future rlimited subprocess, never on the engine's script path.

### 9.4 Sandbox security

Fresh context per execution, **zero ambient authority**, interrupt-based hard deadline, `GOMEMLIMIT` backstop, secret redaction from the per-run log. **Because policy lives at dispatch (not the tool boundary), a script-initiated mutating/prod request is gated identically to a user-clicked one.** MCP-triggered runs additionally receive a **tighter policy object** (deny `persist`, deny global writes, cap total requests) — expressed as the explicit policy parameter of principle 7, not as forked execution. Defense-in-depth later: move script execution to a subprocess (also the only in-process way to get a hard memory ceiling) — not needed day one.

### 9.5 Flow / Runner

Explicit, git-stored, ordered sequences (`chains/*.yaml`): steps (request | script | group), per-step capture rules, `runIf`/retries/delay/continue-on-error, **data-driven iteration** (bind CSV/JSON, run per row — bridges to load testing and `iterationData`). This is the artifact the MCP server and perf runner point at.

---

## 10. MCP server

**Embedded HTTP as primary, thin stdio bridge for zero-config. Built on the official `github.com/modelcontextprotocol/go-sdk` (v1.x) and MCP spec 2025-11-25 semantics.**

### 10.1 SDK choice: official go-sdk, not mark3labs/mcp-go

We use **`github.com/modelcontextprotocol/go-sdk`** (module `.../mcp`), pinned to the latest stable v1.x (v1.6.x line at writing; re-evaluate the jump to v1.7 once the 2026-07-28 spec RC finalizes, since it reshapes Tasks and deprecates elicitation/sampling on a 12-month window). It is the official SDK (maintained in collaboration with Google), reached v1.0 stability, implements Streamable HTTP, elicitation, and bearer auth, and — the decisive signal — **GitHub's own MCP server migrated *from* mark3labs/mcp-go *to* this SDK** specifically to track the spec. mark3labs/mcp-go is fine for a quick internal tool but is the trailing implementation for spec-current features (no Tasks; issue #656 is low-priority "help wanted"), which is the wrong bet for a differentiator feature meant to track the spec durably.

### 10.2 Why embedded + Streamable HTTP (a goroutine, not a stdio sidecar)

The Wails app is already the long-running process holding the core engine, the keychain-backed secret store, and the SQLite cache. Claude should talk to the **actual running window** — not spawn a headless copy, and not a second process that has to re-establish access to all of that (which would duplicate/undermine the single policy chokepoint). So the MCP server runs as a **goroutine in the Wails Go process**, launched from `OnStartup`: `go mcp.Serve(ctx, engine, token, addr)`, mounting `NewStreamableHTTPHandler` on `127.0.0.1:<port>`. Tool handlers call **directly into the same `*core.Engine`** (and same dispatch chokepoint) the GUI uses. Claude Code supports remote/HTTP MCP via `claude mcp add --transport http` with an `Authorization` header, so stdio is not required.

### 10.3 Async Tasks + elicitation (best-effort), progress as the primary mechanism

**MCP 2025-11-25 standardized the async Tasks primitive** (`working`/`input_required`/`completed`/`failed`/`cancelled` durable state machine) and **elicitation**. We map onto these where the SDK's ergonomics are stable — but the 2026-07-28 RC reclassifies Tasks as an *optional extension* and deprecates elicitation, so we **do not hard-couple to Tasks as load-bearing**. Primary mechanism for long-running operations (`run_perf_test`, `run_collection`) is **synchronous streaming calls with progress notifications** (`ServerSession.NotifyProgress`), with Tasks as an additive, best-effort upgrade once the SDK's Tasks support and the extension's final shape settle. This sidesteps the RC's deprecation churn while staying spec-aligned.

- **`run_perf_test` and `run_collection`** stream progress notifications now; adopt Tasks (Claude gets a Task id, polls/streams, fetches on `completed`) as a best-effort layer.
- **Approval** surfaces primarily as a **native prompt in the Wails window** — because desktop user-presence approval is "ask the human at the GUI," not "ask the calling model," and you should not rely on the agent to faithfully relay a security prompt. When the dispatch chokepoint returns `NeedsApproval`, the GUI adapter renders a modal; elicitation is reserved for cases where the MCP client itself must gather structured input (e.g. "which environment?" when ambiguous).

### 10.4 The stdio bridge — no auto-launch

A tiny `apiclient-mcp-bridge` sidecar speaks stdio to Claude and forwards to the app's local HTTP endpoint — solely to satisfy `command`-style `.mcp.json` entries or CI harnesses that only speak stdio. **It does not auto-launch the app** (that would reintroduce the headless-second-copy failure and let any process driving the bridge spawn the GUI with full workspace access, no user present). If the app is down, the bridge returns a structured `isError` ("app not running — open it"). Same tool code, different transport — one policy engine, no forked execution.

### 10.5 Tool surface (annotated)

- **Read (`readOnlyHint`):** `list_workspaces`, `list_requests`, `get_request`, `list_environments`/`resolve_env` (secret-redacted — returns references/placeholders, never raw secret values), `get_last_response`, `get_collection_run`/`get_perf_run`.
- **Execute:** `run_request` (`openWorldHint`, not read-only even for GET), `run_collection` (progress/Task), `run_perf_test` (progress/Task; spawns k6 **sidecar**, never in-process), `open_websocket`/`send_websocket_message`, `call_grpc`, `cancel_run` (`idempotentHint`).
- **Mutate:** `create_request`/`update_request`, `set_env_var`, `delete_request` (`destructiveHint`).
- **Resources:** each request/response as a `resource_link` (`apiclient://workspace/{id}/request/{id}`).

### 10.6 Auth & permission model (defense in depth — token is necessary, not sufficient)

**Localhost + bearer token is not a real trust boundary on a shared/multi-user Mac.** Any local process/user can reach `127.0.0.1:<port>`; the token is handed to any MCP client the user configures. So the real gate is **per-request user presence for anything mutating or prod-targeted**, regardless of grant:

1. **Transport:** bind `127.0.0.1` **only** (never `0.0.0.0`), random high port (fixed default e.g. `47932` with ephemeral fallback on conflict), **bearer pairing token** generated per launch and stored in the OS keychain, verified via the SDK's `auth.RequireBearerToken` middleware (constant-time compare against the keychain token — no OAuth flow needed for the loopback single-user case). **One-click "Copy Claude Code config"** button writes the `claude mcp add … --header "Authorization: Bearer <token>"` line. **One-click token rotation** invalidates outstanding grants.
2. **Capability scoping:** each connection is a **grant** — Read-only (default; execution tools not advertised in `tools/list`), Run-ask-each, or Full-auto; narrowable to specific workspaces/envs.
3. **Per-action policy (the dispatch chokepoint, §5.2):** any request resolving to POST/PUT/PATCH/DELETE, or targeting a `production`-flagged env, **requires live user presence regardless of grant** — a Full-auto grant does *not* silently authorize `DELETE /users/42` against Prod. Auto-allow lists are limited to safe cases (GET on `env=local`); hard-deny lists (`env=production`) cannot be overridden by a grant. Secrets redacted by default with a separate approval-gated unmask. Full **audit log**.
4. **Collection/session-scoped grants** to avoid deadlock under automation: "Allow all GET on env=local for this run," "Allow this collection once." Approved once at the start of a run, applied for its duration, held in memory or the disposable SQLite cache — never persisted as implicit trust across restarts.

Annotations are **untrusted hints** (they drive Claude's own confirmation UI); the server-side dispatch policy is the enforcement of record.

### 10.7 Structured results

Execution tools **always** return `structuredContent` (conforming to a declared `outputSchema`) plus a compact `text` summary. `run_request` returns `{status, ok, duration_ms, resolved_url, method, response_headers, body{truncated, full_body_uri}, extracted{...}, approval}` — the `extracted` map is what makes **agentic request chaining** work (Claude reads `extracted.token`, pipes into the next call). Large bodies truncated with a `resource_link` (or a `body_path` to the persisted body) to protect the context window. `isError:true` for tool-execution failures (target 500, threshold breach, approval denied) so Claude self-corrects.

```go
// go-sdk v1.x surface — verify field/method names against pkg.go.dev at impl time.
func NewMCPServer(engine *core.Engine) *mcp.Server {
    s := mcp.NewServer(&mcp.Implementation{Name: "api-tool", Version: "0.1.0"}, nil)

    mcp.AddTool(s, &mcp.Tool{
        Name:        "run_request",
        Description: "Execute a saved request (HTTP/GraphQL/gRPC/WebSocket/SSE) through the shared engine, subject to policy approval.",
    }, func(ctx context.Context, req *mcp.CallToolRequest, args RunRequestArgs) (*mcp.CallToolResult, RunRequestResult, error) {
        // Origin tag lets the engine's single dispatch chokepoint apply MCP-specific
        // policy (user-presence prompt / session-grant lookup) identically to how it
        // treats chained-request or CLI-initiated calls. NO policy check here — the
        // gate lives in engine.dispatch(), so script-initiated sends are covered too.
        dctx := core.WithOrigin(ctx, core.OriginMCP)
        res, err := engine.RunRequest(dctx, args.toRef(), mcpRunCtx(&grant), bufferSink())
        if err != nil {
            return &mcp.CallToolResult{
                IsError: true,
                Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
            }, RunRequestResult{}, nil // policy-denied & transport errors both surface as tool errors
        }
        return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Summary()}}},
            res.ToStruct(), nil
    })
    // ... list_workspaces, run_collection, run_perf_test, get_last_response, resolve_env, create_request
    return s
}
```
The stdio bridge serves the same server over stdio.

---

## 11. k6 perf-test subsystem

### 11.1 The hard AGPL boundary — SHARPER on Wails than on Tauri

**k6 is AGPL-3.0** (relicensed from Apache-2.0 in April 2021; still AGPLv3 as of k6 2.0, May 2026). On Tauri (Rust host, Go-only k6) a language barrier made in-process embedding awkward enough that nobody reached for it by accident. **On Wails, `go get go.k6.io/k6` "just works" — it compiles, autocompletes, and looks like an ordinary dependency add.** k6's own `xk6` extension mechanism (`go.k6.io/k6/js/modules`) is *designed* to compile into a single Go binary with your code. That ease-of-reach is the sharper trap, and it is the single most important licensing constraint of the port.

**Why in-process import taints the whole app:** importing an AGPL Go package statically links it into one combined executable; under AGPL §5/§13 that executable is a single "work based on the Program," so copyleft extends to the **entire app** (engine, MCP server, GUI glue — everything linked in), obligating the whole codebase to AGPLv3 with Corresponding Source for every user. The §13 network clause is a *red herring* here — shipping a compiled desktop binary that statically links AGPL code **is distribution under §5**, so "it's just a desktop app, not a server" does not exempt it.

**Mandatory decision: k6 stays an arm's-length CLI sidecar, always.**
- `internal/core` and every engine-tree package **never** import `go.k6.io/k6/...`. The CI deny-list bans the **entire `go.k6.io/k6` module prefix** *and* **`go.k6.io/xk6`** *and* any **`github.com/grafana/xk6-*`** extension module — because the sharper accidental import is not the k6 runtime itself but a *convenience* transitive pull (e.g. a "k6 script validator" that imports `go.k6.io/k6/js/modules` to typecheck generated scripts, or a k6 output/extension helper). Deny the whole family explicitly, not just `go.k6.io/k6`.
- **Sobek is NOT AGPL — pre-empt the audit question.** We import `github.com/grafana/sobek` in-process (§9.3) *and* shell out to the AGPL k6 binary, both Grafana projects. A reviewer will flag "you use a Grafana JS engine and a Grafana AGPL tool." The license split is clean: **sobek is Apache-2.0/MIT (goja lineage), not AGPL**; only the **k6 binary** is AGPL, and it is **never linked** — invoked purely via `os/exec`. State this in the attribution/AGPL screen so the split is documented before anyone asks.
- **Do NOT `go:embed` the k6 binary bytes into the Go executable** — Wails' *own* maintainer-recommended pattern for bundling external tools (Discussion #3021) is `go:embed`, and that is precisely the **wrong** answer for k6, because embedding the bytes into your single compiled executable plausibly reads as distributing one combined work. Wails has **no first-class sidecar feature** (unlike Tauri's `externalBin`), so we hand-roll staging in the build pipeline (§11.6).
- k6 is invoked via **`os/exec`** as a genuinely separate OS process communicating over stdout/stdin/files — "mere aggregation," same trust boundary as calling `curl`. Ship it as a **separate, unmodified binary in the `.app` bundle's `Contents/Resources/`** (staged by the packaging step, not the Go compiler) that a user could delete/replace independently, **or** first-run download (§11.6). Never vendor `go.k6.io/k6`, never build a custom `xk6`.

**AGPL conveyance obligation** (separate from linking, still owed): distributing the k6 binary means making its **Corresponding Source** available per §6 — "a pointer to grafana/k6" is legally weak. Build k6 from source in CI, publish the exact source tarball/tag/commit next to each app release, record version+build flags in the release manifest. Legal caveat: whether subprocess invocation truly avoids copyleft is **legally unsettled even for k6's own maintainers** — the source-offer is belt-and-suspenders regardless. **Counsel sign-off before the first public build** is a release blocker.

### 11.2 Two data channels from one run

```go
cmd := exec.CommandContext(ctx, k6Path, "run",
    "--out", "json=-",             // NDJSON metric stream to stdout
    "--summary-export", summaryPath,
    "--no-color", "--quiet",
    scriptPath)
cmd.Env = filteredEnv               // do NOT inherit os.Environ() blindly — strip secrets k6 doesn't need
stdout, _ := cmd.StdoutPipe()
cmd.Stderr = &logBuf                // human-readable progress/errors kept separate from the metric stream
```

- **Live:** `--out json=-` streams **NDJSON to stdout** — each line a `Metric` (metadata) or a `Point` (timestamped sample). Human summary goes to stderr (clean pipe). Parse line-by-line with `bufio.Scanner` (raise its buffer past the 64KB default — dense lines truncate otherwise) + `encoding/json`; on a bad line, log and continue, don't abort the test.
- **End-of-test:** the generated script's `handleSummary(data)` writes rich aggregated `summary.json` (real percentiles + per-threshold pass/fail) to disk; `--summary-export` is a belt-and-suspenders second path so a `handleSummary` template bug doesn't silently lose the run. **Don't compute p95 from the NDJSON stream** — let k6 do it in `handleSummary`.

### 11.3 Script generation

From the *same validated request/flow* the GUI runs (`internal/perf/scriptgen.go`), owned by the core engine so GUI/CLI/MCP share it: methods → `http.<method>`, collections → `group()` + chained captured vars, auth → header injection/login pre-step, per-request `tags:{name}` + custom `Trend` so charts split by request, `discardResponseBodies:true` (override where a check needs the body). Templated via `text/template` with **request URL/headers/body/vars injected as JSON-encoded JS literals** (not string concatenation) to prevent script injection into the generated test. **Secrets pushed via repeated `-e`/`__ENV`** so the committable `script.js` never contains them. Pre/post-request chaining scripts need a **translation layer** (not literal reuse) because k6 runs them in its own sobek VM with k6-specific globals (`http`, `check`, `__VU`, `__ITER`) — treat this translator as a first-class, tested component so the load test doesn't silently drift from what "run request" actually does. Serialize the generated `.js` to the disposable cache dir keyed by request+options hash so users can inspect/export the exact script. UI-driven **thresholds** map cleanly: `http_req_duration: ['p(95)<800']`, `http_req_failed: ['rate<0.01']`, `abortOnFail` for hard SLAs.

### 11.4 Live metrics pipeline

At 50–500 VUs k6 emits thousands of Points/sec — **batching is mandatory**, and *more* so given Wails' backpressure-free `EventsEmit` (§6). Aggregate in Go into **1-second buckets** (t-digest for approximate live percentiles) and emit **one batched event per bucket** at a capped rate; reserve exact percentiles for `handleSummary`. Persist the raw NDJSON (or aggregated buckets, or both) to the system-of-record store as it arrives — "no lossy ring buffer" — so a user can reopen a finished/crashed run or export raw data later. **Live percentiles are labeled "approx" in the UI**; the authoritative final numbers come from `handleSummary` with a visible reconciliation, so users don't file the live/final p95 difference as a bug. Charts render with **uPlot** (already in the frontend stack).

### 11.5 Verdict & regression

k6 exits **99** if any threshold failed (a stable, documented exit-code contract — a semantic we read via `cmd.ProcessState.ExitCode()`, not a package we import), **0** if all passed → the Go runner reads the child exit code for the deterministic PASS/FAIL badge, distinguishing 99 from "k6 crashed" (other nonzero) and success (0); per-threshold detail from `handleSummary`. This maps into the same pass/fail signal used everywhere: MCP `run_perf_test` returns `{passed, failedThresholds:[...]}`, not an opaque code. Cancellation = `os.Interrupt`/SIGINT (graceful; teardown + `handleSummary` still run; UI shows "stopping test…"), `Kill()` fallback after grace. History: per-run `perf/<collection>/runs/<ts-uuid>/{script.js, summary.json, meta.json}` (git-friendly) + SQLite index for trend sparklines; baseline run + drift flags (Δp95 > tolerance).

### 11.6 Scope, sidecar staging & licensing

**v1 = single-process, multi-scenario** (concurrent named scenarios in one process); distributed (k6-operator/Kubernetes) is a later "export manifest" feature, **not in-app orchestration** (cut from v1).

Sidecar staging (the Wails build-tooling gap): Wails has no `externalBin` equivalent, so a **Taskfile/Makefile step copies the right per-arch k6 binary into the `.app` bundle after `wails build`** (universal/lipo'd or per-arch, matching the app's own arch treatment), plus equivalent Windows/Linux packaging steps. **Preferred alternative: first-run checksum-verified download** into the app-support dir, which sidesteps having to notarize someone else's AGPL binary. If bundling instead, budget for it (§B, code-signing): every nested Mach-O must be **individually signed** (don't rely on `--deep`), k6 needs **hardened runtime + `com.apple.security.cs.allow-jit`** for its sobek JS VM (verify empirically), entitlements are **not inherited** by a spawned subprocess so k6 signs with its own, and `get-task-allow` must be absent in release builds. Notarize the whole bundle after all nested binaries are signed. Either way: pinned version, checksum-verified, corresponding-source shipped, **counsel sign-off before first public build** (release blocker, not a footnote).

---

## 12. Frontend architecture

Unchanged by the stack switch — Wails is webview-based, so the SolidJS/TS/Vite/CodeMirror 6/uPlot/Tailwind frontend ports verbatim; only the backend transport (Wails bindings + `EventsEmit` instead of Tauri `invoke`/`Channel`) differs.

### Framework: **SolidJS** (+ TypeScript + Vite)

For a fast, keyboard-driven, streaming-heavy dev tool the decisive properties are **fine-grained reactivity with no virtual DOM** and a small runtime:
- **Solid updates only the exact DOM nodes that changed** — no VDOM diffing. For high-frequency streams (WS frames, gRPC messages, k6 buckets), React would reconcile subtrees per batch; Solid mutates one text node. Serves "lightweight + fast GUI" and keeps the UI thread free for the coalesced streams from §6.
- Tiny bundle → fast cold start in WKWebView (bundle size, not the Wails shell, dominates perceived startup).
- vs **React**: bigger ecosystem, but reconciliation cost is exactly wrong for a streaming tool. vs **Svelte**: also compiled/fast, but Solid's signal model maps more cleanly onto "subscribe a DOM node to a stream."

### State management
- **Solid stores + signals** for local/UI state (open tabs, pane sizes, editor buffers). Note WKWebView `localStorage` isolation/clearing quirks — keep only disposable UI prefs there, never source-of-truth (which lives in Go).
- **TanStack Query (Solid adapter)** over the Wails-binding boundary for server-state (workspace tree, request definitions, history) — caching, invalidation, background refetch, so the webview never becomes a second source of truth (files/DB in Go are truth). This layer is *also* where the frontend PULLS stream history via a bound `GetStream(sessionID, fromSeq)` after an `EventsOn` wake-up (§6).
- **Streaming state** (frame logs, metric series) is a **virtualized window over the Go-side append-only log** (§6), *not* a lossy in-JS ring buffer that owns the data. The UI caps rendered nodes; the full stream is always recoverable from Go.

### Code editor: **CodeMirror 6** (not Monaco)
- **Dramatically lighter** than Monaco — matters for "lightweight/fast" and for having *many* small editors (URL, headers, JSON/GraphQL/protobuf body, response viewer, script tabs).
- First-class **autocomplete/linting** per field (template `{{ }}` completions, GraphQL schema-aware completion from introspection, JSON schema hints).

### Virtualized lists
Everything unbounded is virtualized (**TanStack Virtual**): sidebar request tree, request/response **history**, WS/SSE frame logs, k6 sample streams, large JSON response trees. Combined with Go-side coalescing + persistence, render cost is bounded by viewport, not data volume.

### Event handling & keyboard-first UX
- **Command palette** (fuzzy, every action reachable) + fully customizable hotkeys.
- The webview subscribes via `EventsOn(name, cb)` per session; a small dispatcher routes wake-ups to the right store, which then pulls new data via a binding call.
- Approval prompts from the policy engine arrive as Wails events → modal ("Allow once / this run / session / Deny"), mirroring the MCP prompts so both surfaces present the same decision.

---

## 13. Security, secrets, error handling

- **Sandboxing:** untrusted git-shared scripts run in goja/sobek with zero ambient authority, interrupt-based hard deadline, and `GOMEMLIMIT` backstop (no per-runtime cap — documented limitation, §9.3); **all script-initiated network egress passes the dispatch policy chokepoint** (§5.2), so MCP's tighter policy and prod/mutation gates apply *inside* scripts, not just at tool boundaries. Optional future defense-in-depth: script execution in a subprocess (also the only in-process route to a hard memory ceiling).
- **Secrets:** keychain-rooted (`zalando/go-keyring`) two-tier XChaCha20-Poly1305 (`x/crypto/chacha20poly1305`); **never in git** (§7.4); encrypted export/import for sharing; MCP output redacts secrets by default with approval-gated unmask.
- **MCP boundary:** `127.0.0.1`-only bind + bearer (`auth.RequireBearerToken`) treated as necessary-not-sufficient; **per-request user presence for any mutating/prod action regardless of grant**; one-click token rotation; capability grants + collection-scoped grants; no bridge auto-launch; audit log (§10.6).
- **Error strategy:** typed errors inside `core` (a `EngineError` family / sentinel errors + `errors.As`: protocol/script/storage/**policy**/lag variants) wrapped with `%w`; plain error strings only at binary edges (`cmd/cli`, bound methods). Errors become **structured `ExecutionEvent` (Error kind)** on the event bus, so GUI, CLI reporter, and MCP `isError` render the same failure. Protocol-level failures (500, TLS error, threshold breach, approval denied) are *tool-execution* errors (recoverable, self-correctable), distinct from JSON-RPC/malformed-call protocol errors. DB migration failure → rebuild-from-files, never data loss. Backpressure lag is a first-class, observable event (`ErrSinkLagged`), never a silent drop.

---

## 14. Cross-platform (Windows/Linux later)

The stack was chosen so cross-platform is a build-config problem, not a rewrite — and the Go move makes most of it *more* automatic than Rust, because the dominant cross-compile risks were CGo/native-toolchain ones that pure-Go modules sidestep:

- **`crypto/tls` everywhere** → identical TLS behavior on all three OSes, no OpenSSL linking.
- **Pure-Go dynamic gRPC** (grpc-go + protocompile + dynamicpb) → **no native `protoc` binary in any path** (a strict improvement over both Yaak's vendored-protoc and the Rust design's feature-gated `protoc` fallback — R16 is now moot).
- **Pure-Go SQLite** (`modernc.org/sqlite`) and **pure-Go JS** (`grafana/sobek`) → **zero CGo for the DB and script engines**, so `GOOS/GOARCH` cross-compilation "just works," including Windows-from-macOS CI and universal macOS binaries. The Rust design's biggest week-1 de-risk (rquickjs MSVC) is gone.
- **Sidecars:** k6 (first-run download or per-arch bundle, staged by build tooling — Wails has no `externalBin`) and the MCP bridge are the only native externals; the bridge never auto-launches. Flatpak (Linux) may sandbox `os/exec` of a bundled sidecar unless declared in the manifest permissions.
- **Secrets:** `zalando/go-keyring` per OS (macOS Keychain / Windows Credential Manager / Linux Secret Service; graceful encrypted-file fallback + argon2 passphrase for headless/CI Linux with no keyring daemon).
- **Files:** `.gitattributes eol=lf` pins newlines before Windows lands; ULID-by-reference + fractional order keys avoid path-rename and insert-collision churn.
- **Wails/WKWebView → WebView2 (Windows)/WebKitGTK (Linux):** the macOS leg requires **actual macOS CI runners** (WKWebView needs system WebKit via Cgo bindings in the Wails shell — you cannot cross-compile *to* macOS the way pure-Go allows). Validate the loopback-HTTP binary-body path and `EventsEmit` throughput on each webview; Solid + CodeMirror + uPlot + TanStack Virtual are webview-agnostic. **Early macOS spike:** there are open, unresolved WKWebView memory-leak reports specific to macOS (Wails #2772/#2777) — smoke-test idle memory with the real frontend before committing, since macOS is the primary target.

---

## 15. The Windows IPC latency gap (first-class design constraint)

Promoted from a checkbox to a design constraint because it changes the streaming UX on the stated future target. The ~200ms large-payload IPC on Windows is a **WebView2 platform limitation** (`chrome.webview.postMessage` shared memory is "weirdly slow," per WebView2 engineers; ~5ms macOS vs ~200ms Windows for the same ~10MB transfer). **Wails rides the same WebView2 control and additionally base64+JSON-encodes binary payloads with no zero-copy path and no "Raw Requests"-style mitigation** — so it is likely worse-or-equal to Tauri here, not better. The whole "coalesce on Go, emit batched events" plan is validated macOS-first; high-frequency perf/WS/gRPC paths will feel qualitatively different on Windows.

Consequences baked into the design now:
- **Route large/binary bodies around the IPC bridge via loopback HTTP** (§6) as the default on *both* platforms — response viewer for big payloads, binary downloads, k6 report artifacts. This is the primary Windows mitigation and it's already how macOS avoids the base64 tax.
- **Coalescing windows are tunable per platform** (bigger batches / lower frequency on Windows).
- A **WebView2 throughput regression test** gates any change to the streaming UX (added to CI when Windows work begins).

---

## Known risks & mitigations

| # | Risk | Likelihood / Impact | Mitigation (where in doc) |
|---|---|---|---|
| R1 | **~~Rust YAML ecosystem compromised~~ — RESOLVED by Go.** The Rust crisis (`serde_yaml` deprecated; `serde_yml` RUSTSEC-2025-0068 unsound; `serde_norway` disclaims maintenance) does not recur. Residual trap: the muscle-memory `gopkg.in/yaml.v3` is itself archived. | Low / Medium | Import **`go.yaml.in/yaml/v3`** (spec-org-maintained successor, drop-in); keep **golden round-trip corpus in CI**; ordered lists via explicit `orderKey`, not map/YAML-key order. (§7.1) |
| R2 | **Script-initiated `pm.sendRequest` bypasses approval** if policy sits at the MCP tool boundary. | Was certain / Critical | **Policy at `Engine.dispatch`**; every egress (GUI/CLI/MCP/script/auto-send) gated uniformly; scripts have no raw `net/http` bound, only a `sendRequest` that routes through dispatch. (§5.2, §9.3) |
| R3 | **Event *delivery correctness*, not just backpressure.** `runtime.EventsEmit` has NO backpressure/queueing/coalescing **plus** a live emit-path data race ([#2448](https://github.com/wailsapp/wails/issues/2448), concurrent emit vs. `EventsOn`) and inconsistent delivery under rapid emit ([#2759](https://github.com/wailsapp/wails/issues/2759)) — both unfixable upstream by us. | High / High | Async fallible `EventSink` + per-consumer `ShedPolicy`; bounded per-session channels **fanning into ONE dedicated emitter goroutine** (single emit call site dodges the #2448 race); coalesce (not fire-per-frame, per #2759 — mandatory, not tuning); cap emit 10–30Hz; **frontend PULLS history via a binding**; large bodies over loopback HTTP. (§5.1, §6) |
| R4 | **"Cancel" is non-uniform** (blocking DB write uncancellable; goja `while(true)` ignores context cancel; k6 SIGINT lags). | High / Medium | Documented per-kind semantics; **goja `Interrupt()` watchdog + `ClearInterrupt` on reuse**; capped/timeout-bounded DB ops; UI shows "closing…/stopping…". (§6) |
| R5 | **Auto-send + scripts create request cycles / exponential blowup.** | Medium / High | **Unified declared+script dependency graph** with cycle detection + depth/count budget, fail-closed. (§9.2) |
| R6 | **MCP long-ops modeling churns** with the evolving spec (2025-11-25 Tasks experimental; 2026-07-28 RC reclassifies Tasks as optional + deprecates elicitation). | Medium / Medium | **Official go-sdk v1.x**; primary mechanism = **progress notifications**, Tasks/elicitation best-effort not load-bearing; approval native-in-GUI. (§10.1, §10.3) |
| R7 | **Localhost+token is not a boundary** against a native local attacker. | Medium / High | Token necessary-not-sufficient; **per-request user presence for mutating/prod regardless of grant**; `127.0.0.1`-only bind; one-click rotation; hard-deny lists ungrantable. (§10.6) |
| R8 | **Bridge auto-launch** spawns the GUI headlessly with full workspace access. | Medium / High | **Bridge never auto-launches**; returns structured `isError` if app down. (§10.4) |
| R9 | **AGPL taint from k6 — SHARPER on Wails.** `go get go.k6.io/k6` compiles cleanly and would make the ENTIRE app AGPLv3; Wails' own maintainer-recommended `go:embed` bundling pattern is *also* wrong for k6. | High / Critical (existential) | **Engine never imports k6; never `go:embed` its bytes; never build `xk6`.** `os/exec` sidecar only, unmodified binary in `.app` Resources or first-run download; ship corresponding source per §6; **counsel sign-off before first public build**. (§11.1, §11.6) |
| R10 | **Signing/notarizing the bundled k6 sidecar** (nested-binary signing, JIT entitlement, non-inherited entitlements). | Medium / Medium | Prefer first-run download; if bundling, sign every nested Mach-O individually + hardened runtime + `allow-jit`, verify empirically, notarize whole bundle last. (§11.6) |
| R11 | **Live vs final p95 disagree** → filed as bugs. | High / Low | Label live t-digest percentiles **"approx"**; authoritative from `handleSummary`, reconciled visibly. (§11.4) |
| R12 | **Integer `sortPriority` collides** on concurrent inserts. | High / Medium | **Fractional/lexicographic order keys** (vendored fracdex-style). (§7.1) |
| R13 | **Committed encrypted secrets** are an unresolvable diff/merge mess. | Was certain / Medium | **Secrets never in git**; pre-serialization guard; encrypted export/import. (§7.4) |
| R14 | **Dangling ULID references** after auto-merge → silent logical corruption. | Medium / High | **Post-merge integrity + repair pass** on load; "Workspace health" panel. (§7.2) |
| R15 | **Windows IPC latency** — WebView2 ~40× slower + Wails base64+JSON binary path with no Raw-Requests equivalent → likely worse-or-equal to Tauri. | High (on Windows) / Medium | **Loopback HTTP for large bodies (both platforms)**; per-platform coalescing; WebView2 throughput regression gate. (§6, §15) |
| R16 | **~~`protox` fails on proto2/edition-2023/custom options/`Any`~~ — RESOLVED by Go.** | Low / Low | Go's `dynamicpb` (official runtime) + `protocompile` + `jhump/protoreflect` is the *same stack k6 uses in production* — more cohesive/proven than Rust's `protox`+`prost-reflect`; no `protoc` binary needed. (§8) |
| R17 | **~~HTTP/3 `--cfg reqwest_unstable` is workspace-global~~ — softened by Go.** | Low / Low | **HTTP/3 out of v1**; if ever, `quic-go/http3.Transport` is a per-request `RoundTripper` swap, not a global build flag. (§8) |
| R18 | **Stale/wrong version pins** (coder/websocket vs archived gorilla; go-sdk v1.x; go-git v5 vs alpha v6). | Was certain / Low | Pin verified-current (2026-07): `coder/websocket`, `grpc`+`protoreflect`+`protocompile`, `go-sdk v1.6.x`, `go-git/v5`, `go.yaml.in/yaml/v3`, `modernc.org/sqlite`; `govulncheck` + Dependabot in CI. (§8, §10, §16) |
| R19 | **Engine couples to Wails** → CLI won't link / silently no-ops, "five consumers" premise rots. The subtle path is **ctx-value smuggling** (passing the Wails-flavored ctx into the engine to emit directly) — an `import`-free coupling `go list -deps` can't catch. | Medium / High | **CI asserts no `wailsapp/wails` in `go list -deps ./cmd/cli`** + `golangci-lint depguard` on `internal/core...`; engine emits **only via `EventSink`, never via `ctx`**; **test runs the engine under `context.Background()`** to prove no Wails-ctx dependency leaked. (§3, §6, §16) |
| R20 | **Approval-gate deadlock** under multi-step agent runs. | Medium / Medium | **Collection/session-scoped grants** approved once per run. (§10.6) |
| R21 | **Scope explosion for a solo dev** — 6–8 subsystems, each a product. | High / High | **Cut for v1**: in-app git UI (read-only status only), HTTP/3, distributed k6, full Postman-shim parity (documented subset + compat matrix). Milestone ordering front-loads the spine. (§4, §9.3, §16) |
| R22 | **Postman `pm.*` parity is a multi-month tail.** | High / Medium | Ship a **documented subset with a published compatibility matrix**; JS-source polyfills + narrow host bindings; NO Node built-ins. (§9.3) |
| R23 | **goja/sobek has no per-runtime memory cap.** | Medium / Medium | `SetMaxCallStackSize` + wall-clock timeout catches most runaway allocation; **`GOMEMLIMIT` process backstop**; subprocess for a hard ceiling is a v2 item. (§9.3) |
| R24 | **Wails v3 is alpha (no GA date)** but holds the features we want (multi-window, first-party updater); v2 lacks a built-in updater and native multi-window. | Medium / Medium | **Build on stable v2 now; keep the Wails binding layer thin (all in `cmd/gui`)** so v2→v3 is a bounded port; revisit at v3 beta. (§3) |
| R25 | **Weaker auto-update story than Tauri, hand-rolled from zero.** Wails v2 ships NO built-in updater ([#1178](https://github.com/wailsapp/wails/issues/1178) open); v3's is alpha; no mature first-party cross-platform updater. | Medium / Medium | Use **`minio/selfupdate`** (Ed25519/minisign-verified) as the mechanics layer, decoupled from Wails; own manifest/hosting; **swap a fully codesigned+notarized new `.app`** (not binary-patch-in-place, which invalidates the macOS signature). **macOS caveat:** a running `.app` holds its own bundle and cannot overwrite itself in place — needs the standard **relaunch-helper dance** (stage the new bundle, spawn a small helper that waits for the parent to exit, swaps the bundle, and relaunches), a known-hard Sparkle-less corner. Sparkle only if macOS update-UX is a differentiator worth the two-codepath cost. (§B / roadmap M8) |
| R26 | **Smaller Wails ecosystem** — fewer maintained plugins than Tauri; updater/keychain/deep-link/single-instance are hand-rolled in Go rather than first-party plugins. | Medium / Low | Offset: this project already writes heavy raw-Go plumbing for the shared engine, so the gap is a smaller relative cost. Use Go's own mature primitives (`zalando/go-keyring`, `golang.org/x/sys`). (§4, §7.4) |
| R27 | **WKWebView idle memory leaks on macOS** (open Wails #2772/#2777; version-dependent, 200MB→1GB overnight in some reports); vendor "~10MB" figure is hello-world-only. | Medium / Medium | Budget 80–250MB realistically; **early macOS idle-memory smoke test** with the real frontend before committing; measure on-device, don't cite vendor numbers. (§14) |
| R28 | **Malformed user proto crashes the backend.** A generic tool ingesting arbitrary `.proto`/reflection payloads hits legacy v1-`protoc` custom-option extensions that **nil-panic** during descriptor resolution. | Medium / Medium | Set `RequireInterpretedOptions` appropriately; **`recover()` per descriptor compile** so a bad proto degrades to a clear error, not a crashed goroutine. (§8) |
| R29 | **Scripting `async`/`await` + generators are unsupported** (goja/sobek is ES5.1+partial; blocked upstream, goja #460). Real-world Postman scripts increasingly use `async/await` → SyntaxError on import. | High / Medium | **Compat matrix carries a hard NOT-SUPPORTED row**; all `pm.*` docs/examples are `.then()`-based; v1 **rejects `async` scripts with a specific error** (no in-Go transpiler); transpile is a later option. (§9.3) |
| R30 | **Multi-window UX affordances assumed but impossible on Wails v2** (detached response viewer / floating env inspector / separate k6 window are v3-only). Designing UX around them strands the v1 ship. | Medium / Medium | **Demoted to v1 non-goals**; v1 UX delivers its feel via single-window panels/overlays/splits; detached windows wait for the v3 migration. (§3) |

---

## Build order / milestones

Sequenced so the highest-leverage, hardest-to-change decisions and the week-1 de-risks come first. Each milestone ends in something runnable/testable; the engine is proven headless before any UI depends on it. Note the Go move *removes* two of the Rust design's week-1 spikes (rquickjs MSVC, canonical-YAML emitter) — replaced by Wails-specific de-risks.

### M0 — De-risk spikes (week 1, before committing the plan)
- **Wails v2 `EventsEmit` reliability + throughput** micro-benchmark on macOS (a **ship-block gate**): confirm the single-emitter-goroutine pattern is race-clean under `-race`, the "wake-up + pull-history via binding" pattern holds under rapid emit (#2448/#2759), and the loopback-HTTP-for-large-bodies path works; establish the macOS baseline for the future Windows comparison. (R3, R15, §6)
- **macOS sign + notarize + staple DRY RUN** (a **ship-block gate**, same tier as the EventsEmit spike): a full codesign→notarize→staple cycle on a minimal bundle *with* a nested signed sidecar and the JIT entitlement, before committing the plan — notarization + nested-binary signing + hardened runtime is a multi-day first-time yak-shave. (R10, §11.6)
- **macOS WKWebView idle-memory smoke test** with the real Solid/CodeMirror/uPlot frontend (open leak reports #2772/#2777). (R27, §14)
- **k6 AGPL boundary guard:** a CI check that fails if **`go.k6.io/k6`, `go.k6.io/xk6`, or any `github.com/grafana/xk6-*`** appears anywhere in `go list -deps ./...`, established day one. (R9)
- Stand up **CI gates immediately:** `govulncheck` + Dependabot, `golangci-lint` `depguard` (engine may not import Wails), the **`cli`-builds-without-Wails** check (`go list -deps ./cmd/cli` asserts no `wailsapp/wails`), the **`context.Background()` engine test** (no Wails-ctx smuggling, R19), and the **broadened no-k6/xk6-import** guard. (R9, R18, R19)
- (Removed vs Rust design: rquickjs-MSVC spike and vendored-YAML-emitter spike — both moot in Go.)

### M1 — The spine: headless `core` (HTTP only)
- Domain model, `VariableResolver` (pure templates), storage read/write (canonical `go.yaml.in/yaml/v3` files + `modernc.org/sqlite` cache with FTS5 build tag), **the async `EventSink`**, `SessionRegistry` + `context.CancelFunc`, and **`dispatch()` with a stub `PolicyEngine`** (allow-all but *present*, so the chokepoint exists from day one).
- `RunRequest` for **HTTP/1.1+2 via `net/http`**, single `crypto/tls` config.
- **CLI binary** (`cmd/cli`) consumes the engine end-to-end (`apiclient run request.yaml`, JSON reporter). This *proves* the headless boundary before any Wails exists. (R19)
- Exit criteria: a YAML request runs from the CLI, history lands in SQLite, policy stub is on the egress path, `go list -deps ./cmd/cli` shows zero Wails, and **`RunRequest`/`RunFlow` run green under `context.Background()` with a test `EventSink`** (proves no Wails-ctx dependency; closes the ctx-smuggling leak `go list` can't catch). (R19)

### M2 — Chaining, scripting, real policy
- **Unified dependency graph** (declared `response()` edges) + auto-send through `dispatch`; cycle detection + budget. (R5)
- **`grafana/sobek` `ScriptEngine`** with `Interrupt()` watchdog + `SetMaxCallStackSize` + `GOMEMLIMIT` backstop; `pm`/`bru` documented subset; `pm.sendRequest` routed through `dispatch`; fresh runtime per invocation. Script-initiated edges join the unified graph. (R2, R5, R22, R23)
- **Real `PolicyEngine`**: method/env guards, approval decision type, audit log. (R2, R7)
- Flow/Runner (`chains/*.yaml`) with capture rules, `runIf`/retries, data-driven iteration.
- Exit criteria: a multi-step chain with a pre-request script runs from the CLI; a script-initiated DELETE hits the same policy gate as a top-level one (test asserts no bypass).

### M3 — Wails shell + streaming UI (macOS, v2)
- `cmd/gui` thin bound-method adapters, `AppState`, window/menu/tray; MCP server goroutine wired in `OnStartup`.
- **Solid + Vite + CodeMirror 6 + uPlot + TanStack Query/Virtual**; request editor, response viewer, history.
- **`EventsEmit` wake-up + binding-pull plumbing + loopback-HTTP body server**; coalescing at capped rate; virtualized frame/stream views backed by the **Go-side append-only log** (system of record). Approval modal fed by policy events. (R3, R15)
- Exit criteria: click-to-run parity with the CLI; live HTTP streaming; approval modal works.

### M4 — Streaming protocols
- **WebSocket** (`coder/websocket`) with full-frame persistence + composer. (R3, §8)
- **SSE** (hand-rolled parser). **GraphQL** (POST + introspection + `graphql-transport-ws` subscriptions via `hasura/go-graphql-client`).
- Per-kind cancel semantics surfaced in UI ("closing…"). (R4)
- Exit criteria: WS/SSE/GraphQL consoles live, no dropped frames in the system of record, uniform Cancel.

### M5 — Dynamic gRPC
- **grpc-go + `jhump/protoreflect`(grpcreflect/grpcdynamic) + `bufbuild/protocompile` + `dynamicpb`**, reflection client + `.proto` import; cache `FileDescriptorSet`s; `DynamicMessage` ⇄ JSON editor; unary/server/client/bidi routing. **Harden descriptor parsing:** set `RequireInterpretedOptions` and wrap every compile in a per-parse `recover()`. (R16 moot, R28, §8)
- Exit criteria: a real service (reflection *and* import paths) works — including a proto2/custom-option file (which `protocompile` handles, no `protoc` fallback needed); a **malformed/legacy-custom-option proto degrades to a clear error, not a backend crash** (recover-per-parse asserted in a test). (R28)

### M6 — k6 perf subsystem
- **First-run k6 download** (pinned + checksum) into app-support, spawned via `os/exec`; NDJSON → **1s t-digest buckets** (live, "approx") + `handleSummary` (authoritative); charts in uPlot. (R11, §11)
- Script generation from the same validated request/flow (JSON-escaped literals); thresholds; exit-code-99 verdict; SIGINT cancel with "stopping…"; per-run git-friendly history + trend index.
- **AGPL compliance screen** (license text + attribution + corresponding-source/offer, **plus the explicit note that sobek is Apache/MIT and only the never-linked k6 binary is AGPL**) and a **counsel sign-off checklist item gating the first public build**; the **broadened no-k6/xk6-import CI guard** stays green. (R9)
- Exit criteria: a collection compiles to k6, runs with live charts + final report, PASS/FAIL from exit code; AGPL screen present; `go list -deps` proves k6 is never imported.

### M7 — MCP server (go-sdk v1.x, spec 2025-11-25)
- Embedded `StreamableHTTPHandler` on `127.0.0.1`; bearer via `auth.RequireBearerToken` + **one-click token rotation** + "Copy Claude Code config"; capability + **collection-scoped grants**. (R7, R20)
- Read/Execute/Mutate tools; **long ops as progress notifications** (Tasks best-effort); **approvals native-in-GUI**; structured results + `resource_link`s. (R6)
- **stdio bridge (no auto-launch)** as a staged sidecar. (R8)
- Exit criteria: Claude Code lists/reads/runs; a Prod DELETE triggers an approval prompt regardless of grant; a 20-step collection runs under one session grant without deadlock; bridge returns `isError` when the app is down.

### M8 — Storage hardening, auto-update & polish
- **Fractional order keys** migration; **post-merge integrity + repair pass** and "Workspace health" panel; **encrypted export/import** for team secret sharing; pre-serialization secret guard. (R12, R13, R14)
- **Auto-update** via `minio/selfupdate` (signed, own manifest, full signed+notarized `.app` swap) — the first-class updater milestone the Wails move makes necessary. On macOS, the running `.app` can't overwrite itself, so ship the **relaunch-helper dance** (helper waits for parent exit, swaps the bundle, relaunches). (R25)
- Command palette + customizable hotkeys; read-only git status/diff view via go-git v5 (**not** a full git UI). (R21)
- Exit criteria: concurrent-insert and delete-vs-reference merges resolve cleanly; no secret can be written to a tracked file; a signed update installs and passes Gatekeeper on relaunch.

### M9 — Windows/Linux bring-up (post-popularity)
- WebView2 / WebKitGTK validation of the loopback binary-body path + `EventsEmit` throughput; **WebView2 IPC regression gate**; per-platform coalescing tuning. (R15)
- `go-keyring` per-OS backends; k6 + bridge staged per-arch; Authenticode signing (Windows); AppImage/deb/rpm/Flatpak packaging (mind Flatpak sandboxing of `os/exec` sidecars).
- Exit criteria: streaming UX meets the throughput budget on WebView2; feature parity on all three OSes.

**Explicitly deferred (not in v1):** in-app git *merge/auth/LFS* UI, HTTP/3, distributed k6 (k6-operator/Kubernetes), full Postman `pm.*` parity, script-engine-in-a-subprocess defense-in-depth, Wails v3 migration (until beta).

---

## Highest-leverage decisions to lock now

1. **Headless `core` package first**, event-emitting over an **async, backpressure-aware `EventSink`**, five consumers, one seam. Everything else is an adapter. CI proves the CLI links without Wails (and that k6 is never imported).
2. **Policy at the dispatch chokepoint**, not the MCP boundary — every egress (incl. script `sendRequest` and template auto-send) is gated uniformly.
3. **One `Session` abstraction** with **honest, per-kind cancellation** (`context.Context` everywhere; goja `Interrupt()` for scripts; SIGINT for k6) surfaced in the UI.
4. **`crypto/tls` single source of truth** feeding all protocols.
5. **Files = truth with `go.yaml.in/yaml/v3` + golden corpus; fractional order keys (vendored); secrets never in git; post-merge integrity pass;** `modernc.org/sqlite` (pure Go, FTS5) = disposable cache.
6. **gRPC dynamic** (grpc-go + `protoreflect` + `protocompile` + `dynamicpb`, the same stack k6 uses, **no `protoc`/CGo**) behind an interface; **GraphQL is not a transport**; **HTTP/3 out of v1**.
7. **`grafana/sobek`** (pure Go, no CGo — the MSVC/`v8go` traps avoided) behind a `ScriptEngine` interface; **pure side-effect-free templates**; unified dependency DAG with cycle + budget guards; documented `pm.*`/`bru.*` subset, no Node built-ins.
8. **MCP on the official `go-sdk` v1.x + spec 2025-11-25**: **progress notifications** primary (Tasks/elicitation best-effort), **native-in-GUI approval + per-request user presence for mutating/prod regardless of grant**, collection-scoped grants, **no bridge auto-launch**, one-click token rotation, `127.0.0.1`-only bind.
9. **k6 as unmodified pinned CLI via `os/exec` — NEVER imported, NEVER `go:embed`'d, NEVER `xk6`'d** (the sharper Wails/Go AGPL trap; CI denies the whole `go.k6.io/k6` + `go.k6.io/xk6` + `grafana/xk6-*` family; **sobek is Apache/MIT, only the k6 binary is AGPL and it's never linked**; **corresponding-source + counsel sign-off before first public build**), NDJSON→1s-bucket live pipeline (labeled approx), exit-code-99 verdict, `handleSummary` authoritative.
10. **Treat `EventsEmit` as a wake-up, not a data pipe — for *correctness*, not just throughput** (upstream #2448 race + #2759 rapid-emit inconsistency): funnel all emits through **one dedicated emitter goroutine**, cap the emit rate, coalesce (never fire-per-frame), have the frontend pull history via a binding, route large/binary bodies over loopback HTTP; the Go-side append-only log is the system of record (UI is a virtualized window). The ~40× Windows IPC gap is a design constraint now.
11. **Build on Wails v2 (stable), keep the binding layer thin in `cmd/gui`, and own the auto-update story (`minio/selfupdate`, full signed-bundle swap)** — the two concrete regressions versus Tauri to budget for, and the reason v2→v3 stays a bounded port.
