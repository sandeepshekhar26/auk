import { JSX, Show, createSignal } from 'solid-js'

// Native `title` attributes work in the packaged app's WKWebView (confirmed
// live), but the ~1.2-1.5s OS hover delay plus generic system styling makes
// an icon-only rail feel undiscoverable — a user scanning icons quickly
// perceives "no tooltip at all" since the timer resets on any mouse motion.
// This wraps a trigger in a fast, on-brand tooltip instead. The native
// `title` on the wrapped element is left in place as a fallback (screen
// readers, and any input path that doesn't go through mouse hover).
export default function Tooltip(props: { text: string; side?: 'right' | 'bottom'; children: JSX.Element }) {
  const [visible, setVisible] = createSignal(false)
  let timer: ReturnType<typeof setTimeout> | undefined

  function onEnter() {
    timer = setTimeout(() => setVisible(true), 350)
  }
  function onLeave() {
    if (timer) clearTimeout(timer)
    setVisible(false)
  }

  return (
    <div class="relative inline-flex" onMouseEnter={onEnter} onMouseLeave={onLeave}>
      {props.children}
      <Show when={visible()}>
        <div
          class="pointer-events-none absolute z-50 whitespace-nowrap rounded border border-edge-strong bg-elevated px-2 py-1 text-xs text-ink shadow-lg"
          classList={{
            'left-full top-1/2 ml-2 -translate-y-1/2': (props.side ?? 'right') === 'right',
            'left-1/2 top-full mt-2 -translate-x-1/2': props.side === 'bottom',
          }}
        >
          {props.text}
        </div>
      </Show>
    </div>
  )
}
