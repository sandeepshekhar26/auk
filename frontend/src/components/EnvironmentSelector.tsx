import { For } from 'solid-js'
import { appState, setAppState, setEnvironmentEditorOpen } from '../lib/store'

export default function EnvironmentSelector() {
  return (
    <div class="flex items-center gap-1.5">
      <select
        class="rounded bg-neutral-900 px-2 py-1 text-xs text-neutral-300 focus:outline-none focus:ring-1 focus:ring-neutral-600"
        value={appState.activeEnvironmentId ?? ''}
        onChange={(e) => setAppState('activeEnvironmentId', e.currentTarget.value || null)}
      >
        <option value="">No environment</option>
        <For each={appState.environments}>{(env) => <option value={env.id}>{env.name}</option>}</For>
      </select>
      <button
        class="rounded px-1.5 py-1 text-xs text-neutral-500 hover:bg-neutral-800 hover:text-neutral-200"
        onClick={() => setEnvironmentEditorOpen(true)}
        title="Manage environments"
      >
        Manage
      </button>
    </div>
  )
}
