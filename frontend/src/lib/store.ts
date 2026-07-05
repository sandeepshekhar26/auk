import { createStore } from 'solid-js/store'
import { createSignal } from 'solid-js'
import type { Environment, Folder, FolderRunResult, HistoryEntry, McpConnection, RequestDef, StreamEvent, Workspace } from '../types'

export interface AppState {
  workspaces: Workspace[]
  activeWorkspaceId: string | null
  folders: Folder[]
  requests: RequestDef[]
  environments: Environment[]
  activeEnvironmentId: string | null
  mcpConnections: McpConnection[]
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
  mcpConnections: [],
  openTabIds: [],
  activeTabId: null,
  history: [],
  streamEvents: [],
})

// Selecting an MCP tool to test takes over the SAME main-area real estate
// RequestEditor+ResponseViewer normally occupy (full width, not cramped
// into the drawer) — mutually exclusive with the request tab bar's
// activeTabId, not layered on top of it. openTab (below) clears this so
// picking a request switches back automatically.
export interface McpToolViewTarget {
  connectionId: string
  toolName: string
}
export const [mcpToolView, setMcpToolView] = createSignal<McpToolViewTarget | null>(null)

// A "Run folder" click takes over the SAME main-area real estate as
// mcpToolView above (full width — an aggregate results list deserves more
// room than the drawer), for the same reason and via the same
// mutually-exclusive-with-activeTabId mechanism (openTab below clears this).
export interface FolderRunViewTarget {
  folderId: string
  folderName: string
  running: boolean
  results: FolderRunResult[]
}
export const [folderRunView, setFolderRunView] = createSignal<FolderRunViewTarget | null>(null)

export const [commandPaletteOpen, setCommandPaletteOpen] = createSignal(false)
export const [sidebarFilter, setSidebarFilter] = createSignal('')

// The "explorer" is the on-demand drawer (requests tree + history) that
// replaces a permanently-docked sidebar — collapsed by default. ⌘B, the rail,
// and the command palette all toggle the same signal; explorerTab picks
// which section shows when it opens.
export type ExplorerTab = 'requests' | 'history' | 'git' | 'mcp' | 'cookies'
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

// App-wide dismissable error banner (rendered once, at the top of App.tsx).
// Originally local to App.tsx for its own load-time failures; lifted here so
// any component (e.g. CommandPalette's "Export Workspace…") can surface a
// failure through the same banner instead of each needing its own.
export const [loadError, setLoadError] = createSignal<string | null>(null)

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
  setMcpToolView(null)
  setFolderRunView(null)
}

export function closeTab(requestId: string) {
  setAppState('openTabIds', (ids) => ids.filter((id) => id !== requestId))
  setAppState('activeTabId', (current) => {
    if (current !== requestId) return current
    const remaining = appState.openTabIds.filter((id) => id !== requestId)
    return remaining[remaining.length - 1] ?? null
  })
}

/** Switches the active tab forward (1) or backward (-1) through openTabIds, wrapping around. No-op with 0-1 tabs open. */
export function cycleTab(direction: 1 | -1): void {
  const ids = appState.openTabIds
  if (ids.length < 2) return
  const currentIndex = appState.activeTabId ? ids.indexOf(appState.activeTabId) : -1
  const nextIndex = (currentIndex + direction + ids.length) % ids.length
  setAppState('activeTabId', ids[nextIndex])
}

export function pushStreamEvent(evt: StreamEvent) {
  setAppState('streamEvents', (events) => [...events.slice(-499), evt])
}

// Live WebSocket/SSE sessions started from the GUI, keyed by the request id
// that opened them → the backend session id. Lets a request's editor show
// Connect vs Disconnect (and a message composer) without threading the id
// through props; see lib/stream.ts for the session lifecycle.
export const [activeStreams, setActiveStreams] = createSignal<Record<string, string>>({})

export function setActiveStream(requestId: string, sessionId: string) {
  setActiveStreams((m) => ({ ...m, [requestId]: sessionId }))
}

export function clearActiveStream(requestId: string) {
  setActiveStreams((m) => {
    const next = { ...m }
    delete next[requestId]
    return next
  })
}
