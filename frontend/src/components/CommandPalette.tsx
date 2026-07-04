import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import {
  appState,
  commandPaletteOpen,
  setCommandPaletteOpen,
  openTab,
  setShortcutSheetOpen,
  setSettingsOpen,
  setImportModalOpen,
  streamConsoleOpen,
  setStreamConsoleOpen,
  openExplorer,
} from '../lib/store'
import { createRequest } from '../lib/data'
import { setTheme } from '../lib/theme'
import type { CommandItem } from '../types'

const GROUP_LABEL: Record<CommandItem['group'], string> = {
  action: 'Actions',
  request: 'Requests',
  navigation: 'Navigate',
}

// The command palette is the app's home base, not a bolted-on jump-to —
// everything reachable elsewhere (new request, import, settings, explorer,
// theme) is reachable here too, so ⌘K is a complete substitute for clicking
// around a sidebar (docs/05-ux-north-star.md).
export default function CommandPalette() {
  const [query, setQuery] = createSignal('')
  let inputRef: HTMLInputElement | undefined

  const items = createMemo<CommandItem[]>(() => {
    const actionItems: CommandItem[] = [
      { id: 'action:new-request', title: 'New Request', subtitle: '⌘N', group: 'action', run: () => void createRequest() },
      {
        id: 'action:browse-requests',
        title: 'Browse Requests',
        subtitle: '⌘B',
        group: 'action',
        run: () => openExplorer('requests'),
      },
      { id: 'action:browse-history', title: 'Browse History', group: 'action', run: () => openExplorer('history') },
      { id: 'action:import', title: 'Import…', group: 'action', run: () => setImportModalOpen(true) },
      {
        id: 'action:stream-console',
        title: streamConsoleOpen() ? 'Hide Stream Console' : 'Show Stream Console',
        group: 'action',
        run: () => setStreamConsoleOpen((v) => !v),
      },
      { id: 'action:open-settings', title: 'Open Settings', subtitle: '⌘,', group: 'action', run: () => setSettingsOpen(true) },
      { id: 'action:theme-system', title: 'Theme: System', group: 'action', run: () => setTheme('system') },
      { id: 'action:theme-light', title: 'Theme: Light', group: 'action', run: () => setTheme('light') },
      { id: 'action:theme-dark', title: 'Theme: Dark', group: 'action', run: () => setTheme('dark') },
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

  // Grouped for display so the palette reads as organized surfaces, not a
  // flat dump — Actions first (what you'd reach for), Requests below.
  const grouped = createMemo(() => {
    const out = new Map<CommandItem['group'], CommandItem[]>()
    for (const item of items()) {
      const list = out.get(item.group) ?? []
      list.push(item)
      out.set(item.group, list)
    }
    return out
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
            class="w-full border-b border-edge bg-transparent px-4 py-3 text-base text-ink focus:outline-none"
            placeholder="Jump to a request, run a command…"
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
          />
          <div class="max-h-96 overflow-y-auto py-1">
            <For each={[...grouped().entries()]} fallback={<p class="px-4 py-3 text-sm text-ink-faint">No matches</p>}>
              {([group, groupItems]) => (
                <div>
                  <p class="px-4 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
                    {GROUP_LABEL[group]}
                  </p>
                  <For each={groupItems}>
                    {(item) => (
                      <button
                        class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-ink-dim hover:bg-raised"
                        onClick={() => {
                          item.run()
                          close()
                        }}
                      >
                        <span>{item.title}</span>
                        <span class="ml-4 shrink-0 truncate text-xs text-ink-faint">{item.subtitle}</span>
                      </button>
                    )}
                  </For>
                </div>
              )}
            </For>
          </div>
        </div>
      </div>
    </Show>
  )
}
