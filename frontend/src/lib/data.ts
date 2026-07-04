// Bridges the Go bindings (frontend/wailsjs/go/main/App) into the Solid
// store. This is the piece that was missing end-to-end: components read
// appState, but nothing ever called ListWorkspaces/ListRequests/etc. to put
// real data into it, so the app opened to a permanently empty shell with no
// way to create anything either.
import { appState, setAppState, openTab } from './store'
import { models, wails } from './wails'
import type { model as wailsModel } from '../../wailsjs/go/models'
import type { Environment, Folder, McpConnection, RequestDef } from '../types'

let saveTimers = new Map<string, ReturnType<typeof setTimeout>>()

// Wails' generated model classes represent Go's omitempty nullable-pointer
// fields as `?: T` (possibly undefined); our hand-written types.ts (used
// throughout the rest of the app) uses `T | null` instead. Normalize at
// this one boundary rather than letting `undefined` leak into components.
//
// omitempty ALSO turns an empty/nil Go slice into JSON `null` (not `[]`),
// even for fields types.ts declares as plain (non-nullable) arrays like
// `headers`/`params`/`formFields`/`variables`/`secrets`. Editors that add a
// row do `setAppState(..., (rows = []) => [...rows, x])` — a default
// parameter only covers `undefined`, so a `null` from the backend slipped
// through and `[...null, x]` threw silently inside the store's updater,
// making "+ Add row" a no-op with no visible error on any request/
// environment that started with no rows. Coalescing null -> [] here, once,
// for every array field guarantees the invariant types.ts already claims,
// instead of patching every call site that mutates one of these arrays.
function normalizeRequest(r: wailsModel.RequestDef): RequestDef {
  // Go's BodyKind/AuthKind are string-backed enums; Wails' TS generator
  // widens them to `string` since it doesn't preserve the underlying enum
  // literals. The Go side only ever produces valid enum values, so this
  // narrowing cast is safe.
  const body = (r.body ?? null) as RequestDef['body']
  return {
    ...r,
    protocol: r.protocol as RequestDef['protocol'],
    folderId: r.folderId ?? null,
    headers: r.headers ?? [],
    params: r.params ?? [],
    body: body ? { ...body, formFields: body.formFields ?? [] } : null,
    authRef: (r.authRef ?? null) as RequestDef['authRef'],
    perf: (r.perf ?? null) as RequestDef['perf'],
    assertions: (r.assertions ?? null) as RequestDef['assertions'],
  }
}
function normalizeFolder(f: wailsModel.Folder): Folder {
  return { ...f, parentId: f.parentId ?? null }
}
function normalizeEnvironment(e: wailsModel.Environment): Environment {
  return { ...e, color: e.color ?? null, variables: e.variables ?? [], secrets: e.secrets ?? [] }
}
function normalizeMcpConnection(c: wailsModel.McpConnection): McpConnection {
  return { ...c, transport: c.transport as McpConnection['transport'], args: c.args ?? [] }
}

/** Loads the workspace list and, if none is active yet, selects the first one. */
export async function loadWorkspaces(): Promise<void> {
  const workspaces = await wails.ListWorkspaces()
  setAppState('workspaces', workspaces ?? [])
  if (!appState.activeWorkspaceId && workspaces?.length) {
    setAppState('activeWorkspaceId', workspaces[0].id)
  }
}

/** Loads everything scoped to one workspace: requests, folders, environments, MCP connections. */
export async function loadWorkspaceData(workspaceId: string): Promise<void> {
  const [requests, folders, environments, mcpConnections] = await Promise.all([
    wails.ListRequests(workspaceId),
    wails.ListFolders(workspaceId),
    wails.ListEnvironments(workspaceId),
    wails.ListMcpConnections(workspaceId),
  ])
  setAppState('requests', (requests ?? []).map(normalizeRequest))
  setAppState('folders', (folders ?? []).map(normalizeFolder))
  setAppState('environments', (environments ?? []).map(normalizeEnvironment))
  setAppState('mcpConnections', (mcpConnections ?? []).map(normalizeMcpConnection))
}

/** Creates a new MCP connection config (not yet connected) and reloads the list. */
export async function createMcpConnection(conn: Omit<McpConnection, 'id' | 'workspaceId'>): Promise<void> {
  if (!appState.activeWorkspaceId) return
  const draft: McpConnection = { ...conn, id: crypto.randomUUID(), workspaceId: appState.activeWorkspaceId }
  await wails.CreateMcpConnection(models.McpConnection.createFrom(draft))
  await loadWorkspaceData(appState.activeWorkspaceId)
}

/** Removes an MCP connection config (the backend disconnects any live session first). */
export async function deleteMcpConnection(id: string): Promise<void> {
  if (!appState.activeWorkspaceId) return
  await wails.DeleteMcpConnection(id)
  await loadWorkspaceData(appState.activeWorkspaceId)
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

/**
 * Persists any pending debounced edit for a request immediately and awaits it.
 * Call this before an action that resolves the request from the backend store
 * (StartStream) so it can't race the 400ms save window and act on stale
 * protocol/URL/body — e.g. picking WebSocket then clicking Connect at once.
 */
export async function flushRequestSave(requestId: string): Promise<void> {
  const existing = saveTimers.get(requestId)
  if (existing) {
    clearTimeout(existing)
    saveTimers.delete(requestId)
  }
  const req = appState.requests.find((r) => r.id === requestId)
  if (req) await wails.UpdateRequest(models.RequestDef.createFrom(req))
}
