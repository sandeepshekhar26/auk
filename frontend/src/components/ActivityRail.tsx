import { createMemo } from 'solid-js'
import {
  appState,
  commandPaletteOpen,
  setCommandPaletteOpen,
  explorerOpen,
  explorerTab,
  openExplorer,
  setExplorerOpen,
  setImportModalOpen,
  setSettingsOpen,
} from '../lib/store'
import { createRequest } from '../lib/data'
import Tooltip from './Tooltip'

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
  const workspaceName = createMemo(() => appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)?.name ?? 'Workspace')

  function railButtonClasses(active: boolean) {
    return active
      ? 'flex h-9 w-9 items-center justify-center rounded-md bg-raised text-ink'
      : 'flex h-9 w-9 items-center justify-center rounded-md text-ink-muted hover:bg-raised hover:text-ink-dim'
  }

  return (
    <div class="flex h-full w-12 shrink-0 flex-col items-center gap-1 border-r border-edge bg-surface py-2">
      <Tooltip text={workspaceName()}>
        <div
          class="mb-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-accent text-xs font-bold text-accent-contrast"
          title={workspaceName()}
        >
          {workspaceInitial()}
        </div>
      </Tooltip>

      <Tooltip text="Command palette (⌘K)">
        <button
          class={railButtonClasses(commandPaletteOpen())}
          title="Command palette (⌘K)"
          onClick={() => setCommandPaletteOpen(true)}
        >
          <span class="text-base leading-none">⌕</span>
        </button>
      </Tooltip>

      <Tooltip text="Requests (⌘B)">
        <button
          class={railButtonClasses(explorerOpen() && explorerTab() === 'requests')}
          title="Requests (⌘B)"
          onClick={() => (explorerOpen() && explorerTab() === 'requests' ? setExplorerOpen(false) : openExplorer('requests'))}
        >
          <span class="text-base leading-none">▤</span>
        </button>
      </Tooltip>

      <Tooltip text="History">
        <button
          class={railButtonClasses(explorerOpen() && explorerTab() === 'history')}
          title="History"
          onClick={() => (explorerOpen() && explorerTab() === 'history' ? setExplorerOpen(false) : openExplorer('history'))}
        >
          <span class="text-base leading-none">◷</span>
        </button>
      </Tooltip>

      <Tooltip text="Git">
        <button
          class={railButtonClasses(explorerOpen() && explorerTab() === 'git')}
          title="Git"
          onClick={() => (explorerOpen() && explorerTab() === 'git' ? setExplorerOpen(false) : openExplorer('git'))}
        >
          <span class="text-base leading-none">⎇</span>
        </button>
      </Tooltip>

      <Tooltip text="MCP tool debugger">
        <button
          class={railButtonClasses(explorerOpen() && explorerTab() === 'mcp')}
          title="MCP tool debugger"
          onClick={() => (explorerOpen() && explorerTab() === 'mcp' ? setExplorerOpen(false) : openExplorer('mcp'))}
        >
          <span class="text-base leading-none">◈</span>
        </button>
      </Tooltip>

      <Tooltip text="Cookies">
        <button
          class={railButtonClasses(explorerOpen() && explorerTab() === 'cookies')}
          title="Cookies"
          onClick={() => (explorerOpen() && explorerTab() === 'cookies' ? setExplorerOpen(false) : openExplorer('cookies'))}
        >
          <span class="text-base leading-none">◍</span>
        </button>
      </Tooltip>

      <Tooltip text="New request (⌘N)">
        <button class={railButtonClasses(false)} title="New request (⌘N)" onClick={() => void createRequest()}>
          <span class="text-base leading-none">+</span>
        </button>
      </Tooltip>

      <Tooltip text="Import cURL / OpenAPI / Postman">
        <button class={railButtonClasses(false)} title="Import cURL / OpenAPI / Postman" onClick={() => setImportModalOpen(true)}>
          <span class="text-base leading-none">⇩</span>
        </button>
      </Tooltip>

      <div class="flex-1" />

      <Tooltip text="Settings (⌘,)">
        <button class={railButtonClasses(false)} title="Settings (⌘,)" onClick={() => setSettingsOpen(true)}>
          <span class="text-base leading-none">⚙</span>
        </button>
      </Tooltip>
    </div>
  )
}
