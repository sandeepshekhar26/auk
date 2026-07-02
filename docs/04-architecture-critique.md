# Architecture Review (Wails) — Does the Stack Block Anything?

> Adversarial critique of the Wails/Go architecture, re-run after migrating from Tauri/Rust. All fixes below are folded into [02-architecture.md](02-architecture.md). Kept as a record of the design rationale and the Wails-vs-Tauri tradeoff analysis.

All key facts are verified. The doc's factual claims are largely accurate and current. Now I have what I need to write the adversarial critique. Let me compose the final Markdown response.

## Does Wails block anything? (verdict)

**No hard blockers. Wails does not make anything in this design *impossible*.** Every must-have feature — k6 sidecar, WS/gRPC/HTTP/GraphQL/SSE, chaining + scripting, the exposed MCP server, a lightweight GUI — is achievable on Wails v2/Go, and several things get *easier* than Tauri/Rust (pure-Go SQLite, pure-Go JS, pure-Go dynamic gRPC, `crypto/tls` everywhere, `context.Context` as one cancellation idiom, `internal/` as a compiler-enforced engine boundary). The doc is unusually honest and its verified facts hold up (Wails v3 still alpha at `alpha.102`; go-sdk v1.x real and spec-current; k6 still AGPLv3; `EventsEmit` base64/no-backpressure real; sobek is the k6 lineage).

**But Wails makes four things materially *harder* than the Tauri design, and the doc under-sells two of them:**

1. **Streaming to the webview is a genuine downgrade.** Tauri's typed `Channel<T>` gave you an ordered, backed pipe. Wails `EventsEmit` is a lossy pub/sub wake-up with a *known data race* (#2448) and *known inconsistent-delivery-under-rapid-emit* reports (#2759) — not just "no backpressure." The doc's "wake-up + pull-history-via-binding" pattern is the correct workaround, but it is now **load-bearing for correctness, not just performance**, and the doc frames it as mostly the latter.
2. **Auto-update is hand-rolled from zero** (v2 has no updater, v3's is alpha). Correctly flagged.
3. **Multi-window is impossible in v2** (verified: v2 is single-`wails.Run()`, multi-window is a v3-only feature). The doc lists detached response viewer / floating env inspector / separate k6 dashboard as v3 perks — meaning **those specific UX affordances are off the table for the v1 ship**, and for a product whose entire differentiator is UX, that's a real constraint, not a footnote.
4. **The k6 AGPL trap is sharper and the doc's boundary is *mostly* airtight but has one unguarded seam** (below).

Nothing here is a RECONSIDER-level blocker. But the streaming primitive and the scripting-engine gap are bigger than the doc's risk table implies.

---

## Gaps & Risks (grouped)

### A. Streaming / EventsEmit — the weakest verified spot

- **The data race is real and shipping.** [Issue #2448](https://github.com/wailsapp/wails/issues/2448) is an actual `-race` failure in `runtime.mapaccess1_faststr` inside `Events.Emit`/`notifyBackend` — concurrent `EventsEmit` (from your engine goroutines) vs. `EventsOn` (from the webview registering listeners). This is exactly your access pattern: many session goroutines emitting while the frontend mounts/unmounts subscribers as the user opens tabs. The doc treats `EventsEmit` weakness as "no backpressure + historical dropped-data reports." It should also treat it as **"the emit path itself is not race-clean upstream,"** which you cannot fix — you can only avoid concentrating emits.
- **[#2759 inconsistent data under rapid emit](https://github.com/wailsapp/wails/issues/2759)** confirms the "coalesce, don't fire-per-frame" rule is mandatory, not tuning. Good that the doc already mandates it; the doc should cite this as the reason the rule is non-negotiable.
- **The "frontend PULLS history via a binding" pattern doubles your binding call volume and adds a round-trip per wake-up.** For a 20–30Hz coalesced stream that's 20–30 binding calls/sec/session — each marshalled through the same Go↔JS bridge. This is fine on macOS/WKWebView but is precisely the path that hits the WebView2 latency cliff on Windows. The doc acknowledges the cliff but doesn't note that its *own mitigation* (pull-via-binding) is more bridge-chatty than a push stream would be — so the Windows tax is on the hot path, not just on large bodies.

### B. Wails v2 vs v3 maturity — the doc's bet is right but the cliff is real

- v3 is verified still **alpha (`alpha.102`, no committed GA)** — building on it would be reckless, so v2 is correct.
- **The v2→v3 migration is not "a port of glue."** Verified: v3 replaces the monolithic `wails.Run()` with a service/DI application model, moves the whole build system to `wails3`/Taskfile, and changes the runtime-call signatures (context threading differs). Because your `cmd/gui` also owns the loopback body server, the MCP goroutine wiring, the event coalescer, the approval-modal plumbing, *and* the `EventsEmit`↔binding-pull dance, "thin glue" is optimistic — that package will accrete. The isolation discipline is right; the "bounded port" claim is somewhat rosy. Budget it as a real project, not a weekend.
- **Auto-update ([#1178](https://github.com/wailsapp/wails/issues/1178) still open):** `minio/selfupdate` + full signed-`.app`-swap is the right mechanic. One thing the doc under-specifies: on macOS you cannot swap a running `.app` in place trivially (the running process holds the bundle); you need the standard relaunch-helper dance. That's a known-hard corner of Sparkle-less updaters and deserves a line.

### C. k6 AGPL — one unguarded seam

The boundary is *almost* airtight and the doc's instincts (never import, never `go:embed`, never `xk6`, ship corresponding source, counsel sign-off) are all correct. Two gaps:

- **The CI guard as written is `go list -deps ./...` for `go.k6.io/k6`.** That catches the *k6 runtime*, but the sharper accidental import is **`go.k6.io/k6/js/modules` or a shared helper package** pulled in transitively by a *convenience* dependency — e.g., if you ever add a k6 output/extension helper, or vendor a "k6 script validator" that imports k6's JS module registry to typecheck generated scripts. The guard should ban the **entire `go.k6.io/k6` module path prefix** (it does) *and* also fail on `go.k6.io/xk6` and any `grafana/xk6-*`. Add those to the deny list explicitly.
- **Sobek is the one legitimately-shared lineage that muddies the "arm's length" story.** You import `grafana/sobek` in-process; k6 also uses sobek. Sobek itself is **Apache/MIT-style (goja lineage), not AGPL**, so this is *fine* — but a reviewer/auditor will flag "you import a Grafana JS engine and shell out to a Grafana AGPL tool" and you'll need to show the license split cleanly. Worth a one-line note in the AGPL section pre-empting that: *sobek is not AGPL; only the k6 binary is, and it's never linked.*

### D. goja/sobek — the doc oversells ES compatibility and one API call

This is the gap I'd most want fixed. Verified facts:

- **sobek/goja is fundamentally ES5.1 with partial ES6+ backfill. Native `async/await` is NOT implemented** — it's [blocked on goja's ability to save/restore execution context](https://github.com/dop251/goja/issues/460), and **generators are also not implemented**. The doc's §9.3 sample writes `await pm.sendRequest('GetUser')` as a supported call. That `await` at top level or in an `async` function **will not parse/run** on stock sobek. The doc half-acknowledges this ("support `Promise` so `.then()` chains work even without native `async/await`") — but then the very code sample two sections earlier uses `await`. **These contradict.** The honest surface is: `.then()`/Promise yes, `async/await` **no**. Every `pm.*` example and the compat matrix must be `.then()`-based, or you must transpile user scripts (Babel-in-Go? — you don't have that) before running. This is a real papercut for a Postman-shim: Postman scripts in the wild increasingly use `async/await`, and yours will throw a parse error on them. Call it out in the compat matrix as a hard limitation.
- **Interrupt reliability:** the doc's watchdog + `ClearInterrupt` + concurrent-interrupt stress-test is correct and matches upstream (interrupt fires between VM instructions; `NewProxy`/`Proxy` and `WeakMap` exist). Good.
- **Memory cap:** correctly flagged as absent. One addition — goja's [finalizer-on-reference-cycle](https://github.com/grafana/sobek) footgun means `GOMEMLIMIT` is a *process* backstop that will GC-thrash rather than kill one bad script; the subprocess-rlimit path is the only real per-script ceiling. The doc says this; keep it.
- **Performance vs V8:** the doc never quantifies it. Sobek is an AST-walking-ish interpreter and is roughly an **order of magnitude slower than V8** on heavy compute. For per-request pre/post scripts this is irrelevant (correct call). But the doc should explicitly state: **never run data-transform-heavy or large-loop scripts in-process; that's what the k6 sidecar / a future subprocess is for.** It gestures at this but doesn't make it a rule.

### E. gRPC dynamic — actually delivers, one caveat

- Verified: `protocompile` (now forked into `jhump/protoreflect`) + `dynamicpb` + reflection is exactly the k6 stack and is the strongest dynamic-proto story in any language. Proto2, custom options, and **Editions (2023, and experimental 2024 via `AllowExperimentalEditions`)** are supported. R16 being "moot" is fair.
- **One caveat the doc misses:** custom-option resolution can **nil-panic on legacy v1-protoc-generated extensions** unless you set `BuilderOptions.RequireInterpretedOptions` (per the protoreflect release notes). For a *generic* tool ingesting arbitrary user `.proto`/reflection payloads, you *will* hit weird-in-the-wild descriptors. Add "harden descriptor parsing against panics; recover-per-parse" as an explicit item, because a malformed proto from a user's server should degrade gracefully, not crash the backend goroutine.

### F. The "core engine, no Wails deps" boundary — solid, one leak vector

- The `internal/` + `depguard` + `go list -deps ./cmd/cli` triple guard is genuinely stronger than the Rust workspace boundary. Good.
- **The realistic leak isn't `import wailsapp/wails` in core** (CI catches that). It's **`context.Context` value smuggling.** Wails threads its runtime through the `context.Context` you get in `OnStartup` and bound methods; `runtime.EventsEmit(ctx, …)` *requires that specific context*. The temptation is to pass that Wails-flavored ctx down into the engine so the engine can emit directly — which technically doesn't import Wails (the ctx is opaque) but **couples the engine to Wails' runtime at value level**, and the CLI's plain `context.Background()` will silently no-op or panic on `EventsEmit`. The doc's `EventSink` interface is exactly the right firewall — **enforce that the engine only ever emits via `EventSink`, never touches `ctx` for runtime, and add a test that runs the engine under `context.Background()` to prove no Wails-ctx dependency leaked.** The doc implies this but doesn't state the ctx-smuggling failure mode by name.

### G. Storage / go-git / MCP — prior fixes survived the port

- **Fractional keys, secrets-out-of-git, post-merge integrity pass, golden corpus** all ported intact and correct. `go.yaml.in/yaml/v3` is the right successor to archived `gopkg.in/yaml.v3`. Vendoring fracdex (~200 LOC) is the right call.
- **go-git:** verified v6 is now **alpha** and v5 is **still maintained (v5.18.0 security release, May 2026)**. The doc's "v5 not alpha v6" pin is correct and current. Read-only status/diff scope (not a full git UI) is the right scope trap to avoid. Good.
- **MCP security:** localhost+token-is-necessary-not-sufficient, per-request user presence for mutating/prod, native-in-GUI approval (not agent-relayed), no-bridge-auto-launch, collection-scoped grants — all survived and are correct. The go-sdk v1.x / progress-primary / Tasks-best-effort call is verified-current (v1.5 shipped Apr 2026; v1.7+ tracks the 2026-07-28 spec; elicitation exists but is churning). Good.
- **Backpressure/cancellation honesty:** the per-kind cancellation table is honest (the goja-`while(true)`, uncancellable-SQLite-write, and k6-SIGINT-lag caveats are all correct). Survived intact.

### H. What a solo dev underestimates on Wails specifically

1. **macOS CI is mandatory and not free.** Verified implicitly by the design: the Wails macOS shell needs system WebKit via cgo — **you cannot cross-compile *to* macOS**, so you need real macOS runners for every build+sign+notarize. The doc says this once; it's the single biggest "solo dev underestimates" item — notarization + nested-binary signing + hardened runtime + JIT entitlement for k6 is a multi-day yak-shave the first time.
2. **WKWebView idle memory leaks** ([#2772/#2777](https://github.com/wailsapp/wails/issues)) are real and macOS-specific; the "~10MB" vendor number is hello-world-only. Correctly flagged (R27). Budget 80–250MB.
3. **DevTools/debugging is weaker than a browser.** WKWebView's inspector is less capable than Chrome DevTools; for a streaming-heavy UI you'll want good logging. Not in the doc.
4. **No mature plugin ecosystem** — single-instance, deep-link, tray, updater are all hand-rolled. Flagged (R26), correct.

---

## Concrete Fixes

1. **Reframe R3 from "backpressure" to "delivery correctness."** Cite #2448 (data race) and #2759 (inconsistent rapid-emit) as the *reasons* the pull-via-binding pattern is mandatory. Add: **serialize all `EventsEmit` calls through a single dedicated emitter goroutine** (one ctx, one emit site) to dodge the #2448 race, since you can't fix upstream. The per-session bounded channels already fan into this — make "one emitter goroutine" explicit.
2. **Fix the §9.3 `await pm.sendRequest()` example.** Replace with `.then()` form. Add to the compat matrix a **hard "NOT SUPPORTED" row for `async`/`await` and generators** (blocked upstream in goja/sobek), and decide now whether you pre-transpile user scripts (you currently have no transpiler) or reject `async` scripts with a clear error. This is the single most likely "why does my Postman script throw a SyntaxError" support ticket.
3. **Expand the no-k6 CI guard** to also deny `go.k6.io/xk6` and `grafana/xk6-*` module prefixes, and add a one-line note that **sobek is Apache/MIT (goja lineage), not AGPL** — pre-empting the "you use Grafana JS + Grafana k6" audit question.
4. **Add a `context.Background()` engine test** to §16/M1 exit criteria: run `RunRequest`/`RunFlow` under a plain background context with a test `EventSink`, asserting no reliance on a Wails-flavored ctx. This closes the real leak vector (ctx-value smuggling) that `go list -deps` can't catch.
5. **Harden dynamic-proto parsing:** set `RequireInterpretedOptions` appropriately and wrap descriptor compilation in a per-parse `recover()`, because arbitrary user `.proto`/reflection payloads *will* include panic-triggering legacy custom options. Add to M5 exit criteria.
6. **Demote the v3 multi-window UX features from "attractive perks" to "v1 non-goals."** Detached response viewer / floating env inspector / separate k6 window are **impossible on v2** — either design the v1 UX to not need them (single-window with panels/overlays) or accept they wait for the v3 migration. Don't let a UX-differentiator design assume affordances the chosen stack can't deliver.
7. **Spell out the macOS in-place-update relaunch dance** in R25/M8 (running `.app` can't overwrite itself; needs a relaunch helper). And add "**first macOS build: full sign+notarize+staple dry run**" as an M0 spike alongside the EventsEmit benchmark — it's the same tier of "de-risk before committing."
8. **State the sobek performance rule as a rule:** "in-process scripts are for orchestration/assertions only; any heavy compute or large iteration goes to the k6 sidecar or a future subprocess — sobek is ~10× slower than V8." Currently implied, not mandated.

---

**Recommendation: GO-WITH-CAVEATS** — the Wails/Go port is sound and blocks nothing, but ship-block on three concrete items before committing the plan: (a) the M0 `EventsEmit` reliability + macOS sign/notarize spikes must pass, (b) the `async/await`/generator gap must be fixed in the scripting compat matrix and code samples, and (c) the no-k6 CI guard must be broadened to `xk6`, with sobek's non-AGPL status documented.

Reviewed file: `/Users/skumar/repos/api-tool/docs/02-architecture.md`

**Sources:** [Wails releases](https://github.com/wailsapp/wails/releases) · [Wails v3 status (alpha)](https://github.com/wailsapp/wails/discussions/4447) · [Wails events base64/binary #3212](https://github.com/wailsapp/wails/issues/3212) · [Wails events data race #2448](https://github.com/wailsapp/wails/issues/2448) · [Wails events inconsistent-under-load #2759](https://github.com/wailsapp/wails/issues/2759) · [Wails self-update #1178](https://github.com/wailsapp/wails/issues/1178) · [Wails multi-window #1480](https://github.com/wailsapp/wails/issues/1480) · [Wails v3 multi-window feature](https://v3alpha.wails.io/features/windows/multiple/) · [MCP go-sdk releases](https://github.com/modelcontextprotocol/go-sdk/releases) · [MCP go-sdk v1.0.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.0.0) · [grafana/sobek](https://github.com/grafana/sobek) · [goja async/await blocked #460](https://github.com/dop251/goja/issues/460) · [k6 LICENSE (AGPLv3)](https://github.com/grafana/k6/blob/master/LICENSE.md) · [jhump/protoreflect + protocompile/editions](https://github.com/jhump/protoreflect/releases) · [go-git v6 (alpha) / v5 maintained](https://github.com/go-git/go-git/releases)
