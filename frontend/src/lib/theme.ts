// Theme controller. The actual colors live in src/index.css as CSS
// variables keyed off <html data-theme="...">; this module just decides
// which value that attribute gets and persists the preference via the Go
// settings binding (~/.auk/settings.yaml).
import { createSignal } from 'solid-js'
import { wails } from './wails'
import { models } from './wails'
import { mutateSettings } from './store'

export type ThemePref = 'system' | 'dark' | 'light'

export const [themePref, setThemePrefSignal] = createSignal<ThemePref>('system')

const media = window.matchMedia('(prefers-color-scheme: light)')

function resolve(pref: ThemePref): 'dark' | 'light' {
  if (pref === 'system') return media.matches ? 'light' : 'dark'
  return pref
}

function apply(pref: ThemePref) {
  document.documentElement.dataset.theme = resolve(pref)
}

// While the preference is "system", follow live OS appearance changes.
media.addEventListener('change', () => {
  if (themePref() === 'system') apply('system')
})

/** Loads the persisted preference and applies it. Called once on app mount. */
export async function initTheme(): Promise<void> {
  try {
    const settings = await wails.GetSettings()
    const pref = (settings?.theme as ThemePref) || 'system'
    setThemePrefSignal(pref)
    apply(pref)
  } catch {
    apply('system')
  }
}

/** Applies + persists a new theme preference. */
export function setTheme(pref: ThemePref): void {
  setThemePrefSignal(pref)
  apply(pref)
  // Read-modify-write the WHOLE settings object: UpdateSettings overwrites
  // the file, so persisting {theme} alone would clobber sidebarMode/MCP
  // preferences (it did — caught live when a theme switch wiped the
  // sidebar-mode toggle). Routed through store.ts's mutateSettings so this
  // read-modify-write is serialized with setSidebarMode's — two quick toggles
  // can no longer interleave and clobber each other's field.
  void mutateSettings((s) => models.AppSettings.createFrom({ ...s, theme: pref }))
}
