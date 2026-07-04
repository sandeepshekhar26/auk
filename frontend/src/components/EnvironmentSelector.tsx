import { For, Show, createMemo } from 'solid-js'
import { appState, setAppState, setEnvironmentEditorOpen } from '../lib/store'

export default function EnvironmentSelector() {
  // A native <select>'s <option> background can't be reliably colored
  // cross-platform, so the active environment's color (the actual
  // "avoid prod mistakes" value — glanceable without opening the dropdown)
  // is shown as a dot next to the select instead of trying to tint it.
  const activeColor = createMemo(() => appState.environments.find((e) => e.id === appState.activeEnvironmentId)?.color ?? null)

  return (
    <div class="flex items-center gap-1.5">
      <Show when={activeColor()}>
        <span class="h-2 w-2 shrink-0 rounded-full" style={{ 'background-color': activeColor() ?? undefined }} />
      </Show>
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
