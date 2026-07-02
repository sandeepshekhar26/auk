import { For, Show } from 'solid-js'
import { appState, openTab } from '../lib/store'

function relativeTime(timestamp: string): string {
  const then = new Date(timestamp).getTime()
  if (Number.isNaN(then)) return ''
  const diffMs = Date.now() - then
  const diffSec = Math.round(diffMs / 1000)
  if (diffSec < 5) return 'just now'
  if (diffSec < 60) return `${diffSec}s ago`
  const diffMin = Math.round(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHour = Math.round(diffMin / 60)
  if (diffHour < 24) return `${diffHour}h ago`
  const diffDay = Math.round(diffHour / 24)
  return `${diffDay}d ago`
}

export default function HistoryPanel() {
  return (
    <div class="flex h-full flex-col overflow-y-auto p-2 text-xs">
      <Show when={appState.history.length === 0}>
        <p class="px-2 py-4 text-neutral-600">No requests sent yet.</p>
      </Show>
      <For each={appState.history}>
        {(h) => (
          <button
            class="flex w-full items-center gap-2 rounded px-2 py-1 text-left hover:bg-neutral-800"
            onClick={() => openTab(h.requestId)}
          >
            <span class="font-mono font-semibold text-neutral-400">{h.method}</span>
            <span class="flex-1 truncate text-neutral-300">{h.requestName}</span>
            <span classList={{ 'text-emerald-400': h.status < 400, 'text-red-400': h.status >= 400 }}>{h.status}</span>
            <span class="text-neutral-600">{h.timingMs}ms</span>
            <span class="w-14 shrink-0 text-right text-neutral-600" title={new Date(h.timestamp).toLocaleString()}>
              {relativeTime(h.timestamp)}
            </span>
          </button>
        )}
      </For>
    </div>
  )
}
