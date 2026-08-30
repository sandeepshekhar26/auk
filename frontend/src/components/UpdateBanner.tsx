import { Match, Show, Switch, createSignal, onMount } from 'solid-js'
import {
  checkForUpdatesNow,
  dismissUpdate,
  downloadAndVerify,
  installUpdate,
  setAutoCheck,
  updateBannerVisible,
  updateState,
  formatBytes,
} from '../lib/updater'

// Slim, dismissible top banner announcing an available update — same visual
// language as App.tsx's loadError bar (border-b, px-3 py-1 text-xs), but in
// accent tones for an informational update rather than danger tones for a
// failure. It reads the shared updater signal; App.tsx just has to mount it.
//
// The "Update" button drives download+verify, then flips to "Restart to
// update"; every phase is reflected (Downloading & verifying… / Ready /
// Restarting… / a guided fallback / a visible error) so a failure is never
// silent. "What's new" expands the release notes inline (no webview
// navigation, which would blow away the app).
export default function UpdateBanner() {
  const [notesOpen, setNotesOpen] = createSignal(false)
  const status = () => updateState.status
  const phase = () => updateState.phase

  return (
    <Show when={updateBannerVisible()}>
      <div class="border-b border-accent/30 bg-accent/10 text-xs">
        <div class="flex items-center justify-between gap-3 px-3 py-1">
          <div class="flex min-w-0 items-center gap-2">
            <span class="truncate text-ink">
              <Show
                when={phase() !== 'error'}
                fallback={<span class="font-medium text-danger">Update failed</span>}
              >
                <span class="font-medium text-accent-fg">AUK {status()?.latestVersion}</span> is available
                <Show when={status()?.sizeBytes}>
                  <span class="text-ink-faint"> · {formatBytes(status()?.sizeBytes ?? 0)}</span>
                </Show>
              </Show>
            </span>
            <Show when={status()?.notes}>
              <button
                class="shrink-0 text-ink-dim underline decoration-dotted underline-offset-2 hover:text-ink"
                onClick={() => setNotesOpen((v) => !v)}
              >
                What's new
              </button>
            </Show>
          </div>

          <div class="flex shrink-0 items-center gap-2">
            <Switch>
              <Match when={phase() === 'available'}>
                <button
                  class="rounded bg-accent px-2.5 py-0.5 font-medium text-accent-contrast hover:bg-accent-hover"
                  onClick={downloadAndVerify}
                >
                  Update
                </button>
              </Match>
              <Match when={phase() === 'error'}>
                <button
                  class="rounded bg-accent px-2.5 py-0.5 font-medium text-accent-contrast hover:bg-accent-hover"
                  onClick={downloadAndVerify}
                >
                  Try again
                </button>
              </Match>
              <Match when={phase() === 'working'}>
                <button
                  class="flex items-center gap-1.5 rounded bg-accent/70 px-2.5 py-0.5 font-medium text-accent-contrast"
                  disabled
                >
                  <span class="h-3 w-3 animate-spin rounded-full border-2 border-accent-contrast/30 border-t-accent-contrast" />
                  Downloading and verifying…
                </button>
              </Match>
              <Match when={phase() === 'ready'}>
                <button
                  class="rounded bg-accent px-2.5 py-0.5 font-medium text-accent-contrast hover:bg-accent-hover"
                  onClick={installUpdate}
                >
                  Restart to update
                </button>
              </Match>
              <Match when={phase() === 'installing'}>
                <button
                  class="flex items-center gap-1.5 rounded bg-accent/70 px-2.5 py-0.5 font-medium text-accent-contrast"
                  disabled
                >
                  <span class="h-3 w-3 animate-spin rounded-full border-2 border-accent-contrast/30 border-t-accent-contrast" />
                  Restarting…
                </button>
              </Match>
              <Match when={phase() === 'guided'}>
                <span class="text-ink-dim">Opened in Finder</span>
              </Match>
            </Switch>

            <button class="rounded px-2 py-0.5 text-ink-dim hover:bg-accent/20 hover:text-ink" onClick={dismissUpdate}>
              Dismiss
            </button>
          </div>
        </div>

        {/* A failure is shown, never swallowed (the copy-as-clipboard lesson). */}
        <Show when={phase() === 'error' && updateState.error}>
          <div class="border-t border-danger-edge bg-danger-bg/40 px-3 py-1 text-danger">{updateState.error}</div>
        </Show>

        <Show when={phase() === 'guided' && updateState.guidedMessage}>
          <div class="border-t border-edge px-3 py-1 text-ink-dim">{updateState.guidedMessage}</div>
        </Show>

        <Show when={notesOpen() && status()?.notes}>
          <div class="max-h-48 overflow-auto border-t border-edge/60 bg-field px-3 py-2">
            <pre class="whitespace-pre-wrap font-sans text-[11px] leading-relaxed text-ink-dim">{status()?.notes}</pre>
          </div>
        </Show>
      </div>
    </Show>
  )
}

// UpdateSettingRow is a self-contained block for SettingsModal: the current
// version, a manual "Check for updates" button, an auto-check opt-out, and —
// when an update is waiting — a way to start it from Settings too. Exported so
// the SettingsModal owner can drop it in with a single import; it keeps all its
// own state and never touches shared settings.
export function UpdateSettingRow() {
  const [checking, setChecking] = createSignal(false)
  const [note, setNote] = createSignal<string | null>(null)

  onMount(() => {
    // Populate the current version if no check has run yet this session (e.g.
    // auto-check is off). Cheap and user-initiated (they opened Settings).
    if (!updateState.status) void refresh()
  })

  async function refresh() {
    setChecking(true)
    setNote(null)
    const st = await checkForUpdatesNow()
    setChecking(false)
    if (!st) {
      setNote('Could not check for updates right now.')
    } else if (st.isDevBuild) {
      setNote('Development build — automatic updates are disabled.')
    } else if (st.available) {
      setNote(`AUK ${st.latestVersion} is available.`)
    } else {
      setNote('You are up to date.')
    }
  }

  const currentLabel = () => {
    const s = updateState.status
    if (s?.currentVersion) return s.currentVersion
    if (s?.isDevBuild) return 'development build'
    return '—'
  }

  const updateReady = () =>
    !!updateState.status?.available && !updateState.status?.isDevBuild

  return (
    <div class="flex flex-col gap-2 text-xs">
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <div class="text-ink">Software update</div>
          <div class="text-ink-faint">Version {currentLabel()}</div>
        </div>
        <button
          class="flex items-center gap-1.5 rounded border border-edge px-2.5 py-1 text-ink-dim hover:bg-field hover:text-ink disabled:opacity-60"
          disabled={checking()}
          onClick={refresh}
        >
          <Show when={checking()}>
            <span class="h-3 w-3 animate-spin rounded-full border-2 border-ink-faint/40 border-t-ink-dim" />
          </Show>
          {checking() ? 'Checking…' : 'Check for updates'}
        </button>
      </div>

      <label class="flex items-center gap-2 text-ink-dim">
        <input
          type="checkbox"
          class="accent-accent"
          checked={updateState.autoCheck}
          onChange={(e) => void setAutoCheck(e.currentTarget.checked)}
        />
        <span>Automatically check for updates on launch</span>
      </label>

      <Show when={note()}>
        <div class="text-ink-faint">{note()}</div>
      </Show>

      <Show when={updateReady()}>
        <div>
          <Switch>
            <Match when={updateState.phase === 'ready'}>
              <button
                class="rounded bg-accent px-2.5 py-1 font-medium text-accent-contrast hover:bg-accent-hover"
                onClick={installUpdate}
              >
                Restart to update
              </button>
            </Match>
            <Match when={updateState.phase === 'working'}>
              <span class="text-ink-dim">Downloading and verifying…</span>
            </Match>
            <Match when={true}>
              <button
                class="rounded bg-accent px-2.5 py-1 font-medium text-accent-contrast hover:bg-accent-hover"
                onClick={downloadAndVerify}
              >
                Download and verify update
              </button>
            </Match>
          </Switch>
        </div>
      </Show>
    </div>
  )
}
