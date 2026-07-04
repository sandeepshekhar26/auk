import { createEffect, on, onCleanup, untrack } from 'solid-js'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { javascript } from '@codemirror/lang-javascript'
import { syntaxHighlighting } from '@codemirror/language'
import { jsonHighlightStyle } from '../lib/codeTheme'
import { appState, setAppState } from '../lib/store'

const editorTheme = EditorView.theme({
  '&': { height: '100%', fontSize: '12px', backgroundColor: 'transparent' },
  '.cm-scroller': { fontFamily: '"JetBrains Mono", ui-monospace, monospace', overflow: 'auto' },
  '.cm-content': { caretColor: 'rgb(var(--color-accent-fg))' },
  '.cm-gutters': { backgroundColor: 'transparent', color: 'rgb(var(--color-ink-faint))', border: 'none' },
  '&.cm-focused': { outline: 'none' },
})

// Pre-request script editor (JS, via @codemirror/lang-javascript). Mirrors
// BodyEditor's JsonCodeMirror structure exactly, including the same fix:
// the view-construction effect is keyed on requestIndex ONLY (via untrack
// for the initial text), never on the text prop itself — otherwise every
// keystroke would round-trip through docChanged -> setAppState -> a new
// text prop -> this effect re-firing -> the view being destroyed and
// rebuilt unfocused after each character typed.
export default function ScriptEditor(props: { requestIndex: number }) {
  let container: HTMLDivElement | undefined
  let view: EditorView | undefined
  let lastPushed: string | undefined

  const text = () => appState.requests[props.requestIndex]?.preRequestScript ?? ''

  createEffect(
    on(
      () => props.requestIndex,
      (idx) => {
        if (!container) return

        if (view) {
          view.destroy()
          view = undefined
        }

        const initial = untrack(text)
        lastPushed = initial
        view = new EditorView({
          state: EditorState.create({
            doc: initial,
            extensions: [
              lineNumbers(),
              history(),
              keymap.of([...defaultKeymap, ...historyKeymap]),
              javascript(),
              syntaxHighlighting(jsonHighlightStyle),
              editorTheme,
              EditorView.updateListener.of((update) => {
                if (!update.docChanged) return
                const value = update.state.doc.toString()
                lastPushed = value
                setAppState('requests', idx, 'preRequestScript', value)
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
    if (text() === lastPushed) return
    const current = view.state.doc.toString()
    if (current === text()) return
    view.dispatch({ changes: { from: 0, to: current.length, insert: text() } })
    lastPushed = text()
  })

  onCleanup(() => view?.destroy())

  return <div ref={container} class="h-full min-h-[200px]" />
}
