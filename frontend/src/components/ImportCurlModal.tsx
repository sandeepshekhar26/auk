import { Show, createSignal } from 'solid-js'
import { setAppState, importModalOpen, setImportModalOpen, openTab } from '../lib/store'
import { wails } from '../lib/wails'
import type { RequestDef } from '../types'

export default function ImportCurlModal() {
  const [raw, setRaw] = createSignal('')
  const [preview, setPreview] = createSignal<RequestDef | null>(null)
  const [error, setError] = createSignal<string | null>(null)
  const [importing, setImporting] = createSignal(false)

  function close() {
    setImportModalOpen(false)
    setRaw('')
    setPreview(null)
    setError(null)
  }

  async function handlePreview() {
    const command = raw().trim()
    if (!command) return
    setError(null)
    try {
      const parsed = await wails.ImportCurl(command)
      setPreview(parsed as unknown as RequestDef)
    } catch (err) {
      setPreview(null)
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleImport() {
    setImporting(true)
    setError(null)
    try {
      let parsed = preview()
      if (!parsed) {
        parsed = (await wails.ImportCurl(raw().trim())) as unknown as RequestDef
      }
      setAppState('requests', (reqs) => [...reqs, parsed as RequestDef])
      openTab(parsed.id)
      close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setImporting(false)
    }
  }

  return (
    <Show when={importModalOpen()}>
      <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-24" onClick={close}>
        <div
          class="flex w-full max-w-xl flex-col overflow-hidden rounded-lg border border-neutral-700 bg-neutral-900 shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="border-b border-neutral-800 px-4 py-3">
            <h2 class="text-sm font-semibold text-neutral-100">Import from cURL</h2>
            <p class="mt-0.5 text-xs text-neutral-500">Paste a curl command to create a new request.</p>
          </div>

          <div class="flex flex-col gap-3 px-4 py-3">
            <textarea
              autofocus
              rows={8}
              spellcheck={false}
              class="w-full resize-none rounded border border-neutral-800 bg-neutral-950 p-2 font-mono text-xs text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
              placeholder={"curl -X POST https://api.example.com/users \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"name\":\"jane\"}'"}
              value={raw()}
              onInput={(e) => {
                setRaw(e.currentTarget.value)
                setPreview(null)
                setError(null)
              }}
            />

            <Show when={error()}>
              <p class="rounded border border-red-900 bg-red-950/40 px-2 py-1.5 text-xs text-red-400">{error()}</p>
            </Show>

            <Show when={preview()}>
              {(p) => (
                <div class="flex items-center gap-2 rounded border border-neutral-800 bg-neutral-950 px-2 py-1.5 text-xs">
                  <span class="font-mono font-semibold text-emerald-400">{p().method}</span>
                  <span class="flex-1 truncate font-mono text-neutral-300">{p().url}</span>
                </div>
              )}
            </Show>
          </div>

          <div class="flex items-center justify-between gap-2 border-t border-neutral-800 px-4 py-3">
            <button
              class="text-xs text-neutral-500 hover:text-neutral-300"
              onClick={handlePreview}
              disabled={!raw().trim()}
            >
              Preview
            </button>
            <div class="flex items-center gap-2">
              <button class="rounded px-3 py-1.5 text-xs text-neutral-400 hover:bg-neutral-800" onClick={close}>
                Cancel
              </button>
              <button
                class="rounded bg-emerald-600 px-3 py-1.5 text-xs font-medium text-neutral-100 hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
                disabled={!raw().trim() || importing()}
                onClick={handleImport}
              >
                {importing() ? 'Importing…' : 'Import'}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Show>
  )
}
