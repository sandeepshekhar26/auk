import { For, Show, createMemo, createSignal } from 'solid-js'
import { appState, openTab, sidebarFilter, setSidebarFilter } from '../lib/store'
import { createRequest } from '../lib/data'
import type { Folder, RequestDef } from '../types'
import WorkspaceSwitcher from './WorkspaceSwitcher'

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

export default function Sidebar() {
  const [expanded, setExpanded] = createSignal<Set<string>>(new Set())

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

  function FolderRow(props: { node: FolderNode; depth: number }) {
    const folder = () => props.node.folder!
    const open = () => isExpanded(folder().id)
    return (
      <Show when={!filtering() || nodeMatches(props.node, query())}>
        <button
          class="flex w-full items-center gap-1 rounded px-2 py-1 text-left text-sm text-neutral-300 hover:bg-neutral-800"
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          onClick={() => toggleFolder(folder().id)}
        >
          <span class="w-3 shrink-0 text-neutral-600">{open() ? '▾' : '▸'}</span>
          <span class="truncate text-neutral-400">{folder().name}</span>
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
          class="flex w-full items-center gap-2 rounded px-2 py-1 text-left text-sm text-neutral-300 hover:bg-neutral-800"
          style={{ 'padding-left': `${8 + props.depth * 14}px` }}
          onClick={() => openTab(props.req.id)}
        >
          <span class="w-12 shrink-0 font-mono text-[10px] font-semibold text-emerald-400">{props.req.method}</span>
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
    <div class="flex h-full w-64 flex-col border-r border-neutral-800 bg-neutral-925">
      <div class="border-b border-neutral-800 p-2">
        <WorkspaceSwitcher />
      </div>
      <div class="p-2">
        <input
          class="w-full rounded bg-neutral-900 px-2 py-1 text-sm text-neutral-200 placeholder:text-neutral-500 focus:outline-none focus:ring-1 focus:ring-neutral-600"
          placeholder="Filter requests…"
          value={sidebarFilter()}
          onInput={(e) => setSidebarFilter(e.currentTarget.value)}
        />
      </div>
      <div class="flex-1 overflow-y-auto px-1 pb-2">
        <Show when={isEmpty()}>
          <div class="flex flex-col items-start gap-2 px-2 py-4">
            <p class="text-xs text-neutral-600">No requests yet.</p>
            <button
              class="rounded bg-emerald-600 px-2 py-1 text-xs font-medium text-white hover:bg-emerald-500"
              onClick={() => void createRequest()}
            >
              + New Request
            </button>
          </div>
        </Show>
        <Show when={!isEmpty() && noMatches()}>
          <p class="px-2 py-4 text-xs text-neutral-600">No matches.</p>
        </Show>
        <Show when={!isEmpty() && !noMatches()}>
          <TreeChildren node={tree()} depth={0} />
        </Show>
      </div>
    </div>
  )
}
