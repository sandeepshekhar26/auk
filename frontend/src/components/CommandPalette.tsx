import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from 'solid-js'
import { appState, commandPaletteOpen, openTab, setCommandPaletteOpen } from '../lib/store'
import type { CommandItem } from '../types'
import { availableCommands, chordKeys } from '../lib/keymap'

const GROUP_LABEL: Record<CommandItem['group'], string> = {
  action: 'Actions',
  request: 'Requests',
  navigation: 'Navigate',
}

// The command palette is the app's JUMP surface: everything reachable
// elsewhere (new request, import, settings, sidebar sections, theme) is
// reachable here too, so a keyboard-first user never needs the mouse. It
// complements the docked sidebar (the BROWSE surface) rather than replacing
// it — see docs/05-ux-north-star.md "The navigation model".
export default function CommandPalette() {
  const [query, setQuery] = createSignal('')
  let inputRef: HTMLInputElement | undefined

  const items = createMemo<CommandItem[]>(() => {
    // Actions come STRAIGHT from the command registry, filtered by whether
    // each currently applies. The palette used to keep its own parallel list,
    // which is how a command could have a shortcut but no palette entry (or
    // the reverse). Now the two cannot disagree: one registry, two surfaces.
    const actionItems: CommandItem[] = availableCommands().map((c) => ({
      id: c.id,
      title: c.title,
      // The chord is the most useful subtitle there is — it teaches the
      // shortcut at the moment someone reaches for the command by name, which
      // is how a keyboard-first app trains its own users.
      subtitle: c.chord ? chordKeys(c.chord).join(' ') : c.subtitle,
      group: 'action',
      run: c.run,
    }))
    const requestItems: CommandItem[] = appState.requests.map((r) => ({
      id: `req:${r.id}`,
      title: r.name,
      subtitle: r.url,
      group: 'request',
      run: () => openTab(r.id),
    }))
    const all = [...actionItems, ...requestItems]
    const q = query().trim().toLowerCase()
    if (!q) return all
    return all.filter((i) => i.title.toLowerCase().includes(q) || i.subtitle?.toLowerCase().includes(q))
  })

  // Grouped for display so the palette reads as organized surfaces, not a
  // flat dump — Actions first (what you'd reach for), Requests below.
  const grouped = createMemo(() => {
    const out = new Map<CommandItem['group'], CommandItem[]>()
    for (const item of items()) {
      const list = out.get(item.group) ?? []
      list.push(item)
      out.set(item.group, list)
    }
    return out
  })

  function close() {
    setCommandPaletteOpen(false)
    setQuery('')
  }

  // Escape only. ⌘K and ⌘/ are registry commands dispatched by App.tsx —
  // this component owning them too meant two window listeners firing on one
  // keystroke, which is exactly the class of bug the registry removes.
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && commandPaletteOpen()) close()
  }

  // Focus the search field whenever the palette opens.
  //
  // The bare `autofocus` attribute below never did this: Solid renders the
  // input into a <Show> that mounts AFTER the browser's autofocus pass, so the
  // attribute was inert and focus simply stayed wherever it already was. The
  // palette was therefore unusable by keyboard alone — ⌘K opened it and your
  // keystrokes went into whatever was focused before, which for anyone who had
  // just pressed ⌘L meant silently typing into the request's URL field.
  //
  // requestAnimationFrame, not a bare call: the ref is assigned during the
  // same render that mounts it, and focusing before the element is in the
  // document is a no-op.
  createEffect(
    on(commandPaletteOpen, (open) => {
      if (open) requestAnimationFrame(() => inputRef?.focus())
    }),
  )

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  return (
    <Show when={commandPaletteOpen()}>
      <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-32" onClick={close}>
        <div
          class="w-full max-w-lg overflow-hidden rounded-lg border border-edge-strong bg-field shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <input
            ref={inputRef}
            class="w-full border-b border-edge bg-transparent px-4 py-3 text-base text-ink focus:outline-none"
            placeholder="Jump to a request, run a command…"
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
          />
          <div class="max-h-96 overflow-y-auto py-1">
            <For each={[...grouped().entries()]} fallback={<p class="px-4 py-3 text-sm text-ink-faint">No matches</p>}>
              {([group, groupItems]) => (
                <div>
                  <p class="px-4 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
                    {GROUP_LABEL[group]}
                  </p>
                  <For each={groupItems}>
                    {(item) => (
                      <button
                        class="flex w-full items-center justify-between px-4 py-2 text-left text-sm text-ink-dim hover:bg-raised"
                        onClick={() => {
                          item.run()
                          close()
                        }}
                      >
                        <span>{item.title}</span>
                        <span class="ml-4 shrink-0 truncate text-xs text-ink-faint">{item.subtitle}</span>
                      </button>
                    )}
                  </For>
                </div>
              )}
            </For>
          </div>
        </div>
      </div>
    </Show>
  )
}
