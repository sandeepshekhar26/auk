import { appState, setAppState } from '../lib/store'
import type { RequestBody } from '../types'

// GraphQL request editor: a query pane + a variables (JSON) pane, shown in the
// Body tab when the protocol is GraphQL. Both bind to the same RequestBody the
// engine's graphql handler reads — the query is Body.text, the variables are
// Body.graphqlVariables (validated as JSON server-side on send). Kept separate
// from the generic BodyEditor because GraphQL has no use for the body-kind
// selector (its wire shape is always a {query, variables} JSON envelope).
export default function GraphQLEditor(props: { requestIndex: number }) {
  const req = () => appState.requests[props.requestIndex]
  const body = (): RequestBody => req()?.body ?? { kind: 'graphql', text: '', formFields: [] }

  // Both setters normalize the whole body (coalescing null → [] / '') so a
  // partially-populated body from Go's omitempty can't throw in the store
  // updater — the same null-vs-undefined trap documented in RequestEditor.
  function setQuery(text: string) {
    setAppState('requests', props.requestIndex, 'body', (prev) => ({
      kind: 'graphql' as const,
      text,
      formFields: prev?.formFields ?? [],
      graphqlVariables: prev?.graphqlVariables ?? '',
    }))
  }

  function setVariables(graphqlVariables: string) {
    setAppState('requests', props.requestIndex, 'body', (prev) => ({
      kind: 'graphql' as const,
      text: prev?.text ?? '',
      formFields: prev?.formFields ?? [],
      graphqlVariables,
    }))
  }

  return (
    <div class="flex h-full flex-col">
      <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
        <span class="border-b border-edge px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
          Query
        </span>
        <textarea
          class="min-h-[120px] flex-1 resize-none bg-transparent p-3 font-mono text-xs text-ink focus:outline-none"
          placeholder="query { ... }"
          spellcheck={false}
          value={body().text ?? ''}
          onInput={(e) => setQuery(e.currentTarget.value)}
        />
      </div>
      <div class="flex h-40 flex-col overflow-hidden border-t border-edge">
        <span class="border-b border-edge px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-ink-faint">
          Variables (JSON)
        </span>
        <textarea
          class="flex-1 resize-none bg-transparent p-3 font-mono text-xs text-ink focus:outline-none"
          placeholder={'{ "id": 1 }'}
          spellcheck={false}
          value={body().graphqlVariables ?? ''}
          onInput={(e) => setVariables(e.currentTarget.value)}
        />
      </div>
    </div>
  )
}
