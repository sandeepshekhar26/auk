import { For, Show, createMemo, createSignal } from 'solid-js'
import { createVirtualizer } from '@tanstack/solid-virtual'
import { appState, streamConsoleOpen, setStreamConsoleOpen } from '../lib/store'
import type { StreamEvent } from '../types'

const KIND_FILTERS: Array<{ value: StreamEvent['kind'] | 'all'; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'ws', label: 'WS' },
  { value: 'sse', label: 'SSE' },
  { value: 'grpc', label: 'gRPC' },
  { value: 'perf', label: 'Perf' },
]

const DIRECTION_STYLES: Record<StreamEvent['direction'], string> = {
  sent: 'text-info',
  received: 'text-accent-fg',
  meta: 'text-ink-muted',
  error: 'text-danger',
  done: 'text-ink-faint',
}

const DIRECTION_LABEL: Record<StreamEvent['direction'], string> = {
  sent: '↑ sent',
  received: '↓ recv',
  meta: '· meta',
  error: '✗ error',
  done: '✓ done',
}

export default function StreamConsole() {
  const [kindFilter, setKindFilter] = createSignal<StreamEvent['kind'] | 'all'>('all')
  const [sessionFilter, setSessionFilter] = createSignal('')
  let scrollRef: HTMLDivElement | undefined

  const filtered = createMemo<StreamEvent[]>(() => {
    const kind = kindFilter()
    const session = sessionFilter().trim()
    return appState.streamEvents.filter((evt) => {
      if (kind !== 'all' && evt.kind !== kind) return false
      if (session && evt.sessionId !== session) return false
      return true
    })
  })

  const sessionIds = createMemo(() => {
    const ids = new Set<string>()
    for (const evt of appState.streamEvents) ids.add(evt.sessionId)
    return Array.from(ids)
  })

  const virtualizer = createVirtualizer({
    get count() {
      return filtered().length
    },
    getScrollElement: () => scrollRef ?? null,
    estimateSize: () => 28,
    overscan: 12,
  })

  function close() {
    setStreamConsoleOpen(false)
  }

  return (
    <Show when={streamConsoleOpen()}>
      <div class="fixed inset-0 z-40 flex items-end justify-center bg-black/50 pb-8" onClick={close}>
        <div
          class="flex h-[70vh] w-[90vw] max-w-4xl flex-col overflow-hidden rounded-lg border border-edge-strong bg-surface shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="flex items-center gap-3 border-b border-edge px-3 py-2">
            <span class="text-sm font-semibold text-ink">Stream Console</span>
            <span class="text-xs text-ink-faint">{filtered().length} of {appState.streamEvents.length} events</span>
            <div class="flex flex-1 items-center justify-end gap-2">
              <select
                class="rounded bg-field px-2 py-1 text-xs text-ink-dim focus:outline-none focus:ring-1 focus:ring-edge-strong"
                value={sessionFilter()}
                onChange={(e) => setSessionFilter(e.currentTarget.value)}
              >
                <option value="">All sessions</option>
                <For each={sessionIds()}>{(id) => <option value={id}>{id}</option>}</For>
              </select>
              <div class="flex overflow-hidden rounded border border-edge-strong">
                <For each={KIND_FILTERS}>
                  {(f) => (
                    <button
                      class="px-2 py-1 text-xs"
                      classList={{
                        'bg-elevated text-ink': kindFilter() === f.value,
                        'bg-field text-ink-muted hover:text-ink-dim': kindFilter() !== f.value,
                      }}
                      onClick={() => setKindFilter(f.value)}
                    >
                      {f.label}
                    </button>
                  )}
                </For>
              </div>
              <button
                class="rounded px-2 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink-dim"
                onClick={close}
              >
                Esc
              </button>
            </div>
          </div>
          <div ref={scrollRef} class="flex-1 overflow-y-auto font-mono text-xs">
            <Show
              when={filtered().length > 0}
              fallback={<p class="px-3 py-4 text-ink-faint">No stream events yet.</p>}
            >
              <div
                style={{ height: `${virtualizer.getTotalSize()}px`, position: 'relative', width: '100%' }}
              >
                <For each={virtualizer.getVirtualItems()}>
                  {(row) => {
                    const evt = () => filtered()[row.index]
                    return (
                      <div
                        class="absolute left-0 top-0 flex w-full items-start gap-2 border-b border-edge-soft/60 px-3 py-1 hover:bg-raised/60"
                        style={{ height: `${row.size}px`, transform: `translateY(${row.start}px)` }}
                      >
                        <span class="w-20 shrink-0 text-ink-faint">
                          {new Date(evt().timestamp).toLocaleTimeString(undefined, { hour12: false })}
                        </span>
                        <span class="w-12 shrink-0 uppercase text-ink-muted">{evt().kind}</span>
                        <span class={`w-14 shrink-0 ${DIRECTION_STYLES[evt().direction]}`}>
                          {DIRECTION_LABEL[evt().direction]}
                        </span>
                        <span class="flex-1 truncate text-ink-dim" title={evt().payload}>
                          {evt().payload}
                        </span>
                      </div>
                    )
                  }}
                </For>
              </div>
            </Show>
          </div>
        </div>
      </div>
    </Show>
  )
}
