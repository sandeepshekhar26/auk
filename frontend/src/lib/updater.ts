// Auto-update controller.
//
// Mirrors the shape of lib/theme.ts / lib/store.ts: a small reactive state
// object plus the functions that drive it, all talking to the Go bindings in
// app_update.go (CheckForUpdate / DownloadAndVerifyUpdate / InstallUpdate /
// Get|SetUpdatePref).
//
// WHY THE LOCAL TYPES + window.go ACCESS (instead of importing the generated
// wailsjs bindings): the `updater.*` model namespace and the new App methods
// only appear in frontend/wailsjs after `wails build`/`wails dev` regenerates
// the bindings — which the integrator runs, not this file. To keep `tsc
// --noEmit` and `vite build` green today, this module declares the return
// shapes locally and calls the methods through the same window['go']['main']
// ['App'] bridge the generated wrappers use internally. Once bindings are
// regenerated the calls are byte-for-byte compatible; switching to
// `import { CheckForUpdate } from '@wails/go/main/App'` later is optional.
//
// Dev builds never nag: CheckForUpdate returns available=false for an
// unversioned/dev bundle (empty CFBundleShortVersionString → isDevBuild, or the
// wails-dev default of 1.0.0 which simply outranks every real release), so the
// frontend trusts that flag rather than sniffing import.meta.env.

import { createStore } from 'solid-js/store'

// ---- Types (mirror internal/updater's Go structs) ----

export interface UpdateStatus {
  available: boolean
  currentVersion: string
  latestVersion: string
  url: string
  notes: string
  sizeBytes: number
  isDevBuild: boolean
  error?: string
}

export interface StagedUpdate {
  version: string
  appPath: string
  dmgPath: string
  stagedDir: string
}

export interface InstallResult {
  relaunching: boolean
  guided: boolean
  message?: string
  version: string
}

interface UpdaterBackend {
  CheckForUpdate(): Promise<UpdateStatus>
  DownloadAndVerifyUpdate(): Promise<StagedUpdate>
  InstallUpdate(): Promise<InstallResult>
  GetUpdatePref(): Promise<boolean>
  SetUpdatePref(enabled: boolean): Promise<void>
}

function backend(): UpdaterBackend {
  const go = (window as unknown as { go?: { main?: { App?: Partial<UpdaterBackend> } } }).go
  const app = go?.main?.App
  if (!app || typeof app.CheckForUpdate !== 'function') {
    // The bindings aren't present (e.g. a stale dev bundle from before this
    // feature was built). Surface it rather than throwing an opaque TypeError.
    throw new Error('The update service is unavailable in this build.')
  }
  return app as UpdaterBackend
}

// ---- Reactive state ----

// phase drives the banner's action affordance:
//   idle        — no update (or dev build); banner hidden
//   checking    — a check is in flight
//   available   — an update exists; "Update" button shown
//   working     — downloading + verifying the DMG
//   ready       — verified and staged; "Restart to update" shown
//   installing  — swap launched; app about to relaunch ("Restarting…")
//   guided      — couldn't self-replace; DMG opened for a manual drag-install
//   error       — a step failed; message shown (never a silent failure)
export type UpdatePhase =
  | 'idle'
  | 'checking'
  | 'available'
  | 'working'
  | 'ready'
  | 'installing'
  | 'guided'
  | 'error'

export interface UpdaterState {
  phase: UpdatePhase
  status: UpdateStatus | null
  error: string | null
  guidedMessage: string | null
  dismissed: boolean
  autoCheck: boolean
}

const [updateState, setUpdateState] = createStore<UpdaterState>({
  phase: 'idle',
  status: null,
  error: null,
  guidedMessage: null,
  dismissed: false,
  autoCheck: true,
})

export { updateState }

// Per-version dismissal, so dismissing 0.4.0 doesn't re-nag next launch, but a
// later 0.5.0 does. Lives in localStorage like the sidebar width — a
// per-machine UI convenience, not shared settings.
const DISMISS_KEY = 'auk.updateDismissedVersion'

function dismissedVersion(): string {
  try {
    return localStorage.getItem(DISMISS_KEY) ?? ''
  } catch {
    return ''
  }
}

function rememberDismissed(version: string): void {
  try {
    localStorage.setItem(DISMISS_KEY, version)
  } catch {
    /* dismissal just won't persist across restarts */
  }
}

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// ---- Actions ----

/**
 * Loads the auto-check preference and, if enabled, runs a silent launch check.
 * Call once from App.tsx onMount. Never throws (a failed background check is
 * invisible); never nags a dev build (CheckForUpdate reports available=false).
 */
export async function initUpdateCheck(): Promise<void> {
  let auto = true
  try {
    auto = await backend().GetUpdatePref()
  } catch {
    auto = true // default on (opt-out)
  }
  setUpdateState('autoCheck', auto)
  if (!auto) return
  await runCheck(true)
}

async function runCheck(silent: boolean): Promise<UpdateStatus | null> {
  setUpdateState({ phase: 'checking', error: null })
  try {
    const status = await backend().CheckForUpdate()
    setUpdateState('status', status)
    if (status.available && !status.isDevBuild) {
      setUpdateState({ phase: 'available', dismissed: dismissedVersion() === status.latestVersion })
    } else {
      setUpdateState({ phase: 'idle', dismissed: false })
    }
    return status
  } catch (e) {
    // A launch check fails invisibly; a user-initiated check surfaces the error.
    if (silent) {
      setUpdateState({ phase: 'idle', error: null })
    } else {
      setUpdateState({ phase: 'error', error: errMessage(e) })
    }
    return null
  }
}

/** User-initiated check (Settings "Check for updates"). Surfaces errors. */
export function checkForUpdatesNow(): Promise<UpdateStatus | null> {
  return runCheck(false)
}

/**
 * Downloads the latest DMG and runs the full verification chain
 * (checksum if published, code-signature, Team ID, notarization), staging the
 * verified .app. Drives phase working → ready. Errors are shown, never
 * swallowed.
 */
export async function downloadAndVerify(): Promise<void> {
  setUpdateState({ phase: 'working', error: null })
  try {
    await backend().DownloadAndVerifyUpdate()
    setUpdateState('phase', 'ready')
  } catch (e) {
    setUpdateState({ phase: 'error', error: errMessage(e) })
  }
}

/**
 * Installs the staged update. On the one-click path the app quits and the
 * helper relaunches it (phase stays "installing"/Restarting…); if it can't
 * self-replace, the verified DMG is opened for a guided drag-install.
 */
export async function installUpdate(): Promise<void> {
  setUpdateState({ phase: 'installing', error: null })
  try {
    const res = await backend().InstallUpdate()
    if (res.guided) {
      setUpdateState({
        phase: 'guided',
        guidedMessage: res.message ?? 'Open the downloaded disk image and drag AUK to Applications to finish.',
      })
    }
    // Otherwise the app is relaunching; keep the "installing" state until quit.
  } catch (e) {
    setUpdateState({ phase: 'error', error: errMessage(e) })
  }
}

/**
 * One-click convenience: verify then immediately install. The banner uses the
 * two-step form (Update → Restart) so the user sees "verified" before a
 * relaunch, but this is here for a Settings "Update now" affordance if wanted.
 */
export async function downloadAndInstall(): Promise<void> {
  await downloadAndVerify()
  if (updateState.phase === 'ready') {
    await installUpdate()
  }
}

/** Hides the banner and remembers this version so it won't re-nag next launch. */
export function dismissUpdate(): void {
  const v = updateState.status?.latestVersion
  if (v) rememberDismissed(v)
  setUpdateState('dismissed', true)
}

/** Persists the auto-check-on-launch preference (opt-out). */
export async function setAutoCheck(enabled: boolean): Promise<void> {
  setUpdateState('autoCheck', enabled)
  try {
    await backend().SetUpdatePref(enabled)
  } catch {
    /* persist failure is non-fatal — the in-memory toggle already applied */
  }
}

/**
 * Whether the top banner should render: a real, non-dismissed update in any of
 * its actionable phases. (Settings surfaces updates independently of this, so
 * a dismissed banner never hides the Settings affordance.)
 */
export function updateBannerVisible(): boolean {
  const s = updateState
  if (!s.status?.available || s.status.isDevBuild) return false
  if (s.dismissed) return false
  return (
    s.phase === 'available' ||
    s.phase === 'working' ||
    s.phase === 'ready' ||
    s.phase === 'installing' ||
    s.phase === 'guided' ||
    s.phase === 'error'
  )
}

/** Human-readable size for the "Update (~42 MB)" affordance. */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return ''
  const mb = bytes / (1024 * 1024)
  if (mb >= 1) return `${mb.toFixed(mb >= 10 ? 0 : 1)} MB`
  return `${Math.max(1, Math.round(bytes / 1024))} KB`
}
