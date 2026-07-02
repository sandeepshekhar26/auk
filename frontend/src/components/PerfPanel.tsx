import { For, Show, createMemo, createSignal, onCleanup } from 'solid-js'
import { appState, setAppState } from '../lib/store'
import { wails, events } from '../lib/wails'
import PerfChart from './PerfChart'
import type { PerfConfig, PerfResult, PerfSamplePoint, PerfThreshold } from '../types'

const DEFAULT_CONFIG: PerfConfig = {
  executor: 'constant-vus',
  vus: 10,
  duration: '30s',
  thresholds: [
    { metric: 'http_req_duration', expression: 'p(95)<500' },
    { metric: 'http_req_failed', expression: 'rate<0.01' },
  ],
}

const COMMON_THRESHOLDS = ['http_req_duration', 'http_req_failed', 'http_reqs', 'iteration_duration']

export default function PerfPanel(props: { requestIndex: number }) {
  const [samples, setSamples] = createSignal<PerfSamplePoint[]>([])
  const [result, setResult] = createSignal<PerfResult | null>(null)
  const [running, setRunning] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [k6Missing, setK6Missing] = createSignal<string | null>(null)

  const req = createMemo(() => appState.requests[props.requestIndex])
  const cfg = createMemo<PerfConfig>(() => req()?.perf ?? DEFAULT_CONFIG)

  // Every field edit writes back onto the request's perf config in the store;
  // RequestEditor's debounced save then persists it to the YAML file, so a
  // load test is versioned with its request.
  function patch(p: Partial<PerfConfig>) {
    setAppState('requests', props.requestIndex, 'perf', (prev: PerfConfig | null | undefined) => ({
      ...(prev ?? DEFAULT_CONFIG),
      ...p,
    }))
  }

  function patchThreshold(i: number, p: Partial<PerfThreshold>) {
    const next = [...(cfg().thresholds ?? [])]
    next[i] = { ...next[i], ...p }
    patch({ thresholds: next })
  }
  function addThreshold() {
    patch({ thresholds: [...(cfg().thresholds ?? []), { metric: 'http_req_duration', expression: 'p(95)<500' }] })
  }
  function removeThreshold(i: number) {
    patch({ thresholds: (cfg().thresholds ?? []).filter((_, idx) => idx !== i) })
  }

  async function run() {
    const r = req()
    if (!r) return
    setError(null)
    setResult(null)
    setSamples([])

    const missing = await wails.CheckK6()
    if (missing) {
      setK6Missing(missing)
      return
    }
    setK6Missing(null)
    setRunning(true)

    // Live samples arrive on perf:sample:<requestId>; each is a JSON string.
    const eventName = `perf:sample:${r.id}`
    const off = events.EventsOn(eventName, (payload: string) => {
      try {
        setSamples((prev) => [...prev, JSON.parse(payload) as PerfSamplePoint])
      } catch {
        /* ignore malformed */
      }
    })

    try {
      const res = await wails.RunPerfTest(r.id, appState.activeEnvironmentId ?? '', cfg() as any)
      setResult(res as unknown as PerfResult)
      if (res.error) setError(res.error)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      off()
      setRunning(false)
    }
  }

  function stop() {
    const r = req()
    if (r) void wails.StopPerfTest(r.id)
  }

  onCleanup(() => {
    const r = req()
    if (r && running()) void wails.StopPerfTest(r.id)
  })

  return (
    <div class="flex h-full flex-col gap-3 overflow-y-auto p-3 text-sm">
      {/* config row */}
      <div class="flex flex-wrap items-end gap-3">
        <label class="flex flex-col gap-1">
          <span class="text-xs text-ink-muted">Executor</span>
          <select
            class="rounded bg-field px-2 py-1 text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
            value={cfg().executor}
            onChange={(e) => patch({ executor: e.currentTarget.value as PerfConfig['executor'] })}
          >
            <option value="constant-vus">Constant VUs</option>
            <option value="ramping-vus">Ramping VUs</option>
          </select>
        </label>

        <Show when={cfg().executor === 'constant-vus'}>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-ink-muted">Virtual users</span>
            <input
              type="number"
              min="1"
              class="w-24 rounded bg-field px-2 py-1 text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={cfg().vus ?? 10}
              onInput={(e) => patch({ vus: Math.max(1, Number(e.currentTarget.value) || 1) })}
            />
          </label>
          <label class="flex flex-col gap-1">
            <span class="text-xs text-ink-muted">Duration</span>
            <input
              class="w-24 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={cfg().duration ?? '30s'}
              placeholder="30s"
              onInput={(e) => patch({ duration: e.currentTarget.value })}
            />
          </label>
        </Show>

        <Show when={cfg().executor === 'ramping-vus'}>
          <RampingStages requestIndex={props.requestIndex} />
        </Show>

        <div class="ml-auto flex items-center gap-2">
          <Show when={running()}>
            <span class="flex items-center gap-1.5 text-xs text-ink-muted">
              <span class="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" /> running…
            </span>
          </Show>
          <Show
            when={!running()}
            fallback={
              <button class="rounded bg-danger px-3 py-1.5 text-xs font-medium text-accent-contrast hover:opacity-90" onClick={stop}>
                Stop
              </button>
            }
          >
            <button class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover" onClick={run}>
              Run load test
            </button>
          </Show>
        </div>
      </div>

      {/* thresholds */}
      <div>
        <div class="mb-1 flex items-center gap-2">
          <span class="text-xs font-medium uppercase tracking-wide text-ink-muted">SLA thresholds</span>
          <button class="text-xs text-accent-fg hover:underline" onClick={addThreshold}>
            + add
          </button>
        </div>
        <div class="flex flex-col gap-1">
          <For each={cfg().thresholds ?? []} fallback={<p class="text-xs text-ink-faint">No thresholds — the run always passes.</p>}>
            {(t, i) => (
              <div class="flex items-center gap-2">
                <input
                  list="perf-metrics"
                  class="w-52 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  value={t.metric}
                  onInput={(e) => patchThreshold(i(), { metric: e.currentTarget.value })}
                />
                <input
                  class="w-40 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
                  value={t.expression}
                  placeholder="p(95)<500"
                  onInput={(e) => patchThreshold(i(), { expression: e.currentTarget.value })}
                />
                <button class="rounded px-1.5 py-1 text-xs text-ink-faint hover:bg-raised hover:text-danger" onClick={() => removeThreshold(i())}>
                  ×
                </button>
              </div>
            )}
          </For>
          <datalist id="perf-metrics">
            <For each={COMMON_THRESHOLDS}>{(m) => <option value={m} />}</For>
          </datalist>
        </div>
      </div>

      <Show when={k6Missing()}>
        <div class="rounded border border-warn-edge bg-warn/10 px-3 py-2 text-xs text-warn">
          <p class="font-medium">k6 is not available.</p>
          <p class="mt-0.5 text-ink-dim">{k6Missing()}</p>
          <p class="mt-1 text-ink-faint">Run build/sidecars/download-k6.sh, or install k6 on your PATH.</p>
        </div>
      </Show>

      <Show when={error()}>
        <div class="rounded border border-danger-edge bg-danger-bg/40 px-3 py-2 text-xs text-danger">{error()}</div>
      </Show>

      {/* live chart */}
      <Show when={samples().length > 0 || running()}>
        <div class="rounded border border-edge bg-surface p-2">
          <PerfChart samples={samples()} />
        </div>
      </Show>

      {/* results */}
      <Show when={result()}>
        {(r) => (
          <div class="rounded border border-edge bg-surface p-3">
            <div class="mb-2 flex items-center gap-2">
              <span
                class="rounded px-2 py-0.5 text-xs font-semibold"
                classList={{ 'bg-accent text-accent-contrast': r().passed, 'bg-danger text-accent-contrast': !r().passed }}
              >
                {r().passed ? 'PASSED' : 'FAILED'}
              </span>
              <span class="text-xs text-ink-muted">
                {r().requests.toLocaleString()} requests · {(r().wallMs / 1000).toFixed(1)}s
              </span>
            </div>
            <div class="grid grid-cols-3 gap-x-6 gap-y-1.5 font-mono text-xs sm:grid-cols-4">
              <Stat label="req/s" value={r().rps.toFixed(1)} />
              <Stat label="error rate" value={`${(r().failRate * 100).toFixed(2)}%`} danger={r().failRate > 0} />
              <Stat label="avg" value={`${r().durationAvgMs.toFixed(0)}ms`} />
              <Stat label="min" value={`${r().durationMinMs.toFixed(0)}ms`} />
              <Stat label="med" value={`${r().durationMedMs.toFixed(0)}ms`} />
              <Stat label="p90" value={`${r().durationP90Ms.toFixed(0)}ms`} />
              <Stat label="p95" value={`${r().durationP95Ms.toFixed(0)}ms`} />
              <Stat label="max" value={`${r().durationMaxMs.toFixed(0)}ms`} />
            </div>
            <Show when={(r().thresholdResults ?? []).length > 0}>
              <div class="mt-3 border-t border-edge pt-2">
                <span class="text-xs font-medium uppercase tracking-wide text-ink-muted">Thresholds</span>
                <div class="mt-1 flex flex-col gap-1">
                  <For each={r().thresholdResults}>
                    {(t) => (
                      <div class="flex items-center gap-2 font-mono text-xs">
                        <span classList={{ 'text-accent-fg': t.passed, 'text-danger': !t.passed }}>{t.passed ? '✓' : '✗'}</span>
                        <span class="text-ink-dim">{t.metric}</span>
                        <span class="text-ink-faint">{t.expression}</span>
                      </div>
                    )}
                  </For>
                </div>
              </div>
            </Show>
          </div>
        )}
      </Show>
    </div>
  )
}

function Stat(props: { label: string; value: string; danger?: boolean }) {
  return (
    <div class="flex flex-col">
      <span class="text-[10px] uppercase tracking-wide text-ink-faint">{props.label}</span>
      <span classList={{ 'text-danger': props.danger, 'text-ink': !props.danger }}>{props.value}</span>
    </div>
  )
}

function RampingStages(props: { requestIndex: number }) {
  const cfg = createMemo<PerfConfig>(() => appState.requests[props.requestIndex]?.perf ?? DEFAULT_CONFIG)
  const stages = createMemo(() => cfg().stages ?? [{ duration: '30s', target: 20 }])

  function setStages(next: PerfConfig['stages']) {
    setAppState('requests', props.requestIndex, 'perf', (prev: PerfConfig | null | undefined) => ({
      ...(prev ?? DEFAULT_CONFIG),
      stages: next,
    }))
  }

  return (
    <div class="flex flex-col gap-1">
      <span class="text-xs text-ink-muted">Stages (duration → target VUs)</span>
      <For each={stages()}>
        {(s, i) => (
          <div class="flex items-center gap-1">
            <input
              class="w-20 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={s.duration}
              placeholder="30s"
              onInput={(e) => {
                const next = [...stages()]
                next[i()] = { ...next[i()], duration: e.currentTarget.value }
                setStages(next)
              }}
            />
            <span class="text-ink-faint">→</span>
            <input
              type="number"
              min="0"
              class="w-16 rounded bg-field px-2 py-1 font-mono text-xs text-ink focus:outline-none focus:ring-1 focus:ring-edge-strong"
              value={s.target}
              onInput={(e) => {
                const next = [...stages()]
                next[i()] = { ...next[i()], target: Number(e.currentTarget.value) || 0 }
                setStages(next)
              }}
            />
            <button
              class="rounded px-1.5 py-1 text-xs text-ink-faint hover:bg-raised hover:text-danger"
              onClick={() => setStages(stages().filter((_, idx) => idx !== i()))}
            >
              ×
            </button>
          </div>
        )}
      </For>
      <button class="self-start text-xs text-accent-fg hover:underline" onClick={() => setStages([...stages(), { duration: '30s', target: 20 }])}>
        + add stage
      </button>
    </div>
  )
}
