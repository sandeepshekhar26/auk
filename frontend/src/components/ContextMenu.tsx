import { Index, Show, createSignal, onCleanup, onMount } from 'solid-js'
import { Portal } from 'solid-js/web'

// Small reusable context menu (sidebar tree rows, and anything else that
// needs one later). Deliberately hand-rolled rather than pulling a menu
// library: fixed-position at the pointer, clamped to the viewport, closes
// on outside pointerdown / Escape / window blur, and is keyboard-navigable
// (↑/↓/Home/End move, Enter/Space run, Escape closes). The caller owns the
// open/closed state and passes the anchor point; this component owns
// everything after that.

export interface MenuItem {
  label: string
  /** Renders in the danger color (delete-style actions). */
  danger?: boolean
  /** Draws a separator line above this item. */
  separatorAbove?: boolean
  action: () => void
}

export default function ContextMenu(props: { x: number; y: number; items: MenuItem[]; onClose: () => void }) {
  const [highlighted, setHighlighted] = createSignal(-1)
  const [pos, setPos] = createSignal({ x: props.x, y: props.y })
  let menuRef: HTMLDivElement | undefined

  function run(item: MenuItem) {
    props.onClose()
    item.action()
  }

  function onKeyDown(e: KeyboardEvent) {
    // The menu owns keys while open — the tree's own keyboard nav (and the
    // overlay drawer's Escape-to-close) must not also react to them.
    e.stopPropagation()
    const count = props.items.length
    if (e.key === 'Escape') {
      e.preventDefault()
      props.onClose()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHighlighted((i) => (i + 1) % count)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlighted((i) => (i <= 0 ? count - 1 : i - 1))
    } else if (e.key === 'Home') {
      e.preventDefault()
      setHighlighted(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      setHighlighted(count - 1)
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      const item = props.items[highlighted()]
      if (item) run(item)
    }
  }

  function onOutsidePointerDown(e: PointerEvent) {
    if (menuRef && !menuRef.contains(e.target as Node)) props.onClose()
  }

  onMount(() => {
    // Clamp into the viewport once the real size is measurable.
    if (menuRef) {
      const rect = menuRef.getBoundingClientRect()
      setPos({
        x: Math.max(4, Math.min(props.x, window.innerWidth - rect.width - 4)),
        y: Math.max(4, Math.min(props.y, window.innerHeight - rect.height - 4)),
      })
    }
    menuRef?.focus()
    // Capture phase so a pointerdown that some row swallows still closes us.
    document.addEventListener('pointerdown', onOutsidePointerDown, true)
    window.addEventListener('blur', props.onClose)
  })
  onCleanup(() => {
    document.removeEventListener('pointerdown', onOutsidePointerDown, true)
    window.removeEventListener('blur', props.onClose)
  })

  return (
    <Portal>
      <div
        ref={menuRef}
        tabindex="-1"
        role="menu"
        class="fixed z-[70] min-w-[11rem] rounded-md border border-edge-strong bg-field py-1 shadow-2xl focus:outline-none"
        style={{ left: `${pos().x}px`, top: `${pos().y}px` }}
        onKeyDown={onKeyDown}
        onContextMenu={(e) => e.preventDefault()}
      >
        <Index each={props.items}>
          {(item, index) => (
            <>
              <Show when={item().separatorAbove}>
                <div class="mx-2 my-1 h-px bg-edge-strong" />
              </Show>
              <button
                role="menuitem"
                class="flex w-full items-center px-3 py-1.5 text-left text-xs"
                classList={{
                  'bg-raised': highlighted() === index,
                  'text-danger': item().danger,
                  'text-ink-dim': !item().danger,
                }}
                onMouseEnter={() => setHighlighted(index)}
                onClick={() => run(item())}
              >
                {item().label}
              </button>
            </>
          )}
        </Index>
      </div>
    </Portal>
  )
}
