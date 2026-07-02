import { createEffect, onCleanup, Show } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { json } from '@codemirror/lang-json'
import { appState, setAppState } from '../lib/store'
import type { BodyKind, KeyValue, RequestBody } from '../types'
import KeyValueTable from './KeyValueTable'

const BODY_KINDS: BodyKind[] = ['none', 'json', 'text', 'form', 'graphql', 'binary']

const editorTheme = EditorView.theme(
  {
    '&': { height: '100%', fontSize: '12px', backgroundColor: 'transparent' },
    '.cm-scroller': { fontFamily: '"JetBrains Mono", ui-monospace, monospace', overflow: 'auto' },
    '.cm-content': { caretColor: '#34d399' },
    '.cm-gutters': { backgroundColor: 'transparent', color: '#525252', border: 'none' },
    '&.cm-focused': { outline: 'none' },
  },
  { dark: true },
)

// JSON body editor backed by CodeMirror 6. Kept isolated from Solid's
// reactivity for the actual keystrokes (CodeMirror owns its own document)
// but pushed back into appState on every change so Send/Save see the
// latest text; and re-synced from appState when the active tab changes.
function JsonCodeMirror(props: { requestIndex: number; bodyIndex: number; text: string }) {
  let container: HTMLDivElement | undefined
  let view: EditorView | undefined
  let lastPushed: string | undefined

  createEffect(() => {
    const idx = props.requestIndex
    const initial = props.text ?? ''
    if (!container) return

    if (view) {
      view.destroy()
      view = undefined
    }

    lastPushed = initial
    view = new EditorView({
      state: EditorState.create({
        doc: initial,
        extensions: [
          lineNumbers(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          json(),
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
  })

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

export default function BodyEditor(props: { requestIndex: number }) {
  const req = () => appState.requests[props.requestIndex]
  const body = (): RequestBody => req()?.body ?? { kind: 'none', text: '', formFields: [] }

  function setKind(kind: BodyKind) {
    setAppState('requests', props.requestIndex, 'body', (prev) => ({
      kind,
      text: prev?.text ?? '',
      formFields: prev?.formFields ?? [],
    }))
  }

  function setFormField(index: number, field: keyof KeyValue, value: string | boolean) {
    setAppState('requests', props.requestIndex, 'body', 'formFields', index, field as any, value as any)
  }

  function addFormField() {
    setAppState('requests', props.requestIndex, 'body', 'formFields', (fields: KeyValue[] = []) => [
      ...fields,
      { key: '', value: '', enabled: true },
    ])
  }

  function removeFormField(index: number) {
    setAppState('requests', props.requestIndex, 'body', 'formFields', (fields: KeyValue[] = []) =>
      fields.filter((_, i) => i !== index),
    )
  }

  return (
    <div class="flex h-full flex-col">
      <div class="flex items-center gap-2 border-b border-neutral-800 px-2 py-1.5">
        <span class="text-[10px] font-semibold uppercase tracking-wide text-neutral-600">Body type</span>
        <select
          class="rounded bg-neutral-900 px-2 py-1 font-mono text-xs text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
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
            class="h-full w-full resize-none bg-transparent p-3 font-mono text-xs text-neutral-200 focus:outline-none"
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
          <div class="flex h-full items-center justify-center text-xs text-neutral-600">This request has no body.</div>
        </Show>
        <Show when={body().kind === 'binary'}>
          <div class="flex h-full items-center justify-center text-xs text-neutral-600">
            Binary body editing is not supported yet — use a file reference via the CLI/config for now.
          </div>
        </Show>
      </div>
    </div>
  )
}
