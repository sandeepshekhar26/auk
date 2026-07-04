import { Show, createEffect, createMemo, createSignal, For, on } from 'solid-js'
import { appState, setAppState, setCommandPaletteOpen } from '../lib/store'
import { saveRequestDebounced } from '../lib/data'
import type { KeyValue } from '../types'
import KeyValueTable from './KeyValueTable'
import BodyEditor from './BodyEditor'
import AuthConfigForm from './AuthConfigForm'
import AssertionEditor from './AssertionEditor'
import PerfPanel from './PerfPanel'
import ScriptEditor from './ScriptEditor'

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

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
  }

  function removeRow(field: 'headers' | 'params', index: number) {
    const idx = activeIndex()
    if (idx < 0) return
    setAppState('requests', idx, field, (rows: KeyValue[] | null | undefined) => (rows ?? []).filter((_, i) => i !== index))
  }

  function enabledCount(rows: KeyValue[] | undefined) {
    return (rows ?? []).filter((r) => r.enabled).length
  }

  return (
    <Show when={active()} fallback={<EmptyState />}>
      {(req) => (
        <div class="flex h-full flex-col">
          <div class="flex items-center gap-2 border-b border-edge p-2">
            <select
              class="rounded bg-field px-2 py-1 font-mono text-xs font-semibold text-accent-fg focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={req().method}
              onChange={(e) => setAppState('requests', activeIndex(), 'method', e.currentTarget.value)}
            >
              {METHODS.map((m) => (
                <option value={m}>{m}</option>
              ))}
            </select>
            <input
              class="flex-1 rounded bg-field px-2 py-1 font-mono text-sm text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={req().url}
              placeholder="https://api.example.com/{{ path }}"
              onInput={(e) => setAppState('requests', activeIndex(), 'url', e.currentTarget.value)}
            />
            <button
              class="rounded bg-accent px-3 py-1 text-sm font-medium text-accent-contrast hover:bg-accent-hover"
              onClick={() => props.onSend(req().id)}
            >
              Send
            </button>
          </div>

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
