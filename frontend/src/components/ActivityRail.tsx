import { createMemo } from 'solid-js'
import {
  appState,
  commandPaletteOpen,
  setCommandPaletteOpen,
  explorerOpen,
  explorerTab,
  openExplorer,
  setExplorerOpen,
  setSettingsOpen,
} from '../lib/store'
import { createRequest } from '../lib/data'

// ActivityRail is the entire "always visible" chrome on the left: a slim
// 48px icon strip, not a docked 260px tree. Everything the tree/history used
// to hold permanently now lives one click (or ⌘B / ⌘K) away in the explorer
// drawer or the command palette — this is the structural break from the
// Postman/Insomnia/Yaak "permanent sidebar" convention (docs/05-ux-north-star.md).
export default function ActivityRail() {
  const workspaceInitial = createMemo(() => {
    const ws = appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)
    return (ws?.name ?? '?').trim().slice(0, 1).toUpperCase()
  })

  function railButtonClasses(active: boolean) {
    return active
      ? 'flex h-9 w-9 items-center justify-center rounded-md bg-raised text-ink'
      : 'flex h-9 w-9 items-center justify-center rounded-md text-ink-muted hover:bg-raised hover:text-ink-dim'
  }

  return (
    <div class="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-r border-edge bg-surface py-2">
      <div
        class="mb-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-accent text-xs font-bold text-accent-contrast"
        title={appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)?.name ?? 'Workspace'}
      >
        {workspaceInitial()}
      </div>

      <button
        class={railButtonClasses(commandPaletteOpen())}
        title="Command palette (⌘K)"
        onClick={() => setCommandPaletteOpen(true)}
      >
        <span class="text-base leading-none">⌕</span>
      </button>

      <button
        class={railButtonClasses(explorerOpen() && explorerTab() === 'requests')}
        title="Requests (⌘B)"
        onClick={() => (explorerOpen() && explorerTab() === 'requests' ? setExplorerOpen(false) : openExplorer('requests'))}
      >
        <span class="text-base leading-none">▤</span>
      </button>

      <button
        class={railButtonClasses(explorerOpen() && explorerTab() === 'history')}
        title="History"
        onClick={() => (explorerOpen() && explorerTab() === 'history' ? setExplorerOpen(false) : openExplorer('history'))}
      >
        <span class="text-base leading-none">◷</span>
      </button>

      <button
        class={railButtonClasses(explorerOpen() && explorerTab() === 'git')}
        title="Git"
        onClick={() => (explorerOpen() && explorerTab() === 'git' ? setExplorerOpen(false) : openExplorer('git'))}
      >
        <span class="text-base leading-none">⎇</span>
      </button>

      <button class={railButtonClasses(false)} title="New request (⌘N)" onClick={() => void createRequest()}>
        <span class="text-base leading-none">+</span>
      </button>

      <div class="flex-1" />

      <button class={railButtonClasses(false)} title="Settings (⌘,)" onClick={() => setSettingsOpen(true)}>
        <span class="text-base leading-none">⚙</span>
      </button>
    </div>
  )
}
