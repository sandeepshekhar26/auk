import { Show, createMemo, type JSX } from 'solid-js'
import {
  appState,
  commandPaletteOpen,
  setCommandPaletteOpen,
  explorerOpen,
  explorerTab,
  openExplorer,
  setExplorerOpen,
  setImportModalOpen,
  setMigrateModalOpen,
  setSettingsOpen,
  type ExplorerTab,
} from '../lib/store'
import { createRequest } from '../lib/data'
import Tooltip from './Tooltip'
import {
  IconCollection,
  IconCookie,
  IconFolderPlus,
  IconGitBranch,
  IconHistory,
  IconImport,
  IconPlug,
  IconPlus,
  IconSearch,
  IconSettings,
} from './icons'

// ActivityRail: the slim 48px icon strip that owns SECTION SWITCHING for the
// sidebar — each section icon shows that section in the sidebar (docked
// panel by default, overlay drawer in overlay mode; see Sidebar.tsx and
// docs/05-ux-north-star.md "Navigation model"). Clicking the already-active
// section collapses/closes the sidebar, VSCode-style. Icons are real SVG
// strokes (components/icons.tsx), not unicode glyphs; the active section
// gets a 2px accent bar on the rail edge plus a filled background.
export default function ActivityRail() {
  const workspaceInitial = createMemo(() => {
    const ws = appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)
    return (ws?.name ?? '?').trim().slice(0, 1).toUpperCase()
  })
  const workspaceName = createMemo(() => appState.workspaces.find((w) => w.id === appState.activeWorkspaceId)?.name ?? 'Workspace')

  function RailButton(props: { label: string; active?: boolean; onClick: () => void; children: JSX.Element }) {
    return (
      <Tooltip text={props.label}>
        <div class="relative flex w-12 justify-center py-0.5">
          {/* The active item is a filled accent chip. It replaced a 2px bar
              hugging the window edge, which was easy to miss and looked like a
              rendering artefact once the rail lost its own background. */}
          <button
            class="flex h-9 w-9 items-center justify-center rounded-[10px] transition-colors"
            classList={{
              'bg-accent/10 text-accent-fg': props.active,
              'text-ink-muted hover:bg-raised hover:text-ink-dim': !props.active,
            }}
            title={props.label}
            onClick={props.onClick}
          >
            {props.children}
          </button>
        </div>
      </Tooltip>
    )
  }

  // A section icon opens its section in the sidebar; clicking the section
  // that's already showing collapses/closes the sidebar instead.
  function SectionButton(props: { tab: ExplorerTab; label: string; children: JSX.Element }) {
    const active = () => explorerOpen() && explorerTab() === props.tab
    return (
      <RailButton label={props.label} active={active()} onClick={() => (active() ? setExplorerOpen(false) : openExplorer(props.tab))}>
        {props.children}
      </RailButton>
    )
  }

  return (
    <div class="flex h-full w-12 shrink-0 flex-col items-center gap-0.5 py-1">
      <Tooltip text={workspaceName()}>
        <div
          class="mb-1 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-accent text-xs font-bold text-accent-contrast"
          title={workspaceName()}
        >
          {workspaceInitial()}
        </div>
      </Tooltip>

      <RailButton label="Command palette (⌘K)" active={commandPaletteOpen()} onClick={() => setCommandPaletteOpen(true)}>
        <IconSearch size={17} />
      </RailButton>

      <SectionButton tab="requests" label="Requests (⌘B)">
        <IconCollection size={17} />
      </SectionButton>

      <SectionButton tab="history" label="History">
        <IconHistory size={17} />
      </SectionButton>

      <SectionButton tab="git" label="Git">
        <IconGitBranch size={17} />
      </SectionButton>

      <SectionButton tab="mcp" label="MCP tool debugger">
        <IconPlug size={17} />
      </SectionButton>

      <SectionButton tab="cookies" label="Cookies">
        <IconCookie size={17} />
      </SectionButton>

      <div class="my-1.5 h-px w-5 shrink-0 bg-edge" />

      <RailButton label="New request (⌘N)" onClick={() => void createRequest()}>
        <IconPlus size={17} />
      </RailButton>

      <RailButton label="Import cURL, OpenAPI, Postman, Insomnia, Bruno, HAR" onClick={() => setImportModalOpen(true)}>
        <IconImport size={17} />
      </RailButton>

      {/* Migration is a bigger job than Import (many files -> one workspace,
          plus a report), so it gets its own rail entry rather than hiding
          inside the Import modal — it's the first thing a Postman switcher
          looks for. Distinct icon for the same reason: two download arrows
          side by side would be unreadable at 17px. */}
      <RailButton label="Migrate from Postman" onClick={() => setMigrateModalOpen(true)}>
        <IconFolderPlus size={17} />
      </RailButton>

      <div class="flex-1" />

      <RailButton label="Settings (⌘,)" onClick={() => setSettingsOpen(true)}>
        <IconSettings size={17} />
      </RailButton>
    </div>
  )
}
