import { For } from 'solid-js'
import { appState, setAppState } from '../lib/store'

export default function WorkspaceSwitcher() {
  return (
    <select
      class="w-full truncate rounded bg-field px-2 py-1 text-sm font-medium text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
      value={appState.activeWorkspaceId ?? ''}
      onChange={(e) => setAppState('activeWorkspaceId', e.currentTarget.value || null)}
    >
      <For each={appState.workspaces}>{(ws) => <option value={ws.id}>{ws.name}</option>}</For>
    </select>
  )
}
