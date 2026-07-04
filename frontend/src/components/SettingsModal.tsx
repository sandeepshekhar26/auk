import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js'
import { settingsOpen, setSettingsOpen } from '../lib/store'
import { setTheme, themePref } from '../lib/theme'
import type { ThemePref } from '../lib/theme'
import { wails } from '../lib/wails'

interface MCPStatus {
  running: boolean
  url: string
  token: string
  connectCommand: string
  error?: string
}

const THEME_OPTIONS: { value: ThemePref; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export default function SettingsModal() {
  const [mcp, setMcp] = createSignal<MCPStatus | null>(null)
  const [copied, setCopied] = createSignal(false)
  const [toggling, setToggling] = createSignal(false)

  // Refresh MCP status whenever the panel opens.
  createEffect(() => {
    if (settingsOpen()) {
      wails.GetMCPStatus().then((s) => setMcp(s as MCPStatus)).catch(() => setMcp(null))
    }
  })

  async function toggleMCP() {
    const cur = mcp()
    setToggling(true)
    try {
      const next = await wails.SetMCPEnabled(!(cur?.running ?? false))
      setMcp(next as MCPStatus)
    } finally {
      setToggling(false)
    }
  }

  async function copyConnect() {
    const cmd = mcp()?.connectCommand
    if (!cmd) return
    try {
      await navigator.clipboard.writeText(cmd)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* clipboard unavailable */
    }
  }

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
              <p class="mt-2 text-xs text-ink-faint">Saved to ~/.auk/settings.yaml</p>
            </section>

            <section>
              <div class="flex items-center justify-between">
                <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">MCP Server</h3>
                <button
                  class="rounded px-2 py-1 text-xs font-medium disabled:opacity-50"
                  classList={{
                    'bg-accent text-accent-contrast hover:bg-accent-hover': !(mcp()?.running ?? false),
                    'bg-raised text-ink-dim hover:bg-elevated': mcp()?.running ?? false,
                  }}
                  disabled={toggling()}
                  onClick={toggleMCP}
                >
                  {toggling() ? '…' : (mcp()?.running ? 'Stop' : 'Start')}
                </button>
              </div>
              <p class="mt-1 text-xs text-ink-muted">
                Lets Claude Code drive this app: list and run your requests, run load tests. Mutating
                requests (POST/PUT/PATCH/DELETE) prompt for approval here first.
              </p>

              <Show when={mcp()?.error}>
                <p class="mt-2 rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">{mcp()!.error}</p>
              </Show>

              <Show when={mcp()?.running}>
                <div class="mt-2 flex flex-col gap-1.5">
                  <div class="flex items-center gap-2 text-xs">
                    <span class="h-1.5 w-1.5 rounded-full bg-accent" />
                    <span class="font-mono text-ink-dim">{mcp()!.url}</span>
                  </div>
                  <button
                    class="self-start rounded bg-field px-2 py-1 text-[11px] text-ink-dim hover:bg-raised"
                    onClick={copyConnect}
                  >
                    {copied() ? 'Copied' : 'Copy "claude mcp add" command'}
                  </button>
                </div>
              </Show>
            </section>
          </div>
        </div>
      </div>
    </Show>
  )
}
