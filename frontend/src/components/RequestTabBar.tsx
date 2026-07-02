import { For, Show } from 'solid-js'
import { appState, setAppState, closeTab } from '../lib/store'

export default function RequestTabBar() {
  const requestById = (id: string) => appState.requests.find((r) => r.id === id)

  return (
    <div class="flex h-9 items-stretch overflow-x-auto border-b border-edge bg-app">
      <For each={appState.openTabIds}>
        {(id) => {
          const req = () => requestById(id)
          const active = () => appState.activeTabId === id
          return (
            <Show when={req()}>
              {(r) => (
                <div
                  class="group flex min-w-[9rem] max-w-[14rem] cursor-pointer items-center gap-2 border-r border-edge px-3 text-sm"
                  classList={{ 'bg-field text-ink': active(), 'text-ink-muted hover:bg-field/50': !active() }}
                  onClick={() => setAppState('activeTabId', id)}
                >
                  <span class="truncate">{r().name}</span>
                  <button
                    class="ml-auto shrink-0 rounded px-1 text-ink-faint opacity-0 hover:bg-raised hover:text-ink-dim group-hover:opacity-100"
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
