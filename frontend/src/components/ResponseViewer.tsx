import { For, Show, createEffect, createMemo, createSignal, on, onCleanup } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting } from '@codemirror/language'
import { search, searchKeymap, openSearchPanel, highlightSelectionMatches } from '@codemirror/search'
import { unifiedMergeView } from '@codemirror/merge'
import { jsonHighlightStyle, monoFontFamily } from '../lib/codeTheme'
import type { Assertion, AssertionResult, ResponseData, TimingBreakdown } from '../types'
import { appState } from '../lib/store'
import { wails } from '../lib/wails'

function assertionLabel(a: Assertion): string {
  let target: string = a.source
  if (a.source === 'body') target = a.path ? `body.${a.path}` : 'body'
  else if (a.source === 'header') target = `header[${a.name ?? ''}]`
  return a.value ? `${target} ${a.operator} ${a.value}` : `${target} ${a.operator}`
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

function buildCurl(req: { method: string; url: string; headers: { key: string; value: string; enabled: boolean }[] } | undefined): string {
  if (!req) return ''
  const parts = ['curl', '-X', req.method]
  for (const h of req.headers) {
    if (!h.enabled || !h.key) continue
    parts.push('-H', shellQuote(`${h.key}: ${h.value}`))
  }
  parts.push(shellQuote(req.url))
  return parts.join(' ')
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

export default function ResponseViewer(props: { response: ResponseData | null; loading: boolean }) {
  const [tab, setTab] = createSignal<Tab>('body')
  const [bodyMode, setBodyMode] = createSignal<BodyMode>('pretty')
  const [copied, setCopied] = createSignal(false)
  const [diffMode, setDiffMode] = createSignal(false)
  const [hasPrior, setHasPrior] = createSignal(false)
  const [filterPath, setFilterPath] = createSignal('')
  const [filterState, setFilterState] = createSignal<{ result: string; error: string | null }>({ result: '', error: null })
  const filterActive = createMemo(() => filterPath().trim().length > 0)

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
      },
    ),
  )

  const rawBody = createMemo(() => decodeBody(props.response?.bodyBase64 ?? ''))
  const jsonInfo = createMemo(() => tryPrettyJson(rawBody()))

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

  onCleanup(() => {
    view?.destroy()
  })

  async function copyAsCurl() {
    const cmd = buildCurl(activeRequest())
    if (!cmd) return
    try {
      await navigator.clipboard.writeText(cmd)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // clipboard access denied or unavailable; nothing else we can do here
    }
  }

  return (
    <div class="flex h-full flex-col border-l border-edge">
      <Show when={!props.loading} fallback={<div class="p-3 text-sm text-ink-muted">Sending…</div>}>
        <Show when={props.response} fallback={<div class="p-3 text-sm text-ink-faint">Response will appear here.</div>}>
          {(res) => (
            <div class="flex h-full flex-col">
              <div class="flex items-center gap-3 border-b border-edge p-2 text-xs">
                <span
                  class="font-mono font-semibold"
                  classList={{
                    'text-accent-fg': res().status < 300,
                    'text-warn': res().status >= 300 && res().status < 400,
                    'text-danger': res().status >= 400,
                  }}
                >
                  {res().status} {res().statusText}
                </span>
                <span class="text-ink-muted">{res().timingMs}ms</span>
                <span class="text-ink-muted">{res().bodySize}B</span>
                <button
                  class="ml-auto rounded bg-field px-2 py-1 text-[11px] text-ink-dim hover:bg-raised disabled:cursor-not-allowed disabled:opacity-40"
                  disabled={!activeRequest()}
                  onClick={copyAsCurl}
                  title="Copy as cURL"
                >
                  {copied() ? 'Copied' : 'Copy as cURL'}
                </button>
              </div>

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
                  <span class="ml-1 text-ink-faint">{res().headers.length}</span>
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
                  </button>
                </Show>

                <Show when={tab() === 'body'}>
                  <div class="ml-auto flex items-center gap-1">
                    <button
                      class="rounded px-2 py-0.5 text-[11px] text-ink-muted hover:bg-raised hover:text-ink-dim"
                      onClick={openSearch}
                      title="Search in body (⌘F)"
                    >
                      Search
                    </button>
                    <Show when={hasPrior() && !filterActive()}>
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
                <Show when={filterActive() && filterState().error}>
                  <div class="border-b border-edge bg-danger-bg/40 px-2 py-1 font-mono text-[11px] text-danger">
                    {filterState().error}
                  </div>
                </Show>
                <div ref={setEditorHost} class="h-full overflow-auto" classList={{ hidden: displayText().length === 0 }} />
                <Show when={displayText().length === 0 && !(filterActive() && filterState().error)}>
                  <div class="p-3 text-sm text-ink-faint">
                    {filterActive() ? 'No value at this path yet.' : 'Empty response body.'}
                  </div>
                </Show>
              </div>

              <Show when={tab() === 'headers'}>
                <div class="flex-1 overflow-auto p-2">
                  <Show
                    when={res().headers.length > 0}
                    fallback={<div class="p-1 text-sm text-ink-faint">No headers.</div>}
                  >
                    <table class="w-full border-collapse text-xs">
                      <tbody>
                        {res().headers.map((h) => (
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
          )}
        </Show>
      </Show>
    </div>
  )
}
