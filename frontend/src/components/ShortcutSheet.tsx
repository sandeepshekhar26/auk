import { For, Show, onCleanup, onMount } from 'solid-js'
import { shortcutSheetOpen, setShortcutSheetOpen } from '../lib/store'
import { COMMANDS, chordKeys, type CommandGroup } from '../lib/keymap'

// Dispatch contract: on Cmd/Ctrl+Enter this component fires
// `window.dispatchEvent(new CustomEvent('apitool:send'))`, consumed by
// App.tsx's onSendShortcut to trigger the same send path as a click.
// App.tsx's onGlobalShortcuts owns ⌘W/⌘Shift+]/⌘Shift+[/⌘F the same way —
// one place per cross-component shortcut, dispatched as a CustomEvent
// rather than each component adding its own competing window listener.

// The sheet is GENERATED from the command registry, never hand-maintained.
// The previous hand-written list had already drifted: it advertised shortcuts
// in a fixed order with no notion of which applied, and every new binding was
// a second edit someone had to remember. Now a command that exists is a
// command that is documented, by construction.
const GROUP_ORDER: CommandGroup[] = ['Request', 'Response', 'Navigation', 'Workspace', 'View']

export default function ShortcutSheet() {
  function close() {
    setShortcutSheetOpen(false)
  }

  // The ⌘/ toggle itself is owned by CommandPalette.tsx's global listener
  // (kept in one place to avoid two listeners double-toggling the same
  // signal on the same keypress). This listener only owns ⌘Enter (send)
  // and Escape (close-while-open).
  // Escape only. ⌘Enter (send) and ⌘/ (this sheet) are registry commands
  // dispatched by App.tsx — this component used to own ⌘Enter, which meant a
  // second window listener racing the one in App for the same keystroke.
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && shortcutSheetOpen()) close()
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
          <div class="max-h-[60vh] overflow-y-auto">
            <For each={GROUP_ORDER}>
              {(group) => {
                const rows = () => COMMANDS.filter((c) => c.group === group && c.chord)
                return (
                  <Show when={rows().length > 0}>
                    <div class="px-4 pb-1 pt-3 text-[10px] font-semibold uppercase tracking-[0.08em] text-ink-faint">
                      {group}
                    </div>
                    <table class="w-full text-sm">
                      <tbody>
                        <For each={rows()}>
                          {(c) => (
                            <tr class="border-b border-edge/60 last:border-0">
                              <td class="px-4 py-1.5 text-ink-dim">{c.title}</td>
                              <td class="px-4 py-1.5">
                                <div class="flex justify-end gap-1">
                                  <For each={chordKeys(c.chord!)}>
                                    {(k) => (
                                      <kbd class="rounded border border-edge-strong bg-raised px-1.5 py-0.5 font-mono text-[11px] text-ink-dim">
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
                  </Show>
                )
              }}
            </For>
            <p class="px-4 py-3 text-[11px] leading-relaxed text-ink-faint">
              Every command — including the ones with no shortcut — is searchable in the command
              palette (⌘K).
            </p>
          </div>
        </div>
      </div>
    </Show>
  )
}
