import { For, Show, createMemo } from 'solid-js'

// Renders a form generated from a (simple, flat) JSON Schema object,
// mapping the common types the way every mature JSON-schema-form library
// converges on: string(enum)->select, string->text, number/integer->number
// input, boolean->checkbox, anything else (array/object/oneOf/etc.)->a raw
// JSON textarea for just that one field. This is deliberately NOT a full
// recursive form builder — nested schemas fall back to raw JSON per field,
// which every reference implementation (MCP Inspector included) also does
// rather than trying to perfectly render arbitrarily deep schemas.
//
// Returns null (via canRenderForm below) when the schema isn't a flat
// object schema at all — the caller falls back to one raw-JSON textarea
// for the whole arguments payload in that case.

type JSONSchema = Record<string, any>

export function canRenderForm(schema: unknown): schema is JSONSchema {
  if (!schema || typeof schema !== 'object') return false
  const s = schema as JSONSchema
  return s.type === 'object' && typeof s.properties === 'object' && s.properties !== null
}

// The number <input>'s onInput keeps the RAW STRING the user typed (see
// below for why) rather than a live Number, so a number/integer-typed
// field's value in `formValues` is a string while being edited. Call this
// once, at submission time (not on every keystroke), to convert those
// strings to real JSON numbers before building the tool-call arguments —
// the target schema expects a number, not "5".
export function coerceNumberFields(schema: JSONSchema, values: Record<string, unknown>): Record<string, unknown> {
  const properties = (schema.properties ?? {}) as Record<string, JSONSchema>
  const out = { ...values }
  for (const [key, prop] of Object.entries(properties)) {
    if (prop.type !== 'number' && prop.type !== 'integer') continue
    const v = out[key]
    if (typeof v !== 'string') continue
    if (v.trim() === '') {
      delete out[key]
      continue
    }
    const n = Number(v)
    if (!Number.isNaN(n)) out[key] = n
  }
  return out
}

function fieldKind(prop: JSONSchema): 'select' | 'text' | 'number' | 'boolean' | 'json' {
  if (Array.isArray(prop.enum)) return 'select'
  switch (prop.type) {
    case 'string':
      return 'text'
    case 'number':
    case 'integer':
      return 'number'
    case 'boolean':
      return 'boolean'
    default:
      return 'json'
  }
}

export default function SchemaForm(props: {
  schema: JSONSchema
  value: Record<string, unknown>
  // Per-key, not "here's a whole new object" — the caller (McpToolView)
  // backs `value` with a Solid store and writes through its fine-grained
  // setter, so editing one field never invalidates another field's
  // reactive binding. See the comment on McpToolView's formValues store
  // for the sibling-field-wipe bug this fixes.
  onFieldChange: (key: string, value: unknown) => void
}) {
  const properties = createMemo(() => Object.entries(props.schema.properties as Record<string, JSONSchema>))
  const required = createMemo(() => new Set<string>(Array.isArray(props.schema.required) ? props.schema.required : []))

  function set(key: string, value: unknown) {
    props.onFieldChange(key, value)
  }

  function currentOrDefault(key: string, prop: JSONSchema) {
    const v = props.value[key]
    return v !== undefined ? v : prop.default
  }

  return (
    <div class="flex flex-col gap-2.5">
      <For each={properties()}>
        {([key, prop]) => {
          const kind = fieldKind(prop)
          const label = (
            <label class="flex items-center gap-1 text-[11px] font-medium text-ink-dim">
              <span class="font-mono">{key}</span>
              <Show when={required().has(key)}>
                <span class="text-danger">*</span>
              </Show>
              <Show when={prop.description}>
                <span class="font-normal text-ink-faint">— {prop.description}</span>
              </Show>
            </label>
          )

          return (
            <div class="flex flex-col gap-1">
              {label}
              <Show when={kind === 'text'}>
                <input
                  class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  value={(currentOrDefault(key, prop) as string) ?? ''}
                  placeholder={prop.examples?.[0] ?? ''}
                  onInput={(e) => set(key, e.currentTarget.value)}
                />
              </Show>
              <Show when={kind === 'number'}>
                <input
                  type="text"
                  inputmode={prop.type === 'integer' ? 'numeric' : 'decimal'}
                  class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  // type="text" (not "number") is deliberate: a
                  // type="number" input's value SETTER sanitizes on every
                  // assignment (not just user typing) — assigning it the
                  // literal string "-" (a valid, in-progress prefix of a
                  // negative number) is silently rejected back to "" by the
                  // browser itself, per the HTML spec's value-sanitization
                  // algorithm, because "-" alone has no digits yet. Since
                  // this is a CONTROLLED input (value= reasserted on every
                  // render from formValues), that sanitization fires on our
                  // own reflected re-render, not just raw typing — making it
                  // permanently impossible to type a leading "-" (or a
                  // trailing "." while typing a decimal) with type="number".
                  // inputmode gives the same numeric keyboard on mobile
                  // without any of that spec-mandated interference; the
                  // string is coerced to a real JSON number at submit time
                  // by coerceNumberFields, not on every keystroke.
                  value={(currentOrDefault(key, prop) as string | number) ?? ''}
                  onInput={(e) => set(key, e.currentTarget.value)}
                />
              </Show>
              <Show when={kind === 'boolean'}>
                <label class="flex items-center gap-1.5 text-xs text-ink-dim">
                  <input
                    type="checkbox"
                    class="h-3.5 w-3.5 accent-accent"
                    checked={Boolean(currentOrDefault(key, prop))}
                    onChange={(e) => set(key, e.currentTarget.checked)}
                  />
                  {Boolean(currentOrDefault(key, prop)) ? 'true' : 'false'}
                </label>
              </Show>
              <Show when={kind === 'select'}>
                <select
                  class="w-full rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  value={(currentOrDefault(key, prop) as string) ?? ''}
                  onChange={(e) => set(key, e.currentTarget.value)}
                >
                  <option value="" disabled>
                    — select —
                  </option>
                  <For each={prop.enum as unknown[]}>{(opt) => <option value={String(opt)}>{String(opt)}</option>}</For>
                </select>
              </Show>
              <Show when={kind === 'json'}>
                <textarea
                  class="h-16 w-full resize-y rounded bg-field p-1.5 font-mono text-[11px] text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  placeholder={`${prop.type ?? 'value'} as JSON`}
                  value={props.value[key] !== undefined ? JSON.stringify(props.value[key]) : ''}
                  onInput={(e) => {
                    const text = e.currentTarget.value
                    if (text.trim() === '') {
                      set(key, undefined)
                      return
                    }
                    try {
                      set(key, JSON.parse(text))
                    } catch {
                      // Leave the previous value in props.value until the JSON
                      // becomes valid again — don't corrupt state on every
                      // keystroke of a multi-character edit.
                    }
                  }}
                />
              </Show>
            </div>
          )
        }}
      </For>
    </div>
  )
}
