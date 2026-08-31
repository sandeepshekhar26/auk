import { For, Show, createEffect, createSignal, on, onCleanup, onMount } from 'solid-js'
import { appState } from '../lib/store'
import { copyText } from '../lib/clipboard'
import { SNIPPET_FORMATS, canCopySnippet, resolveSnippet, type SnippetFormat } from '../lib/snippets'
import type { ProtocolKind } from '../types'

// The one "Copy as ▾" control, used from BOTH sides of the split: the
// response header bar (after a send) and the request toolbar (before one).
// Extracted rather than duplicated because the two must agree exactly —
// copying a request as cURL should produce the same command whether or not
// you happened to press Send first, which is only true if both go through
// resolveSnippet's single backend resolve path.
//
// `variant` is purely presentation: 'label' is the wide response-side
// button, 'icon' the unobtrusive `</>` that sits next to Send.
export default function CopyAsMenu(props: {
  requestId: string | undefined
  protocol: ProtocolKind | undefined
  variant?: 'label' | 'icon'
}) {
  const [open, setOpen] = createSignal(false)
  const [copied, setCopied] = createSignal<string | null>(null)
  const [failed, setFailed] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  let resetTimer: ReturnType<typeof setTimeout> | undefined
  onCleanup(() => clearTimeout(resetTimer))

  // This CopyAsMenu instance persists across active-request switches (it's
  // rendered in a non-keyed <Show when={active()}> in RequestEditor) while
  // props.requestId changes reactively. If the dropdown is left open on
  // request A and the user switches to B via a path the click-catcher never
  // sees (⌘⇧], ⌘K → pick), the menu would stay open and the next click would
  // copy B. Reset all transient menu/flash state whenever the request identity
  // changes. (on() also runs once on mount — a harmless reset, since every
  // state already starts empty.)
  createEffect(
    on(
      () => props.requestId,
      () => {
        setOpen(false)
        setCopied(null)
        setFailed(false)
        setError(null)
        clearTimeout(resetTimer)
      },
    ),
  )

  const enabled = () => !!props.requestId && canCopySnippet(props.protocol)

  // ⌘⇧C copies as cURL without opening the menu — the format people reach for
  // 90% of the time. Listening here rather than in the keymap keeps the copy
  // logic (and its error flashing) in the one component that owns it.
  function onCopyAsCurl() {
    if (!enabled()) return
    const curl = SNIPPET_FORMATS.find((f) => f.id === 'curl')
    if (curl) void copySnippet(curl)
  }
  onMount(() => window.addEventListener('apitool:copy-as-curl', onCopyAsCurl))
  onCleanup(() => window.removeEventListener('apitool:copy-as-curl', onCopyAsCurl))

  function flash(label: string | null, err: string | null) {
    setCopied(label)
    setFailed(err !== null)
    setError(err)
    clearTimeout(resetTimer)
    // A failure stays up ~4x longer than a success: "Copied" is a
    // confirmation you already expected, "Copy failed" is news.
    resetTimer = setTimeout(
      () => {
        setCopied(null)
        setFailed(false)
        setError(null)
      },
      err === null ? 1500 : 6000,
    )
  }

  async function copySnippet(format: SnippetFormat) {
    const requestId = props.requestId
    if (!requestId) return
    try {
      const resolved = await resolveSnippet(requestId, appState.activeEnvironmentId ?? '')
      const code = format.build(resolved)
      const result = await copyText(code)
      if (result.ok) {
        setOpen(false)
        flash(format.label, null)
      } else {
        // Keep the menu OPEN on failure so the message renders right where
        // the click happened instead of vanishing with the dropdown.
        flash(null, result.error ?? 'clipboard unavailable')
      }
    } catch (err) {
      // A resolve failure (undefined ${variable}, an auth token fetch that
      // didn't work, a policy block) is exactly the failure a Send would
      // have hit — show the backend's own message rather than a generic one.
      flash(null, err instanceof Error ? err.message : String(err))
    }
  }

  const label = () => {
    if (failed()) return 'Copy failed'
    if (copied()) return `Copied ${copied()}`
    return 'Copy as ▾'
  }

  return (
    <div class="relative">
      <Show
        when={props.variant === 'icon'}
        fallback={
          <button
            class="rounded bg-field px-2 py-1 text-[11px] hover:bg-raised disabled:cursor-not-allowed disabled:opacity-40"
            classList={{ 'text-danger': failed(), 'text-ink-dim': !failed() }}
            disabled={!enabled()}
            onClick={() => setOpen((v) => !v)}
            title={enabled() ? 'Copy this request as a code snippet' : 'Code snippets are only available for HTTP/GraphQL requests'}
          >
            {label()}
          </button>
        }
      >
        <button
          class="rounded bg-field px-2 py-1 font-mono text-xs hover:bg-raised disabled:cursor-not-allowed disabled:opacity-40"
          classList={{
            'text-danger': failed(),
            'text-accent-fg': !failed() && !!copied(),
            'text-ink-muted': !failed() && !copied(),
          }}
          disabled={!enabled()}
          onClick={() => setOpen((v) => !v)}
          title={
            enabled()
              ? 'Copy as code (cURL, Python, JS, Go) — resolves variables, path params and auth, no send required'
              : 'Code snippets are only available for HTTP/GraphQL requests'
          }
        >
          <Show when={failed()} fallback={<Show when={copied()} fallback={'</>'}>{'✓'}</Show>}>
            {'!'}
          </Show>
        </button>
      </Show>

      <Show when={open()}>
        <div class="fixed inset-0 z-10" onClick={() => setOpen(false)} />
        <div
          class="absolute right-0 top-full z-20 mt-1 w-56 rounded border border-edge bg-elevated py-1 shadow-lg"
          onClick={(e) => e.stopPropagation()}
        >
          <For each={SNIPPET_FORMATS}>
            {(format) => (
              <button
                class="block w-full px-3 py-1.5 text-left text-[11px] text-ink-dim hover:bg-raised hover:text-ink"
                onClick={() => copySnippet(format)}
              >
                {format.label}
              </button>
            )}
          </For>
          <Show when={error()}>
            <p class="mt-1 border-t border-edge px-3 pt-1.5 text-[10px] leading-snug text-danger">{error()}</p>
          </Show>
        </div>
      </Show>
    </div>
  )
}
