import { Show, createSignal } from 'solid-js'
import { appState, importModalOpen, setImportModalOpen, openTab } from '../lib/store'
import { models, wails } from '../lib/wails'
import { loadWorkspaceData } from '../lib/data'
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
      // ImportCurl only parses — it does not persist. Fill in the workspace
      // scoping the parser can't know about, persist through the same
      // CreateRequest path as the "+ New Request" flow, then reload from the
      // backend so the store reflects what's actually on disk.
      const req: RequestDef = {
        ...parsed,
        id: parsed.id || crypto.randomUUID(),
        workspaceId: appState.activeWorkspaceId ?? parsed.workspaceId,
        name: parsed.name || `${parsed.method} ${parsed.url}`,
      }
      await wails.CreateRequest(models.RequestDef.createFrom(req))
      if (appState.activeWorkspaceId) await loadWorkspaceData(appState.activeWorkspaceId)
      openTab(req.id)
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
          class="flex w-full max-w-xl flex-col overflow-hidden rounded-lg border border-edge-strong bg-field shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="border-b border-edge px-4 py-3">
            <h2 class="text-sm font-semibold text-ink">Import from cURL</h2>
            <p class="mt-0.5 text-xs text-ink-muted">Paste a curl command to create a new request.</p>
          </div>

          <div class="flex flex-col gap-3 px-4 py-3">
            <textarea
              autofocus
              rows={8}
              spellcheck={false}
              class="w-full resize-none rounded border border-edge bg-app p-2 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder={"curl -X POST https://api.example.com/users \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"name\":\"jane\"}'"}
              value={raw()}
              onInput={(e) => {
                setRaw(e.currentTarget.value)
                setPreview(null)
                setError(null)
              }}
            />

            <Show when={error()}>
              <p class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">{error()}</p>
            </Show>

            <Show when={preview()}>
              {(p) => (
                <div class="flex items-center gap-2 rounded border border-edge bg-app px-2 py-1.5 text-xs">
                  <span class="font-mono font-semibold text-accent-fg">{p().method}</span>
                  <span class="flex-1 truncate font-mono text-ink-dim">{p().url}</span>
                </div>
              )}
            </Show>
          </div>

          <div class="flex items-center justify-between gap-2 border-t border-edge px-4 py-3">
            <button
              class="text-xs text-ink-muted hover:text-ink-dim"
              onClick={handlePreview}
              disabled={!raw().trim()}
            >
              Preview
            </button>
            <div class="flex items-center gap-2">
              <button class="rounded px-3 py-1.5 text-xs text-ink-muted hover:bg-raised" onClick={close}>
                Cancel
              </button>
              <button
                class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
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
