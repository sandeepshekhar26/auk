import { For, Show } from 'solid-js'
import type { KeyValue } from '../types'

// Generic editable key/value table used by the Params and Headers tabs of
// RequestEditor. The caller owns the store path; this component only reads
// `rows` and calls back through `onChange`-style setters so it stays
// storage-agnostic (works for req.headers or req.params identically).
//
// `readOnlyKeys` switches it into DERIVED-ROWS mode, used by the Path group
// of the Params tab: the rows come from parsing `:name` placeholders out of
// the URL, so a key is a fact about the URL rather than something to type,
// and the row set isn't the user's to grow or shrink here. That one flag
// therefore turns off four things at once — key editing, the enabled
// checkbox (a path param can't be "off"; you delete it from the URL), the
// per-row remove button, and "+ Add row" — because they'd all be lies about
// where these rows come from. onAdd/onRemove go unused in that mode and are
// optional.
export default function KeyValueTable(props: {
  rows: KeyValue[]
  keyPlaceholder?: string
  valuePlaceholder?: string
  keyLabel?: string
  readOnlyKeys?: boolean
  onSet: (index: number, field: keyof KeyValue, value: string | boolean) => void
  onAdd?: () => void
  onRemove?: (index: number) => void
}) {
  return (
    <div class="flex flex-col gap-1 p-2">
      <div class="flex items-center gap-2 px-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
        <Show when={!props.readOnlyKeys}>
          <span class="w-5" />
        </Show>
        <span class="flex-1">{props.keyLabel ?? 'Key'}</span>
        <span class="flex-1">Value</span>
        <Show when={!props.readOnlyKeys}>
          <span class="w-5" />
        </Show>
      </div>
      <For each={props.rows}>
        {(row, i) => (
          <div class="flex items-center gap-2">
            <Show when={!props.readOnlyKeys}>
              <input
                type="checkbox"
                class="h-3.5 w-3.5 shrink-0 accent-accent"
                checked={row.enabled}
                onChange={(e) => props.onSet(i(), 'enabled', e.currentTarget.checked)}
              />
            </Show>
            <Show
              when={!props.readOnlyKeys}
              fallback={
                // Styled to line up with the editable key input opposite it
                // (same padding/typography, no field background) so the row
                // reads as one table rather than two mismatched halves.
                <span class="flex-1 truncate px-2 py-1 font-mono text-xs text-ink-dim" title={row.key}>
                  {row.key}
                </span>
              }
            >
              <input
                class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder={props.keyPlaceholder ?? 'key'}
                value={row.key}
                onInput={(e) => props.onSet(i(), 'key', e.currentTarget.value)}
              />
            </Show>
            <input
              class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder={props.valuePlaceholder ?? 'value'}
              value={row.value}
              onInput={(e) => props.onSet(i(), 'value', e.currentTarget.value)}
            />
            <Show when={!props.readOnlyKeys}>
              <button
                class="w-5 shrink-0 rounded text-ink-faint hover:bg-raised hover:text-danger"
                onClick={() => props.onRemove?.(i())}
                title="Remove row"
              >
                ×
              </button>
            </Show>
          </div>
        )}
      </For>
      <Show when={!props.readOnlyKeys}>
        <button
          class="mt-1 self-start rounded px-2 py-1 text-xs text-ink-muted hover:bg-field hover:text-ink-dim"
          onClick={() => props.onAdd?.()}
        >
          + Add row
        </button>
      </Show>
    </div>
  )
}
