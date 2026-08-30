import { Match, Show, Switch, createEffect, createSignal, on, onCleanup, onMount } from 'solid-js'
import ActivityRail from './components/ActivityRail'
import Sidebar from './components/Sidebar'
import RequestTabBar from './components/RequestTabBar'
import RequestEditor from './components/RequestEditor'
import ResponseViewer from './components/ResponseViewer'
import CommandPalette from './components/CommandPalette'
import ShortcutSheet from './components/ShortcutSheet'
import EnvironmentSelector from './components/EnvironmentSelector'
import EnvironmentEditor from './components/EnvironmentEditor'
import ImportCurlModal from './components/ImportCurlModal'
import MigrateModal from './components/MigrateModal'
import StreamConsole from './components/StreamConsole'
import SettingsModal from './components/SettingsModal'
import MCPApprovalModal from './components/MCPApprovalModal'
import ConfirmDialog from './components/ConfirmDialog'
import McpToolView from './components/McpToolView'
import FolderRunView from './components/FolderRunView'
import UpdateBanner from './components/UpdateBanner'
import TrialBadge from './components/TrialBadge'
import { appState, closeTab, cycleTab, folderRunView, initSidebar, loadError, mcpToolView, setExplorerOpen, setLoadError, setMcpApprovals, setSettingsOpen } from './lib/store'
import { events, wails } from './lib/wails'
import { createRequest, flushRequestSave, loadAll, loadHistory, loadWorkspaceData, refreshEnvironments } from './lib/data'
import { initTheme } from './lib/theme'
import { initLicense, licenseStatus } from './lib/license'
import { initUpdateCheck } from './lib/updater'
import type { MCPApproval } from './lib/store'
import type { ResponseData } from './types'

export default function App() {
  const [response, setResponse] = createSignal<ResponseData | null>(null)
  // Every request currently in flight, by id. A SET rather than a single slot:
  // sends are startable per-tab and via ⌘Enter, so two can genuinely overlap —
  // with one slot, starting B would clobber A's id, and A's completion would
  // then clear B's in-flight state mid-flight (reverting B's Cancel button to
  // Send, letting a second concurrent B start, and leaving B uncancellable).
  const [sendingIds, setSendingIds] = createSignal<ReadonlySet<string>>(new Set())
  const isSending = (id: string | undefined) => (id ? sendingIds().has(id) : false)
  // The response pane's spinner tracks the ACTIVE tab only — a background
  // send finishing shouldn't change what the foreground request shows.
  const sending = () => isSending(appState.activeTabId ?? undefined)

  function markSending(id: string, on: boolean) {
    setSendingIds((prev) => {
      const next = new Set(prev)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  function cancelSend(requestId: string) {
    if (isSending(requestId)) wails.CancelSend(requestId)
  }

  // The response pane holds ONE response, so a send that finishes while the
  // user is looking at a different request must not hijack the view. (The
  // result still lands in history either way.)
  function showIfStillActive(requestId: string, data: ResponseData) {
    if (appState.activeTabId === requestId) setResponse(data)
  }

  onMount(() => {
    // Theme first so the correct colors paint before data arrives; a
    // settings-load failure must never block data loading. initSidebar
    // reads the same settings for sidebarMode (docked vs overlay).
    initTheme().catch(() => {})
    initSidebar().catch(() => {})
    // Licensing: records the trial start on first launch and loads current
    // status (StartTrialIfNeeded is idempotent and called inside initLicense).
    // Never blocks data loading, and degrades to "no badge" if the binding
    // isn't there yet (pre-regeneration dev).
    initLicense().catch(() => {})
    // Auto-update: silent launch check; no-ops in dev builds or when the
    // user has turned auto-check off.
    initUpdateCheck().catch(() => {})
    loadAll().catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
    // App-scoped MCP approval listener (see MCPApprovalModal for why it lives
    // here, not in the leaf component). Never cleaned up — the app owns it for
    // its whole lifetime.
    events.EventsOn('mcp:approval', (payload: MCPApproval) => {
      if (payload?.id) setMcpApprovals((q) => [...q, payload])
    })
  })

  // Re-load requests/folders/environments whenever the active workspace
  // changes (WorkspaceSwitcher) — appState.requests/folders/environments
  // hold exactly one workspace's data at a time (see lib/data.ts).
  createEffect(
    on(
      () => appState.activeWorkspaceId,
      (id, prevId) => {
        if (id && id !== prevId) loadWorkspaceData(id).catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
      },
    ),
  )

  function onGlobalShortcuts(e: KeyboardEvent) {
    const meta = e.metaKey || e.ctrlKey
    if (meta && e.key.toLowerCase() === 'n') {
      e.preventDefault()
      createRequest().catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
    }
    if (meta && e.key.toLowerCase() === 'b') {
      e.preventDefault()
      setExplorerOpen((v) => !v)
    }
    if (meta && e.key.toLowerCase() === 'w') {
      if (appState.activeTabId) {
        e.preventDefault()
        closeTab(appState.activeTabId)
      }
    }
    // VSCode's convention for next/previous editor tab — likely already
    // muscle memory for this app's audience.
    if (meta && e.shiftKey && e.key === ']') {
      e.preventDefault()
      cycleTab(1)
    }
    if (meta && e.shiftKey && e.key === '[') {
      e.preventDefault()
      cycleTab(-1)
    }
    // The response body's "Search" button has always been labeled "(⌘F)",
    // but nothing dispatched it globally — it only worked by fluke, if the
    // CodeMirror instance already happened to have keyboard focus (its own
    // searchKeymap binds Mod-f internally). Broadcasting it here means the
    // shortcut works regardless of what currently has focus, matching what
    // the label already promised.
    if (meta && e.key.toLowerCase() === 'f') {
      e.preventDefault()
      window.dispatchEvent(new CustomEvent('apitool:search-body'))
    }
  }

  async function handleSend(requestId: string) {
    // Enforce the soft licence gate at the ACTION, not just on the button —
    // ⌘Enter and the command palette reach here too. A gated send opens
    // Settings → License instead of firing.
    const lic = licenseStatus()
    if (lic?.state === 'trial_expired' || lic?.state === 'license_invalid') {
      setSettingsOpen(true)
      return
    }
    // Re-entry guard: the Send button is REPLACED by Cancel while in flight,
    // so the only way to re-fire the same request is ⌘Enter — which would
    // otherwise start a second concurrent run whose cancel registration
    // clobbers the first's in the backend, leaving neither cancellable.
    if (isSending(requestId)) return
    markSending(requestId, true)
    try {
      // Flush any pending debounced edit first: SendRequest resolves the
      // request from the backend store, so without this a quick edit-then-send
      // (e.g. set a gRPC method/URL then hit Send) would run against stale
      // state — which for gRPC means dialing the wrong transport and hanging.
      await flushRequestSave(requestId)
      const result = await wails.SendRequest(requestId, appState.activeEnvironmentId ?? '')
      // Wails widens Go's string-enum fields (assertion source/operator) to
      // `string`; the backend only ever emits valid enum values.
      showIfStillActive(requestId, result as unknown as ResponseData)
    } catch (err) {
      showIfStillActive(requestId, {
        requestId,
        status: 0,
        statusText: 'Error',
        headers: [],
        bodyBase64: '',
        bodySize: 0,
        timingMs: 0,
        timestamp: new Date().toISOString(),
        error: err instanceof Error ? err.message : String(err),
      })
    } finally {
      markSending(requestId, false)
      // A post-response script may have written variables into the active
      // environment (auth-token chaining) — re-read them so the environment
      // editor isn't showing a stale copy.
      refreshEnvironments().catch(() => {})
      // The backend appends a history entry on any completed run (even a
      // non-2xx response) — refresh so HistoryPanel isn't stuck showing
      // stale data from whenever the app last loaded.
      loadHistory().catch(() => {})
    }
  }

  function onSendShortcut() {
    if (appState.activeTabId) handleSend(appState.activeTabId)
  }

  onMount(() => {
    window.addEventListener('apitool:send', onSendShortcut)
    window.addEventListener('keydown', onGlobalShortcuts)
  })
  onCleanup(() => {
    window.removeEventListener('apitool:send', onSendShortcut)
    window.removeEventListener('keydown', onGlobalShortcuts)
  })

  return (
    <div class="flex h-screen overflow-hidden">
      <ActivityRail />
      {/* In docked mode (the default) Sidebar participates in this flex row
          as a resizable panel between the rail and the editor; in overlay
          mode it renders position:fixed and this slot is empty. */}
      <Sidebar />
      <div class="relative flex flex-1 flex-col overflow-hidden">
        <UpdateBanner />
        <Show when={loadError()}>
          <div class="flex items-center justify-between border-b border-danger-edge bg-danger-bg/60 px-3 py-1 text-xs text-danger">
            <span>{loadError()}</span>
            <button class="rounded px-2 py-0.5 hover:bg-danger-bg/60" onClick={() => setLoadError(null)}>
              Dismiss
            </button>
          </div>
        </Show>

        {/* Slim top strip — everything that used to be a row of always-on
            buttons now lives in the command palette; this only keeps the one
            thing worth glancing at (environment) plus a discoverable ⌘K hint. */}
        <div class="flex h-9 shrink-0 items-center justify-between gap-2 border-b border-edge px-3">
          <span class="truncate text-xs font-medium text-ink-dim">
            {appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)?.name ?? ''}
          </span>
          <div class="flex items-center gap-2">
            <TrialBadge onClick={() => setSettingsOpen(true)} />
            <EnvironmentSelector />
          </div>
        </div>

        {/* An MCP tool selected in McpPanel, or a folder run started from
            Sidebar.tsx, each take over this SAME main area (full width —
            more room than the drawer) rather than living alongside the
            request tab bar; picking a request (openTab) switches back
            automatically (see store.ts). */}
        <Switch
          fallback={
            <>
              <RequestTabBar />
              <div class="flex flex-1 overflow-hidden">
                <div class="flex-1 overflow-hidden">
                  <RequestEditor onSend={handleSend} isSending={isSending} onCancel={cancelSend} />
                </div>
                <div class="w-[45%] overflow-hidden">
                  <ResponseViewer response={response()} loading={sending()} />
                </div>
              </div>
            </>
          }
        >
          <Match when={mcpToolView()}>
            <div class="flex flex-1 overflow-hidden">
              <McpToolView />
            </div>
          </Match>
          <Match when={folderRunView()}>
            <div class="flex flex-1 overflow-hidden">
              <FolderRunView />
            </div>
          </Match>
        </Switch>

      </div>

      <CommandPalette />
      <ShortcutSheet />
      <EnvironmentEditor />
      <ImportCurlModal />
      <MigrateModal />
      <StreamConsole />
      <SettingsModal />
      <MCPApprovalModal />
      <ConfirmDialog />
    </div>
  )
}
