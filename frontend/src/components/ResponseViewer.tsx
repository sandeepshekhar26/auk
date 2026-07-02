import { Show } from 'solid-js'
import type { ResponseData } from '../types'

export default function ResponseViewer(props: { response: ResponseData | null; loading: boolean }) {
  return (
    <div class="flex h-full flex-col border-l border-neutral-800">
      <Show when={!props.loading} fallback={<div class="p-3 text-sm text-neutral-500">Sending…</div>}>
        <Show when={props.response} fallback={<div class="p-3 text-sm text-neutral-600">Response will appear here.</div>}>
          {(res) => (
            <div class="flex h-full flex-col">
              <div class="flex items-center gap-3 border-b border-neutral-800 p-2 text-xs">
                <span
                  class="font-mono font-semibold"
                  classList={{
                    'text-emerald-400': res().status < 300,
                    'text-amber-400': res().status >= 300 && res().status < 400,
                    'text-red-400': res().status >= 400,
                  }}
                >
                  {res().status} {res().statusText}
                </span>
                <span class="text-neutral-500">{res().timingMs}ms</span>
                <span class="text-neutral-500">{res().bodySize}B</span>
              </div>
              <pre class="flex-1 overflow-auto p-3 font-mono text-xs text-neutral-300">{atob(res().bodyBase64 || '')}</pre>
            </div>
          )}
        </Show>
      </Show>
    </div>
  )
}
