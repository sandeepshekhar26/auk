import { Show, createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting, defaultHighlightStyle } from '@codemirror/language'
import type { ResponseData } from '../types'
import { appState } from '../lib/store'

type Tab = 'body' | 'headers'
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

  let editorHost: HTMLDivElement | undefined
  let view: EditorView | undefined

  const rawBody = createMemo(() => decodeBody(props.response?.bodyBase64 ?? ''))
  const jsonInfo = createMemo(() => tryPrettyJson(rawBody()))
  const displayText = createMemo(() => {
    const { pretty, isJson } = jsonInfo()
    if (bodyMode() === 'pretty' && isJson && pretty !== null) return pretty
    return rawBody()
  })

  const activeRequest = createMemo(() => appState.requests.find((r) => r.id === appState.activeTabId))

  createEffect(() => {
    const text = displayText()
    const isJsonView = jsonInfo().isJson
    if (!editorHost) return

    if (view) {
      view.destroy()
      view = undefined
    }

    view = new EditorView({
      state: EditorState.create({
        doc: text,
        extensions: [
          lineNumbers(),
          keymap.of(defaultKeymap),
          EditorView.editable.of(false),
          EditorState.readOnly.of(true),
          syntaxHighlighting(defaultHighlightStyle),
          ...(isJsonView ? [json()] : []),
          EditorView.theme({
            '&': { backgroundColor: 'transparent', height: '100%', fontSize: '12px' },
            '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, monospace', overflow: 'auto' },
            '.cm-gutters': { backgroundColor: 'transparent', color: 'rgb(var(--color-ink-faint))', border: 'none' },
            '.cm-content': { caretColor: 'transparent' },
            '&.cm-focused': { outline: 'none' },
          }),
        ],
      }),
      parent: editorHost,
    })
  })

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

                <Show when={tab() === 'body' && jsonInfo().isJson}>
                  <div class="ml-auto flex items-center gap-1 rounded bg-field p-0.5">
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

              <div class="flex-1 overflow-hidden" classList={{ hidden: tab() !== 'body' }}>
                <Show
                  when={displayText().length > 0}
                  fallback={<div class="p-3 text-sm text-ink-faint">Empty response body.</div>}
                >
                  <div ref={editorHost} class="h-full overflow-auto" />
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
            </div>
          )}
        </Show>
      </Show>
    </div>
  )
}
