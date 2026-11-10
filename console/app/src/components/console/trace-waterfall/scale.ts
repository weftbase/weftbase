// The waterfall's time axis. Pure: no DOM, no clock.
//
// Two coordinate systems:
// - real time, in milliseconds since the epoch (what spans carry);
// - display time, in milliseconds from the left edge of the chart.
// They are the same except across a long wait on a person: a wait longer
// than GAP_MIN_MS is drawn as a short hatched gap, so a ten-minute
// approval does not flatten everything else into a sliver. Durations
// printed on rows and ticks are always real.

/** A wait shorter than this is drawn to scale. */
export const GAP_MIN_MS = 2000
/** A gap's share of the chart, as a fraction of the active time. */
const GAP_SHARE = 0.08
/** …and its bounds in display milliseconds. */
const GAP_FLOOR_MS = 400

export interface Interval {
  from: number
  /** null: still going (runs to the scale's end). */
  to: number | null
}

export interface Gap {
  /** Real time. */
  from: number
  to: number
  /** Display time. */
  d0: number
  d1: number
}

export interface TimeScale {
  /** Real domain. */
  t0: number
  t1: number
  /** Display length: toDisplay(t1). */
  length: number
  gaps: readonly Gap[]
  toDisplay: (t: number) => number
  toReal: (d: number) => number
}

/** makeScale builds the axis for [t0, t1] with `waits` compressed. */
export function makeScale(
  t0: number,
  t1: number,
  waits: readonly Interval[] = []
): TimeScale {
  const end = Math.max(t1, t0 + 1)
  // Clip, sort and merge the waits that are long enough to compress.
  const long: { from: number; to: number }[] = []
  for (const w of [...waits].sort((a, b) => a.from - b.from)) {
    const from = Math.max(t0, w.from)
    const to = Math.min(end, w.to ?? end)
    if (!(to - from > GAP_MIN_MS)) continue
    const prev = long.at(-1)
    if (prev && from <= prev.to) prev.to = Math.max(prev.to, to)
    else long.push({ from, to })
  }
  const waited = long.reduce((n, g) => n + (g.to - g.from), 0)
  const active = end - t0 - waited
  const gapLen = Math.max(GAP_FLOOR_MS, active * GAP_SHARE)

  const gaps: Gap[] = []
  let shift = 0 // real − display, before the next gap
  for (const g of long) {
    const d0 = g.from - t0 - shift
    gaps.push({ from: g.from, to: g.to, d0, d1: d0 + gapLen })
    shift += g.to - g.from - gapLen
  }
  const length = end - t0 - shift

  const toDisplay = (t: number): number => {
    let s = 0
    for (const g of gaps) {
      if (t <= g.from) break
      if (t < g.to)
        return g.d0 + ((t - g.from) / (g.to - g.from)) * (g.d1 - g.d0)
      s += g.to - g.from - (g.d1 - g.d0)
    }
    return t - t0 - s
  }
  const toReal = (d: number): number => {
    let s = 0
    for (const g of gaps) {
      if (d <= g.d0) break
      if (d < g.d1)
        return g.from + ((d - g.d0) / (g.d1 - g.d0)) * (g.to - g.from)
      s += g.to - g.from - (g.d1 - g.d0)
    }
    return d + t0 + s
  }
  return { t0, t1: end, length, gaps, toDisplay, toReal }
}

// 1-2-5 below a minute, then the steps a clock reads well.
const STEPS = [
  1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10_000, 15_000, 30_000,
  60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000, 3_600_000, 7_200_000,
  14_400_000, 43_200_000, 86_400_000,
]

/** niceStep is the smallest readable step of at least `ms`. */
export function niceStep(ms: number): number {
  for (const s of STEPS) if (s >= ms) return s
  return Math.ceil(ms / 86_400_000) * 86_400_000
}

/** tickLabel prints an offset from the start for a tick `step` apart:
 * "0s", "250ms", "1.5s", "40s", "2m", "1m 30s", "1h 05m". */
export function tickLabel(ms: number, step: number): string {
  if (ms === 0) return "0s"
  if (ms < 1000 && step < 1000) return `${ms}ms`
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(step < 1000 ? 3 : 0))}s`
  if (ms < 3_600_000) {
    const m = Math.floor(ms / 60_000)
    const rest = Number(((ms - m * 60_000) / 1000).toFixed(step < 1000 ? 3 : 0))
    if (rest === 0) return `${m}m`
    const [whole, frac] = String(rest).split(".")
    return `${m}m ${whole.padStart(2, "0")}${frac ? `.${frac}` : ""}s`
  }
  const h = Math.floor(ms / 3_600_000)
  const m = Math.round((ms - h * 3_600_000) / 60_000)
  return m === 0 ? `${h}h` : `${h}h ${String(m).padStart(2, "0")}m`
}

export interface Tick {
  /** Display time. */
  d: number
  label: string
}

/** ticks lays out the ruler for the visible window [v0, v1] (display
 * time) over `width` pixels: evenly spaced in real time, none inside a
 * compressed gap, and none closer than `minGap` pixels to the one
 * before (which happens right after a gap). */
export function ticks(
  scale: TimeScale,
  v0: number,
  v1: number,
  width: number,
  minGap = 72
): Tick[] {
  const span = v1 - v0
  if (!(span > 0) || !(width > 0)) return []
  const step = niceStep((span / width) * minGap)
  const out: Tick[] = []
  const pxPer = width / span
  let lastPx = -Infinity
  // Linear stretches between the gaps, in real time.
  const edges: [number, number][] = []
  let from = scale.t0
  for (const g of scale.gaps) {
    edges.push([from, g.from])
    from = g.to
  }
  edges.push([from, scale.t1])
  for (const [a, b] of edges) {
    const lo = Math.max(a, scale.toReal(v0))
    const hi = Math.min(b, scale.toReal(v1))
    if (hi < lo) continue
    const first = Math.ceil((lo - scale.t0) / step) * step
    for (let off = first; scale.t0 + off <= hi; off += step) {
      const d = scale.toDisplay(scale.t0 + off)
      const px = (d - v0) * pxPer
      if (px - lastPx < minGap * 0.6) continue
      out.push({ d, label: tickLabel(off, step) })
      lastPx = px
      if (out.length > 200) return out
    }
  }
  return out
}

/** zoomAt narrows or widens the window [v0, v1] around `anchor` (display
 * time) by `factor` (< 1 zooms in), kept inside [0, length] and no
 * narrower than `min`. */
export function zoomAt(
  v0: number,
  v1: number,
  anchor: number,
  factor: number,
  length: number,
  min = 10
): [number, number] {
  const span = Math.min(length, Math.max(min, (v1 - v0) * factor))
  const ratio = v1 === v0 ? 0.5 : (anchor - v0) / (v1 - v0)
  let a = anchor - ratio * span
  a = Math.max(0, Math.min(length - span, a))
  return [a, a + span]
}

/** pan shifts the window by `delta` display ms, kept inside [0, length]. */
export function pan(
  v0: number,
  v1: number,
  delta: number,
  length: number
): [number, number] {
  const span = v1 - v0
  const a = Math.max(0, Math.min(length - span, v0 + delta))
  return [a, a + span]
}
