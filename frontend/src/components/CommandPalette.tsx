import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import { appState, commandPaletteOpen, setCommandPaletteOpen, openTab, setShortcutSheetOpen, setSettingsOpen } from '../lib/store'
import { createRequest } from '../lib/data'
import { setTheme } from '../lib/theme'
import type { CommandItem } from '../types'

export default function CommandPalette() {
  const [query, setQuery] = createSignal('')
  let inputRef: HTMLInputElement | undefined

  const items = createMemo<CommandItem[]>(() => {
    const actionItems: CommandItem[] = [
      {
        id: 'action:new-request',
        title: 'New Request',
        subtitle: '⌘N',
        group: 'action',
        run: () => void createRequest(),
      },
      {
        id: 'action:open-settings',
        title: 'Open Settings',
        subtitle: '⌘,',
        group: 'action',
        run: () => setSettingsOpen(true),
      },
      {
        id: 'action:theme-system',
        title: 'Theme: System',
        group: 'action',
        run: () => setTheme('system'),
      },
      {
        id: 'action:theme-light',
        title: 'Theme: Light',
        group: 'action',
        run: () => setTheme('light'),
      },
      {
        id: 'action:theme-dark',
        title: 'Theme: Dark',
        group: 'action',
        run: () => setTheme('dark'),
      },
    ]
    const requestItems: CommandItem[] = appState.requests.map((r) => ({
      id: `req:${r.id}`,
      title: r.name,
      subtitle: r.url,
      group: 'request',
      run: () => openTab(r.id),
    }))
    const all = [...actionItems, ...requestItems]
    const q = query().trim().toLowerCase()
    if (!q) return all
    return all.filter((i) => i.title.toLowerCase().includes(q) || i.subtitle?.toLowerCase().includes(q))
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
    if ((e.metaKey || e.ctrlKey) && e.key === '/') {
      e.preventDefault()
      setShortcutSheetOpen((v) => !v)
    }
    if (e.key === 'Escape' && commandPaletteOpen()) close()
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  return (
    <Show when={commandPaletteOpen()}>
      <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-32" onClick={close}>
        <div
          class="w-full max-w-lg overflow-hidden rounded-lg border border-edge-strong bg-field shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <input
            ref={inputRef}
            autofocus
            class="w-full border-b border-edge bg-transparent px-4 py-3 text-sm text-ink focus:outline-none"
            placeholder="Jump to a request, run a command…"
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
          />
          <div class="max-h-80 overflow-y-auto py-1">
            <For each={items()} fallback={<p class="px-4 py-3 text-sm text-ink-faint">No matches</p>}>
              {(item) => (
                <button
                  class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-ink-dim hover:bg-raised"
                  onClick={() => {
                    item.run()
                    close()
                  }}
                >
                  <span>{item.title}</span>
                  <span class="text-xs text-ink-faint">{item.subtitle}</span>
                </button>
              )}
            </For>
          </div>
        </div>
      </div>
    </Show>
  )
}
