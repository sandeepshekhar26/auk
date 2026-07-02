import { For, Show, createMemo } from 'solid-js'
import { appState, openTab, sidebarFilter, setSidebarFilter } from '../lib/store'

export default function Sidebar() {
  const filteredRequests = createMemo(() => {
    const q = sidebarFilter().trim().toLowerCase()
    if (!q) return appState.requests
    return appState.requests.filter((r) => r.name.toLowerCase().includes(q) || r.url.toLowerCase().includes(q))
  })

  return (
    <div class="flex h-full w-64 flex-col border-r border-neutral-800 bg-neutral-925">
      <div class="p-2">
        <input
          class="w-full rounded bg-neutral-900 px-2 py-1 text-sm text-neutral-200 placeholder:text-neutral-500 focus:outline-none focus:ring-1 focus:ring-neutral-600"
          placeholder="Filter requests…"
          value={sidebarFilter()}
          onInput={(e) => setSidebarFilter(e.currentTarget.value)}
        />
      </div>
      <div class="flex-1 overflow-y-auto px-1 pb-2">
        <Show when={filteredRequests().length === 0}>
          <p class="px-2 py-4 text-xs text-neutral-600">No requests yet. Press ⌘N to create one.</p>
        </Show>
        <For each={filteredRequests()}>
          {(req) => (
            <button
              class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-sm text-neutral-300 hover:bg-neutral-800"
              onClick={() => openTab(req.id)}
            >
              <span class="w-12 shrink-0 font-mono text-[10px] font-semibold text-emerald-400">{req.method}</span>
              <span class="truncate">{req.name}</span>
            </button>
          )}
        </For>
      </div>
    </div>
  )
}
