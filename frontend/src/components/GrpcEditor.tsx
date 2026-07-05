import { appState, setAppState } from '../lib/store'
import type { KeyValue, RequestBody } from '../types'

// The engine's gRPC handler addresses the target method via a resolved header
// (see internal/protocols/grpc MethodHeader) and reads the request message as
// JSON from the body. This editor is the minimal UI for that: a fully-qualified
// method field (stored as that header) + a JSON message pane. Method discovery
// happens server-side via reflection, so no .proto is needed here; a
// reflection-powered method browser is a future enhancement.
export const METHOD_HEADER = 'x-grpc-method'

export default function GrpcEditor(props: { requestIndex: number }) {
  const req = () => appState.requests[props.requestIndex]
  const method = () => (req()?.headers ?? []).find((h) => h.key.toLowerCase() === METHOD_HEADER)?.value ?? ''
  const body = (): RequestBody => req()?.body ?? { kind: 'json', text: '', formFields: [] }

  // The method lives in the request headers (the handler's routing convention),
  // upserted here so the dedicated field and the Headers tab stay consistent.
  function setMethod(value: string) {
    setAppState('requests', props.requestIndex, 'headers', (headers: KeyValue[] | null | undefined) => {
      const list = headers ?? []
      const idx = list.findIndex((h) => h.key.toLowerCase() === METHOD_HEADER)
      if (idx >= 0) {
        const next = list.slice()
        next[idx] = { ...next[idx], value, enabled: true }
        return next
      }
      return [...list, { key: METHOD_HEADER, value, enabled: true }]
    })
  }

  function setMessage(text: string) {
    setAppState('requests', props.requestIndex, 'body', (prev) => ({
      kind: 'json' as const,
      text,
      formFields: prev?.formFields ?? [],
    }))
  }

  return (
    <div class="flex h-full flex-col">
      <div class="flex items-center gap-2 border-b border-edge px-2 py-1.5">
        <span class="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">Method</span>
        <input
          class="flex-1 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
          placeholder="package.Service/Method"
          spellcheck={false}
          value={method()}
          onInput={(e) => setMethod(e.currentTarget.value)}
        />
      </div>
      <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
        <span class="border-b border-edge px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
          Request message (JSON)
        </span>
        <textarea
          class="min-h-[120px] flex-1 resize-none bg-transparent p-3 font-mono text-xs text-ink focus:outline-none"
          placeholder={'{ }'}
          spellcheck={false}
          value={body().text ?? ''}
          onInput={(e) => setMessage(e.currentTarget.value)}
        />
      </div>
    </div>
  )
}
