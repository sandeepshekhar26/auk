// Theme controller. The actual colors live in src/index.css as CSS
// variables keyed off <html data-theme="...">; this module just decides
// which value that attribute gets and persists the preference via the Go
// settings binding (~/.apitool/settings.yaml).
import { createSignal } from 'solid-js'
import { wails } from './wails'
import { models } from './wails'

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
  void wails.UpdateSettings(models.AppSettings.createFrom({ theme: pref })).catch(() => {})
}
