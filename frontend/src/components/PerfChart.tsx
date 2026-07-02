import { onCleanup, onMount, createEffect } from 'solid-js'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { PerfSamplePoint } from '../types'

// Reads a semantic theme token as a concrete rgb() string for uPlot, which
// paints to <canvas> and so can't use CSS variables directly.
function tokenColor(name: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(`--color-${name}`).trim()
  return v ? `rgb(${v})` : '#888'
}

// PerfChart renders two live series — requests/sec and p95 latency — sharing
// an x-axis of seconds-since-start. It's fed the growing samples array and
// imperatively pushes data into uPlot (Canvas 2D, ~60fps at negligible CPU,
// the reason uPlot was chosen over SVG chart libs in docs/03-tech-stack.md).
export default function PerfChart(props: { samples: PerfSamplePoint[] }) {
  let el: HTMLDivElement | undefined
  let plot: uPlot | undefined

  function build() {
    if (!el) return
    const accent = tokenColor('accent-fg')
    const info = tokenColor('info')
    const ink = tokenColor('ink-muted')
    const grid = tokenColor('edge')

    const opts: uPlot.Options = {
      width: el.clientWidth || 600,
      height: 220,
      cursor: { drag: { x: true, y: false } },
      legend: { show: true },
      scales: { x: { time: false }, rps: {}, ms: {} },
      axes: [
        { stroke: ink, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid }, values: (_u, vals) => vals.map((v) => `${v}s`) },
        { stroke: ink, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid }, scale: 'rps' },
        { stroke: ink, grid: { show: false }, ticks: { stroke: grid }, side: 1, scale: 'ms' },
      ],
      series: [
        {},
        { label: 'req/s', stroke: accent, width: 2, scale: 'rps', points: { show: false } },
        { label: 'p95 (ms)', stroke: info, width: 2, scale: 'ms', points: { show: false } },
      ],
    }
    plot = new uPlot(opts, toData(props.samples), el)
  }

  function toData(samples: PerfSamplePoint[]): uPlot.AlignedData {
    const x = samples.map((s) => Math.round(s.timeOffsetMs / 1000))
    const rps = samples.map((s) => s.rps)
    const p95 = samples.map((s) => s.p95Ms)
    return [x, rps, p95]
  }

  onMount(() => {
    build()
    const ro = new ResizeObserver(() => {
      if (plot && el) plot.setSize({ width: el.clientWidth || 600, height: 220 })
    })
    if (el) ro.observe(el)
    onCleanup(() => ro.disconnect())
  })

  // Push new data as samples stream in.
  createEffect(() => {
    const data = toData(props.samples)
    if (plot) plot.setData(data)
  })

  onCleanup(() => plot?.destroy())

  return <div ref={el} class="w-full" />
}
