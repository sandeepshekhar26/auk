import { For, Show, createSignal } from 'solid-js'
import { createStore } from 'solid-js/store'
import { appState, setExplorerOpen, setFolderRunView, setMcpToolView } from '../lib/store'
import { createMcpConnection, deleteMcpConnection } from '../lib/data'
import { wails } from '../lib/wails'
import type { McpConnection, McpToolInfo, McpTransportKind } from '../types'

// Per-connection LIVE state (connected/tools/loading/error) is deliberately
// NOT persisted or stored in appState — it's a runtime fact about whether
// this session currently has an open MCP client session, not workspace
// data. Keyed by connection id since multiple connections can be tracked
// at once.
interface ConnState {
  connected: boolean
  loading: boolean
  error: string | null
  tools: McpToolInfo[]
}

const [connStates, setConnStates] = createStore<Record<string, ConnState>>({})

function connState(id: string): ConnState {
  return connStates[id] ?? { connected: false, loading: false, error: null, tools: [] }
}

async function connect(id: string) {
  setConnStates(id, { connected: connState(id).connected, loading: true, error: null, tools: connState(id).tools })
  try {
    const tools = await wails.McpConnect(id)
    setConnStates(id, { connected: true, loading: false, error: null, tools: (tools ?? []) as unknown as McpToolInfo[] })
  } catch (err) {
    setConnStates(id, { connected: false, loading: false, error: err instanceof Error ? err.message : String(err), tools: [] })
  }
}

async function refreshTools(id: string) {
  try {
    const tools = await wails.McpListTools(id)
    setConnStates(id, 'tools', (tools ?? []) as unknown as McpToolInfo[])
  } catch (err) {
    setConnStates(id, { connected: false, loading: false, error: err instanceof Error ? err.message : String(err), tools: [] })
  }
}

function disconnect(id: string) {
  void wails.McpDisconnect(id)
  setConnStates(id, { connected: false, loading: false, error: null, tools: [] })
}

function openTool(connectionId: string, toolName: string) {
  setMcpToolView({ connectionId, toolName })
  setFolderRunView(null)
  setExplorerOpen(false)
}

function AddConnectionForm(props: { onDone: () => void }) {
  const [name, setName] = createSignal('')
  const [transport, setTransport] = createSignal<McpTransportKind>('stdio')
  const [commandLine, setCommandLine] = createSignal('')
  const [url, setUrl] = createSignal('')
  const [bearerToken, setBearerToken] = createSignal('')
  const [saving, setSaving] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  async function save() {
    if (!name().trim()) {
      setError('Name is required.')
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (transport() === 'stdio') {
        const parts = commandLine().trim().split(/\s+/).filter(Boolean)
        if (parts.length === 0) {
          setError('Command is required.')
          setSaving(false)
          return
        }
        await createMcpConnection({ name: name().trim(), transport: 'stdio', command: parts[0], args: parts.slice(1) })
      } else {
        if (!url().trim()) {
          setError('URL is required.')
          setSaving(false)
          return
        }
        await createMcpConnection({ name: name().trim(), transport: 'http', url: url().trim(), bearerToken: bearerToken().trim() || undefined })
      }
      props.onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div class="flex flex-col gap-1.5 rounded border border-edge-soft p-2">
      <input
        class="w-full rounded bg-field px-2 py-1 text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
        placeholder="Connection name (e.g. My dev server)"
        value={name()}
        onInput={(e) => setName(e.currentTarget.value)}
      />
      <div class="flex gap-1 rounded bg-field p-0.5">
        <button
          class="flex-1 rounded px-2 py-1 text-[11px]"
          classList={{ 'bg-elevated text-ink': transport() === 'stdio', 'text-ink-muted hover:text-ink-dim': transport() !== 'stdio' }}
          onClick={() => setTransport('stdio')}
        >
          Stdio (command)
        </button>
        <button
          class="flex-1 rounded px-2 py-1 text-[11px]"
          classList={{ 'bg-elevated text-ink': transport() === 'http', 'text-ink-muted hover:text-ink-dim': transport() !== 'http' }}
          onClick={() => setTransport('http')}
        >
          HTTP (URL)
        </button>
      </div>

      <Show when={transport() === 'stdio'}>
        <input
          class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
          placeholder="node server.js --verbose"
          value={commandLine()}
          onInput={(e) => setCommandLine(e.currentTarget.value)}
        />
      </Show>
      <Show when={transport() === 'http'}>
        <input
          class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
          placeholder="http://localhost:8080/mcp"
          value={url()}
          onInput={(e) => setUrl(e.currentTarget.value)}
        />
        <input
          class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
          placeholder="Bearer token (optional)"
          value={bearerToken()}
          onInput={(e) => setBearerToken(e.currentTarget.value)}
        />
      </Show>

      <Show when={error()}>
        <p class="text-[11px] text-danger">{error()}</p>
      </Show>

      <div class="flex gap-1.5">
        <button
          class="rounded bg-accent px-2 py-1 text-[11px] font-medium text-accent-contrast hover:bg-accent-hover disabled:opacity-50"
          disabled={saving()}
          onClick={save}
        >
          {saving() ? 'Saving…' : 'Save'}
        </button>
        <button class="rounded px-2 py-1 text-[11px] text-ink-muted hover:bg-raised" onClick={props.onDone}>
          Cancel
        </button>
      </div>
    </div>
  )
}

function annotationBadges(tool: McpToolInfo) {
  const badges: { label: string; classes: string }[] = []
  if (tool.readOnlyHint) badges.push({ label: 'read-only', classes: 'text-accent-fg' })
  if (tool.destructiveHint) badges.push({ label: 'destructive', classes: 'text-danger' })
  if (tool.idempotentHint) badges.push({ label: 'idempotent', classes: 'text-info' })
  return badges
}

function ConnectionRow(props: { conn: McpConnection }) {
  const state = () => connState(props.conn.id)

  return (
    <div class="rounded border border-edge-soft p-2">
      <div class="flex items-center gap-2">
        <span class="flex-1 truncate text-xs font-medium text-ink-dim">{props.conn.name}</span>
        <span class="rounded bg-field px-1.5 py-0.5 text-[10px] uppercase text-ink-faint">{props.conn.transport}</span>
        <Show when={!state().connected}>
          <button
            class="rounded bg-accent px-2 py-0.5 text-[11px] font-medium text-accent-contrast hover:bg-accent-hover disabled:opacity-50"
            disabled={state().loading}
            onClick={() => void connect(props.conn.id)}
          >
            {state().loading ? '…' : 'Connect'}
          </button>
        </Show>
        <Show when={state().connected}>
          <button class="rounded bg-field px-2 py-0.5 text-[11px] text-ink-dim hover:bg-raised" onClick={() => disconnect(props.conn.id)}>
            Disconnect
          </button>
        </Show>
        <button
          class="rounded px-1.5 py-0.5 text-[11px] text-ink-faint hover:bg-raised hover:text-danger"
          title="Remove connection"
          onClick={() => {
            disconnect(props.conn.id)
            void deleteMcpConnection(props.conn.id)
          }}
        >
          ×
        </button>
      </div>

      <Show when={state().error}>
        <p class="mt-1.5 rounded border border-danger-edge bg-danger-bg/40 px-2 py-1 text-[11px] text-danger">{state().error}</p>
      </Show>

      <Show when={state().connected}>
        <div class="mt-1.5 flex items-center justify-between">
          <span class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
            {state().tools.length} tool{state().tools.length === 1 ? '' : 's'}
          </span>
          <button class="text-[11px] text-ink-faint hover:text-ink-dim" onClick={() => void refreshTools(props.conn.id)}>
            Refresh
          </button>
        </div>
        <div class="mt-1 flex flex-col gap-0.5">
          <For each={state().tools}>
            {(tool) => (
              <button
                class="flex flex-col items-start gap-0.5 rounded px-2 py-1 text-left hover:bg-raised"
                onClick={() => openTool(props.conn.id, tool.name)}
              >
                <div class="flex w-full items-center gap-1.5">
                  <span class="truncate font-mono text-xs text-ink-dim">{tool.title || tool.name}</span>
                  <For each={annotationBadges(tool)}>
                    {(b) => <span class={`text-[9px] font-semibold uppercase ${b.classes}`}>{b.label}</span>}
                  </For>
                </div>
                <span class="line-clamp-1 text-[11px] text-ink-faint">{tool.description}</span>
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

// McpPanel is the "debug your own MCP server" surface: configure a
// connection (stdio subprocess or Streamable-HTTP URL), connect, see
// published tools with descriptions + safety-hint badges, and select one
// to test — which opens McpToolView in the main area (see store.ts's
// mcpToolView; this mirrors the app's own request-tree -> editor pattern,
// applied to tools instead of HTTP requests).
export default function McpPanel() {
  const [showAdd, setShowAdd] = createSignal(false)

  return (
    <div class="flex h-full flex-col overflow-y-auto p-2 text-xs">
      <div class="mb-2 flex flex-col gap-1.5">
        <Show when={appState.mcpConnections.length === 0 && !showAdd()}>
          <p class="px-1 py-2 text-ink-faint">No MCP servers configured yet.</p>
        </Show>
        <For each={appState.mcpConnections}>{(conn) => <ConnectionRow conn={conn} />}</For>
      </div>

      <Show when={showAdd()} fallback={
        <button
          class="self-start rounded bg-field px-2 py-1 text-[11px] text-ink-dim hover:bg-raised"
          onClick={() => setShowAdd(true)}
        >
          + Add MCP server
        </button>
      }>
        <AddConnectionForm onDone={() => setShowAdd(false)} />
      </Show>
    </div>
  )
}
