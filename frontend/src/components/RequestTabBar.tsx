import { For, Show } from 'solid-js'
import { appState, setAppState, closeTab } from '../lib/store'

export default function RequestTabBar() {
  const requestById = (id: string) => appState.requests.find((r) => r.id === id)

  return (
    <div class="flex h-9 items-stretch overflow-x-auto border-b border-neutral-800 bg-neutral-950">
      <For each={appState.openTabIds}>
        {(id) => {
          const req = () => requestById(id)
          const active = () => appState.activeTabId === id
          return (
            <Show when={req()}>
              {(r) => (
                <div
                  class="group flex min-w-[9rem] max-w-[14rem] cursor-pointer items-center gap-2 border-r border-neutral-800 px-3 text-sm"
                  classList={{ 'bg-neutral-900 text-neutral-100': active(), 'text-neutral-500 hover:bg-neutral-900/50': !active() }}
                  onClick={() => setAppState('activeTabId', id)}
                >
                  <span class="truncate">{r().name}</span>
                  <button
                    class="ml-auto shrink-0 rounded px-1 text-neutral-600 opacity-0 hover:bg-neutral-800 hover:text-neutral-300 group-hover:opacity-100"
                    onClick={(e) => {
                      e.stopPropagation()
                      closeTab(id)
                    }}
                  >
                    ×
                  </button>
                </div>
              )}
            </Show>
          )
        }}
      </For>
    </div>
  )
}
