import { For, Show, createMemo, createSignal } from 'solid-js'
import { appState } from '../lib/store'
import { wails } from '../lib/wails'

// The shapes below mirror the standard GraphQL introspection query result
// (internal/protocols/graphql.introspectionQuery) — only the fields this
// read-only explorer actually renders, not the full introspection schema.
interface TypeRef {
  kind: string
  name: string | null
  ofType?: TypeRef | null
}
interface Field {
  name: string
  args?: { name: string; type: TypeRef }[] | null
  type: TypeRef
}
interface TypeDef {
  kind: string
  name: string
  fields?: Field[] | null
  inputFields?: Field[] | null
  enumValues?: { name: string }[] | null
}
interface SchemaData {
  queryType?: { name: string } | null
  mutationType?: { name: string } | null
  subscriptionType?: { name: string } | null
  types?: TypeDef[] | null
}

// Unwraps NON_NULL/LIST wrapper kinds into GraphQL's own shorthand (String!,
// [Post!]!, ...) — the introspection response nests these as `ofType` chains
// rather than a flat string.
function formatTypeRef(t: TypeRef | null | undefined): string {
  if (!t) return '?'
  if (t.kind === 'NON_NULL') return `${formatTypeRef(t.ofType)}!`
  if (t.kind === 'LIST') return `[${formatTypeRef(t.ofType)}]`
  return t.name ?? '?'
}

function TypeRow(props: { type: TypeDef; isRoot: boolean; onCopy: (name: string) => void }) {
  const [open, setOpen] = createSignal(props.isRoot)
  const children = createMemo(() => props.type.fields ?? props.type.inputFields ?? null)
  const enumValues = createMemo(() => props.type.enumValues ?? null)
  const hasChildren = createMemo(() => (children()?.length ?? 0) > 0 || (enumValues()?.length ?? 0) > 0)

  return (
    <div class="border-b border-edge/60 last:border-0">
      <button
        class="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-raised disabled:hover:bg-transparent"
        onClick={() => hasChildren() && setOpen((v) => !v)}
        disabled={!hasChildren()}
      >
        <span class="w-3 shrink-0 text-ink-faint">{hasChildren() ? (open() ? '▾' : '▸') : ''}</span>
        <span class="font-mono font-medium text-ink">{props.type.name}</span>
        <span class="rounded bg-field px-1 text-[9px] uppercase text-ink-faint">{props.type.kind.replace('_', ' ')}</span>
      </button>
      <Show when={open() && hasChildren()}>
        <div class="pb-1 pl-7">
          <For each={children() ?? []}>
            {(f) => (
              <button
                class="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left font-mono text-[11px] text-ink-dim hover:bg-raised hover:text-ink"
                title="Click to copy this field name"
                onClick={() => props.onCopy(f.name)}
              >
                <span>{f.name}</span>
                <Show when={f.args && f.args.length > 0}>
                  <span class="text-ink-faint">({f.args!.map((a) => a.name).join(', ')})</span>
                </Show>
                <span class="text-ink-faint">: {formatTypeRef(f.type)}</span>
              </button>
            )}
          </For>
          <For each={enumValues() ?? []}>
            {(v) => (
              <button
                class="flex w-full items-center rounded px-1 py-0.5 text-left font-mono text-[11px] text-ink-dim hover:bg-raised hover:text-ink"
                title="Click to copy this value"
                onClick={() => props.onCopy(v.name)}
              >
                {v.name}
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

// Read-only GraphQL schema explorer: fetches via introspection
// (App.FetchGraphQLSchema, resolved through the same template+auth path as
// a normal send — environment variables and auth headers apply) and renders
// a collapsible type/field tree. Deliberately NOT autocomplete-while-typing
// in the query editor — no schema-aware CodeMirror completion source exists
// off the shelf, and hand-building one against the full GraphQL spec is a
// separate, much larger project. Click a field name to copy it, then paste
// into the query editor by hand.
export default function GraphQLSchemaPanel(props: { requestId: string }) {
  const [open, setOpen] = createSignal(false)
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [schema, setSchema] = createSignal<SchemaData | null>(null)
  const [copied, setCopied] = createSignal<string | null>(null)
  let copiedTimer: ReturnType<typeof setTimeout> | undefined

  function copyField(name: string) {
    void navigator.clipboard?.writeText(name)
    setCopied(name)
    if (copiedTimer) clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => setCopied(null), 1200)
  }

  async function fetchSchema() {
    setOpen(true)
    setLoading(true)
    setError(null)
    try {
      const raw = await wails.FetchGraphQLSchema(props.requestId, appState.activeEnvironmentId ?? '')
      const parsed = JSON.parse(raw) as { data?: { __schema?: SchemaData }; errors?: { message: string }[] }
      if (parsed.errors && parsed.errors.length > 0) {
        setError(parsed.errors.map((e) => e.message).join('; '))
        setSchema(null)
      } else if (!parsed.data?.__schema) {
        setError('Introspection response had no __schema — this endpoint may have introspection disabled.')
        setSchema(null)
      } else {
        setSchema(parsed.data.__schema)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSchema(null)
    } finally {
      setLoading(false)
    }
  }

  const rootNames = createMemo(() => {
    const s = schema()
    return new Set([s?.queryType?.name, s?.mutationType?.name, s?.subscriptionType?.name].filter(Boolean) as string[])
  })
  // Introspection's own meta-types (__Schema, __Type, __Field, ...) are
  // noise for this explorer — filtered out rather than shown as just more
  // rows the user has to scroll past.
  const types = createMemo(() => (schema()?.types ?? []).filter((t) => !t.name.startsWith('__')))
  const rootTypes = createMemo(() => types().filter((t) => rootNames().has(t.name)))
  const otherTypes = createMemo(() =>
    types()
      .filter((t) => !rootNames().has(t.name))
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name)),
  )

  return (
    <div class="flex shrink-0 flex-col overflow-hidden border-t border-edge" classList={{ 'h-64': open() }}>
      <div class="flex shrink-0 items-center justify-between border-b border-edge px-2 py-1">
        <span class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Schema</span>
        <div class="flex items-center gap-2">
          <Show when={copied()}>{(name) => <span class="text-[10px] text-accent-fg">Copied {name()}</span>}</Show>
          <Show when={open()}>
            <button class="text-[11px] text-ink-faint hover:text-ink-dim" onClick={() => setOpen(false)}>
              Hide
            </button>
          </Show>
          <button
            class="rounded bg-field px-2 py-0.5 text-[11px] text-ink-dim hover:bg-raised disabled:opacity-50"
            disabled={loading()}
            onClick={() => void fetchSchema()}
          >
            {loading() ? 'Fetching…' : 'Fetch schema'}
          </button>
        </div>
      </div>
      <Show when={open()}>
        <div class="flex-1 overflow-y-auto text-xs">
          <Show when={error()}>
            <p class="p-2 text-danger">{error()}</p>
          </Show>
          <Show when={!error() && !loading() && !schema()}>
            <p class="p-2 text-ink-faint">No schema loaded yet.</p>
          </Show>
          <For each={rootTypes()}>{(t) => <TypeRow type={t} isRoot onCopy={copyField} />}</For>
          <For each={otherTypes()}>{(t) => <TypeRow type={t} isRoot={false} onCopy={copyField} />}</For>
        </div>
      </Show>
    </div>
  )
}
