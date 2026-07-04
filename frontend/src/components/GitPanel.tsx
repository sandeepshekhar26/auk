import { For, Show, createSignal, createEffect } from 'solid-js'
import { explorerOpen, explorerTab } from '../lib/store'
import { wails } from '../lib/wails'
import type { GitCommit, GitFileChangeStatus, GitStatus } from '../types'

function relativeTime(timestamp: string): string {
  const then = new Date(timestamp).getTime()
  if (Number.isNaN(then)) return ''
  const diffSec = Math.round((Date.now() - then) / 1000)
  if (diffSec < 5) return 'just now'
  if (diffSec < 60) return `${diffSec}s ago`
  const diffMin = Math.round(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHour = Math.round(diffMin / 60)
  if (diffHour < 24) return `${diffHour}h ago`
  return `${Math.round(diffHour / 24)}d ago`
}

const STATUS_LABEL: Record<GitFileChangeStatus, string> = {
  added: 'A',
  modified: 'M',
  deleted: 'D',
  renamed: 'R',
  untracked: 'U',
}

const STATUS_CLASS: Record<GitFileChangeStatus, string> = {
  added: 'text-accent-fg',
  modified: 'text-warn',
  deleted: 'text-danger',
  renamed: 'text-info',
  untracked: 'text-ink-faint',
}

// GitPanel is the "git collaboration" slice of the explorer drawer: the
// workspace directory's status, its commit history, and a one-click
// commit+push. It wires internal/gitops (go-git) — the "in-app git" design
// decision that had never actually been implemented before this.
export default function GitPanel() {
  const [status, setStatus] = createSignal<GitStatus | null>(null)
  const [commits, setCommits] = createSignal<GitCommit[]>([])
  const [message, setMessage] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [loadError, setLoadError] = createSignal<string | null>(null)
  const [actionResult, setActionResult] = createSignal<string | null>(null)

  async function refresh() {
    setLoadError(null)
    try {
      const [st, log] = await Promise.all([wails.GitStatus(), wails.GitLog(20)])
      setStatus(st as unknown as GitStatus)
      setCommits((log ?? []) as unknown as GitCommit[])
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err))
    }
  }

  // Refresh whenever the drawer opens on the Git tab — the workspace may
  // have changed on disk since the last time this was visible.
  createEffect(() => {
    if (explorerOpen() && explorerTab() === 'git') void refresh()
  })

  async function commitAndPush() {
    if (!message().trim() || busy()) return
    setBusy(true)
    setActionResult(null)
    try {
      const pushed = await wails.GitCommitAndPush(message())
      setMessage('')
      setActionResult(pushed ? 'Committed and pushed.' : 'Committed locally (no remote configured).')
      await refresh()
    } catch (err) {
      setActionResult(`Failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="flex h-full flex-col overflow-y-auto p-2 text-xs">
      <Show when={loadError()}>
        <p class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-danger">{loadError()}</p>
      </Show>

      <Show when={status()}>
        {(st) => (
          <>
            <div class="flex items-center gap-2 px-1 py-1.5">
              <span class="font-mono font-semibold text-ink-dim">⎇ {st().branch}</span>
              <span
                class="rounded px-1.5 py-0.5 text-[10px] font-semibold"
                classList={{
                  'bg-accent text-accent-contrast': st().clean,
                  'bg-warn text-accent-contrast': !st().clean,
                }}
              >
                {st().clean ? 'CLEAN' : `${st().files.length} CHANGED`}
              </span>
              <Show when={!st().hasRemote}>
                <span class="text-ink-faint" title="No 'origin' remote configured — commits stay local">
                  no remote
                </span>
              </Show>
            </div>

            <Show when={st().files.length > 0}>
              <div class="mb-2 flex flex-col gap-0.5 rounded border border-edge-soft p-1">
                <For each={st().files}>
                  {(f) => (
                    <div class="flex items-center gap-2 px-1 py-0.5 font-mono">
                      <span class={`w-3 shrink-0 font-semibold ${STATUS_CLASS[f.status]}`}>{STATUS_LABEL[f.status]}</span>
                      <span class="truncate text-ink-dim">{f.path}</span>
                    </div>
                  )}
                </For>
              </div>
            </Show>

            <div class="mb-2 flex flex-col gap-1.5 px-1">
              <textarea
                class="h-14 w-full resize-none rounded bg-field p-1.5 font-mono text-[11px] text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                placeholder="Commit message"
                value={message()}
                disabled={st().clean}
                onInput={(e) => setMessage(e.currentTarget.value)}
              />
              <button
                class="self-start rounded bg-accent px-2 py-1 text-[11px] font-medium text-accent-contrast hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-40"
                disabled={st().clean || !message().trim() || busy()}
                onClick={commitAndPush}
              >
                {busy() ? 'Working…' : st().hasRemote ? 'Commit & Push' : 'Commit'}
              </button>
              <Show when={actionResult()}>
                <p class="text-ink-faint">{actionResult()}</p>
              </Show>
            </div>
          </>
        )}
      </Show>

      <h3 class="mt-1 px-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">History</h3>
      <Show when={commits().length === 0}>
        <p class="px-2 py-4 text-ink-faint">No commits yet.</p>
      </Show>
      <For each={commits()}>
        {(c) => (
          <div class="flex flex-col gap-0.5 rounded px-2 py-1.5 hover:bg-raised">
            <div class="flex items-center gap-2">
              <span class="font-mono text-ink-faint">{c.hash}</span>
              <span class="flex-1 truncate text-ink-dim">{c.message}</span>
            </div>
            <div class="flex items-center gap-2 text-ink-faint">
              <span>{c.author}</span>
              <span title={new Date(c.date).toLocaleString()}>{relativeTime(c.date)}</span>
            </div>
          </div>
        )}
      </For>
    </div>
  )
}
