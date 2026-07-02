import { For } from 'solid-js'
import { appState, setAppState } from '../lib/store'

export default function WorkspaceSwitcher() {
  return (
    <select
      class="w-full truncate rounded bg-neutral-900 px-2 py-1 text-sm font-medium text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
      value={appState.activeWorkspaceId ?? ''}
      onChange={(e) => setAppState('activeWorkspaceId', e.currentTarget.value || null)}
    >
      <For each={appState.workspaces}>{(ws) => <option value={ws.id}>{ws.name}</option>}</For>
    </select>
  )
}
