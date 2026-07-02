import { Show, createMemo } from 'solid-js'
import { appState } from '../lib/store'

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']

export default function RequestEditor(props: { onSend: (requestId: string) => void }) {
  const active = createMemo(() => appState.requests.find((r) => r.id === appState.activeTabId))

  return (
    <Show when={active()} fallback={<EmptyState />}>
      {(req) => (
        <div class="flex h-full flex-col">
          <div class="flex items-center gap-2 border-b border-neutral-800 p-2">
            <select class="rounded bg-neutral-900 px-2 py-1 font-mono text-xs font-semibold text-emerald-400" value={req().method}>
              {METHODS.map((m) => (
                <option value={m}>{m}</option>
              ))}
            </select>
            <input
              class="flex-1 rounded bg-neutral-900 px-2 py-1 font-mono text-sm text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
              value={req().url}
              placeholder="https://api.example.com/{{ path }}"
            />
            <button
              class="rounded bg-emerald-600 px-3 py-1 text-sm font-medium text-white hover:bg-emerald-500"
              onClick={() => props.onSend(req().id)}
            >
              Send
            </button>
          </div>
          <div class="flex-1 overflow-y-auto p-3 text-sm text-neutral-500">
            Headers / params / body editors mount here (CodeMirror 6).
          </div>
        </div>
      )}
    </Show>
  )
}

function EmptyState() {
  return (
    <div class="flex h-full flex-col items-center justify-center gap-2 text-neutral-600">
      <p class="text-sm">No request open</p>
      <p class="text-xs">Press ⌘K to search, or ⌘N to create a request</p>
    </div>
  )
}
