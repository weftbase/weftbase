// The playback clock. Pure: positions are display milliseconds on a
// TimeScale (scale.ts), time passed is wall milliseconds.
//
// Playback moves the playhead over the recorded time at `speed`. A
// compressed wait on a person is always crossed in GAP_PLAY_MS of wall
// time, whatever the speed and however long the wait was: speed
// multiplies everything else.

import type { TimeScale } from "./scale"

export const GAP_PLAY_MS = 1000
export const SPEEDS = [0.5, 1, 1.5, 2, 4] as const
export type Speed = (typeof SPEEDS)[number]

/** advance moves the playhead at `d` forward by `dt` wall milliseconds.
 * It never passes the end of the scale. */
export function advance(
  scale: TimeScale,
  d: number,
  dt: number,
  speed: number
): number {
  let left = Math.max(0, dt)
  let at = Math.max(0, d)
  while (left > 0 && at < scale.length) {
    const gap = scale.gaps.find((g) => at >= g.d0 && at < g.d1)
    if (gap) {
      const rate = (gap.d1 - gap.d0) / GAP_PLAY_MS
      const need = (gap.d1 - at) / rate
      if (left < need) return at + left * rate
      left -= need
      at = gap.d1
      continue
    }
    const next = scale.gaps.find((g) => g.d0 > at)
    const stop = next ? next.d0 : scale.length
    const need = (stop - at) / speed
    if (left < need) return at + left * speed
    left -= need
    at = stop
  }
  return Math.min(at, scale.length)
}

/** wallDuration is how long playing the whole scale takes at `speed`. */
export function wallDuration(scale: TimeScale, speed: number): number {
  const gapped = scale.gaps.reduce((n, g) => n + (g.d1 - g.d0), 0)
  return (scale.length - gapped) / speed + scale.gaps.length * GAP_PLAY_MS
}

/** stepTargets are the places step back / forward stop at: the start of
 * every row, in time order, without repeats (display time). */
export function stepTargets(
  scale: TimeScale,
  starts: readonly number[]
): number[] {
  const ds = starts.map((t) => Math.round(scale.toDisplay(t)))
  return [...new Set(ds)].sort((a, b) => a - b)
}

/** stepFrom is the next target after (dir 1) or before (dir -1) `d`.
 * Stepping forward from the last start goes to the end; stepping back
 * from the first goes to 0. */
export function stepFrom(
  targets: readonly number[],
  d: number,
  dir: 1 | -1,
  length: number
): number {
  const eps = 0.5
  if (dir === 1) {
    for (const t of targets) if (t > d + eps) return t
    return length
  }
  for (let i = targets.length - 1; i >= 0; i--)
    if (targets[i] < d - eps) return targets[i]
  return 0
}
