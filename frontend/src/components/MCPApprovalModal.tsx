import { Show, createMemo } from 'solid-js'
import { mcpApprovals, setMcpApprovals } from '../lib/store'
import { wails } from '../lib/wails'

// MCPApprovalModal renders the pending agent-request approval (if any). The
// "mcp:approval" event listener lives at app scope (App.tsx), NOT here — a
// leaf component's onMount/onCleanup lifecycle is too fragile to hold a
// long-lived subscription (a dispose would silently drop future events). This
// component is pure render over the store queue.
export default function MCPApprovalModal() {
  const current = createMemo(() => mcpApprovals()[0] ?? null)

  function respond(allow: boolean) {
    const approval = current()
    if (!approval) return
    void wails.RespondMCPApproval(approval.id, allow)
    setMcpApprovals((q) => q.filter((a) => a.id !== approval.id))
  }

  return (
    <Show when={current()}>
      {(a) => (
        <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50">
          <div
            role="alertdialog"
            aria-modal="true"
            aria-label="MCP request approval"
            class="w-full max-w-md rounded-lg border border-warn-edge bg-surface shadow-2xl"
          >
            <div class="border-b border-edge px-4 py-3">
              <h2 class="text-sm font-semibold text-ink">Agent wants to send a request</h2>
              <p class="mt-0.5 text-xs text-ink-muted">
                An MCP client (e.g. Claude Code) is asking to run a request that can change data.
              </p>
            </div>
            <div class="flex items-center gap-2 px-4 py-3 font-mono text-sm">
              <span class="rounded bg-warn/15 px-1.5 py-0.5 text-xs font-semibold text-warn">{a().method}</span>
              <span class="break-all text-ink-dim">{a().url}</span>
            </div>
            <Show when={mcpApprovals().length > 1}>
              <p class="px-4 pb-1 text-xs text-ink-faint">+{mcpApprovals().length - 1} more waiting</p>
            </Show>
            <div class="flex items-center justify-end gap-2 border-t border-edge px-4 py-3">
              <button
                class="rounded px-3 py-1.5 text-xs font-medium text-ink-dim hover:bg-raised"
                onClick={() => respond(false)}
              >
                Deny
              </button>
              <button
                class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover"
                onClick={() => respond(true)}
              >
                Allow once
              </button>
            </div>
          </div>
        </div>
      )}
    </Show>
  )
}
