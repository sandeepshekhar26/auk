import { Index, Show, createEffect, createMemo, createSignal, onCleanup, onMount, type Accessor } from 'solid-js'
import { appState, setAppState, openTab, sidebarFilter, setSidebarFilter, explorerOpen, explorerTab, setExplorerTab, setExplorerOpen } from '../lib/store'
import { createRequest, createFolder, runFolder, saveFolderDebounced } from '../lib/data'
import type { Folder, KeyValue, RequestDef } from '../types'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import HistoryPanel from './HistoryPanel'
import GitPanel from './GitPanel'
import McpPanel from './McpPanel'
import CookiesPanel from './CookiesPanel'
import KeyValueTable from './KeyValueTable'

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

// A folder survives filtering if it (or any descendant request/folder) matches.
function nodeMatches(node: FolderNode, query: string): boolean {
  if (node.requests.some((r) => matchesRequest(r, query))) return true
  return node.children.some((child) => nodeMatches(child, query))
}

function matchesRequest(req: RequestDef, query: string): boolean {
  return req.name.toLowerCase().includes(query) || req.url.toLowerCase().includes(query)
}

// Sidebar is the EXPLORER DRAWER: an on-demand overlay (⌘B / rail / palette),
// not a permanently-docked tree — the structural departure from Postman/
// Yaak's always-visible sidebar. It holds two sections (Requests / History)
// behind an internal tab switcher, closes on Escape or a click outside, and
// picking a request closes it automatically so the editor gets full width
// back immediately.
export default function Sidebar() {
  const [expanded, setExpanded] = createSignal<Set<string>>(new Set())
  // Which folders currently have their variables editor open — separate from
  // `expanded` (that's the tree-disclosure state for children/requests).
  const [varsOpenFor, setVarsOpenFor] = createSignal<Set<string>>(new Set())
  let filterInput: HTMLInputElement | undefined

  function toggleFolder(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
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

  // All four setters below write through Solid's fine-grained store PATH
  // setter (setAppState('folders', predicate, ...path, value)) rather than
  // reconstructing folder.variables with .map()/.filter()/spread and writing
  // the whole new array back. The path form mutates only the touched leaf,
  // so sibling rows (and, for setFolderVariable, every OTHER field on the
  // SAME row) keep their exact object references — which is what lets
  // KeyValueTable's own <For> avoid remounting the input the user is
  // currently typing into. This is the same fix as the FolderRow/<Index>
  // change above, one level deeper: reconstructing the array here would
  // undo that fix by giving <For> a fresh row object on every keystroke
  // regardless of how stable the outer tree is. RequestEditor's setRow
  // (Params/Headers) already uses this exact pattern; this now matches it.
  //
  // Each function looks up the current folder fresh from appState.folders
  // AFTER the store write (synchronous) rather than accepting a Folder
  // snapshot as a parameter, so the debounced save always persists the
  // latest state even if two edits land in the same debounce window.
  function currentFolder(folderId: string): Folder | undefined {
    return appState.folders.find((f) => f.id === folderId)
  }

  function renameFolder(folderId: string, name: string) {
    setAppState('folders', (f) => f.id === folderId, 'name', name)
    const updated = currentFolder(folderId)
    if (updated) saveFolderDebounced(updated)
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

  function pickRequest(id: string) {
    openTab(id)
    setExplorerOpen(false)
  }

  function startFolderRun(folder: Folder) {
    void runFolder(folder.id, folder.name)
    setExplorerOpen(false)
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && explorerOpen()) setExplorerOpen(false)
  }
  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  // Autofocus the filter input whenever the drawer opens on the Requests tab.
  createEffect(() => {
    if (explorerOpen() && explorerTab() === 'requests') filterInput?.focus()
  })

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
    return (
      <Show when={!filtering() || nodeMatches(props.node(), query())}>
        <div class="group flex w-full items-center gap-1 rounded px-2 py-1 hover:bg-raised" style={{ 'padding-left': `${8 + props.depth * 14}px` }}>
          <button class="shrink-0 text-ink-faint" onClick={() => toggleFolder(folder().id)} title={open() ? 'Collapse' : 'Expand'}>
            {open() ? '▾' : '▸'}
          </button>
          <input
            class="min-w-0 flex-1 truncate rounded bg-transparent px-1 text-sm text-ink-muted focus:bg-field focus:text-ink focus:outline-none"
            value={folder().name}
            onInput={(e) => renameFolder(folder().id, e.currentTarget.value)}
          />
          <button
            class="shrink-0 rounded px-1 text-xs text-ink-faint opacity-0 hover:bg-elevated hover:text-ink-dim group-hover:opacity-100"
            title="New subfolder"
            onClick={() => void createFolder(folder().id)}
          >
            +
          </button>
          <button
            class="shrink-0 rounded px-1 text-xs opacity-0 hover:bg-elevated group-hover:opacity-100"
            classList={{ 'text-accent-fg opacity-100': folder().variables.length > 0, 'text-ink-faint hover:text-ink-dim': folder().variables.length === 0 }}
            title="Folder variables (inherited by every request inside)"
            onClick={() => toggleVarsEditor(folder().id)}
          >
            {folder().variables.length > 0 ? `{${folder().variables.length}}` : '{ }'}
          </button>
          <button
            class="shrink-0 rounded px-1 text-xs text-ink-faint opacity-0 hover:bg-elevated hover:text-ink-dim group-hover:opacity-100"
            title="Run every request in this folder"
            onClick={() => startFolderRun(folder())}
          >
            ▶
          </button>
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
    return (
      <Show when={!filtering() || matchesRequest(props.req(), query())}>
        <button
          class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-sm text-ink-dim hover:bg-raised"
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          onClick={() => pickRequest(props.req().id)}
        >
          <span class="w-12 shrink-0 font-mono text-[10px] font-semibold text-accent-fg">{props.req().method}</span>
          <span class="truncate">{props.req().name}</span>
        </button>
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

  return (
    <Show when={explorerOpen()}>
      {/* Transparent click-catcher — closes the drawer without dimming the
          rest of the app (a heavy modal scrim would fight the "lightweight"
          thesis for what is really just a navigation aid). */}
      <div class="fixed inset-0 z-30" onClick={() => setExplorerOpen(false)} />
      <div class="fixed bottom-0 left-12 top-0 z-40 flex w-72 flex-col border-r border-edge bg-surface shadow-2xl">
        <div class="flex items-center gap-1 border-b border-edge p-2">
          <button
            class="flex-1 rounded px-2 py-1 text-xs font-medium"
            classList={{
              'bg-raised text-ink': explorerTab() === 'requests',
              'text-ink-muted hover:text-ink-dim': explorerTab() !== 'requests',
            }}
            onClick={() => setExplorerTab('requests')}
          >
            Requests
          </button>
          <button
            class="flex-1 rounded px-2 py-1 text-xs font-medium"
            classList={{
              'bg-raised text-ink': explorerTab() === 'history',
              'text-ink-muted hover:text-ink-dim': explorerTab() !== 'history',
            }}
            onClick={() => setExplorerTab('history')}
          >
            History
          </button>
          <button
            class="flex-1 rounded px-2 py-1 text-xs font-medium"
            classList={{
              'bg-raised text-ink': explorerTab() === 'git',
              'text-ink-muted hover:text-ink-dim': explorerTab() !== 'git',
            }}
            onClick={() => setExplorerTab('git')}
          >
            Git
          </button>
          <button
            class="flex-1 rounded px-2 py-1 text-xs font-medium"
            classList={{
              'bg-raised text-ink': explorerTab() === 'mcp',
              'text-ink-muted hover:text-ink-dim': explorerTab() !== 'mcp',
            }}
            onClick={() => setExplorerTab('mcp')}
          >
            MCP
          </button>
          <button
            class="flex-1 rounded px-2 py-1 text-xs font-medium"
            classList={{
              'bg-raised text-ink': explorerTab() === 'cookies',
              'text-ink-muted hover:text-ink-dim': explorerTab() !== 'cookies',
            }}
            onClick={() => setExplorerTab('cookies')}
          >
            Cookies
          </button>
          <button
            class="ml-1 rounded px-1.5 py-1 text-xs text-ink-faint hover:bg-raised hover:text-ink-dim"
            title="Close (Esc)"
            onClick={() => setExplorerOpen(false)}
          >
            Esc
          </button>
        </div>

        <Show when={explorerTab() === 'requests'}>
          <div class="border-b border-edge p-2">
            <WorkspaceSwitcher />
          </div>
          <div class="flex items-center gap-1 p-2">
            <input
              ref={filterInput}
              class="min-w-0 flex-1 rounded bg-field px-2 py-1 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder="Filter requests…"
              value={sidebarFilter()}
              onInput={(e) => setSidebarFilter(e.currentTarget.value)}
            />
            <button
              class="shrink-0 rounded px-1.5 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink"
              title="New folder (root level)"
              onClick={() => void createFolder(null)}
            >
              + Folder
            </button>
          </div>
          <div class="flex-1 overflow-y-auto px-1 pb-2">
            <Show when={isEmpty()}>
              <div class="flex flex-col items-start gap-2 px-2 py-4">
                <p class="text-xs text-ink-faint">No requests yet.</p>
                <div class="flex gap-2">
                  <button
                    class="rounded bg-accent px-2 py-1 text-xs font-medium text-accent-contrast hover:bg-accent-hover"
                    onClick={() => void createRequest()}
                  >
                    + New Request
                  </button>
                  <button
                    class="rounded bg-field px-2 py-1 text-xs font-medium text-ink-dim hover:bg-raised"
                    onClick={() => void createFolder(null)}
                  >
                    + New Folder
                  </button>
                </div>
              </div>
            </Show>
            <Show when={!isEmpty() && noMatches()}>
              <p class="px-2 py-4 text-xs text-ink-faint">No matches.</p>
            </Show>
            <Show when={!isEmpty() && !noMatches()}>
              <TreeChildren node={tree()} depth={0} />
            </Show>
          </div>
        </Show>

        <Show when={explorerTab() === 'history'}>
          <div class="flex-1 overflow-hidden">
            <HistoryPanel />
          </div>
        </Show>

        <Show when={explorerTab() === 'git'}>
          <div class="flex-1 overflow-hidden">
            <GitPanel />
          </div>
        </Show>

        <Show when={explorerTab() === 'mcp'}>
          <div class="flex-1 overflow-hidden">
            <McpPanel />
          </div>
        </Show>

        <Show when={explorerTab() === 'cookies'}>
          <div class="flex-1 overflow-hidden">
            <CookiesPanel />
          </div>
        </Show>
      </div>
    </Show>
  )
}
