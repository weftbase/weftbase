// The waterfall's state that other parts of a page need too: the model,
// the axis and the playhead. A run page calls useTraceView once and
// gives the result to <TraceWaterfall view=…> and reads `stats` for its
// stat strip, so the strip follows the playhead.

import * as React from "react"

import { SPEEDS, advance, stepFrom, stepTargets } from "./clock"
import type { Speed } from "./clock"
import { buildTrace, statsAt } from "./model"
import type { TraceInput, TraceModel, TraceStats } from "./model"
import { makeScale } from "./scale"
import type { TimeScale } from "./scale"

export interface Playback {
  /** The playhead, in display milliseconds (scale.ts). At rest it sits
   * at the end: the whole trace is shown. */
  at: number
  /** The playhead is at the end (nothing is dimmed). */
  atEnd: boolean
  playing: boolean
  speed: Speed
  /** false while the run is live: the view follows "now" instead. */
  enabled: boolean
  toggle: () => void
  play: () => void
  pause: () => void
  /** Back to the start, and play. */
  restart: () => void
  /** To the previous / next row start. */
  step: (dir: 1 | -1) => void
  seek: (d: number) => void
  setSpeed: (s: Speed) => void
}

export interface TraceView {
  model: TraceModel
  scale: TimeScale
  playback: Playback
  /** The figures at the playhead (the whole trace when at the end). */
  stats: TraceStats
  /** The run is still going and the view follows it. */
  live: boolean
}

const TICK_MS = 200

/** useNowWhile ticks while `on`, and keeps the last value after. */
function useNowWhile(on: boolean, start?: number): number {
  const [now, setNow] = React.useState(() => start ?? Date.now())
  React.useEffect(() => {
    if (!on) return
    setNow(Date.now())
    const t = setInterval(() => setNow(Date.now()), TICK_MS)
    return () => clearInterval(t)
  }, [on])
  return now
}

export function useTraceView(input: TraceInput): TraceView {
  const live = !!input.running
  const ticking = useNowWhile(live, input.now)
  const now = live ? ticking : input.now
  const model = React.useMemo(
    () => buildTrace({ ...input, now }),
    // The input's fields are the dependencies: callers build the object
    // inline.
    [
      input.spans,
      input.rootRunId,
      input.chain,
      input.waits,
      input.live,
      input.liveRuns,
      input.compactions,
      input.costs,
      input.root?.label,
      input.root?.secondary,
      input.root?.started,
      input.running,
      now,
    ]
  )
  const scale = React.useMemo(
    () => makeScale(model.t0, model.t1, model.waits),
    [model.t0, model.t1, model.waits]
  )
  const targets = React.useMemo(
    () =>
      stepTargets(
        scale,
        model.nodes.filter((n) => n.kind !== "marker").map((n) => n.start)
      ),
    [scale, model.nodes]
  )

  // null: at rest, at the end.
  const [pos, setPos] = React.useState<number | null>(null)
  const [playing, setPlaying] = React.useState(false)
  const [speed, setSpeed] = React.useState<Speed>(1)
  const scaleRef = React.useRef(scale)
  scaleRef.current = scale
  const posRef = React.useRef(pos)
  posRef.current = pos
  const jump = React.useRef<number | undefined>(undefined)
  const speedRef = React.useRef<number>(speed)
  speedRef.current = speed

  React.useEffect(() => {
    if (live) {
      setPlaying(false)
      setPos(null)
    }
  }, [live])

  React.useEffect(() => {
    if (!playing) return
    let frame = 0
    let last = performance.now()
    // The position is kept here, not read back from the state updater:
    // React may run an updater later, and the end must be known now (or
    // one more frame starts the playback over from 0).
    let at = posRef.current ?? 0
    jump.current = undefined
    const tick = (t: number) => {
      const dt = t - last
      last = t
      const s = scaleRef.current
      // A restart or a seek while playing moves the playhead first.
      if (jump.current !== undefined) {
        at = jump.current
        jump.current = undefined
      }
      at = advance(s, at, dt, speedRef.current)
      if (at >= s.length) {
        setPos(null)
        setPlaying(false)
        return
      }
      setPos(at)
      frame = requestAnimationFrame(tick)
    }
    frame = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(frame)
  }, [playing])

  const at = pos === null ? scale.length : Math.min(pos, scale.length)
  const atEnd = pos === null
  const playback = React.useMemo<Playback>(
    () => ({
      at,
      atEnd,
      playing,
      speed,
      enabled: !live && scale.length > 0 && model.nodes.length > 0,
      play: () => {
        setPos((p) => p ?? 0)
        setPlaying(true)
      },
      pause: () => setPlaying(false),
      toggle: () => {
        if (live) return
        setPos((p) => p ?? 0)
        setPlaying((p) => !p)
      },
      restart: () => {
        jump.current = 0
        setPos(0)
        setPlaying(true)
      },
      step: (dir) => {
        setPlaying(false)
        setPos((p) => {
          const s = scaleRef.current
          const next = stepFrom(targets, p ?? s.length, dir, s.length)
          return next >= s.length ? null : next
        })
      },
      seek: (d) => {
        const s = scaleRef.current
        jump.current = Math.max(0, Math.min(d, s.length))
        setPos(d >= s.length ? null : Math.max(0, d))
      },
      setSpeed,
    }),
    [at, atEnd, playing, speed, live, scale.length, model.nodes.length, targets]
  )

  const stats = React.useMemo(
    () => statsAt(model, atEnd ? Infinity : scale.toReal(at)),
    [model, scale, at, atEnd]
  )

  return { model, scale, playback, stats, live }
}

export { SPEEDS }
