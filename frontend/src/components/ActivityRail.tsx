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
  setSettingsOpen,
  type ExplorerTab,
} from '../lib/store'
import { createRequest } from '../lib/data'
import Tooltip from './Tooltip'
import { IconCollection, IconCookie, IconGitBranch, IconHistory, IconImport, IconPlug, IconPlus, IconSearch, IconSettings } from './icons'

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
          <Show when={props.active}>
            <span class="absolute bottom-1.5 left-0 top-1.5 w-0.5 rounded-r bg-accent" />
          </Show>
          <button
            class="flex h-9 w-9 items-center justify-center rounded-md"
            classList={{
              'bg-raised text-ink': props.active,
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
    <div class="flex h-full w-12 shrink-0 flex-col items-center gap-0.5 border-r border-edge bg-surface py-2">
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

      <div class="my-1 h-px w-6 shrink-0 bg-edge" />

      <RailButton label="New request (⌘N)" onClick={() => void createRequest()}>
        <IconPlus size={17} />
      </RailButton>

      <RailButton label="Import cURL / OpenAPI / Postman" onClick={() => setImportModalOpen(true)}>
        <IconImport size={17} />
      </RailButton>

      <div class="flex-1" />

      <RailButton label="Settings (⌘,)" onClick={() => setSettingsOpen(true)}>
        <IconSettings size={17} />
      </RailButton>
    </div>
  )
}
