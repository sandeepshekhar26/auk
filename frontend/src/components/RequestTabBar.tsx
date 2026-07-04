import { For, Show } from 'solid-js'
import { appState, setAppState, closeTab } from '../lib/store'

// A light, underline-style strip — deliberately NOT the boxy bordered
// "browser tabs" Postman/Insomnia/Yaak all use. Collapses to nothing when no
// request is open, so the single-focus empty state isn't fighting an empty
// tab bar for attention.
export default function RequestTabBar() {
  const requestById = (id: string) => appState.requests.find((r) => r.id === id)

  return (
    <Show when={appState.openTabIds.length > 0}>
      <div class="flex h-9 items-stretch gap-1 overflow-x-auto border-b border-edge px-2">
        <For each={appState.openTabIds}>
          {(id) => {
            const req = () => requestById(id)
            const active = () => appState.activeTabId === id
            return (
              <Show when={req()}>
                {(r) => (
                  <div
                    class="group relative flex min-w-[8rem] max-w-[13rem] cursor-pointer items-center gap-1.5 px-2 text-sm"
                    classList={{ 'text-ink': active(), 'text-ink-muted hover:text-ink-dim': !active() }}
                    onClick={() => setAppState('activeTabId', id)}
                  >
                    <span class="shrink-0 font-mono text-[10px] font-semibold text-accent-fg">{r().method}</span>
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
                    <Show when={active()}>
                      <span class="absolute inset-x-2 -bottom-px h-px bg-accent" />
                    </Show>
                  </div>
                )}
              </Show>
            )
          }}
        </For>
      </div>
    </Show>
  )
}
