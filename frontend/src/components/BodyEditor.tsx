import { createEffect, on, onCleanup, Show, untrack } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { syntaxHighlighting } from '@codemirror/language'
import { jsonHighlightStyle, monoFontFamily } from '../lib/codeTheme'
import { appState, setAppState } from '../lib/store'
import type { BodyKind, KeyValue, RequestBody } from '../types'
import KeyValueTable from './KeyValueTable'

const BODY_KINDS: BodyKind[] = ['none', 'json', 'text', 'form', 'graphql', 'binary']

const editorTheme = EditorView.theme({
  '&': { height: '100%', fontSize: '12px', backgroundColor: 'transparent' },
  '.cm-scroller': { fontFamily: monoFontFamily, overflow: 'auto' },
  '.cm-content': { caretColor: 'rgb(var(--color-accent-fg))' },
  '.cm-gutters': { backgroundColor: 'transparent', color: 'rgb(var(--color-ink-faint))', border: 'none' },
  '&.cm-focused': { outline: 'none' },
})

// JSON body editor backed by CodeMirror 6. Kept isolated from Solid's
// reactivity for the actual keystrokes (CodeMirror owns its own document)
// but pushed back into appState on every change so Send/Save see the
// latest text; and re-synced from appState when the active tab changes.
function JsonCodeMirror(props: { requestIndex: number; bodyIndex: number; text: string }) {
  let container: HTMLDivElement | undefined
  let view: EditorView | undefined
  let lastPushed: string | undefined

  // Keyed on requestIndex ONLY (via untrack for props.text) — this must NOT
  // also track props.text, or every keystroke would round-trip through the
  // store's docChanged -> setAppState -> props.text update and come back
  // here, destroying + recreating the view (and dropping focus) after every
  // single character. Syncing externally-changed text into a still-live view
  // is the second effect's job below.
  createEffect(
    on(
      () => props.requestIndex,
      (idx) => {
        if (!container) return

        if (view) {
          view.destroy()
          view = undefined
        }

        const initial = untrack(() => props.text) ?? ''
        lastPushed = initial
        view = new EditorView({
          state: EditorState.create({
            doc: initial,
            extensions: [
              lineNumbers(),
              history(),
              keymap.of([...defaultKeymap, ...historyKeymap]),
              json(),
              syntaxHighlighting(jsonHighlightStyle),
              editorTheme,
              EditorView.updateListener.of((update) => {
                if (!update.docChanged) return
                const text = update.state.doc.toString()
                lastPushed = text
                setAppState('requests', idx, 'body', 'text', text)
              }),
            ],
          }),
          parent: container,
        })
      },
    ),
  )

  createEffect(() => {
    if (!view) return
    if (props.text === lastPushed) return
    const current = view.state.doc.toString()
    if (current === props.text) return
    view.dispatch({ changes: { from: 0, to: current.length, insert: props.text ?? '' } })
    lastPushed = props.text
  })

  onCleanup(() => view?.destroy())

  return <div ref={container} class="h-full min-h-[200px]" />
}

// internal/protocols/http's Execute sends RequestBody.Text completely
// verbatim as the wire body for every non-none Kind — it never reads
// FormFields itself (confirmed: that field doesn't appear anywhere in
// http.go). So the 'form' kind's key/value table was previously a dead
// end: filling it in updated `formFields` but never touched `text`, meaning
// a "form" body silently sent nothing at all. Keeping `text` synced to the
// url-encoded serialization of `formFields` on every edit is what makes the
// existing http.go code path (unchanged) actually send the form data.
function encodeFormFields(fields: KeyValue[]): string {
  return fields
    .filter((f) => f.enabled && f.key)
    .map((f) => `${encodeURIComponent(f.key)}=${encodeURIComponent(f.value)}`)
    .join('&')
}

export default function BodyEditor(props: { requestIndex: number }) {
  const req = () => appState.requests[props.requestIndex]
  const body = (): RequestBody => req()?.body ?? { kind: 'none', text: '', formFields: [] }

  function setKind(kind: BodyKind) {
    setAppState('requests', props.requestIndex, 'body', (prev) => ({
      kind,
      text: prev?.text ?? '',
      formFields: prev?.formFields ?? [],
      graphqlVariables: prev?.graphqlVariables,
    }))
    // Standard API-client UX (matches Yaak/Postman/Insomnia): picking "form"
    // as the body type suggests the matching Content-Type, but only if the
    // user hasn't already set one — never override an explicit choice.
    if (kind === 'form' && !(req()?.headers ?? []).some((h) => h.key.toLowerCase() === 'content-type')) {
      setAppState('requests', props.requestIndex, 'headers', (hs: KeyValue[] | null | undefined) => [
        ...(hs ?? []),
        { key: 'Content-Type', value: 'application/x-www-form-urlencoded', enabled: true },
      ])
    }
  }

  function syncFormText() {
    setAppState('requests', props.requestIndex, 'body', 'text', encodeFormFields(appState.requests[props.requestIndex]?.body?.formFields ?? []))
  }

  function setFormField(index: number, field: keyof KeyValue, value: string | boolean) {
    setAppState('requests', props.requestIndex, 'body', 'formFields', index, field as any, value as any)
    syncFormText()
  }

  // See the matching comment in RequestEditor's addRow: Go's omitempty
  // serializes an empty formFields slice as JSON null, and a default
  // parameter (`= []`) doesn't catch null, only undefined — `?? []` does.
  function addFormField() {
    setAppState('requests', props.requestIndex, 'body', 'formFields', (fields: KeyValue[] | null | undefined) => [
      ...(fields ?? []),
      { key: '', value: '', enabled: true },
    ])
    syncFormText()
  }

  function removeFormField(index: number) {
    setAppState('requests', props.requestIndex, 'body', 'formFields', (fields: KeyValue[] | null | undefined) =>
      (fields ?? []).filter((_, i) => i !== index),
    )
    syncFormText()
  }

  return (
    <div class="flex h-full flex-col">
      <div class="flex items-center gap-2 border-b border-edge px-2 py-1.5">
        <span class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Body type</span>
        <select
          class="rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
          value={body().kind}
          onChange={(e) => setKind(e.currentTarget.value as BodyKind)}
        >
          {BODY_KINDS.map((k) => (
            <option value={k}>{k}</option>
          ))}
        </select>
      </div>
      <div class="flex-1 overflow-hidden">
        <Show when={body().kind === 'json'}>
          <JsonCodeMirror requestIndex={props.requestIndex} bodyIndex={0} text={body().text ?? ''} />
        </Show>
        <Show when={body().kind === 'text' || body().kind === 'graphql'}>
          <textarea
            class="h-full w-full resize-none bg-transparent p-3 font-mono text-xs text-ink focus:outline-none"
            placeholder={body().kind === 'graphql' ? 'query { ... }' : 'Raw body text'}
            value={body().text ?? ''}
            onInput={(e) => setAppState('requests', props.requestIndex, 'body', 'text', e.currentTarget.value)}
          />
        </Show>
        <Show when={body().kind === 'form'}>
          <div class="h-full overflow-y-auto">
            <KeyValueTable
              rows={body().formFields ?? []}
              keyPlaceholder="field"
              valuePlaceholder="value"
              onSet={setFormField}
              onAdd={addFormField}
              onRemove={removeFormField}
            />
          </div>
        </Show>
        <Show when={body().kind === 'none'}>
          <div class="flex h-full items-center justify-center text-xs text-ink-faint">This request has no body.</div>
        </Show>
        <Show when={body().kind === 'binary'}>
          <div class="flex h-full items-center justify-center text-xs text-ink-faint">
            Binary body editing is not supported yet — use a file reference via the CLI/config for now.
          </div>
        </Show>
      </div>
    </div>
  )
}
