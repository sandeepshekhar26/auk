import { For, Show, createMemo } from 'solid-js'
import { folderRunView, setFolderRunView } from '../lib/store'
import { runFolder } from '../lib/data'

// Same status-color convention as ResponseViewer's own status badge
// (< 300 success, 300-399 redirect, >= 400 error); a request that never got
// a response at all (a dial/network failure — response.error set, status
// still the zero value) reads as an error too.
function statusClasses(status: number, hasError: boolean) {
  return {
    'text-accent-fg': !hasError && status > 0 && status < 300,
    'text-warn': !hasError && status >= 300 && status < 400,
    'text-danger': hasError || status >= 400,
  }
}

// FolderRunView takes over the main area (App.tsx) — the same real estate
// RequestEditor+ResponseViewer / McpToolView occupy — when "Run folder" is
// clicked from Sidebar.tsx, since an aggregate results list deserves more
// room than the drawer. Mutually exclusive with both (see store.ts).
export default function FolderRunView() {
  const target = () => folderRunView()!
  const results = () => target().results
  const succeeded = createMemo(() => results().filter((r) => !r.response.error && r.response.status >= 200 && r.response.status < 300).length)

  return (
    <div class="flex h-full flex-col overflow-hidden">
      <div class="flex items-center justify-between gap-3 border-b border-edge px-3 py-2">
        <div class="min-w-0">
          <span class="truncate text-sm font-semibold text-ink">Run folder: {target().folderName}</span>
          <Show when={!target().running}>
            <span class="ml-2 text-xs text-ink-faint">
              {succeeded()} / {results().length} succeeded
            </span>
          </Show>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <button
            class="rounded bg-accent px-3 py-1.5 text-sm font-medium text-accent-contrast hover:bg-accent-hover disabled:opacity-50"
            disabled={target().running}
            onClick={() => void runFolder(target().folderId, target().folderName)}
          >
            {target().running ? 'Running…' : 'Run again'}
          </button>
          <button class="rounded px-2 py-1 text-xs text-ink-faint hover:bg-raised hover:text-ink-dim" onClick={() => setFolderRunView(null)}>
            Close
          </button>
        </div>
      </div>

      <div class="flex-1 overflow-y-auto p-2">
        <Show when={target().running}>
          <p class="px-2 py-4 text-xs text-ink-faint">Sending requests…</p>
        </Show>
        <Show when={!target().running && results().length === 0}>
          <p class="px-2 py-4 text-xs text-ink-faint">No requests directly inside this folder or its subfolders.</p>
        </Show>
        <div class="flex flex-col gap-1">
          <For each={results()}>
            {(r) => (
              <div class="flex items-center gap-3 rounded px-2 py-1.5 text-xs hover:bg-raised">
                <span class="w-10 shrink-0 text-right font-mono font-semibold" classList={statusClasses(r.response.status, !!r.response.error)}>
                  {r.response.error ? 'ERR' : r.response.status}
                </span>
                <span class="flex-1 truncate text-ink-dim" title={r.requestName}>
                  {r.requestName}
                </span>
                <Show when={r.response.error}>
                  <span class="max-w-[40%] shrink-0 truncate text-danger" title={r.response.error}>
                    {r.response.error}
                  </span>
                </Show>
                <span class="w-14 shrink-0 text-right text-ink-faint">{r.response.timingMs}ms</span>
              </div>
            )}
          </For>
        </div>
      </div>
    </div>
  )
}
