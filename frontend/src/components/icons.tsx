import { Show, type JSX } from 'solid-js'

// Hand-rolled inline SVG icons (lucide-style 24px stroke paths) instead of
// pulling in the lucide-solid package: the rail + tree need ~18 icons total,
// and inlining exactly those keeps the bundle lean and self-hosted (this is
// an offline-capable desktop app — same reasoning as the @fontsource fonts
// in main.tsx). All icons inherit `currentColor`, so they recolor through
// the same semantic text-* tokens as everything else.

export interface IconProps {
  size?: number
  class?: string
}

function makeIcon(children: () => JSX.Element) {
  return (props: IconProps) => (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={props.size ?? 16}
      height={props.size ?? 16}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.75"
      stroke-linecap="round"
      stroke-linejoin="round"
      class={props.class}
      aria-hidden="true"
    >
      {children()}
    </svg>
  )
}

export const IconSearch = makeIcon(() => (
  <>
    <circle cx="11" cy="11" r="8" />
    <path d="m21 21-4.3-4.3" />
  </>
))

// list-tree: the "Collections" rail icon (a tree of requests).
export const IconCollection = makeIcon(() => (
  <>
    <path d="M21 12h-8" />
    <path d="M21 6H8" />
    <path d="M21 18h-8" />
    <path d="M3 6v4c0 1.1.9 2 2 2h3" />
    <path d="M3 6v10c0 1.1.9 2 2 2h3" />
  </>
))

export const IconHistory = makeIcon(() => (
  <>
    <path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" />
    <path d="M3 3v5h5" />
    <path d="M12 7v5l4 2" />
  </>
))

export const IconGitBranch = makeIcon(() => (
  <>
    <path d="M6 3v12" />
    <circle cx="18" cy="6" r="3" />
    <circle cx="6" cy="18" r="3" />
    <path d="M18 9a9 9 0 0 1-9 9" />
  </>
))

export const IconPlug = makeIcon(() => (
  <>
    <path d="M12 22v-5" />
    <path d="M9 8V2" />
    <path d="M15 8V2" />
    <path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z" />
  </>
))

export const IconCookie = makeIcon(() => (
  <>
    <path d="M12 2a10 10 0 1 0 10 10 4 4 0 0 1-5-5 4 4 0 0 1-5-5" />
    <path d="M8.5 8.5v.01" />
    <path d="M16 15.5v.01" />
    <path d="M12 12v.01" />
    <path d="M11 17v.01" />
    <path d="M7 14v.01" />
  </>
))

export const IconPlus = makeIcon(() => (
  <>
    <path d="M5 12h14" />
    <path d="M12 5v14" />
  </>
))

export const IconImport = makeIcon(() => (
  <>
    <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
    <path d="m7 10 5 5 5-5" />
    <path d="M12 15V3" />
  </>
))

export const IconSettings = makeIcon(() => (
  <>
    <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
    <circle cx="12" cy="12" r="3" />
  </>
))

export const IconChevronRight = makeIcon(() => <path d="m9 18 6-6-6-6" />)

export const IconFolder = makeIcon(() => (
  <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
))

export const IconFolderOpen = makeIcon(() => (
  <path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2" />
))

export const IconFolderPlus = makeIcon(() => (
  <>
    <path d="M12 10v6" />
    <path d="M9 13h6" />
    <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
  </>
))

export const IconMore = makeIcon(() => (
  <>
    <circle cx="12" cy="12" r="1" />
    <circle cx="19" cy="12" r="1" />
    <circle cx="5" cy="12" r="1" />
  </>
))

export const IconPin = makeIcon(() => (
  <>
    <path d="M12 17v5" />
    <path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z" />
  </>
))

export const IconPinOff = makeIcon(() => (
  <>
    <path d="M12 17v5" />
    <path d="M15 9.34V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H7.89" />
    <path d="m2 2 20 20" />
    <path d="M9 9v1.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h11" />
  </>
))

export const IconX = makeIcon(() => (
  <>
    <path d="M18 6 6 18" />
    <path d="m6 6 12 12" />
  </>
))

export const IconPlay = makeIcon(() => <path d="M6 3l14 9-14 9V3z" />)

export const IconBraces = makeIcon(() => (
  <>
    <path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5c0 1.1.9 2 2 2h1" />
    <path d="M16 21h1a2 2 0 0 0 2-2v-5c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1" />
  </>
))

// ---------------------------------------------------------------------------
// MethodBadge: the color-coded method/protocol label used by the sidebar
// tree, tab bar, and history — one component so all three stay consistent.
// HTTP methods are colored text (fixed hues per method — the single biggest
// tree-legibility win over a monochrome list); non-HTTP protocols render as
// small tinted chips (WS / GQL / SSE / gRPC) so they read as a different
// kind of thing at a glance. Colors are semantic tokens (--color-method-*,
// --color-proto-* in index.css) so both themes tune them independently —
// never hardcoded hex in JSX.
// ---------------------------------------------------------------------------

export const HTTP_METHOD_COLOR: Record<string, string> = {
  GET: 'text-method-get',
  POST: 'text-method-post',
  PUT: 'text-method-put',
  PATCH: 'text-method-patch',
  DELETE: 'text-method-delete',
  HEAD: 'text-method-misc',
  OPTIONS: 'text-method-misc',
}

const PROTOCOL_CHIP: Record<string, { label: string; cls: string }> = {
  websocket: { label: 'WS', cls: 'text-proto-ws bg-proto-ws/15' },
  graphql: { label: 'GQL', cls: 'text-proto-gql bg-proto-gql/15' },
  sse: { label: 'SSE', cls: 'text-proto-sse bg-proto-sse/15' },
  grpc: { label: 'gRPC', cls: 'text-proto-grpc bg-proto-grpc/15' },
}

/** Abbreviates long HTTP methods so the fixed-width badge column never truncates. */
function methodLabel(method: string): string {
  const m = (method || 'GET').toUpperCase()
  if (m === 'DELETE') return 'DEL'
  if (m === 'OPTIONS') return 'OPT'
  return m
}

export function MethodBadge(props: { method: string; protocol?: string; class?: string }) {
  const chip = () => (props.protocol && props.protocol !== 'http' ? PROTOCOL_CHIP[props.protocol] : undefined)
  return (
    <Show
      when={chip()}
      fallback={
        <span
          class={`shrink-0 font-mono text-[10px] font-semibold leading-4 ${
            HTTP_METHOD_COLOR[(props.method || 'GET').toUpperCase()] ?? 'text-method-misc'
          } ${props.class ?? ''}`}
        >
          {methodLabel(props.method)}
        </span>
      }
    >
      {(c) => (
        <span class={`shrink-0 rounded-sm px-1 font-mono text-[10px] font-semibold leading-4 ${c().cls} ${props.class ?? ''}`}>
          {c().label}
        </span>
      )}
    </Show>
  )
}
