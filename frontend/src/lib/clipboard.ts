// One clipboard write for the whole app, because the obvious one doesn't
// work where it matters.
//
// THE BUG: every copy button used `await navigator.clipboard.writeText(...)`
// inside a try/catch that swallowed the failure. That works in a normal
// browser tab (and therefore in `wails dev`, which is what everything was
// tested against), but in the PACKAGED app the frontend runs inside a
// WKWebView loaded from the wails:// custom scheme. WebKit gates the async
// Clipboard API on a secure context AND on user-activation/permission that
// a custom-scheme page doesn't satisfy, so writeText() rejects (or
// navigator.clipboard is undefined outright). The catch block then did
// nothing at all — the button showed no "Copied", no error, and users
// reported "copy as cURL doesn't work" with nothing in the UI to explain it.
//
// THE FIX: go through Wails' own runtime first — ClipboardSetText is a bound
// native call, so it reaches NSPasteboard directly and is subject to none of
// WebKit's DOM clipboard gating. The web APIs stay as fallbacks for the
// browser-only dev server (`vite dev` on :5173 with no Wails runtime
// injected), where window.runtime doesn't exist.
//
// And it is NEVER silent: callers get an explicit ok:false plus a reason, so
// every call site can (and now does) render a visible failure state.
import { ClipboardSetText } from '../../wailsjs/runtime/runtime'

export interface CopyResult {
  ok: boolean
  /** Human-readable reason, set only when ok is false. */
  error?: string
}

/**
 * Copies text to the system clipboard, trying (in order) the Wails native
 * runtime, the async Clipboard API, and a hidden-textarea execCommand copy.
 * Never throws and never fails silently.
 */
export async function copyText(text: string): Promise<CopyResult> {
  const failures: string[] = []

  // 1. Wails native. `window.runtime` is only injected by the Wails
  // webview, so the import itself is safe but the call throws a TypeError
  // in a plain browser — hence the try, not a capability check on the
  // imported binding (which is always a function).
  try {
    if (typeof window !== 'undefined' && (window as { runtime?: unknown }).runtime) {
      // ClipboardSetText resolves to a boolean: false means the native side
      // refused the write, which is a failure even though nothing threw.
      const ok = await ClipboardSetText(text)
      if (ok !== false) return { ok: true }
      failures.push('Wails clipboard refused the write')
    }
  } catch (err) {
    failures.push(`Wails clipboard: ${message(err)}`)
  }

  // 2. Async Clipboard API — the dev-server path.
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return { ok: true }
    }
  } catch (err) {
    failures.push(`navigator.clipboard: ${message(err)}`)
  }

  // 3. Deprecated but still widely supported synchronous path. Needs a real
  // element in the document and a selection; the textarea is positioned off
  // screen (rather than display:none, which makes it unselectable) and
  // removed again immediately.
  try {
    if (typeof document !== 'undefined' && typeof document.execCommand === 'function') {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.setAttribute('readonly', '')
      ta.style.position = 'fixed'
      ta.style.top = '-1000px'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      ta.setSelectionRange(0, text.length)
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      if (ok) return { ok: true }
      failures.push('execCommand("copy") returned false')
    }
  } catch (err) {
    failures.push(`execCommand: ${message(err)}`)
  }

  return { ok: false, error: failures.join('; ') || 'no clipboard mechanism available' }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
