import { Show, Switch, Match, createEffect, createMemo, createSignal, For, on } from 'solid-js'
import { appState, setAppState, setCommandPaletteOpen, setStreamConsoleOpen, setSettingsOpen, activeStreams, pushStreamEvent } from '../lib/store'
import { licenseStatus } from '../lib/license'
import { saveRequestDebounced } from '../lib/data'
import { startStream, stopStream, sendStreamMessage } from '../lib/stream'
import { wails } from '../lib/wails'
import type { KeyValue, ProtocolKind } from '../types'
import KeyValueTable from './KeyValueTable'
import CopyAsMenu from './CopyAsMenu'
import BodyEditor from './BodyEditor'
import GraphQLEditor from './GraphQLEditor'
import GrpcEditor, { METHOD_HEADER } from './GrpcEditor'
import AuthConfigForm from './AuthConfigForm'
import AssertionEditor from './AssertionEditor'
import PerfPanel from './PerfPanel'
import ScriptEditor from './ScriptEditor'

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

const PROTOCOLS: { value: ProtocolKind; label: string }[] = [
  { value: 'http', label: 'HTTP' },
  { value: 'graphql', label: 'GraphQL' },
  { value: 'websocket', label: 'WS' },
  { value: 'sse', label: 'SSE' },
  { value: 'grpc', label: 'gRPC' },
]

// HTTP and GraphQL are the only protocols that carry an HTTP verb; WS/SSE/gRPC
// address their target purely by URL (+ method header for gRPC), so the method
// dropdown is hidden for them.
const usesHttpMethod = (p: ProtocolKind) => p === 'http' || p === 'graphql'

// WebSocket and SSE always stay open and stream frames, so their action is
// Connect/Disconnect (via StartStream/StopStream) rather than a one-shot
// Send. gRPC is mixed — most methods are unary and should stay a plain
// Send/Response like HTTP, but a server-streaming method needs the SAME
// live-session treatment. There's no reflection-based method picker yet
// (GrpcEditor lets the target be typed freely), so isGrpcServerStreaming is
// populated by a separate async check (see the effect below) rather than
// being knowable synchronously from the protocol alone.
const isStreamingProtocol = (p: ProtocolKind, isGrpcServerStreaming: boolean) =>
  p === 'websocket' || p === 'sse' || (p === 'grpc' && isGrpcServerStreaming)

const URL_PLACEHOLDER: Record<ProtocolKind, string> = {
  http: 'https://api.example.com/${path}',
  graphql: 'https://api.example.com/graphql',
  websocket: 'wss://example.com/socket',
  sse: 'https://example.com/events',
  grpc: 'example.com:443',
}

// URL <-> Params sync helpers. Deliberately NOT the URL/URLSearchParams web
// APIs: AUK's URLs are routinely templated (${baseUrl}/foo), which isn't a
// valid absolute URL those constructors accept, and URLSearchParams always
// percent-encodes on both parse and stringify — which would silently mangle
// a literal ${uuid()} template ref typed into a param's value into
// %24%7Buuid()%7D in the URL bar. A manual, literal split keeps the URL bar
// a WYSIWYG preview; the one real percent-encoding pass already happens
// once, at send time, in the Go backend's buildURL (internal/protocols/http).
function splitQuery(url: string): { base: string; query: string } {
  const idx = url.indexOf('?')
  return idx === -1 ? { base: url, query: '' } : { base: url.slice(0, idx), query: url.slice(idx + 1) }
}

function parseQueryParams(query: string): KeyValue[] {
  if (!query) return []
  return query.split('&').map((pair) => {
    const eq = pair.indexOf('=')
    return eq === -1
      ? { key: pair, value: '', enabled: true }
      : { key: pair.slice(0, eq), value: pair.slice(eq + 1), enabled: true }
  })
}

// Mirrors the backend's own filter (buildURL skips a disabled row or one
// with no key at all — an incomplete row, not a real query param) so the
// URL bar preview never disagrees with what's actually sent.
function buildQueryString(params: KeyValue[] | undefined): string {
  return (params ?? [])
    .filter((p) => p.enabled && p.key)
    .map((p) => `${p.key}=${p.value}`)
    .join('&')
}

// A Postman-style path placeholder: a WHOLE path segment of `:name`.
// Character-for-character the same rule as core.pathParamName in
// internal/core/pathparams.go, because the rows shown here must be exactly
// the ones the backend substitutes — a mismatch would mean either an
// un-fillable placeholder or a filled-in row that silently does nothing.
const PATH_PARAM_SEGMENT = /^:[A-Za-z_][A-Za-z0-9_]*$/

// Extracts the placeholder names from a URL's PATH, in order, de-duplicated.
//
// Same deliberate hand-rolled parse as splitQuery above (URL/URLSearchParams
// reject `${baseUrl}/users/:id`, which is the normal shape here). Only
// segments after the first '/' are considered, so a `host:443` authority is
// never mistaken for a placeholder — and since a name can't start with a
// digit, `:443` wouldn't match even if it were. The query string is dropped
// first, so `?next=/x/:id` contributes nothing.
function parsePathParamNames(url: string): string[] {
  const { base } = splitQuery(url)
  const slash = base.indexOf('/')
  if (slash === -1) return []
  const names: string[] = []
  for (const seg of base.slice(slash).split('/')) {
    if (PATH_PARAM_SEGMENT.test(seg)) {
      const name = seg.slice(1)
      if (!names.includes(name)) names.push(name)
    }
  }
  return names
}

type EditorTab = 'params' | 'headers' | 'body' | 'auth' | 'script' | 'assert' | 'perf'
const TABS: { id: EditorTab; label: string }[] = [
  { id: 'params', label: 'Params' },
  { id: 'headers', label: 'Headers' },
  { id: 'body', label: 'Body' },
  { id: 'auth', label: 'Auth' },
  { id: 'script', label: 'Script' },
  { id: 'assert', label: 'Assert' },
  { id: 'perf', label: 'Perf' },
]

export default function RequestEditor(props: { onSend: (requestId: string) => void }) {
  const [tab, setTab] = createSignal<EditorTab>('params')
  const [composeText, setComposeText] = createSignal('')

  const activeIndex = createMemo(() => appState.requests.findIndex((r) => r.id === appState.activeTabId))
  const active = createMemo(() => appState.requests.find((r) => r.id === appState.activeTabId))

  // Soft licence gate: once the trial has ended (or a stored licence fails to
  // verify), block the two request-INITIATING actions (Send / Connect) —
  // everything else, including viewing, editing and Disconnecting a live
  // stream, stays fully usable. Absent/loading status never gates, so a
  // pre-regeneration dev build or a licensed user is unaffected.
  const sendGated = createMemo(() => {
    const s = licenseStatus()
    return s?.state === 'trial_expired' || s?.state === 'license_invalid'
  })

  const streaming = (requestId: string) => !!activeStreams()[requestId]

  // Populated by DescribeGrpcMethod (a real reflection round trip) whenever
  // the active request is gRPC and its URL or x-grpc-method header settles
  // for 500ms — debounced so this doesn't dial the target on every
  // keystroke. Keyed by request id so switching tabs doesn't show a stale
  // verdict from whatever request was active before the check for THIS one
  // resolves.
  const [grpcServerStreamingByRequest, setGrpcServerStreamingByRequest] = createSignal<Record<string, boolean>>({})
  const isGrpcServerStreaming = (requestId: string) => grpcServerStreamingByRequest()[requestId] === true

  let grpcCheckTimer: ReturnType<typeof setTimeout> | undefined
  createEffect(() => {
    const req = active()
    if (!req || req.protocol !== 'grpc') return
    const requestId = req.id
    const methodHeaderValue = req.headers?.find((h) => h.key.toLowerCase() === METHOD_HEADER)?.value ?? ''
    // Reading both here (not just inside the timeout below) is what makes
    // Solid's tracking re-run this effect when either changes.
    const url = req.url
    void url
    void methodHeaderValue

    if (grpcCheckTimer) clearTimeout(grpcCheckTimer)
    grpcCheckTimer = setTimeout(() => {
      wails
        .DescribeGrpcMethod(requestId, appState.activeEnvironmentId ?? '')
        .then((info) => setGrpcServerStreamingByRequest((prev) => ({ ...prev, [requestId]: !!info?.serverStreaming })))
        .catch(() => setGrpcServerStreamingByRequest((prev) => ({ ...prev, [requestId]: false })))
    }, 500)
  })

  function connect(requestId: string) {
    setStreamConsoleOpen(true)
    startStream(requestId, appState.activeEnvironmentId ?? '').catch((err) => {
      // Surface a dial/handshake failure in the console rather than silently
      // leaving the button on "Connect" with no explanation.
      pushStreamEvent({
        sessionId: 'error',
        kind: 'ws',
        direction: 'meta',
        payload: 'connect failed: ' + (err instanceof Error ? err.message : String(err)),
        timestamp: new Date().toISOString(),
      })
    })
  }

  function sendFrame(requestId: string) {
    const text = composeText().trim()
    if (!text) return
    sendStreamMessage(requestId, text).catch(() => {})
    setComposeText('')
  }

  // Persist any edit to the active request (method/url/headers/params/body/
  // auth all flow through this same store object) — debounced so typing
  // doesn't fire one backend call per keystroke.
  //
  // IMPORTANT: `active` is a `.find()`-based memo whose predicate only
  // reads `r.id`, so Solid's fine-grained tracking only subscribes this
  // effect to `.id` — editing `.url`/`.headers`/etc. would silently never
  // re-fire it (found via manual browser testing: typing a URL updated the
  // input on screen via direct JSX property access, but nothing was ever
  // sent to the backend). JSON.stringify walks every nested field, which
  // forces a read-dependency on all of them, so any real edit re-triggers
  // this effect; `on()` still no-ops if the stringified content is
  // unchanged.
  createEffect(
    on(
      () => {
        const req = active()
        return req ? JSON.stringify(req) : null
      },
      (snapshot) => {
        if (snapshot) saveRequestDebounced(JSON.parse(snapshot))
      },
      { defer: true },
    ),
  )

  // Rebuilds the URL's query-string suffix from the params table's ENABLED
  // rows (mirroring the backend's own filter — see buildQueryString), so
  // editing/toggling/removing a param reflects live in the URL bar. Only
  // writes when the rebuilt query actually differs, so this can't fight
  // with someone mid-typing directly in the URL bar (see onUrlInput).
  function syncUrlFromParams(idx: number) {
    const r = appState.requests[idx]
    if (!r) return
    const { base } = splitQuery(r.url)
    const query = buildQueryString(r.params)
    const nextUrl = query ? `${base}?${query}` : base
    if (nextUrl !== r.url) setAppState('requests', idx, 'url', nextUrl)
  }

  // Reconciles the pathParams rows with the `:name` placeholders currently
  // in the URL. Rows are DERIVED from the URL — typing a new placeholder
  // adds one, deleting it from the URL removes it — but a row's VALUE is
  // the user's, so it's carried across by key on every re-parse. Without
  // that, typing one more character of the URL would blank the value you
  // just entered.
  //
  // Writes only when the ordered list of names actually changed. That's not
  // just an optimization: rewriting the array on every keystroke would
  // recreate the <For> rows, which destroys the focused <input> and drops
  // the caret — the exact focus-loss class of bug this file's other sync
  // helpers are all shaped to avoid.
  function syncPathParamsFromUrl(idx: number) {
    const r = appState.requests[idx]
    if (!r) return
    const names = parsePathParamNames(r.url)
    const current = r.pathParams ?? []
    if (names.length === current.length && names.every((n, i) => current[i].key === n)) return
    const byKey = new Map(current.map((p) => [p.key, p.value]))
    setAppState(
      'requests',
      idx,
      'pathParams',
      names.map((name) => ({ key: name, value: byKey.get(name) ?? '', enabled: true })),
    )
  }

  // Keeps the Path rows correct for URLs this editor didn't type: opening a
  // request whose stored URL already has `:name` (imported from Postman,
  // pulled in over git, hand-edited YAML), or switching tabs. Reading
  // `.url` off the active request is what subscribes this to it — see the
  // save-effect comment above for why `active()` alone wouldn't.
  //
  // `on()` rather than a bare effect specifically so the dependency is the
  // index+URL and nothing else: syncPathParamsFromUrl READS pathParams to
  // decide whether to write them, and inside a tracking scope that read
  // would subscribe the effect to its own output. on()'s callback runs
  // untracked, so it can't.
  createEffect(
    on(
      () => {
        const req = active()
        return req ? `${activeIndex()} ${req.url}` : null
      },
      (key) => {
        if (key !== null) syncPathParamsFromUrl(activeIndex())
      },
    ),
  )

  function setRow(field: 'headers' | 'params', index: number, key: keyof KeyValue, value: string | boolean) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, index, key as any, value as any)
    if (field === 'params') syncUrlFromParams(idx)
  }

  // Fine-grained path write, exactly like setRow: it targets one row's one
  // field so Solid updates that <input>'s value in place instead of
  // rebuilding the row and stealing focus mid-keystroke.
  function setPathParamValue(index: number, value: string) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, 'pathParams', index, 'value', value)
  }

  // Go's omitempty serializes an empty/nil headers-or-params slice as JSON
  // null, not []. A default parameter (`rows = []`) only fires for
  // `undefined`, so `null` slipped through and `[...null, x]` threw —
  // silently, inside the store's updater, with no visible error — meaning
  // "+ Add row" did nothing on any request whose headers/params started
  // empty. `?? []` inside the body catches null AND undefined.
  function addRow(field: 'headers' | 'params') {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, (rows: KeyValue[] | null | undefined) => [
      ...(rows ?? []),
      { key: '', value: '', enabled: true },
    ])
    // A fresh row has no key yet, so it never contributes to the query
    // string (buildQueryString filters empty keys, matching the backend) —
    // this is here so a URL previously widened by a removed disabled row
    // stays correct, not because adding a row itself changes the URL.
    if (field === 'params') syncUrlFromParams(idx)
  }

  function removeRow(field: 'headers' | 'params', index: number) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, (rows: KeyValue[] | null | undefined) => (rows ?? []).filter((_, i) => i !== index))
    if (field === 'params') syncUrlFromParams(idx)
  }

  // The companion direction: typing a query string directly into the URL
  // bar auto-populates the Params tab, matching Postman/Insomnia/Yaak.
  // Guarded so it only touches params when the QUERY portion actually
  // changed — editing the path with an empty query (the common case)
  // would otherwise wipe out an in-progress, not-yet-keyed param row on
  // every keystroke. Disabled rows are never reflected in the URL (see
  // buildQueryString), so they're preserved untouched across a resync
  // rather than being silently dropped.
  function onUrlInput(value: string) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, 'url', value)
    // `:name` placeholders in the path get the same treatment as the query
    // string does below — the URL bar stays the single place you author
    // both, and the Params tab reflects it.
    syncPathParamsFromUrl(idx)
    const { query } = splitQuery(value)
    const current = appState.requests[idx]?.params ?? []
    if (query === buildQueryString(current)) return
    const disabledRows = current.filter((p) => !p.enabled)
    setAppState('requests', idx, 'params', [...parseQueryParams(query), ...disabledRows])
  }

  function enabledCount(rows: KeyValue[] | undefined) {
    return (rows ?? []).filter((r) => r.enabled).length
  }

  return (
    <Show when={active()} fallback={<EmptyState />}>
      {(req) => (
        <div class="flex h-full flex-col">
          <div class="flex items-center border-b border-edge px-2 pt-1.5">
            <input
              class="min-w-0 flex-1 truncate rounded bg-transparent px-1 py-0.5 text-sm font-medium text-ink-dim focus:bg-field focus:text-ink focus:outline-none"
              value={req().name}
              placeholder="Untitled request"
              onInput={(e) => setAppState('requests', activeIndex(), 'name', e.currentTarget.value)}
            />
          </div>
          <div class="flex items-center gap-2 border-b border-edge p-2">
            <select
              class="rounded bg-field px-2 py-1 font-mono text-xs font-semibold text-ink-dim focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={req().protocol || 'http'}
              onChange={(e) => setAppState('requests', activeIndex(), 'protocol', e.currentTarget.value as ProtocolKind)}
              title="Protocol"
            >
              {PROTOCOLS.map((p) => (
                <option value={p.value}>{p.label}</option>
              ))}
            </select>
            <Show when={usesHttpMethod(req().protocol || 'http')}>
              <select
                class="rounded bg-field px-2 py-1 font-mono text-xs font-semibold text-accent-fg focus:outline-none focus:ring-1 focus:ring-edge-strong"
                value={req().method}
                onChange={(e) => setAppState('requests', activeIndex(), 'method', e.currentTarget.value)}
              >
                {METHODS.map((m) => (
                  <option value={m}>{m}</option>
                ))}
              </select>
            </Show>
            <input
              class="flex-1 rounded bg-field px-2 py-1 font-mono text-sm text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={req().url}
              placeholder={URL_PLACEHOLDER[req().protocol || 'http']}
              onInput={(e) => onUrlInput(e.currentTarget.value)}
            />
            <Show
              when={isStreamingProtocol(req().protocol || 'http', isGrpcServerStreaming(req().id))}
              fallback={
                <Show
                  when={!sendGated()}
                  fallback={<ActivateToSend />}
                >
                  <button
                    class="rounded bg-accent px-3 py-1 text-sm font-medium text-accent-contrast hover:bg-accent-hover"
                    onClick={() => props.onSend(req().id)}
                  >
                    Send
                  </button>
                </Show>
              }
            >
              <Show
                when={streaming(req().id)}
                fallback={
                  <Show when={!sendGated()} fallback={<ActivateToSend />}>
                    <button
                      class="rounded bg-accent px-3 py-1 text-sm font-medium text-accent-contrast hover:bg-accent-hover"
                      onClick={() => connect(req().id)}
                    >
                      Connect
                    </button>
                  </Show>
                }
              >
                {/* Disconnect is never gated — you can always stop an
                    in-flight stream, licence state notwithstanding. */}
                <button
                  class="rounded border border-edge-strong bg-field px-3 py-1 text-sm font-medium text-danger hover:bg-raised"
                  onClick={() => stopStream(req().id)}
                >
                  Disconnect
                </button>
              </Show>
            </Show>
            {/* Copy as code, right of Send — because the reason you want a
                cURL command is usually to hand the request to someone else
                or paste it in a terminal, which has nothing to do with
                whether you've run it yet. The response-side menu (same
                component, same backend resolution) stays for after a send. */}
            <CopyAsMenu requestId={req().id} protocol={req().protocol || 'http'} variant="icon" />
          </div>

          {/* WebSocket message composer — only while connected. SSE is
              receive-only, so it never shows this. */}
          <Show when={req().protocol === 'websocket' && streaming(req().id)}>
            <div class="flex items-center gap-2 border-b border-edge px-2 py-1.5">
              <input
                class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="Message to send…"
                value={composeText()}
                onInput={(e) => setComposeText(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') sendFrame(req().id)
                }}
              />
              <button
                class="rounded bg-raised px-3 py-1 text-xs font-medium text-ink-dim hover:bg-elevated"
                onClick={() => sendFrame(req().id)}
              >
                Send frame
              </button>
            </div>
          </Show>

          <div class="flex items-center gap-1 border-b border-edge px-2">
            <For each={TABS}>
              {(t) => (
                <button
                  class="relative px-3 py-2 text-xs font-medium"
                  classList={{
                    'text-ink': tab() === t.id,
                    'text-ink-muted hover:text-ink-dim': tab() !== t.id,
                  }}
                  onClick={() => setTab(t.id)}
                >
                  {t.label}
                  <Show when={t.id === 'params' && enabledCount(req().params) > 0}>
                    <span class="ml-1 text-ink-faint">{enabledCount(req().params)}</span>
                  </Show>
                  <Show when={t.id === 'headers' && enabledCount(req().headers) > 0}>
                    <span class="ml-1 text-ink-faint">{enabledCount(req().headers)}</span>
                  </Show>
                  <Show when={t.id === 'auth' && req().authRef && req().authRef!.kind !== 'none'}>
                    <span class="ml-1 text-accent-fg">●</span>
                  </Show>
                  <Show when={t.id === 'script' && (req().preRequestScript?.trim().length ?? 0) > 0}>
                    <span class="ml-1 text-accent-fg">●</span>
                  </Show>
                  <Show when={t.id === 'assert' && (req().assertions?.length ?? 0) > 0}>
                    <span class="ml-1 text-ink-faint">{req().assertions!.length}</span>
                  </Show>
                  <Show when={t.id === 'perf' && !!req().perf}>
                    <span class="ml-1 text-accent-fg">●</span>
                  </Show>
                  <Show when={tab() === t.id}>
                    <span class="absolute inset-x-2 -bottom-px h-px bg-accent-hover" />
                  </Show>
                </button>
              )}
            </For>
          </div>

          <div class="flex flex-1 flex-col overflow-hidden">
            <Show when={tab() === 'params'}>
              <div class="overflow-y-auto">
                {/* Path params sit ABOVE the query params because that's
                    their order in the URL, and they only appear at all when
                    the URL actually has a `:name` in it — an empty group
                    would just be a permanent question about a feature most
                    requests don't use. */}
                <Show when={(req().pathParams?.length ?? 0) > 0}>
                  <div class="border-b border-edge">
                    <p class="px-3 pt-2 text-[10px] text-ink-faint">
                      From <code class="text-ink-muted">:name</code> placeholders in the URL path. Edit the URL to add
                      or remove one.
                    </p>
                    <KeyValueTable
                      rows={req().pathParams ?? []}
                      keyLabel="Path"
                      readOnlyKeys
                      valuePlaceholder="value"
                      onSet={(i, _field, v) => setPathParamValue(i, String(v))}
                    />
                  </div>
                </Show>
                <KeyValueTable
                  rows={req().params}
                  keyPlaceholder="param"
                  onSet={(i, k, v) => setRow('params', i, k, v)}
                  onAdd={() => addRow('params')}
                  onRemove={(i) => removeRow('params', i)}
                />
              </div>
            </Show>
            <Show when={tab() === 'headers'}>
              <div class="overflow-y-auto">
                <KeyValueTable
                  rows={req().headers}
                  keyPlaceholder="header"
                  onSet={(i, k, v) => setRow('headers', i, k, v)}
                  onAdd={() => addRow('headers')}
                  onRemove={(i) => removeRow('headers', i)}
                />
              </div>
            </Show>
            <Show when={tab() === 'body'}>
              <div class="flex-1 overflow-hidden">
                <Switch fallback={<BodyEditor requestIndex={activeIndex()} />}>
                  <Match when={req().protocol === 'graphql'}>
                    <GraphQLEditor requestIndex={activeIndex()} />
                  </Match>
                  <Match when={req().protocol === 'grpc'}>
                    <GrpcEditor requestIndex={activeIndex()} />
                  </Match>
                </Switch>
              </div>
            </Show>
            <Show when={tab() === 'auth'}>
              <div class="overflow-y-auto">
                <AuthConfigForm requestIndex={activeIndex()} />
              </div>
            </Show>
            <Show when={tab() === 'script'}>
              <div class="flex h-full flex-col overflow-hidden">
                <p class="border-b border-edge px-2 py-1.5 text-[11px] text-ink-faint">
                  Runs after templating and auth, right before Send. Read <code class="text-ink-dim">ctx.request</code>{' '}
                  (method/url/headers/body); call <code class="text-ink-dim">ctx.setHeader(name, value)</code> to
                  add or override a header.
                </p>
                <div class="flex-1 overflow-hidden">
                  <ScriptEditor requestIndex={activeIndex()} />
                </div>
              </div>
            </Show>
            <Show when={tab() === 'assert'}>
              <div class="overflow-y-auto">
                <AssertionEditor requestIndex={activeIndex()} />
              </div>
            </Show>
            <Show when={tab() === 'perf'}>
              <div class="flex-1 overflow-hidden">
                <PerfPanel requestIndex={activeIndex()} />
              </div>
            </Show>
          </div>
        </div>
      )}
    </Show>
  )
}

// This app is built to be driven from the keyboard, so the empty state
// Shown in place of Send/Connect once the trial has ended: clicking opens
// Settings → License rather than silently doing nothing, so the path to
// keep working is one click away. Amber, not red — this is a nudge to buy,
// not an error.
function ActivateToSend() {
  return (
    <button
      class="rounded border border-warn/40 bg-warn/10 px-3 py-1 text-sm font-medium text-warn hover:bg-warn/20"
      title="Your trial has ended — activate a licence to keep sending requests"
      onClick={() => setSettingsOpen(true)}
    >
      Activate to send
    </button>
  )
}

// makes ⌘K the hero rather than a footnote — it's the primary way to get
// anywhere, not a bolted-on shortcut for people who already found the tree.
function EmptyState() {
  return (
    <div class="flex h-full flex-col items-center justify-center gap-4 text-ink-faint">
      <button
        class="flex items-center gap-1.5 rounded-lg border border-edge-strong bg-field px-4 py-2 hover:bg-raised"
        onClick={() => setCommandPaletteOpen(true)}
      >
        <kbd class="rounded border border-edge-strong bg-raised px-2 py-1 font-mono text-sm text-ink-dim">⌘</kbd>
        <kbd class="rounded border border-edge-strong bg-raised px-2 py-1 font-mono text-sm text-ink-dim">K</kbd>
        <span class="ml-2 text-sm text-ink-muted">to jump anywhere</span>
      </button>
      <p class="text-xs">or ⌘N for a new request, ⌘B to browse</p>
    </div>
  )
}
