import { createStore } from 'solid-js/store'
import { createSignal } from 'solid-js'
import { models, wails } from './wails'
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

// The sidebar ("explorer") holds five sections — requests tree, history,
// git, MCP, cookies — the rail picks which one shows (explorerTab), and
// explorerOpen is whether the sidebar is visible at all. It has two modes
// (docs/05-ux-north-star.md "Navigation model"):
//   - "docked" (default): a persistent, resizable panel in the layout,
//     open on launch; explorerOpen=false means collapsed to zero width.
//     Picking a request does NOT close it.
//   - "overlay": the original on-demand slide-over drawer — closed by
//     default, auto-closes when a request is picked, Escape dismisses.
// ⌘B, the rail, and the command palette all toggle the same explorerOpen
// signal in either mode.
export type ExplorerTab = 'requests' | 'history' | 'git' | 'mcp' | 'cookies'
export const [explorerOpen, setExplorerOpen] = createSignal(true)
export const [explorerTab, setExplorerTab] = createSignal<ExplorerTab>('requests')

export function openExplorer(tab: ExplorerTab) {
  setExplorerTab(tab)
  setExplorerOpen(true)
}

export type SidebarMode = 'docked' | 'overlay'
export const [sidebarMode, setSidebarModeSignal] = createSignal<SidebarMode>('docked')

// Sidebar width is a per-machine layout preference, so it lives in
// localStorage (like a window size), not in settings.yaml with the
// cross-cutting preferences.
const SIDEBAR_WIDTH_KEY = 'auk.sidebarWidth'
export const SIDEBAR_MIN_WIDTH = 220
export const SIDEBAR_MAX_WIDTH = 400
const SIDEBAR_DEFAULT_WIDTH = 280

function loadStoredSidebarWidth(): number {
  try {
    const raw = Number(localStorage.getItem(SIDEBAR_WIDTH_KEY))
    if (Number.isFinite(raw) && raw >= SIDEBAR_MIN_WIDTH && raw <= SIDEBAR_MAX_WIDTH) return raw
  } catch {
    /* storage unavailable — fall through to the default */
  }
  return SIDEBAR_DEFAULT_WIDTH
}

export const [sidebarWidth, setSidebarWidthSignal] = createSignal(loadStoredSidebarWidth())

export function setSidebarWidth(px: number): void {
  const clamped = Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, Math.round(px)))
  setSidebarWidthSignal(clamped)
  try {
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(clamped))
  } catch {
    /* width just won't survive a restart */
  }
}

/**
 * Loads the persisted sidebar mode from app settings. Called once on app
 * mount. The signals default to docked+open, so the common case paints
 * correctly before settings arrive; only an explicit "overlay" preference
 * changes anything (and closes the drawer, since overlay starts closed).
 */
export async function initSidebar(): Promise<void> {
  try {
    const settings = await wails.GetSettings()
    if (settings?.sidebarMode === 'overlay') {
      setSidebarModeSignal('overlay')
      setExplorerOpen(false)
    }
  } catch {
    /* unreadable settings — keep the docked default */
  }
}

// All persisted settings mutations funnel through this single queue so two
// quick toggles (e.g. unpin the sidebar, then pick a theme) can't interleave
// their read-modify-write of the shared settings file and clobber each other's
// field. Each GetSettings -> mutate -> UpdateSettings runs only after the
// previous call has fully settled. Callers still set their in-memory Solid
// signal optimistically for instant UI — only the persistence is serialized
// here. Keep the whole-object discipline in the mutate fn: UpdateSettings
// overwrites the file, so writing only one field would clobber the others.
type PersistedSettings = Awaited<ReturnType<typeof wails.GetSettings>>
let settingsQueue: Promise<void> = Promise.resolve()
export function mutateSettings(mutate: (s: PersistedSettings) => PersistedSettings): Promise<void> {
  settingsQueue = settingsQueue.then(async () => {
    try {
      const current = await wails.GetSettings()
      await wails.UpdateSettings(mutate(current))
    } catch {
      /* persist failure is non-fatal — the in-memory signal already applied */
    }
  })
  return settingsQueue
}

/**
 * Applies + persists the sidebar mode. Read-modify-write of the WHOLE
 * settings object (same rule as theme.ts setTheme): UpdateSettings
 * overwrites the file, so sending only {sidebarMode} would clobber
 * theme/MCP preferences. Serialized through mutateSettings so it can't
 * interleave with a concurrent setTheme write.
 */
export function setSidebarMode(mode: SidebarMode): void {
  setSidebarModeSignal(mode)
  // Docking implies "show it"; switching to overlay keeps it open so the
  // user sees the result of the toggle they just clicked (it will close
  // itself on the next pick / Escape, per overlay behavior).
  if (mode === 'docked') setExplorerOpen(true)
  void mutateSettings((s) => models.AppSettings.createFrom({ ...s, sidebarMode: mode }))
}

// UI-only open/closed flags for modals/panels. Components read+write these
// directly; they do NOT own or duplicate this state locally, so multiple
// components (e.g. EnvironmentSelector's "Manage" button and
// EnvironmentEditor itself) can toggle the same panel without prop drilling.
export const [environmentEditorOpen, setEnvironmentEditorOpen] = createSignal(false)
export const [importModalOpen, setImportModalOpen] = createSignal(false)
export const [migrateModalOpen, setMigrateModalOpen] = createSignal(false)
// Request-editor and response tab selection live in the STORE, not inside the
// two components, for one reason: the keyboard layer must be able to drive
// them. While each component owned its own createSignal, "switch to the Auth
// tab" was unreachable from anywhere else — which is precisely why an app that
// calls itself keyboard-first had no shortcut for the tabs its users spend all
// day in. See lib/keymap.ts.
export type EditorTab = 'params' | 'headers' | 'body' | 'auth' | 'script' | 'assert' | 'perf'
export const EDITOR_TABS: { id: EditorTab; label: string }[] = [
  { id: 'params', label: 'Params' },
  { id: 'headers', label: 'Headers' },
  { id: 'body', label: 'Body' },
  { id: 'auth', label: 'Auth' },
  { id: 'script', label: 'Script' },
  { id: 'assert', label: 'Assert' },
  { id: 'perf', label: 'Perf' },
]
export const [editorTab, setEditorTab] = createSignal<EditorTab>('params')

export type ResponseTab = 'body' | 'headers' | 'timing'
export const [responseTab, setResponseTab] = createSignal<ResponseTab>('body')

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
