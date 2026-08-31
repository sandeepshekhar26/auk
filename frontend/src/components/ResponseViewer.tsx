import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting } from '@codemirror/language'
import { search, searchKeymap, openSearchPanel, highlightSelectionMatches } from '@codemirror/search'
import { unifiedMergeView } from '@codemirror/merge'
import { jsonHighlightStyle, monoFontFamily } from '../lib/codeTheme'
import type { Assertion, AssertionResult, KeyValue, RedirectHop, ResponseData, TestResult, TimingBreakdown } from '../types'
import { appState, setLoadError } from '../lib/store'
import { wails } from '../lib/wails'
import CopyAsMenu from './CopyAsMenu'

function assertionLabel(a: Assertion): string {
  let target: string = a.source
  if (a.source === 'body') target = a.path ? `body.${a.path}` : 'body'
  else if (a.source === 'header') target = `header[${a.name ?? ''}]`
  return a.value ? `${target} ${a.operator} ${a.value}` : `${target} ${a.operator}`
}

/**
 * Response-body size for the status header.
 *
 * Deliberately NOT lib/updater.ts's formatBytes: that one is tuned for
 * download sizes and floors at "1 KB", so a 12-byte error body would read as
 * 1 KB. Here the small end is the interesting end.
 */
function formatBodySize(bytes: number): string {
  if (!bytes || bytes < 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024) return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`
  const mb = kb / 1024
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`
}

/**
 * One labelled number in the status header. The label is what turns a bare
 * "248 ms" into something scannable next to the status pill, and tabular
 * figures stop the row jittering as timings change between sends.
 */
function Metric(props: { label: string; value: string }) {
  return (
    <div class="flex shrink-0 flex-col gap-px">
      <span class="text-[9.5px] font-semibold uppercase leading-none tracking-[0.07em] text-ink-faint">{props.label}</span>
      <span class="font-mono text-xs font-medium leading-none text-ink tabular-nums">{props.value}</span>
    </div>
  )
}

type Tab = 'body' | 'headers' | 'timing'
type BodyMode = 'pretty' | 'raw'

function decodeBody(bodyBase64: string): string {
  if (!bodyBase64) return ''
  try {
    const binary = atob(bodyBase64)
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
    return new TextDecoder('utf-8').decode(bytes)
  } catch {
    return ''
  }
}

// Image MIME types AUK renders inline as an <img> data: URI preview.
const IMAGE_MIME = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/svg+xml'])

// Case-insensitive first-match header lookup (HTTP header names are
// case-insensitive); returns '' when absent or when headers is null (a gRPC
// summary response carries no Headers at all).
function headerValue(headers: KeyValue[] | null | undefined, name: string): string {
  if (!headers) return ''
  const lower = name.toLowerCase()
  for (const h of headers) if (h.key.toLowerCase() === lower) return h.value
  return ''
}

// SaveResponseBody is a Go binding added in app_files.go. Wails regenerates the
// typed wrapper in frontend/wailsjs/go/main/App.{d.ts,js} on the next `wails
// dev`/build (and binds it at runtime by reflecting over App's exported
// methods), at which point `wails.SaveResponseBody` is statically typed. Until
// that regeneration runs we reach it through a locally-typed view of the
// bindings module so tsc and the bundler stay green. See INTEGRATION NOTES.
function saveResponseBodyBinding(requestId: string, bodyBase64: string, contentType: string): Promise<string> {
  return (
    wails as unknown as {
      SaveResponseBody(requestId: string, bodyBase64: string, contentType: string): Promise<string>
    }
  ).SaveResponseBody(requestId, bodyBase64, contentType)
}

function tryPrettyJson(raw: string): { pretty: string | null; isJson: boolean } {
  const trimmed = raw.trim()
  if (!trimmed) return { pretty: null, isJson: false }
  try {
    const parsed = JSON.parse(trimmed)
    return { pretty: JSON.stringify(parsed, null, 2), isJson: true }
  } catch {
    return { pretty: null, isJson: false }
  }
}

// Splits a hop's cumulative httptrace timestamps into the non-overlapping
// waterfall segments a request debugger shows (DNS -> connect -> TLS ->
// waiting-on-server -> downloading), the same breakdown Chrome DevTools'
// Network panel uses. ttfbMs/totalMs are measured from hop start, so
// "waiting" and "content" are derived by subtraction — clamped to 0
// because a reused connection (0 DNS/connect/TLS) can otherwise make the
// subtraction go slightly negative from clock-resolution jitter.
function timingPhases(t: TimingBreakdown) {
  const waiting = Math.max(0, t.ttfbMs - t.dnsMs - t.connectMs - t.tlsMs)
  const content = Math.max(0, t.totalMs - t.ttfbMs)
  const raw = [
    { label: 'DNS lookup', ms: t.dnsMs, colorClass: 'bg-info' },
    { label: 'Connecting', ms: t.connectMs, colorClass: 'bg-keyword' },
    { label: 'TLS handshake', ms: t.tlsMs, colorClass: 'bg-warn' },
    { label: 'Waiting (TTFB)', ms: waiting, colorClass: 'bg-accent' },
    { label: 'Content download', ms: content, colorClass: 'bg-ink-faint' },
  ]
  const total = Math.max(1, t.totalMs)
  return raw.map((p) => ({ ...p, pct: (p.ms / total) * 100 }))
}

interface RedirectWarning {
  // The warning applies to the transition FROM redirectChain[afterIndex] TO
  // redirectChain[afterIndex + 1] — rendered attached to that hop's row.
  afterIndex: number
  kind: 'downgrade' | 'cross-origin'
  message: string
}

// Flags two things a redirect chain can do that a plain status-code list
// won't surface on its own: silently drop from HTTPS to plaintext HTTP
// (the classic SSL-stripping shape — a real credential/token exposure risk,
// not just informational), or hop to a different origin entirely. At most
// one warning per transition — a scheme downgrade is by definition also an
// origin change, and restating both would just be noise for the same fact.
function computeRedirectWarnings(chain: RedirectHop[]): RedirectWarning[] {
  const warnings: RedirectWarning[] = []
  for (let i = 0; i < chain.length - 1; i++) {
    let a: URL
    let b: URL
    try {
      a = new URL(chain[i].url)
      b = new URL(chain[i + 1].url)
    } catch {
      continue
    }
    if (a.protocol === 'https:' && b.protocol === 'http:') {
      warnings.push({ afterIndex: i, kind: 'downgrade', message: `Redirects from HTTPS to plaintext HTTP: ${b.origin}` })
    } else if (a.origin !== b.origin) {
      warnings.push({ afterIndex: i, kind: 'cross-origin', message: `Redirects to a different origin: ${b.origin}` })
    }
  }
  return warnings
}

export default function ResponseViewer(props: { response: ResponseData | null; loading: boolean }) {
  const [tab, setTab] = createSignal<Tab>('body')
  const [bodyMode, setBodyMode] = createSignal<BodyMode>('pretty')
  const [diffMode, setDiffMode] = createSignal(false)
  const [hasPrior, setHasPrior] = createSignal(false)
  const [filterPath, setFilterPath] = createSignal('')
  const [filterState, setFilterState] = createSignal<{ result: string; error: string | null }>({ result: '', error: null })
  const filterActive = createMemo(() => filterPath().trim().length > 0)
  // A previewable body (image/HTML) auto-shows its rendered form; the
  // Preview/Raw toggle flips back to the source editor. Reset to shown on every
  // new response by the on(() => props.response) effect below.
  const [showPreview, setShowPreview] = createSignal(true)

  const [editorHost, setEditorHost] = createSignal<HTMLDivElement>()
  let view: EditorView | undefined
  // The previous response body for the SAME request, captured the moment a new
  // response replaces it — powers "diff vs previous" without a backend archive
  // (durable cross-session history diffing is a noted follow-up).
  let priorBody = ''

  createEffect(
    on(
      () => props.response,
      (resp, prevResp) => {
        if (resp && prevResp && resp.requestId === prevResp.requestId) {
          priorBody = decodeBody(prevResp.bodyBase64)
        } else {
          priorBody = ''
          setDiffMode(false)
          setFilterPath('')
        }
        setHasPrior(priorBody.length > 0)
        // Default a newly-arrived previewable body (image/HTML) to its rendered
        // form; harmless for non-previewable bodies (renderedPreview() stays
        // false when previewKind() is 'none').
        setShowPreview(true)
      },
    ),
  )

  const rawBody = createMemo(() => decodeBody(props.response?.bodyBase64 ?? ''))
  const jsonInfo = createMemo(() => tryPrettyJson(rawBody()))

  // Content-Type-driven rich preview. mimeType strips any ";charset=…"
  // parameter; previewKind decides whether the body renders as an inline image,
  // a sandboxed HTML iframe, or falls through to the existing text views.
  const contentType = createMemo(() => headerValue(props.response?.headers, 'content-type'))
  const mimeType = createMemo(() => contentType().split(';')[0]?.trim().toLowerCase() ?? '')
  const previewKind = createMemo<'image' | 'html' | 'none'>(() => {
    const m = mimeType()
    if (IMAGE_MIME.has(m)) return 'image'
    if (m === 'text/html') return 'html'
    return 'none'
  })
  const renderedPreview = createMemo(() => previewKind() !== 'none' && showPreview())

  // Debounced (150ms) so a fast typist filtering a large body doesn't fire
  // one JSONPathFilter IPC call per keystroke; `cancelled` guards against a
  // stale in-flight call overwriting a newer one if a response ever arrives
  // out of order. jsonpath.Get is reused as-is on the Go side (see app.go's
  // JSONPathFilter) rather than reimplemented here, so path semantics can't
  // drift from what json.get()/assertions already rely on.
  createEffect(() => {
    const path = filterPath().trim()
    const body = rawBody()
    if (!path) {
      setFilterState({ result: '', error: null })
      return
    }
    let cancelled = false
    const timer = setTimeout(() => {
      wails
        .JSONPathFilter(body, path)
        .then((result) => {
          if (!cancelled) setFilterState({ result, error: null })
        })
        .catch((err) => {
          if (!cancelled) setFilterState({ result: '', error: err instanceof Error ? err.message : String(err) })
        })
    }, 150)
    onCleanup(() => {
      cancelled = true
      clearTimeout(timer)
    })
  })

  // A filtered value is always shown pretty-printed if it's itself an
  // object/array (tryPrettyJson already implements exactly that check), and
  // as bodyIsJson's ValueToString rendered it plainly for scalars — reusing
  // tryPrettyJson here instead of duplicating its JSON.parse/stringify logic.
  const filteredInfo = createMemo(() => tryPrettyJson(filterState().result))
  const filteredDisplayText = createMemo(() => {
    const { pretty, isJson } = filteredInfo()
    if (isJson && pretty !== null) return pretty
    return filterState().result
  })

  const displayText = createMemo(() => {
    if (filterActive()) {
      return filterState().error ? '' : filteredDisplayText()
    }
    const { pretty, isJson } = jsonInfo()
    if (bodyMode() === 'pretty' && isJson && pretty !== null) return pretty
    return rawBody()
  })

  const activeRequest = createMemo(() => appState.requests.find((r) => r.id === appState.activeTabId))

  const redirectWarnings = createMemo(() => computeRedirectWarnings(props.response?.redirectChain ?? []))
  const hasDowngradeWarning = createMemo(() => redirectWarnings().some((w) => w.kind === 'downgrade'))

  createEffect(() => {
    const host = editorHost()
    const text = displayText()
    const isJsonView = filterActive() ? filteredInfo().isJson : jsonInfo().isJson
    const showDiff = diffMode() && hasPrior() && !filterActive()
    if (!host) return

    if (view) {
      view.destroy()
      view = undefined
    }

    view = new EditorView({
      state: EditorState.create({
        doc: text,
        extensions: [
          lineNumbers(),
          highlightSelectionMatches(),
          search({ top: true }),
          keymap.of([...searchKeymap, ...defaultKeymap]),
          EditorView.editable.of(false),
          EditorState.readOnly.of(true),
          syntaxHighlighting(jsonHighlightStyle),
          ...(isJsonView ? [json()] : []),
          // In diff mode, overlay a unified diff against the previous response
          // body for this request (green = added, red = removed).
          ...(showDiff ? [unifiedMergeView({ original: priorBody, mergeControls: false })] : []),
          EditorView.theme({
            '&': { backgroundColor: 'transparent', height: '100%', fontSize: '12px' },
            '.cm-scroller': { fontFamily: monoFontFamily, overflow: 'auto' },
            '.cm-gutters': { backgroundColor: 'transparent', color: 'rgb(var(--color-ink-faint))', border: 'none' },
            '.cm-content': { caretColor: 'transparent' },
            '&.cm-focused': { outline: 'none' },
          }),
        ],
      }),
      parent: host,
    })
  })

  function openSearch() {
    if (view) {
      view.focus()
      openSearchPanel(view)
    }
  }

  // Save the response body to a file the user picks in a native dialog. The
  // decode + write happen in Go (app_files.go), but the BYTES come from the
  // response this pane is currently showing rather than the backend's
  // last-response cache: a folder run or an MCP-driven call can overwrite that
  // cache for the same request id without changing what's on screen, and a
  // save must never write something the user isn't looking at. A cancelled
  // dialog returns '' (no error); only a genuine failure surfaces via loadError.
  async function saveResponseBody(requestId: string) {
    const res = props.response
    if (!res) return
    try {
      await saveResponseBodyBinding(requestId, res.bodyBase64 ?? '', headerValue(res.headers ?? [], 'content-type'))
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    }
  }

  onMount(() => window.addEventListener('apitool:search-body', openSearch))
  onCleanup(() => {
    window.removeEventListener('apitool:search-body', openSearch)
    view?.destroy()
  })

  return (
    <div class="flex h-full flex-col border-l border-edge">
      <Show when={!props.loading} fallback={<div class="p-3 text-sm text-ink-muted">Sending…</div>}>
        <Show when={props.response} fallback={<div class="p-3 text-sm text-ink-faint">Response will appear here.</div>}>
          {(res) => {
            // gRPC's summary response (unary AND server-streaming) never
            // sets Headers at all — Go's nil slice zero-value marshals to
            // JSON null, not [] — so this can't be assumed non-null the way
            // a real HTTP/GraphQL response's headers always are.
            const headers = () => res().headers ?? []
            // Captured console.log output from the post-response script.
            // Read defensively off the response rather than through the
            // ResponseData type: the Go field that carries it is pending
            // (see INTEGRATION NOTES in docs/08-scripting.md), and this
            // renders the moment it lands without another change here.
            const scriptLogs = (): string[] => (res() as { scriptLogs?: string[] | null }).scriptLogs ?? []
            // One place decides the colour of everything in the status header,
            // so the dot, the number and the reason phrase can never disagree.
            // Status 0 is a transport failure ("0 Error") — it used to fall
            // into the `< 300` branch and render in success green.
            const tone = () => {
              const st = res().status
              if (st === 0 || st >= 400) return { text: 'text-danger', bg: 'bg-danger/10', dot: 'bg-danger' }
              if (st >= 300) return { text: 'text-warn', bg: 'bg-warn/10', dot: 'bg-warn' }
              return { text: 'text-accent-fg', bg: 'bg-accent/10', dot: 'bg-accent' }
            }
            return (
            <div class="flex h-full flex-col">
              {/* Three separate facts, presented as three. This was one
                  undifferentiated run-on ("200 200 OK 266ms 509B") in which the
                  single most important thing on the screen — did it work — had
                  no more weight than the byte count beside it. */}
              <div class="flex items-center gap-4 border-b border-edge px-3 py-2 text-xs">
                <div class={`flex shrink-0 items-center gap-2 rounded-lg py-1 pl-2.5 pr-3 ${tone().bg}`}>
                  <span class={`h-1.5 w-1.5 shrink-0 rounded-full ${tone().dot}`} />
                  <span class={`font-mono text-sm font-semibold leading-none tracking-tight ${tone().text}`}>{res().status}</span>
                  <span class={`whitespace-nowrap text-xs font-medium leading-none ${tone().text}`}>{res().statusText}</span>
                </div>
                <Metric label="Time" value={`${res().timingMs} ms`} />
                <Metric label="Size" value={formatBodySize(res().bodySize)} />
                {/* Same control as the one next to Send — see CopyAsMenu.
                    It resolves the request from the backend rather than from
                    this response, so the two always agree and neither needs a
                    prior send. */}
                <div class="ml-auto flex items-center gap-2">
                  <Show when={res().bodySize > 0}>
                    <button
                      class="rounded px-2 py-0.5 text-[11px] text-ink-muted hover:bg-raised hover:text-ink-dim"
                      onClick={() => void saveResponseBody(res().requestId)}
                      title="Save response body to a file"
                    >
                      Save
                    </button>
                  </Show>
                  <CopyAsMenu requestId={activeRequest()?.id} protocol={activeRequest()?.protocol} />
                </div>
              </div>

              {/* Surfaces resp.Error (a dial failure, a policy rejection, a
                  not-yet-supported gRPC method, ...) — without this, a
                  failed send showed only the bare "0 Error" status badge
                  and "Empty response body", with the actual reason
                  nowhere in the UI. Found while live-testing gRPC
                  client-streaming rejection (2026-07-05); not specific to
                  gRPC, so fixed here for every protocol at once. */}
              <Show when={res().error}>
                <div class="border-b border-danger-edge bg-danger-bg/40 px-3 py-2 text-xs text-danger">{res().error}</div>
              </Show>

              {/* A post-response script that could not RUN — syntax error,
                  timeout, or a throw outside a test() — is a FAILED run, not
                  a silent pass (ResponseData.Passed() is false for it). It
                  gets the same prominence as a transport error rather than
                  being folded in among the individual test rows, because no
                  test result below can be trusted to be complete. */}
              <Show when={res().scriptError}>
                <div class="border-b border-danger-edge bg-danger-bg/40 px-3 py-2 text-xs text-danger">
                  <span class="font-semibold">Script error</span> — {res().scriptError}
                </div>
              </Show>

              <Show when={(res().assertionResults?.length ?? 0) > 0}>
                {(() => {
                  const results = (): AssertionResult[] => res().assertionResults ?? []
                  const allPassed = () => results().every((r) => r.passed)
                  const passCount = () => results().filter((r) => r.passed).length
                  return (
                    <details class="border-b border-edge" open>
                      <summary class="flex cursor-pointer list-none items-center gap-2 px-2 py-1.5 text-xs">
                        <span
                          class="rounded px-1.5 py-0.5 text-[10px] font-semibold"
                          classList={{ 'bg-accent text-accent-contrast': allPassed(), 'bg-danger text-accent-contrast': !allPassed() }}
                        >
                          {allPassed() ? 'ASSERTIONS PASSED' : 'ASSERTIONS FAILED'}
                        </span>
                        <span class="text-ink-muted">
                          {passCount()}/{results().length} passed
                        </span>
                      </summary>
                      <div class="flex flex-col gap-0.5 px-2 pb-2">
                        <For each={results()}>
                          {(r) => (
                            <div class="flex items-center gap-2 font-mono text-[11px]">
                              <span classList={{ 'text-accent-fg': r.passed, 'text-danger': !r.passed }}>{r.passed ? '✓' : '✗'}</span>
                              <span class="text-ink-dim">{assertionLabel(r.assertion)}</span>
                              <span class="truncate text-ink-faint">{r.error ? `(${r.error})` : `→ ${r.actual}`}</span>
                            </div>
                          )}
                        </For>
                      </div>
                    </details>
                  )
                })()}
              </Show>

              {/* test() results from the post-response script, alongside
                  the declarative assertions above: two ways of writing the
                  same kind of check, one shared verdict. */}
              <Show when={(res().testResults?.length ?? 0) > 0}>
                {(() => {
                  const results = (): TestResult[] => res().testResults ?? []
                  const allPassed = () => results().every((r) => r.passed)
                  const passCount = () => results().filter((r) => r.passed).length
                  return (
                    <details class="border-b border-edge" open>
                      <summary class="flex cursor-pointer list-none items-center gap-2 px-2 py-1.5 text-xs">
                        <span
                          class="rounded px-1.5 py-0.5 text-[10px] font-semibold"
                          classList={{ 'bg-accent text-accent-contrast': allPassed(), 'bg-danger text-accent-contrast': !allPassed() }}
                        >
                          {allPassed() ? 'TESTS PASSED' : 'TESTS FAILED'}
                        </span>
                        <span class="text-ink-muted">
                          {passCount()}/{results().length} passed
                        </span>
                      </summary>
                      <div class="flex flex-col gap-0.5 px-2 pb-2">
                        <For each={results()}>
                          {(r) => (
                            <div class="flex items-start gap-2 font-mono text-[11px]">
                              <span classList={{ 'text-accent-fg': r.passed, 'text-danger': !r.passed }}>{r.passed ? '✓' : '✗'}</span>
                              <span class="text-ink-dim">{r.name || '(unnamed test)'}</span>
                              {/* The failure message is the whole point of a
                                  test — never truncated, always wrapped. */}
                              <Show when={!r.passed && r.error}>
                                <span class="min-w-0 flex-1 whitespace-pre-wrap break-words text-danger">{r.error}</span>
                              </Show>
                            </div>
                          )}
                        </For>
                      </div>
                    </details>
                  )
                })()}
              </Show>

              <Show when={scriptLogs().length > 0}>
                <details class="border-b border-edge">
                  <summary class="flex cursor-pointer list-none items-center gap-2 px-2 py-1.5 text-xs">
                    <span class="rounded bg-raised px-1.5 py-0.5 text-[10px] font-semibold text-ink-dim">CONSOLE</span>
                    <span class="text-ink-muted">{scriptLogs().length} lines</span>
                  </summary>
                  <div class="flex flex-col gap-0.5 px-2 pb-2">
                    <For each={scriptLogs()}>
                      {(line) => <div class="whitespace-pre-wrap break-words font-mono text-[11px] text-ink-dim">{line}</div>}
                    </For>
                  </div>
                </details>
              </Show>

              <div class="flex items-center gap-1 border-b border-edge px-2 py-1">
                <button
                  class="rounded px-2 py-1 text-xs font-medium"
                  classList={{
                    'bg-raised text-ink': tab() === 'body',
                    'text-ink-muted hover:text-ink-dim': tab() !== 'body',
                  }}
                  onClick={() => setTab('body')}
                >
                  Body
                </button>
                <button
                  class="rounded px-2 py-1 text-xs font-medium"
                  classList={{
                    'bg-raised text-ink': tab() === 'headers',
                    'text-ink-muted hover:text-ink-dim': tab() !== 'headers',
                  }}
                  onClick={() => setTab('headers')}
                >
                  Headers
                  <span class="ml-1 text-ink-faint">{headers().length}</span>
                </button>
                <Show when={res().timing}>
                  <button
                    class="rounded px-2 py-1 text-xs font-medium"
                    classList={{
                      'bg-raised text-ink': tab() === 'timing',
                      'text-ink-muted hover:text-ink-dim': tab() !== 'timing',
                    }}
                    onClick={() => setTab('timing')}
                  >
                    Timing
                    <Show when={(res().redirectChain?.length ?? 0) > 1}>
                      <span class="ml-1 text-ink-faint">{res().redirectChain!.length} hops</span>
                    </Show>
                    <Show when={redirectWarnings().length > 0}>
                      <span
                        class="ml-1"
                        classList={{ 'text-danger': hasDowngradeWarning(), 'text-warn': !hasDowngradeWarning() }}
                        title={redirectWarnings()
                          .map((w) => w.message)
                          .join('; ')}
                      >
                        ⚠
                      </span>
                    </Show>
                  </button>
                </Show>

                <Show when={tab() === 'body'}>
                  <div class="ml-auto flex items-center gap-1">
                    <Show when={previewKind() !== 'none'}>
                      <div class="flex items-center gap-1 rounded bg-field p-0.5">
                        <button
                          class="rounded px-2 py-0.5 text-[11px]"
                          classList={{
                            'bg-elevated text-ink': showPreview(),
                            'text-ink-muted hover:text-ink-dim': !showPreview(),
                          }}
                          onClick={() => setShowPreview(true)}
                        >
                          Preview
                        </button>
                        <button
                          class="rounded px-2 py-0.5 text-[11px]"
                          classList={{
                            'bg-elevated text-ink': !showPreview(),
                            'text-ink-muted hover:text-ink-dim': showPreview(),
                          }}
                          onClick={() => setShowPreview(false)}
                        >
                          Raw
                        </button>
                      </div>
                    </Show>
                    <Show when={!renderedPreview()}>
                      <button
                        class="rounded px-2 py-0.5 text-[11px] text-ink-muted hover:bg-raised hover:text-ink-dim"
                        onClick={openSearch}
                        title="Search in body (⌘F)"
                      >
                        Search
                      </button>
                    </Show>
                    <Show when={hasPrior() && !filterActive() && previewKind() === 'none'}>
                      <button
                        class="rounded px-2 py-0.5 text-[11px]"
                        classList={{
                          'bg-elevated text-ink': diffMode(),
                          'text-ink-muted hover:bg-raised hover:text-ink-dim': !diffMode(),
                        }}
                        onClick={() => setDiffMode((v) => !v)}
                        title="Diff against the previous response for this request"
                      >
                        Diff
                      </button>
                    </Show>
                    <Show when={jsonInfo().isJson && !diffMode() && !filterActive()}>
                      <div class="flex items-center gap-1 rounded bg-field p-0.5">
                        <button
                          class="rounded px-2 py-0.5 text-[11px]"
                          classList={{
                            'bg-elevated text-ink': bodyMode() === 'pretty',
                            'text-ink-muted hover:text-ink-dim': bodyMode() !== 'pretty',
                          }}
                          onClick={() => setBodyMode('pretty')}
                        >
                          Pretty
                        </button>
                        <button
                          class="rounded px-2 py-0.5 text-[11px]"
                          classList={{
                            'bg-elevated text-ink': bodyMode() === 'raw',
                            'text-ink-muted hover:text-ink-dim': bodyMode() !== 'raw',
                          }}
                          onClick={() => setBodyMode('raw')}
                        >
                          Raw
                        </button>
                      </div>
                    </Show>
                  </div>
                </Show>
              </div>

              <Show when={tab() === 'body' && jsonInfo().isJson}>
                <div class="flex items-center gap-2 border-b border-edge px-2 py-1">
                  <span class="shrink-0 font-mono text-[10px] uppercase tracking-wide text-ink-faint">JSONPath</span>
                  <input
                    class="min-w-0 flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                    placeholder="Filter, e.g. data.items[0].name"
                    value={filterPath()}
                    onInput={(e) => setFilterPath(e.currentTarget.value)}
                  />
                  <Show when={filterActive()}>
                    <button
                      class="shrink-0 rounded px-1.5 py-0.5 text-xs text-ink-faint hover:bg-raised hover:text-ink-dim"
                      onClick={() => setFilterPath('')}
                      title="Clear filter"
                    >
                      ×
                    </button>
                  </Show>
                </div>
              </Show>

              <div class="flex-1 overflow-hidden" classList={{ hidden: tab() !== 'body' }}>
                {/* Rich preview auto-selected by Content-Type (inline image or
                    rendered HTML). The Preview/Raw toggle above flips back to
                    the source editor, which stays mounted (just hidden) so its
                    search state survives the toggle. */}
                <Show when={renderedPreview()}>
                  <Show when={previewKind() === 'image'}>
                    <div class="flex h-full items-center justify-center overflow-auto p-4">
                      <img
                        src={`data:${mimeType()};base64,${res().bodyBase64}`}
                        alt="Response preview"
                        class="max-h-full max-w-full object-contain"
                      />
                    </div>
                  </Show>
                  <Show when={previewKind() === 'html'}>
                    {/* sandbox="" (an empty token list) neutralizes ALL script
                        execution, form submission, popups, and same-origin
                        access: a response body must never run as live JS in the
                        app's origin. srcdoc renders the returned markup inertly. */}
                    <iframe
                      class="h-full w-full border-0 bg-white"
                      sandbox=""
                      srcdoc={rawBody()}
                      title="Rendered HTML response"
                    />
                  </Show>
                </Show>

                <Show when={!renderedPreview() && filterActive() && filterState().error}>
                  <div class="border-b border-edge bg-danger-bg/40 px-2 py-1 font-mono text-[11px] text-danger">
                    {filterState().error}
                  </div>
                </Show>
                <div
                  ref={setEditorHost}
                  class="h-full overflow-auto"
                  classList={{ hidden: renderedPreview() || displayText().length === 0 }}
                />
                <Show when={!renderedPreview() && displayText().length === 0 && !(filterActive() && filterState().error)}>
                  <div class="p-3 text-sm text-ink-faint">
                    {filterActive() ? 'No value at this path yet.' : 'Empty response body.'}
                  </div>
                </Show>
              </div>

              <Show when={tab() === 'headers'}>
                <div class="flex-1 overflow-auto p-2">
                  <Show
                    when={headers().length > 0}
                    fallback={<div class="p-1 text-sm text-ink-faint">No headers.</div>}
                  >
                    <table class="w-full border-collapse text-xs">
                      <tbody>
                        {headers().map((h) => (
                          <tr class="border-b border-edge-soft">
                            <td class="w-1/3 whitespace-nowrap py-1.5 pr-3 align-top font-mono text-ink-muted">{h.key}</td>
                            <td class="break-all py-1.5 font-mono text-ink-dim">{h.value}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </Show>
                </div>
              </Show>

              <Show when={tab() === 'timing' && res().timing}>
                {(() => {
                  const phases = createMemo(() => timingPhases(res().timing!))
                  return (
                    <div class="flex-1 overflow-auto p-3">
                      <div class="flex h-3 overflow-hidden rounded-full bg-field">
                        <For each={phases()}>
                          {(p) => (
                            <Show when={p.ms > 0}>
                              <div
                                class={p.colorClass}
                                style={{ width: `${p.pct}%` }}
                                title={`${p.label}: ${p.ms}ms`}
                              />
                            </Show>
                          )}
                        </For>
                      </div>
                      <table class="mt-3 w-full border-collapse text-xs">
                        <tbody>
                          <For each={phases()}>
                            {(p) => (
                              <tr class="border-b border-edge-soft">
                                <td class="w-1/2 py-1.5 pr-3 align-top">
                                  <span class={`mr-2 inline-block h-2 w-2 rounded-full ${p.colorClass}`} />
                                  <span class="text-ink-dim">{p.label}</span>
                                </td>
                                <td class="py-1.5 text-right font-mono text-ink-muted">{p.ms}ms</td>
                              </tr>
                            )}
                          </For>
                          <tr>
                            <td class="pt-2 font-semibold text-ink">
                              {(res().redirectChain?.length ?? 0) > 1 ? 'Final hop total' : 'Total'}
                            </td>
                            <td class="pt-2 text-right font-mono font-semibold text-ink">{res().timing!.totalMs}ms</td>
                          </tr>
                        </tbody>
                      </table>

                      <Show when={(res().redirectChain?.length ?? 0) > 1}>
                        <div class="mt-4">
                          <h3 class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
                            Redirect chain
                          </h3>
                          <div class="mt-1 flex flex-col gap-1">
                            <For each={res().redirectChain}>
                              {(hop, i) => (
                                <>
                                  <div class="flex items-center gap-2 font-mono text-[11px]">
                                    <span class="text-ink-faint">{i() + 1}.</span>
                                    <span class="text-ink-muted">{hop.method}</span>
                                    <span class="flex-1 truncate text-ink-dim">{hop.url}</span>
                                    <span
                                      classList={{
                                        'text-accent-fg': hop.status < 300,
                                        'text-warn': hop.status >= 300 && hop.status < 400,
                                        'text-danger': hop.status >= 400,
                                      }}
                                    >
                                      {hop.status}
                                    </span>
                                    <span class="text-ink-faint">{hop.timingMs}ms</span>
                                  </div>
                                  <For each={redirectWarnings().filter((w) => w.afterIndex === i())}>
                                    {(w) => (
                                      <div
                                        class="ml-4 flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px]"
                                        classList={{
                                          'border-danger-edge bg-danger-bg/40 text-danger': w.kind === 'downgrade',
                                          'border-warn-edge bg-warn/10 text-warn': w.kind === 'cross-origin',
                                        }}
                                      >
                                        <span>⚠</span>
                                        <span>{w.message}</span>
                                      </div>
                                    )}
                                  </For>
                                </>
                              )}
                            </For>
                          </div>
                        </div>
                      </Show>
                    </div>
                  )
                })()}
              </Show>
            </div>
          )}}
        </Show>
      </Show>
    </div>
  )
}
