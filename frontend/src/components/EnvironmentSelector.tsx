import { For } from 'solid-js'
import { appState, setAppState } from '../lib/store'

export default function EnvironmentSelector() {
  return (
    <select
      class="rounded bg-neutral-900 px-2 py-1 text-xs text-neutral-300 focus:outline-none focus:ring-1 focus:ring-neutral-600"
      value={appState.activeEnvironmentId ?? ''}
      onChange={(e) => setAppState('activeEnvironmentId', e.currentTarget.value || null)}
    >
      <option value="">No environment</option>
      <For each={appState.environments}>{(env) => <option value={env.id}>{env.name}</option>}</For>
    </select>
  )
}
