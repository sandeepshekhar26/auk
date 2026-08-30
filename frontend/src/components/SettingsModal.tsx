import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js'
import { appState, settingsOpen, setSettingsOpen } from '../lib/store'
import { setTheme, themePref } from '../lib/theme'
import type { ThemePref } from '../lib/theme'
import { wails } from '../lib/wails'
import { copyText } from '../lib/clipboard'
import LicenseSection from './LicenseSection'
import { MethodBadge } from './icons'
import { UpdateSettingRow } from './UpdateBanner'

interface MCPStatus {
  running: boolean
  url: string
  token: string
  connectCommand: string
  error?: string
}

interface MockStatus {
  running: boolean
  port: number
  workspaceId: string
  routes: number
  error?: string
}

interface MockRoute {
  method: string
  path: string
  requestId: string
  requestName: string
  status: number
}

// StartMockServer/StopMockServer/MockServerStatus/MockServerRoutes are Go
// bindings added in app_mockserver.go, and `mockPort` is a new AppSettings
// field. Wails regenerates the typed wrappers in
// frontend/wailsjs/go/main/App.{d.ts,js} (and the models) on the next `wails
// dev`/build, at which point these are statically typed. Until that
// regeneration runs we reach them through a locally-typed view of the
// bindings module so tsc and the bundler stay green — same pattern as
// ResponseViewer.tsx's saveResponseBodyBinding. See INTEGRATION NOTES.
interface MockBindings {
  StartMockServer(workspaceId: string, port: number): Promise<MockStatus>
  StopMockServer(): Promise<MockStatus>
  MockServerStatus(): Promise<MockStatus>
  MockServerRoutes(): Promise<MockRoute[]>
}

function mockBindings(): MockBindings {
  return wails as unknown as MockBindings
}

const DEFAULT_MOCK_PORT = 8725

const THEME_OPTIONS: { value: ThemePref; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export default function SettingsModal() {
  const [mcp, setMcp] = createSignal<MCPStatus | null>(null)
  const [copied, setCopied] = createSignal(false)
  const [copyFailed, setCopyFailed] = createSignal(false)
  const [toggling, setToggling] = createSignal(false)
  const [mock, setMock] = createSignal<MockStatus | null>(null)
  const [mockRoutes, setMockRoutes] = createSignal<MockRoute[]>([])
  const [mockPort, setMockPort] = createSignal(DEFAULT_MOCK_PORT)
  const [mockBusy, setMockBusy] = createSignal(false)
  const [mockError, setMockError] = createSignal('')
  const [mockUrlCopied, setMockUrlCopied] = createSignal(false)
  const [mockUrlCopyFailed, setMockUrlCopyFailed] = createSignal(false)

  // Refresh MCP status whenever the panel opens.
  createEffect(() => {
    if (settingsOpen()) {
      wails.GetMCPStatus().then((s) => setMcp(s as MCPStatus)).catch(() => setMcp(null))
    }
  })

  // Same for the mock server. The port input seeds from the running server
  // when there is one, else from the remembered setting, else the default —
  // so the number on screen is always the one Start would actually use.
  createEffect(() => {
    if (!settingsOpen()) return
    void refreshMock()
    wails
      .GetSettings()
      .then((s) => {
        const saved = (s as unknown as { mockPort?: number }).mockPort
        if (saved && saved > 0 && !mock()?.running) setMockPort(saved)
      })
      .catch(() => {
        /* unreadable settings — keep the default port */
      })
  })

  async function refreshMock() {
    try {
      const st = await mockBindings().MockServerStatus()
      setMock(st)
      if (st.running) {
        setMockPort(st.port)
        setMockRoutes(await mockBindings().MockServerRoutes())
      } else {
        setMockRoutes([])
      }
    } catch {
      setMock(null)
      setMockRoutes([])
    }
  }

  // Start serves the ACTIVE workspace: the mock replays what that workspace
  // recorded, so tying it to the current selection (rather than a second
  // workspace picker in Settings) keeps "what am I serving?" unambiguous.
  async function toggleMock() {
    setMockBusy(true)
    setMockError('')
    try {
      if (mock()?.running) {
        setMock(await mockBindings().StopMockServer())
        setMockRoutes([])
        return
      }
      const workspaceId = appState.activeWorkspaceId
      if (!workspaceId) {
        setMockError('Select a workspace first.')
        return
      }
      setMock(await mockBindings().StartMockServer(workspaceId, mockPort()))
      setMockRoutes(await mockBindings().MockServerRoutes())
    } catch (err) {
      setMockError(err instanceof Error ? err.message : String(err))
    } finally {
      setMockBusy(false)
    }
  }

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

  // lib/clipboard's copyText, not navigator.clipboard: the async Clipboard
  // API rejects inside the packaged app's WKWebView, and the old catch here
  // swallowed that so the button just never changed. copyFailed drives a
  // visible failure state instead.
  async function copyConnect() {
    const cmd = mcp()?.connectCommand
    if (!cmd) return
    const result = await copyText(cmd)
    setCopied(result.ok)
    setCopyFailed(!result.ok)
    setTimeout(() => {
      setCopied(false)
      setCopyFailed(false)
    }, result.ok ? 1500 : 4000)
  }

  // Same lib/clipboard path (and same visible-failure handling) as
  // copyConnect above — navigator.clipboard rejects in the packaged
  // WKWebView.
  async function copyMockUrl() {
    const st = mock()
    if (!st?.running) return
    const result = await copyText(`http://127.0.0.1:${st.port}`)
    setMockUrlCopied(result.ok)
    setMockUrlCopyFailed(!result.ok)
    setTimeout(() => {
      setMockUrlCopied(false)
      setMockUrlCopyFailed(false)
    }, result.ok ? 1500 : 4000)
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
              <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">License</h3>
              <div class="mt-2">
                <LicenseSection />
              </div>
            </section>

            <section>
              <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">Updates</h3>
              <div class="mt-2">
                <UpdateSettingRow />
              </div>
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
                    class="self-start rounded bg-field px-2 py-1 text-[11px] hover:bg-raised"
                    classList={{ 'text-danger': copyFailed(), 'text-ink-dim': !copyFailed() }}
                    onClick={copyConnect}
                  >
                    {copyFailed() ? 'Copy failed' : copied() ? 'Copied' : 'Copy "claude mcp add" command'}
                  </button>
                </div>
              </Show>
            </section>

            <section>
              <div class="flex items-center justify-between">
                <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">Mock Server</h3>
                <div class="flex items-center gap-2">
                  <label class="sr-only" for="mock-port">
                    Mock server port
                  </label>
                  <input
                    id="mock-port"
                    type="number"
                    min="1"
                    max="65535"
                    class="w-20 rounded border border-edge bg-field px-2 py-1 text-right font-mono text-xs text-ink focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent disabled:opacity-50"
                    value={mockPort()}
                    disabled={mock()?.running ?? false}
                    onInput={(e) => setMockPort(Number(e.currentTarget.value) || 0)}
                  />
                  <button
                    class="rounded px-2 py-1 text-xs font-medium disabled:opacity-50"
                    classList={{
                      'bg-accent text-accent-contrast hover:bg-accent-hover': !(mock()?.running ?? false),
                      'bg-raised text-ink-dim hover:bg-elevated': mock()?.running ?? false,
                    }}
                    disabled={mockBusy()}
                    onClick={toggleMock}
                  >
                    {mockBusy() ? '…' : (mock()?.running ? 'Stop' : 'Start')}
                  </button>
                </div>
              </div>
              <p class="mt-1 text-xs text-ink-muted">
                Serve this workspace's recorded responses on localhost — point your frontend at it.
              </p>

              <Show when={mockError() || mock()?.error}>
                <p class="mt-2 rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">
                  {mockError() || mock()?.error}
                </p>
              </Show>

              <Show when={mock()?.running}>
                <div class="mt-2 flex flex-col gap-1.5">
                  <div class="flex items-center gap-2 text-xs">
                    <span class="h-1.5 w-1.5 rounded-full bg-accent" />
                    <span class="font-mono text-ink-dim">http://127.0.0.1:{mock()!.port}</span>
                    <span class="text-ink-faint">
                      · {mock()!.routes} {mock()!.routes === 1 ? 'route' : 'routes'}
                    </span>
                  </div>
                  <button
                    class="self-start rounded bg-field px-2 py-1 text-[11px] hover:bg-raised"
                    classList={{ 'text-danger': mockUrlCopyFailed(), 'text-ink-dim': !mockUrlCopyFailed() }}
                    onClick={copyMockUrl}
                  >
                    {mockUrlCopyFailed() ? 'Copy failed' : mockUrlCopied() ? 'Copied' : 'Copy base URL'}
                  </button>

                  <Show
                    when={mockRoutes().length > 0}
                    fallback={
                      <p class="text-[11px] text-ink-faint">
                        No routes yet — send a request in this workspace to record one.
                      </p>
                    }
                  >
                    <ul class="max-h-40 divide-y divide-edge overflow-y-auto rounded border border-edge">
                      <For each={mockRoutes()}>
                        {(r) => (
                          <li class="flex items-center gap-2 px-2 py-1" title={r.requestName}>
                            <MethodBadge method={r.method} class="w-8 text-right" />
                            <span class="truncate font-mono text-[11px] text-ink-dim">{r.path}</span>
                            <span class="ml-auto shrink-0 font-mono text-[10px] text-ink-faint">{r.status}</span>
                          </li>
                        )}
                      </For>
                    </ul>
                  </Show>
                </div>
              </Show>
            </section>
          </div>
        </div>
      </div>
    </Show>
  )
}
