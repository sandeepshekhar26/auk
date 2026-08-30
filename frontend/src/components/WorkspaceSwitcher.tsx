import { For, createMemo } from 'solid-js'
import { appState, setAppState } from '../lib/store'

export default function WorkspaceSwitcher() {
  // ListWorkspaces returns Go-map order (random per launch). Sort a COPY by
  // orderKey — the same rule the request tree uses (buildTree's byOrderKey and
  // data.ts's sibling sorts: orderKey.localeCompare) — so the list is stable
  // and matches the backend's minted/healed ordering. Never sort
  // appState.workspaces in place; it's the live store array.
  const sortedWorkspaces = createMemo(() =>
    [...appState.workspaces].sort((a, b) => a.orderKey.localeCompare(b.orderKey)),
  )
  return (
    <select
      class="w-full truncate rounded bg-field px-2 py-1 text-sm font-medium text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
      value={appState.activeWorkspaceId ?? ''}
      onChange={(e) => setAppState('activeWorkspaceId', e.currentTarget.value || null)}
    >
      <For each={sortedWorkspaces()}>{(ws) => <option value={ws.id}>{ws.name}</option>}</For>
    </select>
  )
}
