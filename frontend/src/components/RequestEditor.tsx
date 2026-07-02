import { Show, createEffect, createMemo, createSignal, For, on } from 'solid-js'
import { appState, setAppState } from '../lib/store'
import { saveRequestDebounced } from '../lib/data'
import type { KeyValue } from '../types'
import KeyValueTable from './KeyValueTable'
import BodyEditor from './BodyEditor'
import AuthConfigForm from './AuthConfigForm'

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

type EditorTab = 'params' | 'headers' | 'body' | 'auth'
const TABS: { id: EditorTab; label: string }[] = [
  { id: 'params', label: 'Params' },
  { id: 'headers', label: 'Headers' },
  { id: 'body', label: 'Body' },
  { id: 'auth', label: 'Auth' },
]

export default function RequestEditor(props: { onSend: (requestId: string) => void }) {
  const [tab, setTab] = createSignal<EditorTab>('params')

  const activeIndex = createMemo(() => appState.requests.findIndex((r) => r.id === appState.activeTabId))
  const active = createMemo(() => appState.requests.find((r) => r.id === appState.activeTabId))

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

  function setRow(field: 'headers' | 'params', index: number, key: keyof KeyValue, value: string | boolean) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, index, key as any, value as any)
  }

  function addRow(field: 'headers' | 'params') {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, (rows: KeyValue[] = []) => [...rows, { key: '', value: '', enabled: true }])
  }

  function removeRow(field: 'headers' | 'params', index: number) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, (rows: KeyValue[] = []) => rows.filter((_, i) => i !== index))
  }

  function enabledCount(rows: KeyValue[] | undefined) {
    return (rows ?? []).filter((r) => r.enabled).length
  }

  return (
    <Show when={active()} fallback={<EmptyState />}>
      {(req) => (
        <div class="flex h-full flex-col">
          <div class="flex items-center gap-2 border-b border-neutral-800 p-2">
            <select
              class="rounded bg-neutral-900 px-2 py-1 font-mono text-xs font-semibold text-emerald-400 focus:outline-none focus:ring-1 focus:ring-neutral-600"
              value={req().method}
              onChange={(e) => setAppState('requests', activeIndex(), 'method', e.currentTarget.value)}
            >
              {METHODS.map((m) => (
                <option value={m}>{m}</option>
              ))}
            </select>
            <input
              class="flex-1 rounded bg-neutral-900 px-2 py-1 font-mono text-sm text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
              value={req().url}
              placeholder="https://api.example.com/{{ path }}"
              onInput={(e) => setAppState('requests', activeIndex(), 'url', e.currentTarget.value)}
            />
            <button
              class="rounded bg-emerald-600 px-3 py-1 text-sm font-medium text-white hover:bg-emerald-500"
              onClick={() => props.onSend(req().id)}
            >
              Send
            </button>
          </div>

          <div class="flex items-center gap-1 border-b border-neutral-800 px-2">
            <For each={TABS}>
              {(t) => (
                <button
                  class="relative px-3 py-2 text-xs font-medium"
                  classList={{
                    'text-neutral-100': tab() === t.id,
                    'text-neutral-500 hover:text-neutral-300': tab() !== t.id,
                  }}
                  onClick={() => setTab(t.id)}
                >
                  {t.label}
                  <Show when={t.id === 'params' && enabledCount(req().params) > 0}>
                    <span class="ml-1 text-neutral-600">{enabledCount(req().params)}</span>
                  </Show>
                  <Show when={t.id === 'headers' && enabledCount(req().headers) > 0}>
                    <span class="ml-1 text-neutral-600">{enabledCount(req().headers)}</span>
                  </Show>
                  <Show when={t.id === 'auth' && req().authRef && req().authRef!.kind !== 'none'}>
                    <span class="ml-1 text-emerald-500">●</span>
                  </Show>
                  <Show when={tab() === t.id}>
                    <span class="absolute inset-x-2 -bottom-px h-px bg-emerald-500" />
                  </Show>
                </button>
              )}
            </For>
          </div>

          <div class="flex flex-1 flex-col overflow-hidden">
            <Show when={tab() === 'params'}>
              <div class="overflow-y-auto">
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
                <BodyEditor requestIndex={activeIndex()} />
              </div>
            </Show>
            <Show when={tab() === 'auth'}>
              <div class="overflow-y-auto">
                <AuthConfigForm requestIndex={activeIndex()} />
              </div>
            </Show>
          </div>
        </div>
      )}
    </Show>
  )
}

function EmptyState() {
  return (
    <div class="flex h-full flex-col items-center justify-center gap-2 text-neutral-600">
      <p class="text-sm">No request open</p>
      <p class="text-xs">Press ⌘K to search, or ⌘N to create a request</p>
    </div>
  )
}
