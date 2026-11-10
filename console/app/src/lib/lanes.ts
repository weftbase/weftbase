// Waterfall lanes: one colour per agent run, so a subtree reads as one
// block. The root run takes the first hue (lane 1); each other run takes
// the next of the six in the order it first appears. The hues never
// cycle: the order is what keeps neighbours apart for colour-blind
// readers (styles.css, --lane-*). A run past the sixth is neutral (lane
// 0); its name and its place in the tree still tell it apart. Lane 0 is
// also what is not an agent's work at all: stock OpenTelemetry spans and
// the hatched wait on a person.

import type { Span } from "./api/types"

export const LANE_COUNT = 6

export interface Lane {
  index: number
  /** Model calls. */
  solid: string
  /** Tool calls and summary bars. */
  dim: string
  /** Connector lines and icon tint. */
  text: string
  border: string
}

// Written out in full: Tailwind only emits classes it can read here.
export const LANES: readonly Lane[] = [
  {
    index: 0,
    solid: "bg-lane-0",
    dim: "bg-lane-0-dim",
    text: "text-lane-0",
    border: "border-lane-0",
  },
  {
    index: 1,
    solid: "bg-lane-1",
    dim: "bg-lane-1-dim",
    text: "text-lane-1",
    border: "border-lane-1",
  },
  {
    index: 2,
    solid: "bg-lane-2",
    dim: "bg-lane-2-dim",
    text: "text-lane-2",
    border: "border-lane-2",
  },
  {
    index: 3,
    solid: "bg-lane-3",
    dim: "bg-lane-3-dim",
    text: "text-lane-3",
    border: "border-lane-3",
  },
  {
    index: 4,
    solid: "bg-lane-4",
    dim: "bg-lane-4-dim",
    text: "text-lane-4",
    border: "border-lane-4",
  },
  {
    index: 5,
    solid: "bg-lane-5",
    dim: "bg-lane-5-dim",
    text: "text-lane-5",
    border: "border-lane-5",
  },
  {
    index: 6,
    solid: "bg-lane-6",
    dim: "bg-lane-6-dim",
    text: "text-lane-6",
    border: "border-lane-6",
  },
]

/** assignLanes maps each run id of a span tree to its lane. `rootRunId`
 * gets lane 1; the others take lanes 2..6 in order of their first span's
 * start, and neutral after that. Stable for a given set of spans: a lane
 * follows the agent, not its position after filtering. */
export function assignLanes(
  spans: readonly Span[],
  rootRunId: string
): Map<string, Lane> {
  const first = new Map<string, number>()
  for (const s of spans) {
    const run = s.run_id ?? rootRunId
    const t = Date.parse(s.start)
    const seen = first.get(run)
    if (seen === undefined || t < seen) first.set(run, t)
  }
  const lanes = new Map<string, Lane>([[rootRunId, LANES[1]]])
  let next = 2
  for (const [run] of [...first].sort(
    (a, b) => a[1] - b[1] || (a[0] < b[0] ? -1 : 1)
  )) {
    if (lanes.has(run)) continue
    lanes.set(run, next <= LANE_COUNT ? LANES[next] : LANES[0])
    next++
  }
  return lanes
}
