/**
 * The keyboard layer.
 *
 * ONE registry owns every shortcut in AUK. Before this file, shortcuts were
 * declared in whichever component happened to need them — App.tsx owned six,
 * CommandPalette owned ⌘K and ⌘/, ShortcutSheet owned ⌘Enter, Sidebar owned
 * its own arrows — across 32 separate keydown handlers. Three things followed
 * from that, and all three are why "keyboard-first" was not yet true:
 *
 *   1. Nothing could enumerate the shortcuts, so the ShortcutSheet was a
 *      hand-maintained list that drifted from what the app actually did.
 *   2. Nothing could detect a collision; two components could bind the same
 *      chord and both would fire.
 *   3. Anything a component kept in its own local state — the request and
 *      response tabs, most obviously — was simply unreachable from the
 *      keyboard, because no outside code could touch it.
 *
 * So: commands are declared here once, with their chord, their label and the
 * condition under which they apply. The dispatcher, the shortcut sheet and the
 * command palette are all generated from this list. A command with no chord is
 * still a command — it shows up in the palette, searchable by name. That is
 * the actual promise of keyboard-first: not "there are shortcuts", but
 * "everything is reachable without the mouse".
 */

import {
  appState,
  closeTab,
  cycleTab,
  setAppState,
  setCommandPaletteOpen,
  setEditorTab,
  setEnvironmentEditorOpen,
  setExplorerOpen,
  setImportModalOpen,
  setMigrateModalOpen,
  setResponseTab,
  setSettingsOpen,
  setShortcutSheetOpen,
  setStreamConsoleOpen,
  openExplorer,
  shortcutSheetOpen,
  streamConsoleOpen,
  type EditorTab,
  type ResponseTab,
} from './store'
import { createRequest, createFolder, duplicateRequest, runFolder } from './data'
import { exportActiveWorkspace, exportActiveWorkspaceOpenAPI } from './exporters'
import { setTheme } from './theme'

export type CommandGroup = 'Request' | 'Response' | 'Navigation' | 'Workspace' | 'View'

export interface Command {
  id: string
  title: string
  group: CommandGroup
  /**
   * Canonical chord, lowercase, modifiers in a fixed order:
   * `mod` (⌘ on macOS, Ctrl elsewhere), then `alt`, then `shift`, then the key.
   * e.g. `mod+l`, `mod+shift+]`, `mod+1`. Omit for palette-only commands.
   */
  chord?: string
  /** Extra context for the palette row (never the chord — that's rendered). */
  subtitle?: string
  /**
   * Availability. A command whose `when` is false is not dispatched and is
   * hidden from the palette — so "Close tab" doesn't appear with no tabs open.
   */
  when?: () => boolean
  run: () => void
}

/** True when a request is open and focused — most Request commands need this. */
const hasActiveRequest = () => Boolean(appState.activeTabId)

/**
 * Focus the URL field of the active request. Implemented as a DOM query rather
 * than a ref passed down through three components: the field is uniquely
 * identified by a data attribute, and threading a ref from App through
 * RequestEditor for one shortcut would be far more coupling than one selector.
 */
function focusURL() {
  const el = document.querySelector<HTMLInputElement>('[data-auk-url-input]')
  if (!el) return
  el.focus()
  el.select()
}

function focusSidebarFilter() {
  const el = document.querySelector<HTMLInputElement>('[data-auk-sidebar-filter]')
  if (el) {
    el.focus()
    el.select()
  }
}

const editorTabCommand = (n: number, id: EditorTab, label: string): Command => ({
  id: `view:editor-tab:${id}`,
  title: `Go to ${label} tab`,
  group: 'Request',
  chord: `mod+${n}`,
  when: hasActiveRequest,
  run: () => setEditorTab(id),
})

const responseTabCommand = (n: number, id: ResponseTab, label: string): Command => ({
  id: `view:response-tab:${id}`,
  title: `Go to response ${label}`,
  group: 'Response',
  chord: `mod+shift+${n}`,
  when: hasActiveRequest,
  run: () => setResponseTab(id),
})

/**
 * Every command in the app. Order here is the order the shortcut sheet shows
 * within each group.
 */
export const COMMANDS: Command[] = [
  // ---- Request -----------------------------------------------------------
  {
    id: 'request:send',
    title: 'Send request',
    group: 'Request',
    chord: 'mod+enter',
    when: hasActiveRequest,
    // Dispatched as an event rather than called directly: the send path lives
    // in App.tsx, which owns in-flight state and cancellation.
    run: () => window.dispatchEvent(new CustomEvent('apitool:send')),
  },
  {
    id: 'request:new',
    title: 'New request',
    group: 'Request',
    chord: 'mod+n',
    run: () => void createRequest(),
  },
  {
    id: 'request:focus-url',
    title: 'Focus the URL bar',
    group: 'Request',
    // ⌘L is the universal "go to the address bar" chord in every browser; an
    // API client's URL field is the same affordance.
    chord: 'mod+l',
    when: hasActiveRequest,
    run: focusURL,
  },
  {
    id: 'request:duplicate',
    title: 'Duplicate request',
    group: 'Request',
    chord: 'mod+d',
    when: hasActiveRequest,
    run: () => {
      if (appState.activeTabId) void duplicateRequest(appState.activeTabId)
    },
  },
  {
    id: 'request:close-tab',
    title: 'Close request tab',
    group: 'Request',
    chord: 'mod+w',
    when: hasActiveRequest,
    run: () => {
      if (appState.activeTabId) closeTab(appState.activeTabId)
    },
  },
  {
    id: 'request:run-folder',
    title: 'Run the containing folder',
    group: 'Request',
    subtitle: 'assertions, reports, a real exit code',
    chord: 'mod+shift+r',
    // Only when the request actually lives in a folder — a loose request has
    // no suite to run, and a disabled-looking command is worse than absent.
    when: () => {
      const req = appState.requests.find((r) => r.id === appState.activeTabId)
      return Boolean(req?.folderId)
    },
    run: () => {
      // The folder the ACTIVE request sits in — running "this request's suite"
      // is the motion; asking which folder is not.
      const req = appState.requests.find((r) => r.id === appState.activeTabId)
      if (!req?.folderId) return
      const folder = appState.folders.find((f) => f.id === req.folderId)
      void runFolder(req.folderId, folder?.name ?? 'Folder')
    },
  },
  {
    id: 'request:copy-as-curl',
    title: 'Copy request as cURL',
    group: 'Request',
    chord: 'mod+shift+c',
    when: hasActiveRequest,
    run: () => window.dispatchEvent(new CustomEvent('apitool:copy-as-curl')),
  },
  editorTabCommand(1, 'params', 'Params'),
  editorTabCommand(2, 'headers', 'Headers'),
  editorTabCommand(3, 'body', 'Body'),
  editorTabCommand(4, 'auth', 'Auth'),
  editorTabCommand(5, 'script', 'Script'),
  editorTabCommand(6, 'assert', 'Assert'),
  editorTabCommand(7, 'perf', 'Perf'),

  // ---- Response ----------------------------------------------------------
  {
    id: 'response:search',
    title: 'Search in the response body',
    group: 'Response',
    chord: 'mod+f',
    run: () => window.dispatchEvent(new CustomEvent('apitool:search-body')),
  },
  responseTabCommand(1, 'body', 'body'),
  responseTabCommand(2, 'headers', 'headers'),
  responseTabCommand(3, 'timing', 'timing'),

  // ---- Navigation --------------------------------------------------------
  {
    id: 'nav:palette',
    title: 'Command palette',
    group: 'Navigation',
    subtitle: 'every command, searchable',
    chord: 'mod+k',
    run: () => setCommandPaletteOpen(true),
  },
  {
    id: 'nav:toggle-sidebar',
    title: 'Toggle the sidebar',
    group: 'Navigation',
    chord: 'mod+b',
    run: () => setExplorerOpen((v) => !v),
  },
  {
    id: 'nav:filter-requests',
    title: 'Filter the request tree',
    group: 'Navigation',
    chord: 'mod+shift+f',
    run: () => {
      setExplorerOpen(true)
      openExplorer('requests')
      // The panel may be mounting; focus after the frame it appears in.
      requestAnimationFrame(focusSidebarFilter)
    },
  },
  {
    id: 'nav:next-tab',
    title: 'Next request tab',
    group: 'Navigation',
    chord: 'mod+shift+]',
    when: hasActiveRequest,
    run: () => cycleTab(1),
  },
  {
    id: 'nav:prev-tab',
    title: 'Previous request tab',
    group: 'Navigation',
    chord: 'mod+shift+[',
    when: hasActiveRequest,
    run: () => cycleTab(-1),
  },
  { id: 'nav:browse-requests', title: 'Browse requests', group: 'Navigation', run: () => openExplorer('requests') },
  { id: 'nav:browse-history', title: 'Browse history', group: 'Navigation', run: () => openExplorer('history') },
  { id: 'nav:browse-git', title: 'Browse source control', group: 'Navigation', run: () => openExplorer('git') },
  { id: 'nav:browse-mcp', title: 'Browse MCP tools', group: 'Navigation', run: () => openExplorer('mcp') },
  { id: 'nav:browse-cookies', title: 'Browse cookies', group: 'Navigation', run: () => openExplorer('cookies') },

  // ---- Workspace ---------------------------------------------------------
  {
    id: 'workspace:environments',
    title: 'Manage environments',
    group: 'Workspace',
    chord: 'mod+e',
    run: () => setEnvironmentEditorOpen(true),
  },
  { id: 'workspace:new-folder', title: 'New folder', group: 'Workspace', run: () => void createFolder(null) },
  { id: 'workspace:import', title: 'Import…', group: 'Workspace', subtitle: 'Postman, OpenAPI, HAR, Insomnia, Bruno', run: () => setImportModalOpen(true) },
  { id: 'workspace:migrate', title: 'Migrate from Postman…', group: 'Workspace', subtitle: 'collections + environments', run: () => setMigrateModalOpen(true) },
  { id: 'workspace:export', title: 'Export workspace (JSON)…', group: 'Workspace', run: () => void exportActiveWorkspace() },
  { id: 'workspace:export-openapi', title: 'Export as OpenAPI…', group: 'Workspace', run: () => void exportActiveWorkspaceOpenAPI() },

  // ---- View --------------------------------------------------------------
  {
    id: 'view:settings',
    title: 'Open settings',
    group: 'View',
    chord: 'mod+,',
    run: () => setSettingsOpen(true),
  },
  {
    id: 'view:shortcuts',
    title: 'Keyboard shortcuts',
    group: 'View',
    chord: 'mod+/',
    run: () => setShortcutSheetOpen((v) => !v),
  },
  {
    id: 'view:stream-console',
    title: 'Toggle the stream console',
    group: 'View',
    run: () => setStreamConsoleOpen(!streamConsoleOpen()),
  },
  { id: 'view:theme-system', title: 'Theme: System', group: 'View', run: () => void setTheme('system') },
  { id: 'view:theme-light', title: 'Theme: Light', group: 'View', run: () => void setTheme('light') },
  { id: 'view:theme-dark', title: 'Theme: Dark', group: 'View', run: () => void setTheme('dark') },
]

/**
 * Normalise a KeyboardEvent to the canonical chord form. Returns '' for a
 * bare modifier press.
 *
 * `event.key` (not `code`) so the chord follows the user's layout: ⌘/ is
 * whatever key produces '/' on their keyboard, not physical Slash.
 */
export function chordFor(e: KeyboardEvent): string {
  const key = e.key
  if (key === 'Meta' || key === 'Control' || key === 'Shift' || key === 'Alt') return ''

  const parts: string[] = []
  if (e.metaKey || e.ctrlKey) parts.push('mod')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')

  // Enter/Escape/arrows keep their names; everything else lowercases so 'K'
  // (which is what ⌘⇧K reports) matches a chord written as 'mod+shift+k'.
  const named = key.length > 1 ? key.toLowerCase() : key.toLowerCase()
  parts.push(named)
  return parts.join('+')
}

/**
 * Whether a keystroke should be ignored because the user is typing.
 *
 * Single-key shortcuts would be unusable otherwise, and even modified chords
 * must yield a few universal editing combos (⌘A/⌘C/⌘V/⌘X/⌘Z) back to the
 * field — binding ⌘A to anything would break select-all inside the URL bar.
 * ⌘Enter is deliberately NOT yielded: sending from inside the URL field is
 * exactly what a keyboard-first user expects.
 */
export function isTypingTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el) return false
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable === true
}

const EDITING_CHORDS = new Set(['mod+a', 'mod+c', 'mod+v', 'mod+x', 'mod+z', 'mod+shift+z'])

/** Commands that currently apply, in registry order. */
export function availableCommands(): Command[] {
  return COMMANDS.filter((c) => !c.when || c.when())
}

/**
 * Resolve a keystroke to the command that should run, or null.
 *
 * Exported (rather than inlined into the listener) so it can be unit-tested
 * without a DOM: the collision and precedence rules here are the part most
 * likely to break silently.
 */
export function resolveCommand(e: KeyboardEvent): Command | null {
  const chord = chordFor(e)
  if (!chord) return null
  if (!chord.startsWith('mod')) return null // no unmodified global shortcuts
  if (EDITING_CHORDS.has(chord)) return null
  if (isTypingTarget(e.target) && chord === 'mod+f') return null // let fields have find-in-field

  for (const c of COMMANDS) {
    if (c.chord === chord && (!c.when || c.when())) return c
  }
  return null
}

/**
 * Duplicate-chord detector. Called once at startup in dev: a collision means
 * two commands silently compete, and the registry exists precisely so that is
 * findable. Returns the offending chords.
 */
export function duplicateChords(): string[] {
  const seen = new Set<string>()
  const dupes = new Set<string>()
  for (const c of COMMANDS) {
    if (!c.chord) continue
    if (seen.has(c.chord)) dupes.add(c.chord)
    seen.add(c.chord)
  }
  return [...dupes]
}

/** Human-readable key caps for a chord, e.g. 'mod+shift+k' -> ['⌘','⇧','K']. */
export function chordKeys(chord: string): string[] {
  const isMac = typeof navigator !== 'undefined' && navigator.platform.toUpperCase().includes('MAC')
  return chord.split('+').map((part) => {
    switch (part) {
      case 'mod':
        return isMac ? '⌘' : 'Ctrl'
      case 'shift':
        return '⇧'
      case 'alt':
        return isMac ? '⌥' : 'Alt'
      case 'enter':
        return '↵'
      case 'escape':
        return 'Esc'
      default:
        return part.length === 1 ? part.toUpperCase() : part
    }
  })
}

export { shortcutSheetOpen }
