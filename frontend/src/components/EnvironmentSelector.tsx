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
      {/* Chip treatment matching the redesigned chrome: the select carries
          its own bordered surface so it reads as a control on the window
          ground, and the environment's colour dot sits INSIDE the chip —
          it belongs to the value, not to the row. */}
      <div class="flex h-7 items-center gap-1.5 rounded-lg border border-edge bg-surface pl-2.5 pr-1">
        <Show when={activeColor()}>
          <span class="h-[7px] w-[7px] shrink-0 rounded-full" style={{ 'background-color': activeColor() ?? undefined }} />
        </Show>
        <select
          class="bg-transparent pr-1 text-xs font-medium text-ink focus:outline-none"
          value={appState.activeEnvironmentId ?? ''}
          onChange={(e) => setAppState('activeEnvironmentId', e.currentTarget.value || null)}
        >
          <option value="">No environment</option>
          <For each={appState.environments}>{(env) => <option value={env.id}>{env.name}</option>}</For>
        </select>
      </div>
      <button
        class="h-7 rounded-lg px-2 text-xs text-ink-muted hover:bg-raised hover:text-ink"
        onClick={() => setEnvironmentEditorOpen(true)}
        title="Manage environments"
      >
        Manage
      </button>
    </div>
  )
}
