import { For, Show } from 'solid-js'
import { appState } from '../lib/store'

export default function HistoryPanel() {
  return (
    <div class="flex h-full flex-col overflow-y-auto p-2 text-xs">
      <Show when={appState.history.length === 0}>
        <p class="px-2 py-4 text-neutral-600">No requests sent yet.</p>
      </Show>
      <For each={appState.history}>
        {(h) => (
          <div class="flex items-center gap-2 rounded px-2 py-1 hover:bg-neutral-800">
            <span class="font-mono font-semibold text-neutral-400">{h.method}</span>
            <span class="flex-1 truncate text-neutral-300">{h.requestName}</span>
            <span classList={{ 'text-emerald-400': h.status < 400, 'text-red-400': h.status >= 400 }}>{h.status}</span>
            <span class="text-neutral-600">{h.timingMs}ms</span>
          </div>
        )}
      </For>
    </div>
  )
}
