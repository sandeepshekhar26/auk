// Bridges the Go bindings (frontend/wailsjs/go/main/App) into the Solid
// store. This is the piece that was missing end-to-end: components read
// appState, but nothing ever called ListWorkspaces/ListRequests/etc. to put
// real data into it, so the app opened to a permanently empty shell with no
// way to create anything either.
import { appState, setAppState, openTab } from './store'
import { models, wails } from './wails'
import type { model as wailsModel } from '../../wailsjs/go/models'
import type { Environment, Folder, RequestDef } from '../types'

let saveTimers = new Map<string, ReturnType<typeof setTimeout>>()

// Wails' generated model classes represent Go's omitempty nullable-pointer
// fields as `?: T` (possibly undefined); our hand-written types.ts (used
// throughout the rest of the app) uses `T | null` instead. Normalize at
// this one boundary rather than letting `undefined` leak into components.
function normalizeRequest(r: wailsModel.RequestDef): RequestDef {
  // Go's BodyKind/AuthKind are string-backed enums; Wails' TS generator
  // widens them to `string` since it doesn't preserve the underlying enum
  // literals. The Go side only ever produces valid enum values, so this
  // narrowing cast is safe.
  return {
    ...r,
    protocol: r.protocol as RequestDef['protocol'],
    folderId: r.folderId ?? null,
    body: (r.body ?? null) as RequestDef['body'],
    authRef: (r.authRef ?? null) as RequestDef['authRef'],
    perf: (r.perf ?? null) as RequestDef['perf'],
  }
}
function normalizeFolder(f: wailsModel.Folder): Folder {
  return { ...f, parentId: f.parentId ?? null }
}
function normalizeEnvironment(e: wailsModel.Environment): Environment {
  return { ...e, color: e.color ?? null }
}

/** Loads the workspace list and, if none is active yet, selects the first one. */
export async function loadWorkspaces(): Promise<void> {
  const workspaces = await wails.ListWorkspaces()
  setAppState('workspaces', workspaces ?? [])
  if (!appState.activeWorkspaceId && workspaces?.length) {
    setAppState('activeWorkspaceId', workspaces[0].id)
  }
}

/** Loads everything scoped to one workspace: requests, folders, environments. */
export async function loadWorkspaceData(workspaceId: string): Promise<void> {
  const [requests, folders, environments] = await Promise.all([
    wails.ListRequests(workspaceId),
    wails.ListFolders(workspaceId),
    wails.ListEnvironments(workspaceId),
  ])
  setAppState('requests', (requests ?? []).map(normalizeRequest))
  setAppState('folders', (folders ?? []).map(normalizeFolder))
  setAppState('environments', (environments ?? []).map(normalizeEnvironment))
}

/** History is global (not workspace-scoped) — see internal/storage/history.go. */
export async function loadHistory(): Promise<void> {
  const history = await wails.ListHistory()
  setAppState('history', history ?? [])
}

export async function loadAll(): Promise<void> {
  await loadWorkspaces()
  if (appState.activeWorkspaceId) {
    await loadWorkspaceData(appState.activeWorkspaceId)
  }
  await loadHistory()
}

/** Creates a new blank request in the active workspace, persists it, and opens it as a tab. */
export async function createRequest(): Promise<void> {
  if (!appState.activeWorkspaceId) return

  const draft: RequestDef = {
    id: crypto.randomUUID(),
    workspaceId: appState.activeWorkspaceId,
    folderId: null,
    name: 'New Request',
    protocol: 'http',
    method: 'GET',
    url: '',
    headers: [],
    params: [],
    body: null,
    authRef: null,
    orderKey: '',
  }

  await wails.CreateRequest(models.RequestDef.createFrom(draft))
  await loadWorkspaceData(appState.activeWorkspaceId)
  openTab(draft.id)
}

/**
 * Persists an edited request, debounced per-request-id so rapid keystrokes
 * (typing a URL, editing a header) don't fire one backend call each — this
 * is what makes RequestEditor's edits survive a reload instead of being
 * purely local Solid-store state.
 */
export function saveRequestDebounced(req: RequestDef, delayMs = 400): void {
  const existing = saveTimers.get(req.id)
  if (existing) clearTimeout(existing)
  saveTimers.set(
    req.id,
    setTimeout(() => {
      saveTimers.delete(req.id)
      void wails.UpdateRequest(models.RequestDef.createFrom(req))
    }, delayMs),
  )
}
