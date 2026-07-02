import { For, Show, onCleanup, onMount } from 'solid-js'
import { settingsOpen, setSettingsOpen } from '../lib/store'
import { setTheme, themePref } from '../lib/theme'
import type { ThemePref } from '../lib/theme'

const THEME_OPTIONS: { value: ThemePref; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export default function SettingsModal() {
  function close() {
    setSettingsOpen(false)
  }

  function onKeyDown(e: KeyboardEvent) {
    // Cmd/Ctrl+, — the standard macOS Preferences shortcut.
    if ((e.metaKey || e.ctrlKey) && e.key === ',') {
      e.preventDefault()
      setSettingsOpen((v) => !v)
    }
    if (e.key === 'Escape' && settingsOpen()) close()
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  return (
    <Show when={settingsOpen()}>
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={close}>
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Settings"
          class="w-full max-w-md rounded-lg border border-edge bg-surface shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <header class="flex items-center justify-between border-b border-edge px-4 py-3">
            <h2 class="text-sm font-semibold text-ink">Settings</h2>
            <button
              aria-label="Close settings"
              class="rounded px-2 py-0.5 text-sm text-ink-muted hover:bg-raised hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
              onClick={close}
            >
              ×
            </button>
          </header>

          <div class="space-y-6 px-4 py-4">
            <section>
              <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">Appearance</h3>
              <div
                role="radiogroup"
                aria-label="Theme"
                class="mt-2 flex divide-x divide-edge overflow-hidden rounded-md border border-edge"
              >
                <For each={THEME_OPTIONS}>
                  {(opt) => (
                    <button
                      role="radio"
                      aria-checked={themePref() === opt.value}
                      class="flex-1 px-3 py-1.5 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
                      classList={{
                        'bg-accent font-medium text-accent-contrast': themePref() === opt.value,
                        'bg-field text-ink-dim hover:bg-raised hover:text-ink': themePref() !== opt.value,
                      }}
                      onClick={() => setTheme(opt.value)}
                    >
                      {opt.label}
                    </button>
                  )}
                </For>
              </div>
              <p class="mt-2 text-xs text-ink-faint">Saved to ~/.apitool/settings.yaml</p>
            </section>
          </div>
        </div>
      </div>
    </Show>
  )
}
