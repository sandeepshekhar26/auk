import { Show, createSignal } from 'solid-js'
import { appState, setAppState, importModalOpen, setImportModalOpen, openTab } from '../lib/store'
import { models, wails } from '../lib/wails'
import { loadWorkspaces, loadWorkspaceData } from '../lib/data'
import type { RequestDef } from '../types'

const FORMAT_LABEL: Record<string, string> = {
  curl: 'cURL command',
  openapi: 'OpenAPI / Swagger spec',
  postman: 'Postman collection',
}

// ImportCurlModal is the general Import surface (name kept for the store
// signal it's wired to): paste a cURL command, an OpenAPI spec, or a Postman
// collection and it auto-detects. cURL creates one request; OpenAPI/Postman
// create a whole new workspace of folders + requests + environments.
export default function ImportCurlModal() {
  const [raw, setRaw] = createSignal('')
  const [format, setFormat] = createSignal('')
  const [error, setError] = createSignal<string | null>(null)
  const [importing, setImporting] = createSignal(false)

  function close() {
    setImportModalOpen(false)
    setRaw('')
    setFormat('')
    setError(null)
  }

  async function onInput(value: string) {
    setRaw(value)
    setError(null)
    if (!value.trim()) {
      setFormat('')
      return
    }
    try {
      setFormat(await wails.DetectImportFormat(value))
    } catch {
      setFormat('')
    }
  }

  async function handleImport() {
    const content = raw().trim()
    if (!content) return
    setImporting(true)
    setError(null)
    try {
      if (format() === 'curl') {
        // Single request: parse, scope to the active workspace, persist, open.
        const parsed = (await wails.ImportCurl(content)) as unknown as RequestDef
        const req: RequestDef = {
          ...parsed,
          id: parsed.id || crypto.randomUUID(),
          workspaceId: appState.activeWorkspaceId ?? parsed.workspaceId,
          name: parsed.name || `${parsed.method} ${parsed.url}`,
        }
        await wails.CreateRequest(models.RequestDef.createFrom(req))
        if (appState.activeWorkspaceId) await loadWorkspaceData(appState.activeWorkspaceId)
        openTab(req.id)
      } else {
        // Collection: creates a whole new workspace; switch to it.
        const newWorkspaceId = await wails.ImportCollection(content)
        await loadWorkspaces()
        setAppState('activeWorkspaceId', newWorkspaceId)
        await loadWorkspaceData(newWorkspaceId)
      }
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
            <h2 class="text-sm font-semibold text-ink">Import</h2>
            <p class="mt-0.5 text-xs text-ink-muted">
              Paste a cURL command, an OpenAPI/Swagger spec, or a Postman collection — the format is detected automatically.
            </p>
          </div>

          <div class="flex flex-col gap-3 px-4 py-3">
            <textarea
              autofocus
              rows={10}
              spellcheck={false}
              class="w-full resize-none rounded border border-edge bg-app p-2 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              placeholder={'curl https://api.example.com/users\n\n— or paste an OpenAPI spec / Postman collection JSON —'}
              value={raw()}
              onInput={(e) => onInput(e.currentTarget.value)}
            />

            <div class="flex items-center gap-2 text-xs">
              <Show when={raw().trim()}>
                <Show
                  when={format()}
                  fallback={<span class="text-warn">Unrecognized format</span>}
                >
                  <span class="rounded bg-accent px-2 py-0.5 text-[11px] font-medium text-accent-contrast">
                    Detected: {FORMAT_LABEL[format()] ?? format()}
                  </span>
                </Show>
              </Show>
            </div>

            <Show when={error()}>
              <p class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">{error()}</p>
            </Show>
          </div>

          <div class="flex items-center justify-end gap-2 border-t border-edge px-4 py-3">
            <button class="rounded px-3 py-1.5 text-xs text-ink-muted hover:bg-raised" onClick={close}>
              Cancel
            </button>
            <button
              class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
              disabled={!raw().trim() || !format() || importing()}
              onClick={handleImport}
            >
              {importing() ? 'Importing…' : 'Import'}
            </button>
          </div>
        </div>
      </div>
    </Show>
  )
}
