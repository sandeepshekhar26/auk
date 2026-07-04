import { createStore } from 'solid-js/store'
import { createSignal } from 'solid-js'
import type { Environment, Folder, HistoryEntry, RequestDef, StreamEvent, Workspace } from '../types'

export interface AppState {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  folders: Folder[]
  requests: RequestDef[]
  environments: Environment[]
  activeEnvironmentId: string | null
  openTabIds: string[]
  activeTabId: string | null
  history: HistoryEntry[]
  streamEvents: StreamEvent[]
}

export const [appState, setAppState] = createStore<AppState>({
  workspaces: [],
  activeWorkspaceId: null,
  folders: [],
  requests: [],
  environments: [],
  activeEnvironmentId: null,
  openTabIds: [],
  activeTabId: null,
  history: [],
  streamEvents: [],
})

export const [commandPaletteOpen, setCommandPaletteOpen] = createSignal(false)
export const [sidebarFilter, setSidebarFilter] = createSignal('')

// The "explorer" is the on-demand drawer (requests tree + history) that
// replaces a permanently-docked sidebar — collapsed by default. ⌘B, the rail,
// and the command palette all toggle the same signal; explorerTab picks
// which section shows when it opens.
export type ExplorerTab = 'requests' | 'history'
export const [explorerOpen, setExplorerOpen] = createSignal(false)
export const [explorerTab, setExplorerTab] = createSignal<ExplorerTab>('requests')

export function openExplorer(tab: ExplorerTab) {
  setExplorerTab(tab)
  setExplorerOpen(true)
}

// UI-only open/closed flags for modals/panels. Components read+write these
// directly; they do NOT own or duplicate this state locally, so multiple
// components (e.g. EnvironmentSelector's "Manage" button and
// EnvironmentEditor itself) can toggle the same panel without prop drilling.
export const [environmentEditorOpen, setEnvironmentEditorOpen] = createSignal(false)
export const [importModalOpen, setImportModalOpen] = createSignal(false)
export const [shortcutSheetOpen, setShortcutSheetOpen] = createSignal(false)
export const [streamConsoleOpen, setStreamConsoleOpen] = createSignal(false)
export const [settingsOpen, setSettingsOpen] = createSignal(false)

// Pending MCP approval prompts (agent-initiated mutating requests waiting on
// Allow/Deny). A queue: multiple agent calls can stack up.
export interface MCPApproval {
  id: string
  method: string
  url: string
}
export const [mcpApprovals, setMcpApprovals] = createSignal<MCPApproval[]>([])

export function openTab(requestId: string) {
  setAppState('openTabIds', (ids) => (ids.includes(requestId) ? ids : [...ids, requestId]))
  setAppState('activeTabId', requestId)
}

export function closeTab(requestId: string) {
  setAppState('openTabIds', (ids) => ids.filter((id) => id !== requestId))
  setAppState('activeTabId', (current) => {
    if (current !== requestId) return current
    const remaining = appState.openTabIds.filter((id) => id !== requestId)
    return remaining[remaining.length - 1] ?? null
  })
}

export function pushStreamEvent(evt: StreamEvent) {
  setAppState('streamEvents', (events) => [...events.slice(-499), evt])
}
