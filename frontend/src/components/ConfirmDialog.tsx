import { Show, createEffect, onCleanup } from 'solid-js'
import { confirmState, answerConfirm } from '../lib/confirm'

// The single in-app confirmation dialog, mounted once at the app root (see
// App.tsx). It renders whenever confirmDialog() has a pending question and
// resolves that promise on the user's answer. Keyboard: Enter confirms,
// Escape cancels; focus lands on the safe (cancel) action by default so a
// stray Enter doesn't fire a destructive default. Replaces native confirm().
export default function ConfirmDialog() {
  let cancelBtn: HTMLButtonElement | undefined

  // Focus the cancel button when a dialog opens, and wire global keys while
  // it's up. A destructive action should never be the thing a reflexive
  // Enter lands on, so cancel holds focus; Enter still confirms explicitly
  // via the keydown handler below.
  //
  // Two rules this handler must obey, both learned the hard way:
  //
  //  1. MODIFIED Enter is NOT a confirmation. ⌘Enter is the app's global Send
  //     shortcut (ShortcutSheet), which stays live while this dialog is up —
  //     so a bare `e.key === 'Enter'` check turns a reflexive "send my
  //     request" into "yes, permanently delete this request". Only unmodified
  //     Enter confirms.
  //  2. The dialog OWNS these keys while open — stopImmediatePropagation, not
  //     just preventDefault. This listener is capture-phase, so without it the
  //     same keypress still reaches every bubble-phase window handler: Escape
  //     would cancel the delete AND close the Environment editor behind it
  //     (discarding unsaved secret values), collapse the sidebar, or close
  //     Settings. Same convention as ContextMenu.tsx ("the menu owns keys
  //     while open").
  createEffect(() => {
    if (!confirmState()) return
    // Remember what had focus so it can be restored on close — otherwise
    // dismissing a dialog opened from the keyboard (F2/⌘⌫ in the tree) drops
    // focus to <body> and kills tree navigation mid-flow.
    const previouslyFocused = document.activeElement as HTMLElement | null
    cancelBtn?.focus()
    const onKey = (e: KeyboardEvent) => {
      const modified = e.metaKey || e.ctrlKey || e.altKey || e.shiftKey
      if (e.key === 'Escape' && !modified) {
        e.preventDefault()
        e.stopImmediatePropagation()
        answerConfirm(false)
      } else if (e.key === 'Enter' && !modified) {
        e.preventDefault()
        e.stopImmediatePropagation()
        answerConfirm(true)
      }
    }
    window.addEventListener('keydown', onKey, true)
    onCleanup(() => {
      window.removeEventListener('keydown', onKey, true)
      // Deferred: at cleanup time focus is usually still on the dialog button
      // that is about to be unmounted, so the "did anything else claim focus?"
      // check is only meaningful once the DOM has settled. Restore only if
      // focus ended up nowhere (body) or on a now-detached node — never steal
      // it back from something that deliberately took it.
      queueMicrotask(() => {
        const active = document.activeElement as HTMLElement | null
        const focusIsLost = !active || active === document.body || !active.isConnected
        if (focusIsLost && previouslyFocused?.isConnected) previouslyFocused.focus()
      })
    })
  })

  return (
    <Show when={confirmState()}>
      {(state) => (
        <div
          class="fixed inset-0 z-[60] flex items-center justify-center bg-black/40 p-4"
          onClick={() => answerConfirm(false)}
        >
          <div
            class="w-full max-w-sm rounded-lg border border-edge bg-surface shadow-2xl"
            role="alertdialog"
            aria-modal="true"
            aria-label={state().title}
            onClick={(e) => e.stopPropagation()}
          >
            <div class="px-4 pt-4">
              <h2 class="text-sm font-semibold text-ink">{state().title}</h2>
              <Show when={state().body}>
                <p class="mt-1.5 text-xs leading-relaxed text-ink-dim">{state().body}</p>
              </Show>
            </div>
            <div class="flex justify-end gap-2 px-4 pb-4 pt-4">
              <button
                ref={cancelBtn}
                class="rounded bg-field px-3 py-1.5 text-xs font-medium text-ink-dim hover:bg-raised focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
                onClick={() => answerConfirm(false)}
              >
                {state().cancelText ?? 'Cancel'}
              </button>
              <button
                class="rounded px-3 py-1.5 text-xs font-medium focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
                classList={{
                  // Subtle danger (bordered, tinted) rather than a filled red —
                  // the dark-theme --color-danger is too light to carry white
                  // text; this matches the app's existing danger treatment.
                  'border border-danger-edge bg-danger-bg/60 text-danger hover:bg-danger-bg': !!state().danger,
                  'bg-accent text-accent-contrast hover:bg-accent-hover': !state().danger,
                }}
                onClick={() => answerConfirm(true)}
              >
                {state().confirmText ?? 'Confirm'}
              </button>
            </div>
          </div>
        </div>
      )}
    </Show>
  )
}
