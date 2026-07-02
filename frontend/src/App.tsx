import { createSignal } from 'solid-js'
import Sidebar from './components/Sidebar'
import RequestTabBar from './components/RequestTabBar'
import RequestEditor from './components/RequestEditor'
import ResponseViewer from './components/ResponseViewer'
import CommandPalette from './components/CommandPalette'
import EnvironmentSelector from './components/EnvironmentSelector'
import type { ResponseData } from './types'

export default function App() {
  const [response, setResponse] = createSignal<ResponseData | null>(null)
  const [sending, setSending] = createSignal(false)

  async function handleSend(_requestId: string) {
    setSending(true)
    try {
      // Wired up once internal/core.ExecutionEngine.RunRequest is bound (see cmd/gui).
    } finally {
      setSending(false)
    }
  }

  return (
    <div class="flex h-screen flex-col overflow-hidden">
      <div class="flex h-8 items-center justify-end gap-2 border-b border-neutral-800 px-2">
        <EnvironmentSelector />
      </div>
      <div class="flex flex-1 overflow-hidden">
        <Sidebar />
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
    </div>
  )
}
