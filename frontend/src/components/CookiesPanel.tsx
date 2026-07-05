import { For, Show, createEffect, createSignal } from 'solid-js'
import { createStore } from 'solid-js/store'
import { appState } from '../lib/store'
import { wails } from '../lib/wails'
import type { KeyValue } from '../types'

const inputClass =
  'rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong'

// Session-lifetime only (see internal/cookiejar's package doc) — nothing
// here survives an app restart, matching what's actually captured/editable
// on the backend today.
export default function CookiesPanel() {
  const [cookies, setCookies] = createStore<KeyValue[]>([])
  const [newName, setNewName] = createSignal('')
  const [newValue, setNewValue] = createSignal('')
  const saveTimers = new Map<string, ReturnType<typeof setTimeout>>()

  async function reload() {
    if (!appState.activeWorkspaceId) {
      setCookies([])
      return
    }
    const list = await wails.ListCookies(appState.activeWorkspaceId)
    setCookies(list ?? [])
  }

  // Reload whenever the drawer's Cookies tab becomes the active workspace
  // (covers both a fresh workspace switch and simply opening this tab).
  createEffect(() => {
    appState.activeWorkspaceId
    void reload()
  })

  // Fine-grained path setter (matches Sidebar.tsx's setFolderVariable fix
  // this session) — mutates only the touched row's value in place, so
  // <For>'s reference-keying doesn't remount the input the user is typing
  // into. Debounced per cookie NAME (not array index, since index isn't
  // stable if the list is ever reordered/mutated elsewhere while typing).
  function setValue(index: number, value: string) {
    const name = cookies[index]?.key
    if (!name) return
    setCookies(index, 'value', value)
    const existing = saveTimers.get(name)
    if (existing) clearTimeout(existing)
    saveTimers.set(
      name,
      setTimeout(() => {
        saveTimers.delete(name)
        if (appState.activeWorkspaceId) void wails.SetCookie(appState.activeWorkspaceId, name, value)
      }, 400),
    )
  }

  async function removeCookie(index: number) {
    const name = cookies[index]?.key
    if (!name || !appState.activeWorkspaceId) return
    await wails.DeleteCookie(appState.activeWorkspaceId, name)
    await reload()
  }

  async function addCookie() {
    const name = newName().trim()
    if (!name || !appState.activeWorkspaceId) return
    await wails.SetCookie(appState.activeWorkspaceId, name, newValue())
    setNewName('')
    setNewValue('')
    await reload()
  }

  return (
    <div class="flex h-full flex-col overflow-y-auto p-2 text-xs">
      <p class="px-1 pb-2 text-[11px] text-ink-faint">
        Captured automatically from Set-Cookie responses, readable via <code>{'${cookie(name)}'}</code>. Cleared on app restart.
      </p>
      <Show when={cookies.length === 0}>
        <p class="px-1 py-4 text-ink-faint">No cookies captured yet for this workspace.</p>
      </Show>
      <div class="flex flex-col gap-1">
        <For each={cookies}>
          {(cookie, i) => (
            <div class="flex items-center gap-2 px-1">
              <span class="w-1/3 shrink-0 truncate font-mono text-ink-muted" title={cookie.key}>
                {cookie.key}
              </span>
              <input class={`${inputClass} flex-1`} value={cookie.value} onInput={(e) => setValue(i(), e.currentTarget.value)} />
              <button
                class="w-5 shrink-0 rounded text-ink-faint hover:bg-raised hover:text-danger"
                onClick={() => void removeCookie(i())}
                title="Delete cookie"
              >
                ×
              </button>
            </div>
          )}
        </For>
      </div>
      <div class="mt-2 flex items-center gap-2 border-t border-edge px-1 pt-2">
        <input
          class={`${inputClass} w-1/3 shrink-0`}
          placeholder="name"
          value={newName()}
          onInput={(e) => setNewName(e.currentTarget.value)}
        />
        <input
          class={`${inputClass} flex-1`}
          placeholder="value"
          value={newValue()}
          onInput={(e) => setNewValue(e.currentTarget.value)}
        />
        <button
          class="shrink-0 rounded px-2 py-1 text-ink-muted hover:bg-field hover:text-ink-dim"
          onClick={() => void addCookie()}
        >
          + Add
        </button>
      </div>
    </div>
  )
}
