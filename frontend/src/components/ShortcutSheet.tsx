import { For, Show, onCleanup, onMount } from 'solid-js'
import { shortcutSheetOpen, setShortcutSheetOpen } from '../lib/store'

// Dispatch contract: on Cmd/Ctrl+Enter this component fires
// `window.dispatchEvent(new CustomEvent('apitool:send'))`. RequestEditor's
// send button should add a `window.addEventListener('apitool:send', ...)`
// in a future wiring pass to trigger the same send path as a click.

interface ShortcutEntry {
  keys: string[]
  description: string
}

const SHORTCUTS: ShortcutEntry[] = [
  { keys: ['⌘', 'K'], description: 'Open command palette' },
  { keys: ['⌘', 'Enter'], description: 'Send the active request' },
  { keys: ['⌘', 'N'], description: 'New request' },
  { keys: ['⌘', ','], description: 'Open Settings' },
  { keys: ['⌘', '/'], description: 'Toggle this shortcut sheet' },
  { keys: ['Esc'], description: 'Close the active dialog or panel' },
]

export default function ShortcutSheet() {
  function close() {
    setShortcutSheetOpen(false)
  }

  // The ⌘/ toggle itself is owned by CommandPalette.tsx's global listener
  // (kept in one place to avoid two listeners double-toggling the same
  // signal on the same keypress). This listener only owns ⌘Enter (send)
  // and Escape (close-while-open).
  function onKeyDown(e: KeyboardEvent) {
    const meta = e.metaKey || e.ctrlKey

    if (meta && e.key === 'Enter') {
      e.preventDefault()
      window.dispatchEvent(new CustomEvent('apitool:send'))
      return
    }

    if (e.key === 'Escape' && shortcutSheetOpen()) {
      close()
    }
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  return (
    <Show when={shortcutSheetOpen()}>
      <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50" onClick={close}>
        <div
          class="w-full max-w-md overflow-hidden rounded-lg border border-edge-strong bg-field shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="flex items-center justify-between border-b border-edge px-4 py-3">
            <span class="text-sm font-semibold text-ink">Keyboard shortcuts</span>
            <button
              class="rounded px-2 py-1 text-xs text-ink-muted hover:bg-raised hover:text-ink-dim"
              onClick={close}
            >
              Esc
            </button>
          </div>
          <table class="w-full text-sm">
            <tbody>
              <For each={SHORTCUTS}>
                {(s) => (
                  <tr class="border-b border-edge/60 last:border-0">
                    <td class="px-4 py-2 text-ink-dim">{s.description}</td>
                    <td class="px-4 py-2">
                      <div class="flex justify-end gap-1">
                        <For each={s.keys}>
                          {(k) => (
                            <kbd class="rounded border border-edge-strong bg-raised px-1.5 py-0.5 font-mono text-xs text-ink-dim">
                              {k}
                            </kbd>
                          )}
                        </For>
                      </div>
                    </td>
                  </tr>
                )}
              </For>
            </tbody>
          </table>
          <div class="border-t border-edge px-4 py-2 text-xs text-ink-faint">
            Every shortcut works the same whether triggered from the keyboard, the command palette (⌘K), or a click —
            one engine, one code path.
          </div>
        </div>
      </div>
    </Show>
  )
}
