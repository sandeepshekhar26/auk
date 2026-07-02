import { For, Show, createMemo } from 'solid-js'
import { appState, setAppState } from '../lib/store'
import type { Assertion, AssertionOperator, AssertionSource } from '../types'

const SOURCES: { value: AssertionSource; label: string }[] = [
  { value: 'status', label: 'Status' },
  { value: 'body', label: 'Body (JSON path)' },
  { value: 'header', label: 'Header' },
  { value: 'responseTime', label: 'Response time (ms)' },
]

const OPERATORS: { value: AssertionOperator; label: string; needsValue: boolean }[] = [
  { value: 'eq', label: '= equals', needsValue: true },
  { value: 'neq', label: '≠ not equals', needsValue: true },
  { value: 'contains', label: '⊃ contains', needsValue: true },
  { value: 'matches', label: '~ matches regex', needsValue: true },
  { value: 'lt', label: '< less than', needsValue: true },
  { value: 'gt', label: '> greater than', needsValue: true },
  { value: 'exists', label: '∃ exists', needsValue: false },
  { value: 'notExists', label: '∄ not exists', needsValue: false },
]

function needsValue(op: AssertionOperator): boolean {
  return OPERATORS.find((o) => o.value === op)?.needsValue ?? true
}

// AssertionEditor is the "Assert" tab: declarative response tests that turn a
// request into a CI-gateable check. Edits write onto request.assertions and
// persist via RequestEditor's debounced save.
export default function AssertionEditor(props: { requestIndex: number }) {
  const assertions = createMemo<Assertion[]>(() => appState.requests[props.requestIndex]?.assertions ?? [])

  function set(next: Assertion[]) {
    setAppState('requests', props.requestIndex, 'assertions', next)
  }
  function patch(i: number, p: Partial<Assertion>) {
    const next = [...assertions()]
    next[i] = { ...next[i], ...p }
    set(next)
  }
  function add() {
    set([...assertions(), { source: 'status', operator: 'eq', value: '200', enabled: true }])
  }
  function remove(i: number) {
    set(assertions().filter((_, idx) => idx !== i))
  }

  return (
    <div class="flex flex-col gap-1 p-2">
      <div class="mb-1 flex items-center gap-2">
        <span class="text-xs text-ink-muted">
          Assertions run on every send. Any failure fails the request — in the app, the CLI (non-zero exit), and MCP.
        </span>
      </div>
      <For
        each={assertions()}
        fallback={<p class="px-1 py-3 text-xs text-ink-faint">No assertions yet.</p>}
      >
        {(a, i) => (
          <div class="flex flex-wrap items-center gap-1.5">
            <input
              type="checkbox"
              class="accent-accent"
              checked={a.enabled}
              onChange={(e) => patch(i(), { enabled: e.currentTarget.checked })}
            />
            <select
              class="rounded bg-field px-1.5 py-1 text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={a.source}
              onChange={(e) => patch(i(), { source: e.currentTarget.value as AssertionSource })}
            >
              <For each={SOURCES}>{(s) => <option value={s.value}>{s.label}</option>}</For>
            </select>

            <Show when={a.source === 'body'}>
              <input
                class="w-40 rounded bg-field px-1.5 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="data.items[0].id"
                value={a.path ?? ''}
                onInput={(e) => patch(i(), { path: e.currentTarget.value })}
              />
            </Show>
            <Show when={a.source === 'header'}>
              <input
                class="w-36 rounded bg-field px-1.5 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="Content-Type"
                value={a.name ?? ''}
                onInput={(e) => patch(i(), { name: e.currentTarget.value })}
              />
            </Show>

            <select
              class="rounded bg-field px-1.5 py-1 text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={a.operator}
              onChange={(e) => patch(i(), { operator: e.currentTarget.value as AssertionOperator })}
            >
              <For each={OPERATORS}>{(o) => <option value={o.value}>{o.label}</option>}</For>
            </select>

            <Show when={needsValue(a.operator)}>
              <input
                class="w-32 rounded bg-field px-1.5 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="expected"
                value={a.value ?? ''}
                onInput={(e) => patch(i(), { value: e.currentTarget.value })}
              />
            </Show>

            <button
              class="rounded px-1.5 py-1 text-xs text-ink-faint hover:bg-raised hover:text-danger"
              onClick={() => remove(i())}
            >
              ×
            </button>
          </div>
        )}
      </For>
      <button class="mt-1 self-start text-xs text-accent-fg hover:underline" onClick={add}>
        + add assertion
      </button>
    </div>
  )
}
