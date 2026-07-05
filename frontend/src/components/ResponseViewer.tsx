import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting } from '@codemirror/language'
import { search, searchKeymap, openSearchPanel, highlightSelectionMatches } from '@codemirror/search'
import { unifiedMergeView } from '@codemirror/merge'
import { jsonHighlightStyle, monoFontFamily } from '../lib/codeTheme'
import type { Assertion, AssertionResult, AuthConfig, RedirectHop, RequestBody, RequestDef, ResponseData, TimingBreakdown } from '../types'
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

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

// JSON's escaping (\", \\, \n, \r, \t, \uXXXX) is a subset of Python/JS/Go's
// own double-quoted string literal escaping, so JSON.stringify's output is
// valid source syntax in all three languages for the realistic content here
// (header/URL/body text) — one implementation, not three near-duplicates.
function dqStr(s: string): string {
  return JSON.stringify(s)
}

// btoa() throws on non-Latin1 input; Go's base64.StdEncoding (what the real
// Basic-auth header uses) encodes raw UTF-8 bytes, so credentials with
// non-ASCII characters need the same byte-level encoding here, not a naive
// btoa(str) call.
function base64FromUtf8(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let binary = ''
  bytes.forEach((b) => {
    binary += String.fromCharCode(b)
  })
  return btoa(binary)
}

function authKindLabel(kind: AuthConfig['kind']): string {
  switch (kind) {
    case 'jwt':
      return 'JWT'
    case 'oauth2':
      return 'OAuth2'
    case 'awsSigV4':
      return 'AWS SigV4'
    default:
      return kind
  }
}

// Mirrors internal/protocols/http/http.go's buildURL merge (params layered
// onto any query string already in the raw URL), using encodeURIComponent
// rather than Go's url.QueryEscape (which encodes space as '+' and sorts
// keys) — a cross-language snippet should look the way a Python/JS/Go
// reader expects their OWN language's query encoding to look, not replicate
// Go's exact byte-for-byte wire encoding.
function mergeQueryParams(url: string, params: { key: string; value: string; enabled: boolean }[]): string {
  const enabled = params.filter((p) => p.enabled && p.key)
  if (enabled.length === 0) return url
  const qs = enabled.map((p) => `${encodeURIComponent(p.key)}=${encodeURIComponent(p.value)}`).join('&')
  return url + (url.includes('?') ? '&' : '?') + qs
}

// internal/protocols/http's Execute sends RequestBody.Text completely
// verbatim as the wire body, regardless of Kind — there's no per-kind
// server-side encoding or auto-Content-Type in that path (BodyEditor's
// 'form' kind now keeps Text synced to an encoded string itself; 'binary'
// has no editor yet, so there's nothing meaningful to reproduce for it).
// The one real exception is the SEPARATE graphql PROTOCOL (not Body.Kind
// under http) — internal/protocols/graphql/graphql.go always builds a
// {query,variables} JSON envelope and always sets Content-Type: application/json,
// regardless of what body.kind happens to say.
function resolveBody(protocol: RequestDef['protocol'], body: RequestBody | null): { text: string | null; isGraphqlEnvelope: boolean } {
  if (protocol === 'graphql') {
    let variables: unknown
    const raw = body?.graphqlVariables?.trim()
    if (raw) {
      try {
        variables = JSON.parse(raw)
      } catch {
        variables = undefined
      }
    }
    const envelope: Record<string, unknown> = { query: body?.text ?? '' }
    if (variables !== undefined) envelope.variables = variables
    return { text: JSON.stringify(envelope), isGraphqlEnvelope: true }
  }
  if (!body || body.kind === 'none' || body.kind === 'binary' || !body.text) return { text: null, isGraphqlEnvelope: false }
  return { text: body.text, isGraphqlEnvelope: false }
}

interface SnippetHeader {
  key: string
  value: string
}

interface ResolvedSnippetRequest {
  method: string
  url: string
  headers: SnippetHeader[]
  bodyText: string | null
  authNote: string | null
}

// Shared resolution step for every "Copy as" format — computes exactly what
// AUK would actually put on the wire for this request (headers including
// the ones simple auth kinds derive, params merged into the URL, body per
// resolveBody above) ONCE, so cURL/Python/JS/Go can't drift from each other
// or from reality. Values are used exactly as stored — any ${...} template
// expressions are left as literal text, same as the existing cURL export
// already did, since resolving them would need a live templating call
// scoped to whichever environment is active.
function resolveSnippetRequest(req: RequestDef): ResolvedSnippetRequest {
  const headers: SnippetHeader[] = []
  const params = [...req.params]
  let authNote: string | null = null

  const { text: bodyText, isGraphqlEnvelope } = resolveBody(req.protocol, req.body)
  if (isGraphqlEnvelope) headers.push({ key: 'Content-Type', value: 'application/json' })

  for (const h of req.headers) if (h.enabled && h.key) headers.push({ key: h.key, value: h.value })

  const auth = req.authRef
  if (auth) {
    switch (auth.kind) {
      case 'basic':
        if (auth.basic) headers.push({ key: 'Authorization', value: `Basic ${base64FromUtf8(`${auth.basic.username}:${auth.basic.password}`)}` })
        break
      case 'bearer':
        if (auth.bearer?.token) headers.push({ key: 'Authorization', value: `Bearer ${auth.bearer.token}` })
        break
      case 'apikey':
        if (auth.apikey?.key) {
          if (auth.apikey.in === 'header') headers.push({ key: auth.apikey.key, value: auth.apikey.value })
          else params.push({ key: auth.apikey.key, value: auth.apikey.value, enabled: true })
        }
        break
      case 'jwt':
      case 'oauth2':
      case 'awsSigV4':
        authNote = `${authKindLabel(auth.kind)} authentication is computed by AUK at send time and is not reproduced in this snippet.`
        break
    }
  }

  return { method: req.method, url: mergeQueryParams(req.url, params), headers, bodyText, authNote }
}

function buildCurl(req: RequestDef): string {
  const r = resolveSnippetRequest(req)
  const parts = ['curl', '-X', r.method]
  for (const h of r.headers) parts.push('-H', shellQuote(`${h.key}: ${h.value}`))
  if (r.bodyText !== null) parts.push('-d', shellQuote(r.bodyText))
  parts.push(shellQuote(r.url))
  const cmd = parts.join(' ')
  return r.authNote ? `# ${r.authNote}\n${cmd}` : cmd
}

function buildPython(req: RequestDef): string {
  const r = resolveSnippetRequest(req)
  const lines: string[] = []
  if (r.authNote) lines.push(`# ${r.authNote}`, '')
  lines.push('import requests', '', `url = ${dqStr(r.url)}`)
  const callArgs = ['method', 'url']
  if (r.headers.length > 0) {
    lines.push('headers = {')
    for (const h of r.headers) lines.push(`    ${dqStr(h.key)}: ${dqStr(h.value)},`)
    lines.push('}')
    callArgs.push('headers=headers')
  }
  if (r.bodyText !== null) {
    lines.push(`data = ${dqStr(r.bodyText)}`)
    callArgs.push('data=data')
  }
  lines.push('', `response = requests.request(${dqStr(r.method)}, url${callArgs.length > 2 ? ', ' + callArgs.slice(2).join(', ') : ''})`)
  lines.push('print(response.status_code)', 'print(response.text)')
  return lines.join('\n')
}

function buildFetch(req: RequestDef): string {
  const r = resolveSnippetRequest(req)
  const lines: string[] = []
  if (r.authNote) lines.push(`// ${r.authNote}`, '')
  const opts: string[] = [`  method: ${dqStr(r.method)},`]
  if (r.headers.length > 0) {
    opts.push('  headers: {')
    for (const h of r.headers) opts.push(`    ${dqStr(h.key)}: ${dqStr(h.value)},`)
    opts.push('  },')
  }
  if (r.bodyText !== null) opts.push(`  body: ${dqStr(r.bodyText)},`)
  lines.push(`fetch(${dqStr(r.url)}, {`, ...opts, '})', '  .then((response) => response.text())', '  .then((text) => console.log(text))')
  return lines.join('\n')
}

function buildGo(req: RequestDef): string {
  const r = resolveSnippetRequest(req)
  const lines: string[] = ['package main', '', 'import (', '\t"fmt"', '\t"io"', '\t"net/http"']
  if (r.bodyText !== null) lines.push('\t"strings"')
  lines.push(')', '')
  if (r.authNote) lines.push(`// ${r.authNote}`)
  lines.push('func main() {')
  const bodyExpr = r.bodyText !== null ? `strings.NewReader(${dqStr(r.bodyText)})` : 'nil'
  lines.push(`\treq, err := http.NewRequest(${dqStr(r.method)}, ${dqStr(r.url)}, ${bodyExpr})`, '\tif err != nil {', '\t\tpanic(err)', '\t}')
  for (const h of r.headers) lines.push(`\treq.Header.Set(${dqStr(h.key)}, ${dqStr(h.value)})`)
  lines.push(
    '',
    '\tresp, err := http.DefaultClient.Do(req)',
    '\tif err != nil {',
    '\t\tpanic(err)',
    '\t}',
    '\tdefer resp.Body.Close()',
    '',
    '\tbody, _ := io.ReadAll(resp.Body)',
    '\tfmt.Println(resp.StatusCode)',
    '\tfmt.Println(string(body))',
    '}',
  )
  return lines.join('\n')
}

interface SnippetFormat {
  id: string
  label: string
  build: (req: RequestDef) => string
}

const SNIPPET_FORMATS: SnippetFormat[] = [
  { id: 'curl', label: 'cURL', build: buildCurl },
  { id: 'python', label: 'Python (requests)', build: buildPython },
  { id: 'js', label: 'JavaScript (fetch)', build: buildFetch },
  { id: 'go', label: 'Go (net/http)', build: buildGo },
]

export default function ResponseViewer(props: { response: ResponseData | null; loading: boolean }) {
  const [tab, setTab] = createSignal<Tab>('body')
  const [bodyMode, setBodyMode] = createSignal<BodyMode>('pretty')
  const [copyMenuOpen, setCopyMenuOpen] = createSignal(false)
  const [copiedFormat, setCopiedFormat] = createSignal<string | null>(null)
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

  onMount(() => window.addEventListener('apitool:search-body', openSearch))
  onCleanup(() => {
    window.removeEventListener('apitool:search-body', openSearch)
    view?.destroy()
  })

  // WS/SSE/gRPC don't fit the single request/response shape these formats
  // generate for — gating here (rather than just disabling on !activeRequest())
  // avoids offering a "Copy as Python" that can't mean anything for a
  // streaming connection.
  const canCopySnippet = createMemo(() => {
    const p = activeRequest()?.protocol
    return p === 'http' || p === 'graphql'
  })

  async function copySnippet(format: SnippetFormat) {
    const req = activeRequest()
    if (!req) return
    const code = format.build(req)
    if (!code) return
    try {
      await navigator.clipboard.writeText(code)
      setCopiedFormat(format.label)
      setCopyMenuOpen(false)
      setTimeout(() => setCopiedFormat(null), 1500)
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
                <div class="relative ml-auto">
                  <button
                    class="rounded bg-field px-2 py-1 text-[11px] text-ink-dim hover:bg-raised disabled:cursor-not-allowed disabled:opacity-40"
                    disabled={!canCopySnippet()}
                    onClick={() => setCopyMenuOpen((v) => !v)}
                    title={canCopySnippet() ? 'Copy this request as a code snippet' : 'Code snippets are only available for HTTP/GraphQL requests'}
                  >
                    {copiedFormat() ? `Copied ${copiedFormat()}` : 'Copy as ▾'}
                  </button>
                  <Show when={copyMenuOpen()}>
                    <div class="fixed inset-0 z-10" onClick={() => setCopyMenuOpen(false)} />
                    <div
                      class="absolute right-0 top-full z-20 mt-1 w-48 rounded border border-edge bg-elevated py-1 shadow-lg"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <For each={SNIPPET_FORMATS}>
                        {(format) => (
                          <button
                            class="block w-full px-3 py-1.5 text-left text-[11px] text-ink-dim hover:bg-raised hover:text-ink"
                            onClick={() => copySnippet(format)}
                          >
                            {format.label}
                          </button>
                        )}
                      </For>
                    </div>
                  </Show>
                </div>
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
          )}
        </Show>
      </Show>
    </div>
  )
}
