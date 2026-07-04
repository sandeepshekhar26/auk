import { For, Show, createEffect, createMemo, createSignal } from 'solid-js'
import { createStore, reconcile } from 'solid-js/store'
import { EditorState } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting } from '@codemirror/language'
import { jsonHighlightStyle } from '../lib/codeTheme'
import { appState, mcpToolView } from '../lib/store'
import { wails } from '../lib/wails'
import SchemaForm, { canRenderForm, coerceNumberFields } from './SchemaForm'
import type { McpCallResult, McpToolInfo } from '../types'

const editorTheme = EditorView.theme({
  '&': { backgroundColor: 'transparent', height: '100%', fontSize: '12px' },
  '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, monospace', overflow: 'auto' },
  '.cm-gutters': { backgroundColor: 'transparent', color: 'rgb(var(--color-ink-faint))', border: 'none' },
  '.cm-content': { caretColor: 'transparent' },
  '&.cm-focused': { outline: 'none' },
})

// Read-only pretty-printed viewer for one text content block. Unlike
// BodyEditor/ScriptEditor (editable, and fixed earlier this session for a
// focus-loss bug caused by tracking their OWN text prop), this is
// read-only — there's no write-back path that could create a destroy/
// rebuild loop, so tracking `props.text` directly is safe here. The
// always-rendered-div + signal-ref pattern (not a plain variable) still
// applies, matching the fix made to ResponseViewer earlier this session.
function TextResultView(props: { text: string }) {
  const [host, setHost] = createSignal<HTMLDivElement>()
  let view: EditorView | undefined

  const isJson = createMemo(() => {
    try {
      JSON.parse(props.text)
      return true
    } catch {
      return false
    }
  })
  const displayText = createMemo(() => {
    if (!isJson()) return props.text
    try {
      return JSON.stringify(JSON.parse(props.text), null, 2)
    } catch {
      return props.text
    }
  })

  createEffect(() => {
    const el = host()
    const text = displayText()
    if (!el) return
    if (view) {
      view.destroy()
      view = undefined
    }
    view = new EditorView({
      state: EditorState.create({
        doc: text,
        extensions: [
          lineNumbers(),
          EditorView.editable.of(false),
          EditorState.readOnly.of(true),
          syntaxHighlighting(jsonHighlightStyle),
          ...(isJson() ? [json()] : []),
          editorTheme,
        ],
      }),
      parent: el,
    })
  })

  return <div ref={setHost} class="h-full min-h-[80px] overflow-auto rounded bg-field" />
}

function ResultView(props: { result: McpCallResult }) {
  return (
    <div class="flex flex-col gap-2">
      <Show when={props.result.isError}>
        <div class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">
          Tool reported an error (isError: true) — see content below.
        </div>
      </Show>
      <For each={props.result.content}>
        {(block) => (
          <div>
            <Show when={block.type === 'text'}>
              <div class="h-48">
                <TextResultView text={block.text ?? ''} />
              </div>
            </Show>
            <Show when={block.type === 'image' && block.dataBase64}>
              <img
                src={`data:${block.mimeType ?? 'image/png'};base64,${block.dataBase64}`}
                class="max-h-64 max-w-full rounded border border-edge"
                alt="Tool result image"
              />
            </Show>
            <Show when={block.type === 'audio' && block.dataBase64}>
              <audio controls class="w-full" src={`data:${block.mimeType ?? 'audio/mpeg'};base64,${block.dataBase64}`} />
            </Show>
            <Show when={block.type === 'unknown'}>
              <p class="rounded bg-field px-2 py-1.5 text-xs text-ink-faint">Unsupported content type.</p>
            </Show>
          </div>
        )}
      </For>
      <Show when={props.result.structuredContent !== undefined && props.result.structuredContent !== null}>
        <div>
          <h4 class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Structured content</h4>
          <div class="h-32">
            <TextResultView text={JSON.stringify(props.result.structuredContent, null, 2)} />
          </div>
        </div>
      </Show>
    </div>
  )
}

// Seeds initial form values so what's actually submitted always matches
// what the form visually shows — without this, a required boolean field
// displays as an unchecked "false" checkbox but never actually lands in
// the submitted JSON unless the user clicks it (a checkbox always shows a
// definite checked/unchecked state, unlike a text/number input's genuinely
// empty placeholder-only state before the user types), so the server-side
// "required: confirm" validation fails with no clue why. Any property with
// a schema-declared default is seeded too, for the same reason.
function defaultFormValues(schema: Record<string, any>): Record<string, unknown> {
  const values: Record<string, unknown> = {}
  const properties = (schema.properties ?? {}) as Record<string, Record<string, any>>
  for (const [key, prop] of Object.entries(properties)) {
    if (prop.default !== undefined) values[key] = prop.default
    else if (prop.type === 'boolean') values[key] = false
  }
  return values
}

interface RecentCall {
  argsJSON: string
  result: McpCallResult | null
  error: string | null
  timestamp: string
}

// McpToolView takes over the main area (App.tsx) when a tool is selected
// from McpPanel — full width, the same real estate RequestEditor+
// ResponseViewer normally occupy, since a schema form + response viewer
// deserves more room than the 288px drawer. Re-fetches the tool's own
// info via McpListTools rather than sharing McpPanel's internal state,
// keeping the two components decoupled (the session is already open by
// the time a tool can be selected, so this is a cheap re-list, not a
// reconnect).
export default function McpToolView() {
  const target = () => mcpToolView()
  const connection = createMemo(() => appState.mcpConnections.find((c) => c.id === target()?.connectionId))

  const [tool, setTool] = createSignal<McpToolInfo | null>(null)
  const [loadError, setLoadError] = createSignal<string | null>(null)
  // A store (not a signal) so each field's own binding only reacts to ITS
  // OWN key changing. With a plain signal, set() had to build a whole new
  // object (`{...prev, [key]: value}`) on every keystroke, which changes
  // the signal's identity and re-runs EVERY field's reactive value=
  // expression — including the JSON-textarea fallback fields, whose
  // value= derives from the last COMMITTED value, not whatever the user
  // is still mid-typing. That meant editing field B could silently
  // overwrite field A's uncommitted, not-yet-valid-JSON text back to its
  // last committed value, discarding keystrokes in a field the user never
  // touched. A store's setStore(key, value) only invalidates that key's
  // subscribers.
  const [formValues, setFormValues] = createStore<Record<string, unknown>>({})
  const [rawJson, setRawJson] = createSignal('{}')
  const [mode, setMode] = createSignal<'form' | 'json'>('form')
  const [calling, setCalling] = createSignal(false)
  const [result, setResult] = createSignal<McpCallResult | null>(null)
  const [callError, setCallError] = createSignal<string | null>(null)
  const [recent, setRecent] = createSignal<RecentCall[]>([])

  createEffect(async () => {
    const t = target()
    setTool(null)
    setLoadError(null)
    setResult(null)
    setCallError(null)
    setFormValues(reconcile({}))
    setRawJson('{}')
    setRecent([])
    if (!t) return
    try {
      const tools = await wails.McpListTools(t.connectionId)
      const found = ((tools ?? []) as unknown as McpToolInfo[]).find((x) => x.name === t.toolName)
      if (!found) {
        setLoadError(`Tool "${t.toolName}" was not found — it may have been removed from the server.`)
        return
      }
      setTool(found)
      setMode(canRenderForm(found.inputSchema) ? 'form' : 'json')
      if (canRenderForm(found.inputSchema)) setFormValues(reconcile(defaultFormValues(found.inputSchema as Record<string, any>)))
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    }
  })

  function switchMode(next: 'form' | 'json') {
    if (next === mode()) return
    if (next === 'json') {
      const schema = tool()?.inputSchema
      const coerced = schema && canRenderForm(schema) ? coerceNumberFields(schema as Record<string, any>, formValues) : formValues
      setRawJson(JSON.stringify(coerced, null, 2))
    } else {
      try {
        const parsed = JSON.parse(rawJson())
        const schema = tool()?.inputSchema
        const defaults = schema && canRenderForm(schema) ? defaultFormValues(schema as Record<string, any>) : {}
        if (parsed && typeof parsed === 'object') setFormValues(reconcile({ ...defaults, ...parsed }))
      } catch {
        // Keep whatever form values were there before — an invalid JSON
        // edit shouldn't wipe out previously-valid form state.
      }
    }
    setMode(next)
  }

  async function callTool() {
    const t = target()
    if (!t) return
    const schema = tool()?.inputSchema
    const argsJSON =
      mode() === 'form'
        ? JSON.stringify(schema && canRenderForm(schema) ? coerceNumberFields(schema as Record<string, any>, formValues) : formValues)
        : rawJson()
    setCalling(true)
    setCallError(null)
    try {
      const res = (await wails.McpCallTool(t.connectionId, t.toolName, argsJSON)) as unknown as McpCallResult
      setResult(res)
      setRecent((prev) => [{ argsJSON, result: res, error: null, timestamp: new Date().toISOString() }, ...prev].slice(0, 10))
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      setCallError(message)
      setResult(null)
      setRecent((prev) => [{ argsJSON, result: null, error: message, timestamp: new Date().toISOString() }, ...prev].slice(0, 10))
    } finally {
      setCalling(false)
    }
  }

  function rerun(call: RecentCall) {
    try {
      const parsed = JSON.parse(call.argsJSON)
      setFormValues(reconcile(parsed && typeof parsed === 'object' ? parsed : {}))
    } catch {
      setFormValues(reconcile({}))
    }
    setRawJson(call.argsJSON)
  }

  return (
    <div class="flex h-full flex-col overflow-hidden">
      <Show when={loadError()}>
        <div class="border-b border-danger-edge bg-danger-bg/40 px-3 py-2 text-sm text-danger">{loadError()}</div>
      </Show>

      <Show when={tool()}>
        {(t) => (
          <>
            <div class="flex items-start justify-between gap-3 border-b border-edge px-3 py-2">
              <div class="min-w-0">
                <div class="flex items-center gap-2">
                  <span class="truncate text-sm font-semibold text-ink">{t().title || t().name}</span>
                  <span class="rounded bg-field px-1.5 py-0.5 text-[10px] text-ink-faint">{connection()?.name}</span>
                  <Show when={t().readOnlyHint}>
                    <span class="text-[10px] font-semibold uppercase text-accent-fg">read-only</span>
                  </Show>
                  <Show when={t().destructiveHint}>
                    <span class="text-[10px] font-semibold uppercase text-danger">destructive</span>
                  </Show>
                  <Show when={t().idempotentHint}>
                    <span class="text-[10px] font-semibold uppercase text-info">idempotent</span>
                  </Show>
                </div>
                <p class="mt-0.5 text-xs text-ink-muted">{t().description}</p>
              </div>
              <button
                class="shrink-0 rounded bg-accent px-3 py-1.5 text-sm font-medium text-accent-contrast hover:bg-accent-hover disabled:opacity-50"
                disabled={calling()}
                onClick={callTool}
              >
                {calling() ? 'Calling…' : 'Call'}
              </button>
            </div>

            <div class="flex flex-1 overflow-hidden">
              <div class="flex w-1/2 flex-col overflow-y-auto border-r border-edge p-3">
                <div class="mb-2 flex items-center justify-between">
                  <h3 class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Arguments</h3>
                  <Show when={canRenderForm(t().inputSchema)}>
                    <div class="flex items-center gap-1 rounded bg-field p-0.5">
                      <button
                        class="rounded px-2 py-0.5 text-[11px]"
                        classList={{ 'bg-elevated text-ink': mode() === 'form', 'text-ink-muted': mode() !== 'form' }}
                        onClick={() => switchMode('form')}
                      >
                        Form
                      </button>
                      <button
                        class="rounded px-2 py-0.5 text-[11px]"
                        classList={{ 'bg-elevated text-ink': mode() === 'json', 'text-ink-muted': mode() !== 'json' }}
                        onClick={() => switchMode('json')}
                      >
                        JSON
                      </button>
                    </div>
                  </Show>
                </div>

                <Show when={mode() === 'form' && canRenderForm(t().inputSchema)}>
                  <SchemaForm
                    schema={t().inputSchema as Record<string, any>}
                    value={formValues}
                    onFieldChange={(key, value) => setFormValues(key, value)}
                  />
                </Show>
                <Show when={mode() === 'json'}>
                  <textarea
                    class="h-48 w-full resize-y rounded bg-field p-2 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                    placeholder="{}"
                    value={rawJson()}
                    onInput={(e) => setRawJson(e.currentTarget.value)}
                  />
                </Show>

                <Show when={recent().length > 0}>
                  <h3 class="mb-1 mt-4 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Recent calls (this session)</h3>
                  <div class="flex flex-col gap-1">
                    <For each={recent()}>
                      {(call) => (
                        <button
                          class="flex items-center gap-2 rounded px-2 py-1 text-left text-[11px] hover:bg-raised"
                          onClick={() => rerun(call)}
                        >
                          <span classList={{ 'text-danger': !!call.error, 'text-accent-fg': !call.error }}>{call.error ? '✗' : '✓'}</span>
                          <span class="flex-1 truncate font-mono text-ink-dim">{call.argsJSON}</span>
                        </button>
                      )}
                    </For>
                  </div>
                </Show>
              </div>

              <div class="flex w-1/2 flex-col overflow-y-auto p-3">
                <h3 class="mb-2 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Result</h3>
                <Show when={callError()}>
                  <div class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">{callError()}</div>
                </Show>
                <Show when={result()}>{(r) => <ResultView result={r()} />}</Show>
                <Show when={!result() && !callError()}>
                  <p class="text-xs text-ink-faint">Call the tool to see its result here.</p>
                </Show>
              </div>
            </div>
          </>
        )}
      </Show>
    </div>
  )
}
