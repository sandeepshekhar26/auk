import { Index, Match, Show, Switch, createEffect, createMemo, createSignal, on, onCleanup, onMount, type Accessor, type JSX } from 'solid-js'
import { confirmDialog } from '../lib/confirm'
import {
  appState,
  setAppState,
  openTab,
  sidebarFilter,
  setSidebarFilter,
  explorerOpen,
  explorerTab,
  setExplorerOpen,
  setImportModalOpen,
  sidebarMode,
  setSidebarMode,
  sidebarWidth,
  setSidebarWidth,
  type ExplorerTab,
} from '../lib/store'
import {
  createFolder,
  createRequest,
  createRequestIn,
  deleteFolder,
  deleteRequest,
  duplicateRequest,
  moveFolder,
  moveRequest,
  orderKeyBetween,
  renameFolder,
  renameRequest,
  runFolder,
  saveFolderDebounced,
} from '../lib/data'
import type { Folder, KeyValue, RequestDef } from '../types'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import HistoryPanel from './HistoryPanel'
import GitPanel from './GitPanel'
import McpPanel from './McpPanel'
import CookiesPanel from './CookiesPanel'
import KeyValueTable from './KeyValueTable'
import ContextMenu, { type MenuItem } from './ContextMenu'
import Tooltip from './Tooltip'
import { IconChevronRight, IconCollection, IconFolder, IconFolderOpen, IconFolderPlus, IconMore, IconPin, IconPinOff, IconPlus, IconX, MethodBadge } from './icons'

interface FolderNode {
  folder: Folder | null // null = synthetic root
  children: FolderNode[]
  requests: RequestDef[]
}

function buildTree(folders: Folder[], requests: RequestDef[]): FolderNode {
  const nodeByFolderId = new Map<string, FolderNode>()
  const root: FolderNode = { folder: null, children: [], requests: [] }

  for (const folder of folders) {
    nodeByFolderId.set(folder.id, { folder, children: [], requests: [] })
  }
  for (const folder of folders) {
    const node = nodeByFolderId.get(folder.id)!
    const parent = folder.parentId ? nodeByFolderId.get(folder.parentId) : undefined
    ;(parent ?? root).children.push(node)
  }
  for (const req of requests) {
    const node = req.folderId ? nodeByFolderId.get(req.folderId) : undefined
    ;(node ?? root).requests.push(req)
  }

  const byOrderKey = (a: { folder: Folder | null } | RequestDef, b: { folder: Folder | null } | RequestDef) => {
    const aKey = 'folder' in a ? a.folder?.orderKey ?? '' : a.orderKey
    const bKey = 'folder' in b ? b.folder?.orderKey ?? '' : b.orderKey
    return aKey.localeCompare(bKey)
  }
  const sortTree = (node: FolderNode) => {
    node.children.sort(byOrderKey)
    node.requests.sort(byOrderKey)
    node.children.forEach(sortTree)
  }
  sortTree(root)

  return root
}

// Counts every request and child folder nested (at any depth) inside node,
// for the delete confirmation's "this also deletes N items" warning.
function countDescendants(node: FolderNode): number {
  return node.requests.length + node.children.reduce((sum, child) => sum + 1 + countDescendants(child), 0)
}

// A folder survives filtering if it (or any descendant request/folder) matches.
function nodeMatches(node: FolderNode, query: string): boolean {
  if (node.requests.some((r) => matchesRequest(r, query))) return true
  return node.children.some((child) => nodeMatches(child, query))
}

function matchesRequest(req: RequestDef, query: string): boolean {
  return req.name.toLowerCase().includes(query) || req.url.toLowerCase().includes(query)
}

const SECTION_TITLE: Record<ExplorerTab, string> = {
  requests: 'Requests',
  history: 'History',
  git: 'Git',
  mcp: 'MCP',
  cookies: 'Cookies',
}

// Keyboard-navigation model: a flat list of the rows the tree is currently
// showing, in render order, addressed by a cursor index.
type VisibleRow = { kind: 'folder'; id: string; node: FolderNode } | { kind: 'request'; id: string; req: RequestDef }

// Drag & drop model. "before"/"after" reorder relative to an existing row
// (adopting that row's parent), "into" drops a request into a folder,
// "root-end" appends at the workspace root.
type DragNode = { kind: 'folder' | 'request'; id: string; parentId: string | null }
type DropTarget =
  | { kind: 'before' | 'after'; rowKind: 'folder' | 'request'; id: string }
  | { kind: 'into'; id: string }
  | { kind: 'root-end' }

// Sidebar is the app's navigation panel, in one of two modes
// (settings.sidebarMode, toggled by the pin button in the header):
//   - DOCKED (default): a persistent, resizable (220-400px) panel between
//     the rail and the editor — the layout Postman/Insomnia/Yaak users
//     already know. ⌘B collapses/expands it; picking a request keeps it
//     open.
//   - OVERLAY: the original slide-over drawer for maximum editor width —
//     opens on demand (⌘B/rail), auto-closes when a request is picked,
//     Escape dismisses.
// The rail owns section switching (requests/history/git/mcp/cookies) — the
// sidebar renders exactly one section plus a small header naming it.
export default function Sidebar() {
  const [expanded, setExpanded] = createSignal<Set<string>>(new Set())
  // Which folders currently have their variables editor open — separate from
  // `expanded` (that's the tree-disclosure state for children/requests).
  const [varsOpenFor, setVarsOpenFor] = createSignal<Set<string>>(new Set())
  // Row (request or folder id) currently in inline-rename mode. Rows render
  // as plain text spans except during a rename — an input-per-row reads as a
  // form, not a tree.
  const [renamingId, setRenamingId] = createSignal<string | null>(null)
  const [cursor, setCursor] = createSignal(-1)
  const [treeFocused, setTreeFocused] = createSignal(false)
  const [menu, setMenu] = createSignal<{ x: number; y: number; items: MenuItem[] } | null>(null)
  const [dragging, setDragging] = createSignal<DragNode | null>(null)
  const [dropTarget, setDropTarget] = createSignal<DropTarget | null>(null)
  const [resizing, setResizing] = createSignal(false)
  let filterInput: HTMLInputElement | undefined
  let treeRef: HTMLDivElement | undefined

  function toggleFolder(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function expandFolder(id: string) {
    setExpanded((prev) => (prev.has(id) ? prev : new Set(prev).add(id)))
  }

  function collapseFolder(id: string) {
    setExpanded((prev) => {
      if (!prev.has(id)) return prev
      const next = new Set(prev)
      next.delete(id)
      return next
    })
  }

  function toggleVarsEditor(id: string) {
    setVarsOpenFor((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // All three setters below write through Solid's fine-grained store PATH
  // setter (setAppState('folders', predicate, ...path, value)) rather than
  // reconstructing folder.variables with .map()/.filter()/spread and writing
  // the whole new array back. The path form mutates only the touched leaf,
  // so sibling rows (and, for setFolderVariable, every OTHER field on the
  // SAME row) keep their exact object references — which is what lets
  // KeyValueTable's own <For> avoid remounting the input the user is
  // currently typing into. This is the same fix as the FolderRow/<Index>
  // change (see TreeChildren), one level deeper: reconstructing the array
  // here would undo that fix by giving <For> a fresh row object on every
  // keystroke regardless of how stable the outer tree is.
  //
  // Each function looks up the current folder fresh from appState.folders
  // AFTER the store write (synchronous) rather than accepting a Folder
  // snapshot as a parameter, so the debounced save always persists the
  // latest state even if two edits land in the same debounce window.
  function currentFolder(folderId: string): Folder | undefined {
    return appState.folders.find((f) => f.id === folderId)
  }

  function addFolderVariable(folderId: string) {
    setAppState('folders', (f) => f.id === folderId, 'variables', (vars: KeyValue[] | null | undefined) => [
      ...(vars ?? []),
      { key: '', value: '', enabled: true },
    ])
    const updated = currentFolder(folderId)
    if (updated) saveFolderDebounced(updated)
  }

  function setFolderVariable(folderId: string, index: number, field: keyof KeyValue, value: string | boolean) {
    setAppState('folders', (f) => f.id === folderId, 'variables', index, field as any, value as any)
    const updated = currentFolder(folderId)
    if (updated) saveFolderDebounced(updated)
  }

  function removeFolderVariable(folderId: string, index: number) {
    setAppState('folders', (f) => f.id === folderId, 'variables', (vars: KeyValue[] | null | undefined) =>
      (vars ?? []).filter((_, i) => i !== index),
    )
    const updated = currentFolder(folderId)
    if (updated) saveFolderDebounced(updated)
  }

  const tree = createMemo(() => buildTree(appState.folders, appState.requests))
  const query = createMemo(() => sidebarFilter().trim().toLowerCase())
  const filtering = createMemo(() => query().length > 0)

  const isEmpty = createMemo(() => appState.folders.length === 0 && appState.requests.length === 0)
  const noMatches = createMemo(() => filtering() && !nodeMatches(tree(), query()))

  function isExpanded(id: string) {
    // While filtering, force-expand every folder so matches deep in the tree stay visible.
    return filtering() || expanded().has(id)
  }

  // The rows currently on screen, in render order (folders before requests
  // at each level, matching TreeChildren) — the substrate for ↑/↓ keyboard
  // navigation and cursor addressing.
  const visibleRows = createMemo<VisibleRow[]>(() => {
    const rows: VisibleRow[] = []
    const walk = (node: FolderNode) => {
      for (const child of node.children) {
        if (filtering() && !nodeMatches(child, query())) continue
        rows.push({ kind: 'folder', id: child.folder!.id, node: child })
        if (isExpanded(child.folder!.id)) walk(child)
      }
      for (const req of node.requests) {
        if (filtering() && !matchesRequest(req, query())) continue
        rows.push({ kind: 'request', id: req.id, req })
      }
    }
    walk(tree())
    return rows
  })

  const cursorRowId = createMemo(() => visibleRows()[cursor()]?.id ?? null)

  function setCursorTo(id: string) {
    setCursor(visibleRows().findIndex((r) => r.id === id))
  }

  function moveCursor(index: number) {
    setCursor(index)
    const id = visibleRows()[index]?.id
    if (id && treeRef) {
      treeRef.querySelector(`[data-tree-row="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest' })
    }
  }

  function openRequest(id: string) {
    openTab(id)
    // Overlay mode auto-closes on pick (its whole point: maximum editor
    // width). Docked mode stays put — the reason it's the default.
    if (sidebarMode() === 'overlay') setExplorerOpen(false)
  }

  function startFolderRun(folder: Folder) {
    void runFolder(folder.id, folder.name)
    if (sidebarMode() === 'overlay') setExplorerOpen(false)
  }

  async function confirmDeleteFolder(node: FolderNode) {
    const folder = node.folder!
    const nested = countDescendants(node)
    const warning = nested > 0 ? ` This also deletes ${nested} item${nested === 1 ? '' : 's'} inside it.` : ''
    const ok = await confirmDialog({
      title: `Delete folder "${folder.name}"?`,
      body: `${warning ? warning.trim() + ' ' : ''}This cannot be undone.`,
      confirmText: 'Delete',
      danger: true,
    })
    if (!ok) return
    void deleteFolder(folder.id)
  }

  async function confirmDeleteRequest(req: RequestDef) {
    const ok = await confirmDialog({
      title: `Delete request "${req.name}"?`,
      body: 'This cannot be undone.',
      confirmText: 'Delete',
      danger: true,
    })
    if (!ok) return
    void deleteRequest(req.id)
  }

  function startRename(id: string) {
    setCursorTo(id)
    setRenamingId(id)
  }

  function commitRename(id: string, kind: 'folder' | 'request', value: string) {
    setRenamingId(null)
    const trimmed = value.trim()
    if (trimmed) {
      if (kind === 'request') void renameRequest(id, trimmed)
      else void renameFolder(id, trimmed)
    }
    treeRef?.focus()
  }

  function cancelRename() {
    setRenamingId(null)
    treeRef?.focus()
  }

  // ----- context menus ------------------------------------------------------

  function openMenuAt(x: number, y: number, items: MenuItem[]) {
    setMenu({ x, y, items })
  }

  function requestMenuItems(req: RequestDef): MenuItem[] {
    return [
      { label: 'Open', action: () => openRequest(req.id) },
      { label: 'Duplicate', action: () => void duplicateRequest(req.id) },
      { label: 'Rename', action: () => startRename(req.id) },
      { label: 'Delete', danger: true, separatorAbove: true, action: () => confirmDeleteRequest(req) },
    ]
  }

  function folderMenuItems(node: FolderNode): MenuItem[] {
    const folder = node.folder!
    return [
      {
        label: 'New request here',
        action: () => {
          expandFolder(folder.id)
          void createRequestIn(folder.id)
        },
      },
      {
        label: 'New subfolder',
        action: () => {
          expandFolder(folder.id)
          void createFolder(folder.id)
        },
      },
      { label: 'Folder variables', action: () => toggleVarsEditor(folder.id) },
      { label: 'Run folder', action: () => startFolderRun(folder) },
      { label: 'Rename', separatorAbove: true, action: () => startRename(folder.id) },
      { label: 'Delete', danger: true, separatorAbove: true, action: () => confirmDeleteFolder(node) },
    ]
  }

  // ----- drag & drop --------------------------------------------------------

  function sortedRequestSiblings(folderId: string | null, excludeId: string): RequestDef[] {
    return appState.requests
      .filter((r) => r.folderId === folderId && r.id !== excludeId)
      .sort((a, b) => a.orderKey.localeCompare(b.orderKey))
  }

  function sortedFolderSiblings(parentId: string | null, excludeId: string): Folder[] {
    return appState.folders
      .filter((f) => f.parentId === parentId && f.id !== excludeId)
      .sort((a, b) => a.orderKey.localeCompare(b.orderKey))
  }

  function performDrop() {
    const drag = dragging()
    const target = dropTarget()
    setDropTarget(null)
    if (!drag || !target) return

    if (drag.kind === 'request') {
      if (target.kind === 'into') {
        const siblings = sortedRequestSiblings(target.id, drag.id)
        const last = siblings[siblings.length - 1]
        expandFolder(target.id)
        void moveRequest(drag.id, target.id, orderKeyBetween(last?.orderKey ?? '', ''))
      } else if (target.kind === 'root-end') {
        const siblings = sortedRequestSiblings(null, drag.id)
        const last = siblings[siblings.length - 1]
        void moveRequest(drag.id, null, orderKeyBetween(last?.orderKey ?? '', ''))
      } else if (target.rowKind === 'request') {
        const anchor = appState.requests.find((r) => r.id === target.id)
        if (!anchor) return
        const siblings = sortedRequestSiblings(anchor.folderId, drag.id)
        const anchorIndex = siblings.findIndex((r) => r.id === anchor.id)
        const prev = target.kind === 'before' ? siblings[anchorIndex - 1] : anchor
        const next = target.kind === 'before' ? anchor : siblings[anchorIndex + 1]
        void moveRequest(drag.id, anchor.folderId, orderKeyBetween(prev?.orderKey ?? '', next?.orderKey ?? ''))
      }
    } else {
      // Folders reorder among SAME-PARENT siblings only (enforced again at
      // dragover) — no reparenting via drag, which keeps the gesture
      // unambiguous ("into vs between" needs no pixel-perfect hover zones).
      if (target.kind === 'root-end') {
        if (drag.parentId !== null) return
        const siblings = sortedFolderSiblings(null, drag.id)
        const last = siblings[siblings.length - 1]
        void moveFolder(drag.id, null, orderKeyBetween(last?.orderKey ?? '', ''))
      } else if (target.kind === 'before' || target.kind === 'after') {
        if (target.rowKind !== 'folder') return
        const anchor = appState.folders.find((f) => f.id === target.id)
        if (!anchor || anchor.parentId !== drag.parentId) return
        const siblings = sortedFolderSiblings(anchor.parentId, drag.id)
        const anchorIndex = siblings.findIndex((f) => f.id === anchor.id)
        const prev = target.kind === 'before' ? siblings[anchorIndex - 1] : anchor
        const next = target.kind === 'before' ? anchor : siblings[anchorIndex + 1]
        void moveFolder(drag.id, anchor.parentId, orderKeyBetween(prev?.orderKey ?? '', next?.orderKey ?? ''))
      }
    }
  }

  function rowDragStart(e: DragEvent, node: DragNode) {
    setDragging(node)
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move'
      // WebKit won't start an HTML5 drag at all unless some data is set.
      e.dataTransfer.setData('text/plain', node.id)
    }
  }

  function rowDragEnd() {
    setDragging(null)
    setDropTarget(null)
  }

  // ----- keyboard navigation ------------------------------------------------

  function onTreeKeyDown(e: KeyboardEvent) {
    if (renamingId() || menu()) return
    const rows = visibleRows()
    if (rows.length === 0) return
    const index = Math.min(cursor(), rows.length - 1)
    const row = index >= 0 ? rows[index] : undefined

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      moveCursor(Math.min(index + 1, rows.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (index <= 0) {
        setCursor(-1)
        filterInput?.focus()
      } else {
        moveCursor(index - 1)
      }
    } else if (e.key === 'ArrowRight' && row) {
      e.preventDefault()
      if (row.kind === 'folder') {
        if (!isExpanded(row.id)) expandFolder(row.id)
        else moveCursor(Math.min(index + 1, rows.length - 1))
      }
    } else if (e.key === 'ArrowLeft' && row) {
      e.preventDefault()
      if (row.kind === 'folder' && isExpanded(row.id) && !filtering()) {
        collapseFolder(row.id)
      } else {
        const parentId = row.kind === 'folder' ? row.node.folder!.parentId : row.req.folderId
        if (parentId) {
          const parentIndex = rows.findIndex((r) => r.kind === 'folder' && r.id === parentId)
          if (parentIndex >= 0) moveCursor(parentIndex)
        }
      }
    } else if (e.key === 'Enter' && row) {
      e.preventDefault()
      if (row.kind === 'request') openRequest(row.id)
      else toggleFolder(row.id)
    } else if (e.key === 'F2' && row) {
      e.preventDefault()
      startRename(row.id)
    } else if ((e.key === 'Backspace' || e.key === 'Delete') && (e.metaKey || e.ctrlKey) && row) {
      e.preventDefault()
      if (row.kind === 'request') {
        const req = appState.requests.find((r) => r.id === row.id)
        if (req) confirmDeleteRequest(req)
      } else {
        confirmDeleteFolder(row.node)
      }
    }
  }

  // Escape dismisses the OVERLAY drawer only — a docked sidebar isn't a
  // transient surface, so Escape leaves it alone. (The rename input and the
  // context menu stopPropagation their own Escapes before this sees them.)
  function onWindowKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && sidebarMode() === 'overlay' && explorerOpen()) setExplorerOpen(false)
  }
  onMount(() => window.addEventListener('keydown', onWindowKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onWindowKeyDown))

  // Autofocus the filter whenever the sidebar opens (⌘B, rail, palette) onto
  // the Requests section — but not on the initial mount, where stealing
  // focus at app launch would fight the editor.
  let skipInitialFocus = true
  createEffect(
    on([explorerOpen, explorerTab], ([open, tab]) => {
      if (skipInitialFocus) {
        skipInitialFocus = false
        return
      }
      if (open && tab === 'requests') filterInput?.focus()
    }),
  )

  // ----- resize -------------------------------------------------------------

  // Teardown for an in-flight resize drag, hoisted to component scope so an
  // unmount mid-drag (onCleanup below) can detach the window listeners too.
  let endResize: (() => void) | null = null

  function startResize(e: PointerEvent) {
    e.preventDefault()
    setResizing(true)
    const startX = e.clientX
    const startWidth = sidebarWidth()
    const onMove = (ev: PointerEvent) => setSidebarWidth(startWidth + (ev.clientX - startX))
    // Shared teardown for pointerup AND pointercancel: a macOS gesture
    // interruption ends the drag via pointercancel with NO pointerup, so
    // without this the listeners would leak, resizing() would stay true
    // (transition permanently disabled, handle stuck accent-colored), and
    // every later mouse move would keep resizing with no button held.
    const onEnd = () => {
      setResizing(false)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onEnd)
      window.removeEventListener('pointercancel', onEnd)
      endResize = null
    }
    endResize = onEnd
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onEnd)
    window.addEventListener('pointercancel', onEnd)
  }
  // If the sidebar unmounts while a resize drag is still active, detach the
  // window listeners so they don't leak past the component's lifetime.
  onCleanup(() => endResize?.())

  // ----- rows ---------------------------------------------------------------

  // Rename input: uncontrolled (value only seeds it), so keystrokes never
  // touch the store — commit on Enter/blur, cancel on Escape. This is what
  // makes mid-rename typing immune to store-driven remounts by
  // construction, on top of the <Index> stability below.
  function RenameInput(props: { value: string; onCommit: (value: string) => void; onCancel: () => void }) {
    let inputRef: HTMLInputElement | undefined
    let settled = false // Enter also fires blur — commit exactly once
    onMount(() => {
      inputRef?.focus()
      inputRef?.select()
    })
    return (
      <input
        ref={inputRef}
        class="min-w-0 flex-1 rounded bg-field px-1 text-sm text-ink outline-none ring-1 ring-accent"
        value={props.value}
        onKeyDown={(e) => {
          e.stopPropagation()
          if (e.key === 'Enter') {
            settled = true
            props.onCommit(e.currentTarget.value)
          } else if (e.key === 'Escape') {
            settled = true
            props.onCancel()
          }
        }}
        onBlur={(e) => {
          if (!settled) props.onCommit(e.currentTarget.value)
        }}
        onClick={(e) => e.stopPropagation()}
        onDblClick={(e) => e.stopPropagation()}
      />
    )
  }

  // node/req are accessors, not plain values — FolderRow/RequestRow are
  // rendered via <Index> (see TreeChildren) rather than <For>, since
  // buildTree() allocates fresh FolderNode wrapper objects on every
  // appState.folders/requests change. <For> keys by item REFERENCE, so with
  // ever-fresh wrappers it tore down and remounted the entire subtree AND
  // ANY focused input inside it (e.g. mid-rename, or typing a folder
  // variable's value) on literally every keystroke — the same class of bug
  // as the BodyEditor focus-loss issue found earlier, just in the tree
  // instead of an editor. <Index> keys by array POSITION instead: renaming
  // or editing a folder's variables doesn't change its sort position (that's
  // orderKey-driven, untouched by either), so the component instance at that
  // position stays mounted and just re-reads the accessor for new content.
  function FolderRow(props: { node: Accessor<FolderNode>; depth: number }) {
    const folder = () => props.node().folder!
    const open = () => isExpanded(folder().id)
    const varsOpen = () => varsOpenFor().has(folder().id)
    const renaming = () => renamingId() === folder().id
    const isCursor = () => treeFocused() && cursorRowId() === folder().id
    const dropInto = () => {
      const t = dropTarget()
      return t?.kind === 'into' && t.id === folder().id
    }
    const dropLine = (edge: 'before' | 'after') => {
      const t = dropTarget()
      return t?.kind === edge && t.rowKind === 'folder' && t.id === folder().id
    }
    return (
      <Show when={!filtering() || nodeMatches(props.node(), query())}>
        <div
          class="group relative flex w-full cursor-default select-none items-center gap-1 rounded px-2 py-1"
          classList={{
            'hover:bg-raised/60': !dropInto(),
            'bg-accent/15': dropInto(),
            'ring-1 ring-inset ring-accent': dropInto() || isCursor(),
          }}
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          data-tree-row={folder().id}
          draggable={!renaming()}
          onDragStart={(e) => rowDragStart(e, { kind: 'folder', id: folder().id, parentId: folder().parentId })}
          onDragEnd={rowDragEnd}
          onDragOver={(e) => {
            const drag = dragging()
            if (!drag) return
            if (drag.kind === 'request') {
              e.preventDefault()
              e.stopPropagation()
              if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
              setDropTarget({ kind: 'into', id: folder().id })
            } else if (drag.id !== folder().id && drag.parentId === folder().parentId) {
              e.preventDefault()
              e.stopPropagation()
              if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
              const rect = e.currentTarget.getBoundingClientRect()
              const before = e.clientY < rect.top + rect.height / 2
              setDropTarget({ kind: before ? 'before' : 'after', rowKind: 'folder', id: folder().id })
            } else {
              // Folder dragged over a non-sibling (different parent — no
              // reparenting via drag) or over itself: not a valid drop here, so
              // clear any stale indicator instead of leaving it lit on this row.
              setDropTarget(null)
            }
          }}
          onDrop={(e) => {
            e.preventDefault()
            e.stopPropagation()
            performDrop()
          }}
          onClick={() => {
            setCursorTo(folder().id)
            toggleFolder(folder().id)
          }}
          onContextMenu={(e) => {
            e.preventDefault()
            setCursorTo(folder().id)
            openMenuAt(e.clientX, e.clientY, folderMenuItems(props.node()))
          }}
        >
          <span class="shrink-0 text-ink-faint transition-transform duration-100" classList={{ 'rotate-90': open() }}>
            <IconChevronRight size={13} />
          </span>
          <span class="shrink-0 text-ink-muted">{open() ? <IconFolderOpen size={14} /> : <IconFolder size={14} />}</span>
          <Show
            when={!renaming()}
            fallback={<RenameInput value={folder().name} onCommit={(v) => commitRename(folder().id, 'folder', v)} onCancel={cancelRename} />}
          >
            <span class="min-w-0 flex-1 truncate text-sm text-ink-dim">{folder().name}</span>
          </Show>
          <Show when={folder().variables.length > 0}>
            <button
              class="shrink-0 rounded px-1 font-mono text-[10px] text-accent-fg hover:bg-elevated"
              title="Folder variables (inherited by every request inside)"
              onClick={(e) => {
                e.stopPropagation()
                toggleVarsEditor(folder().id)
              }}
            >
              {`{${folder().variables.length}}`}
            </button>
          </Show>
          <button
            class="shrink-0 rounded p-0.5 text-ink-faint opacity-0 hover:bg-elevated hover:text-ink-dim focus:opacity-100 group-hover:opacity-100"
            title="Folder actions"
            onClick={(e) => {
              e.stopPropagation()
              setCursorTo(folder().id)
              const rect = e.currentTarget.getBoundingClientRect()
              openMenuAt(rect.left, rect.bottom + 2, folderMenuItems(props.node()))
            }}
          >
            <IconMore size={14} />
          </button>
          <Show when={dropLine('before')}>
            <div class="pointer-events-none absolute inset-x-1 top-0 h-0.5 rounded bg-accent" />
          </Show>
          <Show when={dropLine('after')}>
            <div class="pointer-events-none absolute inset-x-1 bottom-0 h-0.5 rounded bg-accent" />
          </Show>
        </div>
        <Show when={varsOpen()}>
          <div style={{ 'padding-left': `${8 + (props.depth + 1) * 14}px` }}>
            <KeyValueTable
              rows={folder().variables}
              keyPlaceholder="VAR_NAME"
              onSet={(i, field, value) => setFolderVariable(folder().id, i, field, value)}
              onAdd={() => addFolderVariable(folder().id)}
              onRemove={(i) => removeFolderVariable(folder().id, i)}
            />
          </div>
        </Show>
        <Show when={open()}>
          <TreeChildren node={props.node()} depth={props.depth + 1} />
        </Show>
      </Show>
    )
  }

  function RequestRow(props: { req: Accessor<RequestDef>; depth: number }) {
    const req = () => props.req()
    const active = () => appState.activeTabId === req().id
    const renaming = () => renamingId() === req().id
    const isCursor = () => treeFocused() && cursorRowId() === req().id
    const dropLine = (edge: 'before' | 'after') => {
      const t = dropTarget()
      return t?.kind === edge && t.rowKind === 'request' && t.id === req().id
    }
    return (
      <Show when={!filtering() || matchesRequest(props.req(), query())}>
        <div
          class="group relative flex w-full cursor-default select-none items-center gap-1.5 rounded px-2 py-1"
          classList={{
            'bg-raised': active(),
            'hover:bg-raised/60': !active(),
            'ring-1 ring-inset ring-accent': isCursor(),
          }}
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          data-tree-row={req().id}
          draggable={!renaming()}
          onDragStart={(e) => rowDragStart(e, { kind: 'request', id: req().id, parentId: req().folderId })}
          onDragEnd={rowDragEnd}
          onDragOver={(e) => {
            const drag = dragging()
            // Dragging a folder over a request row (or a request over itself) is
            // a no-op here — clear any stale indicator instead of leaving the
            // insertion line lit on this row while the drop wouldn't land.
            if (!drag || drag.kind !== 'request' || drag.id === req().id) {
              setDropTarget(null)
              return
            }
            e.preventDefault()
            e.stopPropagation()
            if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
            const rect = e.currentTarget.getBoundingClientRect()
            const before = e.clientY < rect.top + rect.height / 2
            setDropTarget({ kind: before ? 'before' : 'after', rowKind: 'request', id: req().id })
          }}
          onDrop={(e) => {
            e.preventDefault()
            e.stopPropagation()
            performDrop()
          }}
          onClick={() => {
            setCursorTo(req().id)
            openRequest(req().id)
          }}
          onContextMenu={(e) => {
            e.preventDefault()
            setCursorTo(req().id)
            openMenuAt(e.clientX, e.clientY, requestMenuItems(req()))
          }}
        >
          <span class="flex w-10 shrink-0 justify-end">
            <MethodBadge method={req().method} protocol={req().protocol} />
          </span>
          <Show
            when={!renaming()}
            fallback={<RenameInput value={req().name} onCommit={(v) => commitRename(req().id, 'request', v)} onCancel={cancelRename} />}
          >
            <span class="min-w-0 flex-1 truncate text-sm" classList={{ 'text-ink': active(), 'text-ink-dim': !active() }}>
              {req().name}
            </span>
          </Show>
          <button
            class="shrink-0 rounded p-0.5 text-ink-faint opacity-0 hover:bg-elevated hover:text-ink-dim focus:opacity-100 group-hover:opacity-100"
            title="Request actions"
            onClick={(e) => {
              e.stopPropagation()
              setCursorTo(req().id)
              const rect = e.currentTarget.getBoundingClientRect()
              openMenuAt(rect.left, rect.bottom + 2, requestMenuItems(req()))
            }}
          >
            <IconMore size={14} />
          </button>
          <Show when={active()}>
            <span class="pointer-events-none absolute bottom-1 left-0 top-1 w-0.5 rounded-r bg-accent" />
          </Show>
          <Show when={dropLine('before')}>
            <div class="pointer-events-none absolute inset-x-1 top-0 h-0.5 rounded bg-accent" />
          </Show>
          <Show when={dropLine('after')}>
            <div class="pointer-events-none absolute inset-x-1 bottom-0 h-0.5 rounded bg-accent" />
          </Show>
        </div>
      </Show>
    )
  }

  function TreeChildren(props: { node: FolderNode; depth: number }) {
    return (
      <>
        <Index each={props.node.children}>{(child) => <FolderRow node={child} depth={props.depth} />}</Index>
        <Index each={props.node.requests}>{(req) => <RequestRow req={req} depth={props.depth} />}</Index>
      </>
    )
  }

  // ----- chrome -------------------------------------------------------------

  function SectionHeader(props: { children?: JSX.Element }) {
    return (
      <div class="flex h-9 shrink-0 items-center gap-1 border-b border-edge pl-3 pr-2">
        <span class="flex-1 truncate text-[11px] font-semibold uppercase tracking-wide text-ink-muted">
          {SECTION_TITLE[explorerTab()]}
        </span>
        {props.children}
        <Tooltip text={sidebarMode() === 'docked' ? 'Unpin: overlay mode (auto-hides after picking a request)' : 'Pin: keep the sidebar docked'} side="bottom">
          <button
            class="flex h-6 w-6 items-center justify-center rounded hover:bg-raised"
            classList={{ 'text-accent-fg': sidebarMode() === 'docked', 'text-ink-muted hover:text-ink-dim': sidebarMode() !== 'docked' }}
            title={sidebarMode() === 'docked' ? 'Unpin sidebar (overlay mode)' : 'Pin sidebar (docked mode)'}
            onClick={() => setSidebarMode(sidebarMode() === 'docked' ? 'overlay' : 'docked')}
          >
            {sidebarMode() === 'docked' ? <IconPin size={14} /> : <IconPinOff size={14} />}
          </button>
        </Tooltip>
        <Show when={sidebarMode() === 'overlay'}>
          <Tooltip text="Close (Esc)" side="bottom">
            <button
              class="flex h-6 w-6 items-center justify-center rounded text-ink-muted hover:bg-raised hover:text-ink-dim"
              title="Close (Esc)"
              onClick={() => setExplorerOpen(false)}
            >
              <IconX size={14} />
            </button>
          </Tooltip>
        </Show>
      </div>
    )
  }

  function SidebarBody() {
    return (
      <>
        <SectionHeader>
          <Show when={explorerTab() === 'requests'}>
            <Tooltip text="New request (⌘N)" side="bottom">
              <button
                class="flex h-6 w-6 items-center justify-center rounded text-ink-muted hover:bg-raised hover:text-ink-dim"
                title="New request (⌘N)"
                onClick={() => void createRequest()}
              >
                <IconPlus size={14} />
              </button>
            </Tooltip>
            <Tooltip text="New folder" side="bottom">
              <button
                class="flex h-6 w-6 items-center justify-center rounded text-ink-muted hover:bg-raised hover:text-ink-dim"
                title="New folder"
                onClick={() => void createFolder(null)}
              >
                <IconFolderPlus size={14} />
              </button>
            </Tooltip>
          </Show>
        </SectionHeader>

        <Switch>
          <Match when={explorerTab() === 'requests'}>
            <div class="border-b border-edge p-2">
              <WorkspaceSwitcher />
            </div>
            <div class="p-2 pb-1">
              <input
                ref={filterInput}
                class="h-8 w-full rounded-lg bg-field px-2.5 text-[12.5px] text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="Filter requests…"
                value={sidebarFilter()}
                onInput={(e) => setSidebarFilter(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key === 'ArrowDown') {
                    e.preventDefault()
                    treeRef?.focus()
                    moveCursor(0)
                  } else if (e.key === 'Escape' && sidebarFilter()) {
                    e.stopPropagation()
                    setSidebarFilter('')
                  }
                }}
              />
            </div>
            <div
              ref={treeRef}
              tabindex="0"
              class="flex-1 overflow-y-auto px-1 pb-2 focus:outline-none"
              onKeyDown={onTreeKeyDown}
              onFocusIn={() => setTreeFocused(true)}
              onFocusOut={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) setTreeFocused(false)
              }}
              onDragOver={(e) => {
                const drag = dragging()
                if (!drag || e.target !== e.currentTarget) return
                if (drag.kind === 'request' || drag.parentId === null) {
                  e.preventDefault()
                  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
                  setDropTarget({ kind: 'root-end' })
                }
              }}
              onDrop={(e) => {
                if (e.target !== e.currentTarget) return
                e.preventDefault()
                performDrop()
              }}
              onDragLeave={(e) => {
                if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropTarget(null)
              }}
            >
              <Show when={isEmpty()}>
                <div class="flex flex-col items-center gap-3 px-4 py-10 text-center">
                  <IconCollection size={28} class="text-ink-faint" />
                  <div>
                    <p class="text-sm font-medium text-ink-dim">No requests yet</p>
                    <p class="mt-1 text-xs text-ink-muted">Create your first request, or import cURL, OpenAPI, Postman, Insomnia, Bruno, or HAR.</p>
                  </div>
                  <div class="flex gap-2">
                    <button
                      class="rounded bg-accent px-2.5 py-1 text-xs font-medium text-accent-contrast hover:bg-accent-hover"
                      onClick={() => void createRequest()}
                    >
                      New Request
                    </button>
                    <button
                      class="rounded bg-field px-2.5 py-1 text-xs font-medium text-ink-dim hover:bg-raised"
                      onClick={() => setImportModalOpen(true)}
                    >
                      Import…
                    </button>
                  </div>
                </div>
              </Show>
              <Show when={!isEmpty() && noMatches()}>
                <p class="px-2 py-4 text-xs text-ink-faint">No matches.</p>
              </Show>
              <Show when={!isEmpty() && !noMatches()}>
                <TreeChildren node={tree()} depth={0} />
                <Show when={dropTarget()?.kind === 'root-end'}>
                  <div class="mx-2 mt-0.5 h-0.5 rounded bg-accent" />
                </Show>
              </Show>
            </div>
          </Match>

          <Match when={explorerTab() === 'history'}>
            <div class="flex-1 overflow-hidden">
              <HistoryPanel />
            </div>
          </Match>

          <Match when={explorerTab() === 'git'}>
            <div class="flex-1 overflow-hidden">
              <GitPanel />
            </div>
          </Match>

          <Match when={explorerTab() === 'mcp'}>
            <div class="flex-1 overflow-hidden">
              <McpPanel />
            </div>
          </Match>

          <Match when={explorerTab() === 'cookies'}>
            <div class="flex-1 overflow-hidden">
              <CookiesPanel />
            </div>
          </Match>
        </Switch>
      </>
    )
  }

  return (
    <>
      <Show when={sidebarMode() === 'docked'}>
        {/* Collapsing animates width on this shell while the inner column
            keeps a fixed pixel width — content slides out of view instead of
            re-wrapping on every animation frame (the cheap way to animate a
            layout-participating panel). */}
        <div
          class="relative h-full shrink-0 overflow-hidden"
          classList={{
            // The panel treatment only applies while open: at width 0 a border
            // and shadow would paint a 1px sliver against the window ground.
            panel: explorerOpen(),
            'transition-[width] duration-150 ease-out': !resizing(),
          }}
          style={{ width: explorerOpen() ? `${sidebarWidth()}px` : '0px' }}
        >
          {/* Collapsed (explorerOpen false) means width:0 with overflow:hidden
              on the shell, but this inner column keeps its fixed pixel width so
              its inputs/buttons/tabindex tree would stay focusable and Tab could
              land in the invisible sidebar. `inert` removes it from the tab order
              (and from hit-testing) while keeping it laid out for the width
              animation. undefined (not false) so the attribute is absent when
              open — a present `inert` of any value is still inert. */}
          <div
            class="flex h-full flex-col overflow-hidden"
            style={{ width: `${sidebarWidth()}px` }}
            inert={!explorerOpen() ? true : undefined}
          >
            <SidebarBody />
          </div>
          <div
            class="absolute inset-y-0 right-0 z-10 w-1 cursor-col-resize"
            classList={{ 'bg-accent/60': resizing(), 'hover:bg-accent/30': !resizing() }}
            onPointerDown={startResize}
          />
        </div>
      </Show>

      <Show when={sidebarMode() === 'overlay' && explorerOpen()}>
        {/* Transparent click-catcher — closes the drawer without dimming the
            rest of the app (a heavy modal scrim would fight the "lightweight"
            thesis for what is really just a navigation aid). */}
        <div class="fixed inset-0 z-30" onClick={() => setExplorerOpen(false)} />
        <div
          class="fixed bottom-1 left-12 top-1 z-40 flex flex-col rounded-xl border border-edge bg-surface shadow-2xl"
          style={{ width: `${sidebarWidth()}px` }}
        >
          <SidebarBody />
          <div
            class="absolute inset-y-0 right-0 z-10 w-1 cursor-col-resize"
            classList={{ 'bg-accent/60': resizing(), 'hover:bg-accent/30': !resizing() }}
            onPointerDown={startResize}
          />
        </div>
      </Show>

      <Show when={menu()}>{(m) => <ContextMenu x={m().x} y={m().y} items={m().items} onClose={() => setMenu(null)} />}</Show>
    </>
  )
}
