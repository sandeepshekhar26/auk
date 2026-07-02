# UX North Star — the winning bet

> **This is the product's primary differentiator.** Protocols, k6, MCP, chaining — those are the reasons someone *tries* the app. UX is the reason they *switch* and *stay*. Features get copied; feel does not. Every screen, interaction, and default in this product is held to the standard below. When a feature and the UX conflict, the UX wins — the feature waits until it can ship without degrading the experience.

The competitive landscape (Postman, Insomnia, Yaak, Bruno, Hoppscotch) has the features roughly covered. None of them is *delightful* to use — they are heavy, mouse-bound, cluttered, slow to start, or all four. That is the opening.

---

## The one-sentence standard

**The fastest, most keyboard-fluent, least-cluttered API client in existence** — an app that feels instant, gets out of your way, and lets a power user go from cold-start to sent-request without touching the mouse.

---

## The five UX laws (non-negotiable)

Every PR touching the frontend is reviewed against these. A change that violates one doesn't merge.

### 1. Instant, always
- **Cold start to interactive < 1s.** Perceived startup is a feature. Lazy-load everything non-essential; the sidebar and last-open request render before anything else.
- **Every interaction responds within one frame (16ms).** No spinner for anything the app already has in memory. Typing in the URL bar, switching tabs, expanding a JSON node — zero perceptible latency.
- **Streaming *feels* live.** WS frames, gRPC messages, k6 metrics, SSE events appear as they arrive with no jank — backed by Go-side coalescing + virtualized rendering (see [02-architecture.md](02-architecture.md) §6), never a frozen UI during a firehose. (On Wails this is load-bearing: raw `EventsEmit` is a lossy wake-up, so the backend coalesces and the frontend pulls authoritative payloads via bindings.)

### 2. Keyboard-first, mouse-optional
- **A command palette (⌘K) reaches every action.** Send, switch environment, new request, run collection, open perf test, jump to any request by fuzzy name.
- **Zero-mouse round trip.** New request → set method → type URL → add header → send → inspect response → extract a value into the next request, all from the keyboard.
- **Discoverable shortcuts.** Every actionable element shows its binding on hover/focus; a searchable shortcut sheet (⌘/) lists them all. Bindings are user-remappable.
- **Vim-friendly where it counts** — editor and list navigation support modal/hjkl motion for those who want it, off by default.

### 3. Density without clutter
- **Progressive disclosure.** The default view shows what 90% of requests need; advanced options (proxy, mTLS, timeouts, cert pinning) are one keystroke away, never in your face.
- **No chrome tax.** Minimal toolbars, no nagging modals, no "upgrade" banners, no account wall, no telemetry prompts. The response is the hero of the screen.
- **One primary action per view**, visually obvious. The eye should never hunt for "Send."

### 4. Best-in-class editing & inspection
- **Editors that feel native to the content** — JSON/GraphQL/protobuf/script editors (CodeMirror 6) with real syntax awareness, folding, and inline error squiggles, not a dumb textarea.
- **Response inspection is a joy** — instant pretty/raw/preview toggle, virtualized tree for huge payloads, in-place search, one-click copy-as-cURL, and **response diffing** (two responses, history, or cross-environment) as a first-class view.
- **Live values are honest** — approximate live metrics are labeled approximate; the app never shows two numbers that disagree without explanation (see review finding #21).

### 5. Zero-config, zero-surprise
- **It works before you configure it.** Import an OpenAPI/Postman/cURL and send immediately. Sensible defaults everywhere.
- **Nothing happens you didn't ask for.** No background network calls, no surprise writes to disk, no auto-sync. Destructive and production actions always confirm (this is also the MCP policy model — see [02-architecture.md](02-architecture.md) §MCP).
- **Errors are actionable** — a failed TLS handshake or a gRPC reflection miss explains *what* and *what to do*, not a stack trace.

---

## Why the architecture already backs this

The UX bet is only credible because the stack was chosen to serve it — this isn't aspiration bolted onto an indifferent foundation:

| UX law | What makes it achievable |
|---|---|
| Instant, always | Wails (Go + native OS webview, no bundled Chromium; budget ~80–250MB with the known WKWebView idle-leak reports, still far below Electron); **SolidJS** fine-grained reactivity (no VDOM diff on streaming updates); Go-side stream coalescing + TanStack Virtual. |
| Keyboard-first | Command-palette-first information architecture; every command routed through the same headless engine, so the palette and the buttons call identical code. |
| Density without clutter | Progressive disclosure baked into the component model; CodeMirror (tree-shaken, ~50–150KB) instead of Monaco (2–5MB). |
| Best-in-class editing | CodeMirror 6 language packages; virtualized response trees; response-diff as a v1.0 feature. |
| Zero-config | One-shot importers (OpenAPI/Postman/Insomnia/cURL); files-as-truth means no setup, no login, no server. |

The "lightweight + fast GUI" pillar in the roadmap and the "fast, keyboard-first GUI" pillar in the overview are **the same commitment as this document** — this doc is the standard they're measured against.

---

## How this shows up in the roadmap

UX is not a milestone — it's a constraint on *every* milestone. Concretely:
- **v0.1** ships the command palette, full keyboard round-trip, and the <1s startup budget *as acceptance criteria*, not "later polish."
- Every subsequent feature (WS console, gRPC editor, perf dashboard, MCP approval flow) has a UX bar it must clear before it's considered done.
- A **frame-budget / startup-time regression test** guards the "instant, always" law in CI (paired with the Windows IPC-throughput test from review finding #10).

---

## The bar, restated

If a new user opens this app next to Postman and Insomnia, the thing they notice in the first ten seconds — before they discover k6 or MCP or gRPC — should be: **"this one is fast, and it gets out of my way."** That reaction is the product. Protect it.
