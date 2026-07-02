import { For } from 'solid-js'
import type { KeyValue } from '../types'

// Generic editable key/value table used by the Params and Headers tabs of
// RequestEditor. The caller owns the store path; this component only reads
// `rows` and calls back through `onChange`-style setters so it stays
// storage-agnostic (works for req.headers or req.params identically).
export default function KeyValueTable(props: {
  rows: KeyValue[]
  keyPlaceholder?: string
  valuePlaceholder?: string
  onSet: (index: number, field: keyof KeyValue, value: string | boolean) => void
  onAdd: () => void
  onRemove: (index: number) => void
}) {
  return (
    <div class="flex flex-col gap-1 p-2">
      <div class="flex items-center gap-2 px-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
        <span class="w-5" />
        <span class="flex-1">Key</span>
        <span class="flex-1">Value</span>
        <span class="w-5" />
      </div>
      <For each={props.rows}>
        {(row, i) => (
          <div class="flex items-center gap-2">
            <input
              type="checkbox"
              class="h-3.5 w-3.5 shrink-0 accent-accent"
              checked={row.enabled}
              onChange={(e) => props.onSet(i(), 'enabled', e.currentTarget.checked)}
            />
            <input
              class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder={props.keyPlaceholder ?? 'key'}
              value={row.key}
              onInput={(e) => props.onSet(i(), 'key', e.currentTarget.value)}
            />
            <input
              class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder={props.valuePlaceholder ?? 'value'}
              value={row.value}
              onInput={(e) => props.onSet(i(), 'value', e.currentTarget.value)}
            />
            <button
              class="w-5 shrink-0 rounded text-ink-faint hover:bg-raised hover:text-danger"
              onClick={() => props.onRemove(i())}
              title="Remove row"
            >
              ×
            </button>
          </div>
        )}
      </For>
      <button
        class="mt-1 self-start rounded px-2 py-1 text-xs text-ink-muted hover:bg-field hover:text-ink-dim"
        onClick={() => props.onAdd()}
      >
        + Add row
      </button>
    </div>
  )
}
