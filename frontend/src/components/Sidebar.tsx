import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import { appState, openTab, sidebarFilter, setSidebarFilter, explorerOpen, explorerTab, setExplorerTab, setExplorerOpen } from '../lib/store'
import { createRequest } from '../lib/data'
import type { Folder, RequestDef } from '../types'
import WorkspaceSwitcher from './WorkspaceSwitcher'
import HistoryPanel from './HistoryPanel'
import GitPanel from './GitPanel'

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
  let filterInput: HTMLInputElement | undefined

  function toggleFolder(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
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

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && explorerOpen()) setExplorerOpen(false)
  }
  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  // Autofocus the filter input whenever the drawer opens on the Requests tab.
  createEffect(() => {
    if (explorerOpen() && explorerTab() === 'requests') filterInput?.focus()
  })

  function FolderRow(props: { node: FolderNode; depth: number }) {
    const folder = () => props.node.folder!
    const open = () => isExpanded(folder().id)
    return (
      <Show when={!filtering() || nodeMatches(props.node, query())}>
        <button
          class="flex w-full items-center gap-1 rounded px-2 py-1 text-left text-sm text-ink-dim hover:bg-raised"
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          onClick={() => toggleFolder(folder().id)}
        >
          <span class="w-3 shrink-0 text-ink-faint">{open() ? '▾' : '▸'}</span>
          <span class="truncate text-ink-muted">{folder().name}</span>
        </button>
        <Show when={open()}>
          <TreeChildren node={props.node} depth={props.depth + 1} />
        </Show>
      </Show>
    )
  }

  function RequestRow(props: { req: RequestDef; depth: number }) {
    return (
      <Show when={!filtering() || matchesRequest(props.req, query())}>
        <button
          class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-sm text-ink-dim hover:bg-raised"
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          onClick={() => pickRequest(props.req.id)}
        >
          <span class="w-12 shrink-0 font-mono text-[10px] font-semibold text-accent-fg">{props.req.method}</span>
          <span class="truncate">{props.req.name}</span>
        </button>
      </Show>
    )
  }

  function TreeChildren(props: { node: FolderNode; depth: number }) {
    return (
      <>
        <For each={props.node.children}>{(child) => <FolderRow node={child} depth={props.depth} />}</For>
        <For each={props.node.requests}>{(req) => <RequestRow req={req} depth={props.depth} />}</For>
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
          <div class="p-2">
            <input
              ref={filterInput}
              class="w-full rounded bg-field px-2 py-1 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder="Filter requests…"
              value={sidebarFilter()}
              onInput={(e) => setSidebarFilter(e.currentTarget.value)}
            />
          </div>
          <div class="flex-1 overflow-y-auto px-1 pb-2">
            <Show when={isEmpty()}>
              <div class="flex flex-col items-start gap-2 px-2 py-4">
                <p class="text-xs text-ink-faint">No requests yet.</p>
                <button
                  class="rounded bg-accent px-2 py-1 text-xs font-medium text-accent-contrast hover:bg-accent-hover"
                  onClick={() => void createRequest()}
                >
                  + New Request
                </button>
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
      </div>
    </Show>
  )
}
