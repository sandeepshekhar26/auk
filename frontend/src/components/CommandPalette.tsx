import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import { appState, commandPaletteOpen, setCommandPaletteOpen, openTab } from '../lib/store'
import type { CommandItem } from '../types'

export default function CommandPalette() {
  const [query, setQuery] = createSignal('')
  let inputRef: HTMLInputElement | undefined

  const items = createMemo<CommandItem[]>(() => {
    const requestItems: CommandItem[] = appState.requests.map((r) => ({
      id: `req:${r.id}`,
      title: r.name,
      subtitle: r.url,
      group: 'request',
      run: () => openTab(r.id),
    }))
    const q = query().trim().toLowerCase()
    if (!q) return requestItems
    return requestItems.filter((i) => i.title.toLowerCase().includes(q) || i.subtitle?.toLowerCase().includes(q))
  })

  function close() {
    setCommandPaletteOpen(false)
    setQuery('')
  }

  function onKeyDown(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault()
      setCommandPaletteOpen((v) => !v)
    }
    if (e.key === 'Escape' && commandPaletteOpen()) close()
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  return (
    <Show when={commandPaletteOpen()}>
      <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-32" onClick={close}>
        <div
          class="w-full max-w-lg overflow-hidden rounded-lg border border-neutral-700 bg-neutral-900 shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <input
            ref={inputRef}
            autofocus
            class="w-full border-b border-neutral-800 bg-transparent px-4 py-3 text-sm text-neutral-100 focus:outline-none"
            placeholder="Jump to a request, run a command…"
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
          />
          <div class="max-h-80 overflow-y-auto py-1">
            <For each={items()} fallback={<p class="px-4 py-3 text-sm text-neutral-600">No matches</p>}>
              {(item) => (
                <button
                  class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-neutral-300 hover:bg-neutral-800"
                  onClick={() => {
                    item.run()
                    close()
                  }}
                >
                  <span>{item.title}</span>
                  <span class="text-xs text-neutral-600">{item.subtitle}</span>
                </button>
              )}
            </For>
          </div>
        </div>
      </div>
    </Show>
  )
}
