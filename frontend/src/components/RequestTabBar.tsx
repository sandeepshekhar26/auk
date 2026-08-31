import { For, Show } from 'solid-js'
import { appState, setAppState, closeTab } from '../lib/store'
import { MethodBadge } from './icons'

// Pill tabs, not underline or boxy browser tabs: the active tab is a filled
// chip floating in the strip, matching how every other selected thing in the
// redesign reads (rail item, segmented control, sub-tab). Collapses to
// nothing when no request is open, so the launch screen isn't fighting an
// empty tab bar for attention.
export default function RequestTabBar() {
  const requestById = (id: string) => appState.requests.find((r) => r.id === id)

  return (
    <Show when={appState.openTabIds.length > 0}>
      <div class="flex h-10 shrink-0 items-center gap-1 overflow-x-auto border-b border-edge px-1.5">
        <For each={appState.openTabIds}>
          {(id) => {
            const req = () => requestById(id)
            const active = () => appState.activeTabId === id
            return (
              <Show when={req()}>
                {(r) => (
                  <div
                    class="group flex h-7 min-w-[8rem] max-w-[13rem] cursor-pointer items-center gap-1.5 rounded-lg px-2.5 text-[12.5px]"
                    classList={{
                      'bg-field font-semibold text-ink': active(),
                      'font-normal text-ink-muted hover:bg-raised/50 hover:text-ink-dim': !active(),
                    }}
                    onClick={() => setAppState('activeTabId', id)}
                  >
                    <MethodBadge method={r().method} protocol={r().protocol} />
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
    </Show>
  )
}
