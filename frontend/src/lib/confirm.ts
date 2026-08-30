import { createSignal } from 'solid-js'

// Promise-based confirmation, replacing the browser's native confirm(). The
// native dialog is a UX wart (OS-chrome modal that doesn't match the app) and,
// just as importantly, it's un-driveable by the automated preview harness —
// it blocks the whole webview on a dialog nothing in the tooling can click, so
// any flow guarded by confirm() couldn't be tested. This routes every
// confirmation through an in-app <ConfirmDialog/> instead.
//
// Usage mirrors confirm() but async:
//   if (!(await confirmDialog({ title: 'Delete X?', body: '…', danger: true }))) return

export interface ConfirmOptions {
  title: string
  body?: string
  confirmText?: string
  cancelText?: string
  // Styles the confirm button as destructive (red). Deletes set this.
  danger?: boolean
}

interface PendingConfirm extends ConfirmOptions {
  resolve: (ok: boolean) => void
}

const [pending, setPending] = createSignal<PendingConfirm | null>(null)

// The signal the <ConfirmDialog/> component renders from.
export const confirmState = pending

/**
 * Shows the in-app confirmation dialog and resolves true if the user confirms,
 * false if they cancel/dismiss. Only one dialog is shown at a time; a second
 * call while one is open resolves the earlier one as cancelled (a new question
 * supersedes an unanswered one — matching how a fresh confirm() would replace
 * the prior intent).
 */
export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
    setPending((prev) => {
      prev?.resolve(false)
      return { ...opts, resolve }
    })
  })
}

/** Called by the dialog component when the user answers. */
export function answerConfirm(ok: boolean): void {
  const p = pending()
  if (!p) return
  setPending(null)
  p.resolve(ok)
}
