import { For } from 'solid-js'
import { appState, setAppState, setEnvironmentEditorOpen } from '../lib/store'

export default function EnvironmentSelector() {
  return (
    <div class="flex items-center gap-1.5">
      <select
        class="rounded bg-field px-2 py-1 text-xs text-ink-dim focus:outline-none focus:ring-1 focus:ring-edge-strong"
        value={appState.activeEnvironmentId ?? ''}
        onChange={(e) => setAppState('activeEnvironmentId', e.currentTarget.value || null)}
      >
        <option value="">No environment</option>
        <For each={appState.environments}>{(env) => <option value={env.id}>{env.name}</option>}</For>
      </select>
      <button
        class="rounded px-1.5 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink"
        onClick={() => setEnvironmentEditorOpen(true)}
        title="Manage environments"
      >
        Manage
      </button>
    </div>
  )
}
