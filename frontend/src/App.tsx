import { createSignal, onCleanup, onMount } from 'solid-js'
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
import { appState, setImportModalOpen, streamConsoleOpen, setStreamConsoleOpen } from './lib/store'
import { wails } from './lib/wails'
import type { ResponseData } from './types'

export default function App() {
  const [response, setResponse] = createSignal<ResponseData | null>(null)
  const [sending, setSending] = createSignal(false)
  const [showHistory, setShowHistory] = createSignal(false)

  async function handleSend(requestId: string) {
    setSending(true)
    try {
      const result = await wails.SendRequest(requestId, appState.activeEnvironmentId ?? '')
      setResponse(result)
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
    }
  }

  function onSendShortcut() {
    if (appState.activeTabId) handleSend(appState.activeTabId)
  }

  onMount(() => window.addEventListener('apitool:send', onSendShortcut))
  onCleanup(() => window.removeEventListener('apitool:send', onSendShortcut))

  return (
    <div class="flex h-screen flex-col overflow-hidden">
      <div class="flex h-8 items-center justify-end gap-2 border-b border-neutral-800 px-2">
        <button
          class="rounded px-2 py-1 text-xs text-neutral-500 hover:bg-neutral-800 hover:text-neutral-200"
          onClick={() => setImportModalOpen(true)}
        >
          Import cURL
        </button>
        <button
          class="rounded px-2 py-1 text-xs text-neutral-500 hover:bg-neutral-800 hover:text-neutral-200"
          classList={{ 'bg-neutral-800 text-neutral-200': streamConsoleOpen() }}
          onClick={() => setStreamConsoleOpen((v) => !v)}
        >
          Stream Console
        </button>
        <EnvironmentSelector />
      </div>
      <div class="flex flex-1 overflow-hidden">
        <div class="flex flex-col">
          <div class="flex-1 overflow-hidden">
            <Sidebar />
          </div>
          <div class="flex h-56 flex-col border-r border-t border-neutral-800 bg-neutral-925">
            <button
              class="flex items-center justify-between border-b border-neutral-800 px-3 py-1.5 text-left text-xs font-semibold uppercase tracking-wide text-neutral-500 hover:text-neutral-300"
              onClick={() => setShowHistory((v) => !v)}
            >
              History
              <span class="text-neutral-600">{showHistory() ? '▾' : '▸'}</span>
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
    </div>
  )
}
