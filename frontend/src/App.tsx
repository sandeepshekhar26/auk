import { Show, createEffect, createSignal, on, onCleanup, onMount } from 'solid-js'
import Sidebar from './components/Sidebar'
import RequestTabBar from './components/RequestTabBar'
import RequestEditor from './components/RequestEditor'
import ResponseViewer from './components/ResponseViewer'
import CommandPalette from './components/CommandPalette'
import ShortcutSheet from './components/ShortcutSheet'
import EnvironmentSelector from './components/EnvironmentSelector'
import EnvironmentEditor from './components/EnvironmentEditor'
import ImportCurlModal from './components/ImportCurlModal'
import StreamConsole from './components/StreamConsole'
import HistoryPanel from './components/HistoryPanel'
import SettingsModal from './components/SettingsModal'
import { appState, setImportModalOpen, setSettingsOpen, streamConsoleOpen, setStreamConsoleOpen } from './lib/store'
import { wails } from './lib/wails'
import { createRequest, loadAll, loadHistory, loadWorkspaceData } from './lib/data'
import { initTheme } from './lib/theme'
import type { ResponseData } from './types'

export default function App() {
  const [response, setResponse] = createSignal<ResponseData | null>(null)
  const [sending, setSending] = createSignal(false)
  const [showHistory, setShowHistory] = createSignal(false)
  const [loadError, setLoadError] = createSignal<string | null>(null)

  onMount(() => {
    // Theme first so the correct colors paint before data arrives; a
    // settings-load failure must never block data loading.
    initTheme().catch(() => {})
    loadAll().catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
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

  function onNewRequestShortcut(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'n') {
      e.preventDefault()
      createRequest().catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))
    }
  }

  async function handleSend(requestId: string) {
    setSending(true)
    try {
      const result = await wails.SendRequest(requestId, appState.activeEnvironmentId ?? '')
      // Wails widens Go's string-enum fields (assertion source/operator) to
      // `string`; the backend only ever emits valid enum values.
      setResponse(result as unknown as ResponseData)
    } catch (err) {
      setResponse({
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
      setSending(false)
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
    window.addEventListener('keydown', onNewRequestShortcut)
  })
  onCleanup(() => {
    window.removeEventListener('apitool:send', onSendShortcut)
    window.removeEventListener('keydown', onNewRequestShortcut)
  })

  return (
    <div class="flex h-screen flex-col overflow-hidden">
      <Show when={loadError()}>
        <div class="flex items-center justify-between border-b border-danger-edge bg-danger-bg/60 px-3 py-1 text-xs text-danger">
          <span>{loadError()}</span>
          <button class="rounded px-2 py-0.5 hover:bg-danger-bg/60" onClick={() => setLoadError(null)}>
            Dismiss
          </button>
        </div>
      </Show>
      <div class="flex h-8 items-center justify-end gap-2 border-b border-edge px-2">
        <button
          class="rounded bg-accent px-2 py-1 text-xs font-medium text-accent-contrast hover:bg-accent-hover"
          onClick={() => createRequest().catch((err) => setLoadError(err instanceof Error ? err.message : String(err)))}
        >
          + New Request
        </button>
        <button
          class="rounded px-2 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink"
          onClick={() => setImportModalOpen(true)}
        >
          Import
        </button>
        <button
          class="rounded px-2 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink"
          classList={{ 'bg-raised text-ink': streamConsoleOpen() }}
          onClick={() => setStreamConsoleOpen((v) => !v)}
        >
          Stream Console
        </button>
        <button
          class="rounded px-2 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink"
          onClick={() => setSettingsOpen(true)}
        >
          Settings
        </button>
        <EnvironmentSelector />
      </div>
      <div class="flex flex-1 overflow-hidden">
        <div class="flex flex-col">
          <div class="flex-1 overflow-hidden">
            <Sidebar />
          </div>
          <div class="flex h-56 flex-col border-r border-t border-edge bg-surface">
            <button
              class="flex items-center justify-between border-b border-edge px-3 py-1.5 text-left text-xs font-semibold uppercase tracking-wide text-ink-muted hover:text-ink-dim"
              onClick={() => setShowHistory((v) => !v)}
            >
              History
              <span class="text-ink-faint">{showHistory() ? '▾' : '▸'}</span>
            </button>
            <div class="flex-1 overflow-hidden" classList={{ hidden: !showHistory() }}>
              <HistoryPanel />
            </div>
          </div>
        </div>
        <div class="flex flex-1 flex-col overflow-hidden">
          <RequestTabBar />
          <div class="flex flex-1 overflow-hidden">
            <div class="flex-1 overflow-hidden">
              <RequestEditor onSend={handleSend} />
            </div>
            <div class="w-[45%] overflow-hidden">
              <ResponseViewer response={response()} loading={sending()} />
            </div>
          </div>
        </div>
      </div>
      <CommandPalette />
      <ShortcutSheet />
      <EnvironmentEditor />
      <ImportCurlModal />
      <StreamConsole />
      <SettingsModal />
    </div>
  )
}
