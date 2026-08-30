// Bridges the Go bindings (frontend/wailsjs/go/main/App) into the Solid
// store. This is the piece that was missing end-to-end: components read
// appState, but nothing ever called ListWorkspaces/ListRequests/etc. to put
// real data into it, so the app opened to a permanently empty shell with no
// way to create anything either.
import { appState, setAppState, openTab, closeTab, setFolderRunView, setMcpToolView } from './store'
import { models, wails } from './wails'
import type { model as wailsModel } from '../../wailsjs/go/models'
import type { Environment, Folder, FolderRunResult, McpConnection, RequestDef } from '../types'

let saveTimers = new Map<string, ReturnType<typeof setTimeout>>()
let folderSaveTimers = new Map<string, ReturnType<typeof setTimeout>>()

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
    // Same omitempty null-vs-undefined trap as headers/params above: the
    // URL parser rebuilds this list with `[...(rows ?? []), ...]`-style
    // updates, and a `null` arriving from a request that has never had a
    // `:name` placeholder would break the first one to run.
    pathParams: r.pathParams ?? [],
    body: body ? { ...body, formFields: body.formFields ?? [] } : null,
    authRef: (r.authRef ?? null) as RequestDef['authRef'],
    perf: (r.perf ?? null) as RequestDef['perf'],
    assertions: (r.assertions ?? null) as RequestDef['assertions'],
  }
}
function normalizeFolder(f: wailsModel.Folder): Folder {
  return { ...f, parentId: f.parentId ?? null, variables: f.variables ?? [] }
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

/**
 * Re-reads ONLY the environments for the active workspace.
 *
 * A post-response script's `vars.set()` persists into the active environment
 * on the backend, so after a send (or a folder run) the in-memory copy the
 * environment editor renders from can be stale. Deliberately narrower than
 * loadWorkspaceData: that also replaces `requests`, which would clobber any
 * unsaved edit sitting in the editor's debounce window.
 */
export async function refreshEnvironments(): Promise<void> {
  const workspaceId = appState.activeWorkspaceId
  if (!workspaceId) return
  const environments = await wails.ListEnvironments(workspaceId)
  setAppState('environments', (environments ?? []).map(normalizeEnvironment))
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

// Bumped on every runFolder call so a stale in-flight run can tell it's been
// superseded (the user closed the view, switched folders, or double-clicked
// Run) and skip writing its late-arriving results over whatever's current.
let runFolderGeneration = 0

/**
 * Sends every request in folderId (recursing into subfolders), sequentially,
 * and puts the aggregate results in folderRunView for FolderRunView to
 * render — the "batch send" entry point, mirroring how McpPanel's openTool
 * takes over the same main-area real estate. Each RunRequest call also
 * appends a real history entry on the backend, so history is reloaded
 * afterward to pick those up.
 */
export async function runFolder(folderId: string, folderName: string): Promise<void> {
  if (!appState.activeWorkspaceId) return
  const generation = ++runFolderGeneration
  setMcpToolView(null)
  setFolderRunView({ folderId, folderName, running: true, results: [] })
  const results = await wails.RunFolder(appState.activeWorkspaceId, folderId, appState.activeEnvironmentId ?? '')
  if (generation !== runFolderGeneration) return
  setFolderRunView({ folderId, folderName, running: false, results: (results ?? []) as unknown as FolderRunResult[] })
  await loadHistory()
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

/** Deletes a request and closes its tab if it's currently open. */
export async function deleteRequest(id: string): Promise<void> {
  if (!appState.activeWorkspaceId) return
  await wails.DeleteRequest(id)
  closeTab(id)
  await loadWorkspaceData(appState.activeWorkspaceId)
}

/**
 * Creates a folder, optionally nested under parentId (undefined/null = a
 * root-level folder in the active workspace). Mirrors createRequest's
 * assign-id-client-side, persist, reload pattern.
 */
export async function createFolder(parentId?: string | null): Promise<void> {
  if (!appState.activeWorkspaceId) return

  const draft: Folder = {
    id: crypto.randomUUID(),
    workspaceId: appState.activeWorkspaceId,
    parentId: parentId ?? null,
    name: 'New Folder',
    orderKey: '',
    variables: [],
  }

  await wails.CreateFolder(models.Folder.createFrom(draft))
  await loadWorkspaceData(appState.activeWorkspaceId)
}

// Mirrors the backend's own cascade (FileStore.removeFolderLocked): every
// request directly inside folderId, plus every request inside any nested
// subfolder, recursively. Used only to know which open tabs to close —
// the actual deletion is one DeleteFolder call, the backend does the walk
// for real.
function collectDescendantRequestIds(folderId: string): string[] {
  const childFolderIds = appState.folders.filter((f) => f.parentId === folderId).map((f) => f.id)
  const directRequestIds = appState.requests.filter((r) => r.folderId === folderId).map((r) => r.id)
  return [...directRequestIds, ...childFolderIds.flatMap(collectDescendantRequestIds)]
}

/** Deletes a folder and everything nested inside it, closing tabs for any requests that go with it. */
export async function deleteFolder(id: string): Promise<void> {
  if (!appState.activeWorkspaceId) return
  const descendantRequestIds = collectDescendantRequestIds(id)
  await wails.DeleteFolder(id)
  descendantRequestIds.forEach(closeTab)
  await loadWorkspaceData(appState.activeWorkspaceId)
}

/** Persists an edited folder (rename or variables change), debounced per-folder-id like saveRequestDebounced. */
export function saveFolderDebounced(folder: Folder, delayMs = 400): void {
  const existing = folderSaveTimers.get(folder.id)
  if (existing) clearTimeout(existing)
  folderSaveTimers.set(
    folder.id,
    setTimeout(() => {
      folderSaveTimers.delete(folder.id)
      void wails.UpdateFolder(models.Folder.createFrom(folder))
    }, delayMs),
  )
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

// Cancels a pending debounced save for id. saveRequestDebounced captures a
// deep-copied SNAPSHOT (RequestEditor passes JSON.parse(snapshot)); left to
// fire after an immediate move/rename write it would push that stale snapshot
// (old orderKey/folderId/name) back to disk, silently reverting the gesture.
function cancelPendingRequestSave(id: string): void {
  const existing = saveTimers.get(id)
  if (existing) {
    clearTimeout(existing)
    saveTimers.delete(id)
  }
}

// Folder equivalent. saveFolderDebounced captures a live store reference (not a
// snapshot), so it wouldn't revert a rename/move today — but cancelling keeps
// the immediate write the single persistence and stops a redundant late write
// from racing it (and future-proofs if folders ever move to snapshots).
function cancelPendingFolderSave(id: string): void {
  const existing = folderSaveTimers.get(id)
  if (existing) {
    clearTimeout(existing)
    folderSaveTimers.delete(id)
  }
}

// ---------------------------------------------------------------------------
// Fractional order keys (client-side twin of internal/storage/orderkey.go).
// The backend mints an at-the-end key whenever it sees an EMPTY orderKey,
// but drag-reorder and duplicate-after-source need "insert between two
// specific siblings" keys minted client-side and passed through explicitly
// (PutRequest/PutFolder trust a non-empty key as-is). Same alphabet, same
// algorithm, so keys from either side interleave correctly.
// ---------------------------------------------------------------------------

const ORDER_KEY_ALPHABET = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'
const ORDER_KEY_BASE = ORDER_KEY_ALPHABET.length

/**
 * Returns a key sorting strictly between a and b (plain string comparison,
 * matching the tree's orderKey.localeCompare sort). "" for a = insert at the
 * very start; "" for b = insert at the very end.
 */
export function orderKeyBetween(a: string, b: string): string {
  a = a.replace(/0+$/, '')
  if (a !== '' && b !== '' && a >= b) {
    // Defensive: callers should never pass an inverted/equal range.
    return orderKeyMidpoint(a, '')
  }
  return orderKeyMidpoint(a, b)
}

function orderKeyMidpoint(a: string, b: string): string {
  if (b !== '') {
    let n = 0
    while (n < a.length && n < b.length && a[n] === b[n]) n++
    if (n > 0) return b.slice(0, n) + orderKeyMidpoint(a.slice(n), b.slice(n))
  }
  const digitA = a !== '' ? ORDER_KEY_ALPHABET.indexOf(a[0]) : 0
  const digitB = b !== '' ? ORDER_KEY_ALPHABET.indexOf(b[0]) : ORDER_KEY_BASE
  if (digitB - digitA > 1) {
    return ORDER_KEY_ALPHABET[digitA + Math.floor((digitB - digitA + 1) / 2)]
  }
  if (digitA === digitB) {
    // Only reachable when a is exhausted and b starts with '0' — emit '0'
    // and keep narrowing against b's remainder (see orderkey.go).
    return ORDER_KEY_ALPHABET[0] + orderKeyMidpoint('', b === '' ? '' : b.slice(1))
  }
  // digitB == digitA+1 from here on.
  if (b !== '' && b.length > 1 && digitB !== 0) {
    return b[0]
  }
  return ORDER_KEY_ALPHABET[digitA] + orderKeyMidpoint(a === '' ? '' : a.slice(1), '')
}

// ---------------------------------------------------------------------------
// Sidebar tree operations: move (drag & drop), duplicate, rename. All write
// through Solid's fine-grained store PATH setter (never array
// reconstruction — see Sidebar.tsx's reactivity notes) and persist
// IMMEDIATELY (not debounced): each is a discrete gesture, not a keystroke
// stream, so there's nothing to coalesce and no window for a stale save.
// ---------------------------------------------------------------------------

/** Moves a request to (folderId, orderKey) and persists it — the drag & drop commit. */
export async function moveRequest(id: string, folderId: string | null, orderKey: string): Promise<void> {
  cancelPendingRequestSave(id) // else a mid-edit debounced snapshot reverts this move
  setAppState('requests', (r) => r.id === id, 'folderId', folderId)
  setAppState('requests', (r) => r.id === id, 'orderKey', orderKey)
  const req = appState.requests.find((r) => r.id === id)
  if (req) await wails.UpdateRequest(models.RequestDef.createFrom(req))
}

/** Moves a folder to (parentId, orderKey) and persists it — sibling reorder via drag & drop. */
export async function moveFolder(id: string, parentId: string | null, orderKey: string): Promise<void> {
  cancelPendingFolderSave(id) // keep this immediate write the single persistence (see helper)
  setAppState('folders', (f) => f.id === id, 'parentId', parentId)
  setAppState('folders', (f) => f.id === id, 'orderKey', orderKey)
  const folder = appState.folders.find((f) => f.id === id)
  if (folder) await wails.UpdateFolder(models.Folder.createFrom(folder))
}

/** Renames a request and persists immediately (rename commits on Enter/blur, not per keystroke). */
export async function renameRequest(id: string, name: string): Promise<void> {
  cancelPendingRequestSave(id) // else a mid-edit debounced snapshot reverts this rename
  setAppState('requests', (r) => r.id === id, 'name', name)
  const req = appState.requests.find((r) => r.id === id)
  if (req) await wails.UpdateRequest(models.RequestDef.createFrom(req))
}

/** Renames a folder and persists immediately. */
export async function renameFolder(id: string, name: string): Promise<void> {
  cancelPendingFolderSave(id) // keep this immediate write the single persistence (see helper)
  setAppState('folders', (f) => f.id === id, 'name', name)
  const folder = appState.folders.find((f) => f.id === id)
  if (folder) await wails.UpdateFolder(models.Folder.createFrom(folder))
}

/**
 * Duplicates a request: copies every field, mints a fresh id, names it
 * "<name> copy", and lands it directly after the source among its siblings
 * (explicit between-key — the backend would otherwise append it at the
 * end). Opens the copy as a tab so a duplicate-then-tweak flow starts
 * immediately.
 */
export async function duplicateRequest(sourceId: string): Promise<string | null> {
  if (!appState.activeWorkspaceId) return null
  const source = appState.requests.find((r) => r.id === sourceId)
  if (!source) return null

  const siblings = appState.requests
    .filter((r) => r.folderId === source.folderId)
    .sort((a, b) => a.orderKey.localeCompare(b.orderKey))
  const sourceIndex = siblings.findIndex((r) => r.id === source.id)
  const next = sourceIndex >= 0 ? siblings[sourceIndex + 1] : undefined

  // JSON round-trip unwraps the Solid store proxy into a plain deep copy so
  // the draft shares no nested arrays/objects with the live source row.
  const draft: RequestDef = {
    ...(JSON.parse(JSON.stringify(source)) as RequestDef),
    id: crypto.randomUUID(),
    name: `${source.name} copy`,
    orderKey: orderKeyBetween(source.orderKey, next?.orderKey ?? ''),
  }

  await wails.CreateRequest(models.RequestDef.createFrom(draft))
  await loadWorkspaceData(appState.activeWorkspaceId)
  openTab(draft.id)
  return draft.id
}

/**
 * Creates a new blank request directly inside folderId (null = workspace
 * root) — the folder context menu's "New request here". Same
 * assign-id-client-side, persist, reload, open flow as createRequest.
 */
export async function createRequestIn(folderId: string | null): Promise<void> {
  if (!appState.activeWorkspaceId) return

  const draft: RequestDef = {
    id: crypto.randomUUID(),
    workspaceId: appState.activeWorkspaceId,
    folderId,
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
