import { createStore } from 'solid-js/store'
import { createSignal } from 'solid-js'
import type { Environment, Folder, HistoryEntry, RequestDef, Workspace } from '../types'

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
})

export const [commandPaletteOpen, setCommandPaletteOpen] = createSignal(false)
export const [sidebarFilter, setSidebarFilter] = createSignal('')

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
