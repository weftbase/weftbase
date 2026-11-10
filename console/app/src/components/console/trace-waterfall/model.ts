// The waterfall's data model. Pure: spans in, a tree of rows out.
//
// The server returns flat spans (GET /v1/runs/{id}/spans?tree=1). They
// nest by parent_span_id; a child run's `invoke_agent` span hangs under
// the parent's `execute_tool` span. On top of that this file:
// - folds a handoff (the tool call and the child agent it started) into
//   ONE row: the child agent, with the connector that marks the handoff;
// - folds a chain (a parked run and the runs that resumed it) into one
//   root, with each wait on a person as a hatched "waiting" row;
// - adds rows for calls in flight that have no span yet (a live run) and
//   thin marker rows for context compactions;
// - gives every row its agent's lane (lib/lanes.ts) and its kind
//   (lib/span-kind.ts).

import type { CostFields, RunCompaction, Span } from "@/lib/api/types"
import { tokens } from "@/lib/format"
import { LANES, assignLanes } from "@/lib/lanes"
import type { Lane } from "@/lib/lanes"
import {
  SPAN_KINDS,
  baseKind,
  spanFailed,
  spanLabel,
  spanSecondary,
} from "@/lib/span-kind"
import type { SpanKind } from "@/lib/span-kind"

/** A wait on a person, in real milliseconds. */
export interface TraceWait {
  /** The run that parked, when known. */
  run?: string
  from: number
  /** null while the wait lasts. */
  to: number | null
  /** One line for the tooltip and the detail: "approved by ada". */
  decided?: string
}

/** A call that has started and has no span yet (spans arrive when a
 * call ends). Built from a live run's events. */
export interface LiveCall {
  runId: string
  kind: "model" | "tool"
  label: string
  secondary?: string
  start: number
  step?: number
  callId?: string
}

/** A child run that has no agent span yet: a run reports its root span
 * when it ends, so a subagent in flight is known by its run row only. */
export interface LiveRun {
  runId: string
  /** The run whose call started it. */
  parentRunId: string
  /** The call that started it, and that call's step. */
  callId?: string
  step?: number
  /** The agent's name. */
  label: string
  model?: string
  start: number
  /** When it ended; absent while it is going. */
  end?: number
  failed?: boolean
}

export interface TraceInput {
  spans: readonly Span[]
  /** The run the trace belongs to. Absent for a raw trace: rows are
   * neutral and nothing is folded. */
  rootRunId?: string
  /** The runs that are one logical run (a park and its resumes), oldest
   * first. Defaults to [rootRunId]. */
  chain?: readonly string[]
  waits?: readonly TraceWait[]
  live?: readonly LiveCall[]
  /** Child runs known by their row and not by an agent span yet. */
  liveRuns?: readonly LiveRun[]
  compactions?: readonly (RunCompaction & { run_id?: string })[]
  /** Cost by run id: the label after an agent row's bar. */
  costs?: ReadonlyMap<string, CostFields>
  /** Stands in for the root row while the run has no root span yet. */
  root?: { label: string; secondary?: string; started: number }
  /** The run is still going: the root and the calls in flight end at
   * `now`, and so does the axis. */
  running?: boolean
  now?: number
}

export type NodeKind = SpanKind | "marker"

export interface TraceNode {
  /** The span id (the value of `?span=`), or a synthetic id. */
  id: string
  /** Other ids that select this row (a folded handoff's tool span, the
   * later root spans of a chain). */
  aliases: string[]
  /** The row's span; undefined for a synthetic row. */
  span?: Span
  /** Every span folded into the row, the row's own first. */
  spans: Span[]
  /** The tool call that started this agent, when the row is a handoff. */
  handoff?: Span
  /** The row is an agent another agent's call started: a folded handoff,
   * or a child run in flight (which has no `handoff` span yet). */
  handed: boolean
  parent?: TraceNode
  children: TraceNode[]
  depth: number
  /** What is drawn: failed and waiting are laid over the base kind. */
  kind: NodeKind
  /** What the row is. */
  base: "agent" | "model" | "tool" | "handoff" | "other" | "marker"
  label: string
  /** The one muted word after the name. */
  secondary?: string
  /** The tool of a tool call or of a wait, as the server names it. */
  tool?: string
  /** The model of a model call or an agent, in full. */
  model?: string
  /** The run the row belongs to (as the server names it). */
  runId?: string
  /** On a child run in flight: the run whose call started it. */
  startedBy?: string
  lane: Lane
  /** Real milliseconds. */
  start: number
  end: number
  /** Still going: `end` is "now". */
  running: boolean
  failed: boolean
  step?: number
  callId?: string
  tokensIn?: number
  tokensOut?: number
  /** On an agent row whose run has a cost. */
  cost?: CostFields
  /** The wait this row draws. */
  wait?: TraceWait
  /** A retried attempt: ↻ inside the bar. */
  retry: boolean
  /** One small glyph after the name, with why. */
  glyph?: { what: "truncated" | "max_tokens" | "repaired"; title: string }
  /** Why the row looks the way it does (never ran, denied, …). */
  note?: string
}

export interface TraceModel {
  roots: TraceNode[]
  /** Every row, parents before children. */
  nodes: TraceNode[]
  /** By id and by alias. */
  byId: Map<string, TraceNode>
  /** Real milliseconds. */
  t0: number
  t1: number
  waits: TraceWait[]
  /** The slowest call (a leaf), when there is more than one. */
  slowestId?: string
  running: boolean
}

const ms = (t: string) => Date.parse(t)
const num = (v: unknown): number | undefined =>
  typeof v === "number" ? v : undefined
const str = (v: unknown): string | undefined =>
  typeof v === "string" && v !== "" ? v : undefined

function glyphOf(s: Span): TraceNode["glyph"] {
  const cut =
    num(s.attrs["weft.tool.truncated_bytes"]) ??
    num(s.attrs["weft.content.truncated_bytes"])
  if (cut)
    return {
      what: "truncated",
      title: `Output truncated: ${tokens(cut)} bytes were cut`,
    }
  const reasons = s.attrs["gen_ai.response.finish_reasons"]
  if (
    Array.isArray(reasons) &&
    reasons.some((r) => r === "length" || r === "max_tokens")
  )
    return {
      what: "max_tokens",
      title: "The reply stopped at the output token limit",
    }
  if (s.attrs["weft.tool.args_repaired"])
    return {
      what: "repaired",
      title: "The arguments were repaired before the call",
    }
  return undefined
}

function noteOf(s: Span): string | undefined {
  if (s.attrs["weft.tool.denied"]) {
    const by = str(s.attrs["weft.tool.denied_by"])
    return by
      ? `Denied (${by}): the tool did not run`
      : "Denied: the tool did not run"
  }
  if (s.attrs["weft.tool.approved"]) {
    const by = str(s.attrs["weft.tool.approved_by"])
    return by ? `Approved by ${by}` : "Approved by a person"
  }
  return undefined
}

/** shortModel drops what does not tell two models apart at a glance:
 * a path ("models/…", "acme/…") and the vendor's prefix. */
export function shortModel(name: string | undefined): string | undefined {
  if (!name) return undefined
  const last = name.split("/").at(-1) ?? name
  return last.replace(/^(claude|models)-/, "") || name
}

function nodeOf(s: Span, lane: Lane): TraceNode {
  const base = baseKind(s)
  const model =
    base === "model" || base === "agent"
      ? (str(s.attrs["gen_ai.response.model"]) ??
        str(s.attrs["gen_ai.request.model"]))
      : undefined
  const origin = str(s.attrs["weft.tool.origin"])
  const failed = spanFailed(s)
  const attempt = num(s.attrs["weft.attempt.index"])
  return {
    id: s.span_id,
    aliases: [],
    span: s,
    spans: [s],
    handed: false,
    children: [],
    depth: 0,
    kind: failed && base !== "agent" ? "failed" : base,
    base,
    // A model call is named by what it did once its tool calls are
    // known (nameModelCalls); a tool by itself, with where it comes from
    // only when that is not the app.
    label: base === "model" ? "Model call" : spanLabel(s),
    secondary:
      base === "model" || base === "agent"
        ? shortModel(model)
        : base === "tool" || base === "handoff"
          ? origin && origin !== "local" && origin !== "subagent"
            ? origin
            : undefined
          : spanSecondary(s),
    tool: base === "tool" || base === "handoff" ? spanLabel(s) : undefined,
    model,
    runId: s.run_id,
    lane,
    start: ms(s.start),
    end: Math.max(ms(s.end), ms(s.start)),
    running: false,
    failed,
    step: num(s.attrs["weft.step.index"]),
    callId: str(s.attrs["gen_ai.tool.call.id"]),
    tokensIn: num(s.attrs["gen_ai.usage.input_tokens"]),
    tokensOut: num(s.attrs["gen_ai.usage.output_tokens"]),
    retry:
      (attempt !== undefined && attempt > 1) || !!s.attrs["weft.tool.retry"],
    glyph: glyphOf(s),
    note: noteOf(s),
  }
}

function synthetic(
  id: string,
  base: TraceNode["base"],
  kind: NodeKind,
  label: string,
  lane: Lane,
  start: number,
  end: number
): TraceNode {
  return {
    id,
    aliases: [],
    spans: [],
    handed: false,
    children: [],
    depth: 0,
    kind,
    base,
    label,
    lane,
    start,
    end,
    running: false,
    failed: false,
    retry: false,
  }
}

function adopt(parent: TraceNode, child: TraceNode) {
  child.parent = parent
  parent.children.push(child)
}

/** nameModelCalls names each model call by what it did: the tools it
 * asked for (the tool rows of its step that started after it), a
 * handoff, or the reply. "Model call" stays when that is not known. */
function nameModelCalls(nodes: Iterable<TraceNode>) {
  for (const n of nodes) {
    if (n.base !== "model" || !n.parent || n.failed) continue
    const asked = n.parent.children.filter(
      (c) =>
        c !== n &&
        c.step === n.step &&
        c.step !== undefined &&
        c.start >= n.end &&
        (c.handoff?.run_id ?? c.startedBy ?? c.span?.run_id ?? c.runId) ===
          (n.span?.run_id ?? n.runId) &&
        (c.base === "tool" || c.base === "handoff" || c.handed)
    )
    const handoffs = asked.filter((c) => c.handed || c.base === "handoff")
    if (asked.length > 0 && handoffs.length === asked.length) {
      const names = handoffs.map((c) =>
        c.handed ? c.label : (c.tool ?? c.label)
      )
      n.label = `Hand off to ${names.slice(0, 2).join(", ")}${names.length > 2 ? ` +${names.length - 2}` : ""}`
    } else if (asked.length > 0) {
      const names = asked.map((c) => c.tool ?? c.label)
      n.label = `Call ${names.slice(0, 2).join(", ")}${names.length > 2 ? ` +${names.length - 2}` : ""}`
    } else {
      const reasons = n.span?.attrs["gen_ai.response.finish_reasons"]
      const calls = n.span?.attrs["weft.model.tool_calls"]
      if (
        calls === 0 ||
        (Array.isArray(reasons) &&
          reasons.some((r) => r === "stop" || r === "end_turn"))
      )
        n.label = "Reply"
    }
  }
}

/** liveIds: the ids the row had (or would have had) before its span
 * arrived: `run:<id>` for a child run, `live:…` for a call. */
function liveIds(n: TraceNode): string[] {
  if (n.spans.length === 0 || n.runId === undefined) return []
  if (n.base === "agent") return [`run:${n.runId}`]
  const run = n.span?.run_id ?? n.runId
  if (n.base === "model") return [`live:${run}:model:${n.step ?? ""}:`]
  if (n.base === "tool" || n.base === "handoff")
    return [`live:${run}:tool:${n.step ?? ""}:${n.callId ?? ""}`]
  return []
}

/** compactionLabel: "Context compacted: 41k → 9k tokens". */
export function compactionLabel(c: RunCompaction): string {
  if (c.tokens_before !== undefined && c.tokens_after !== undefined)
    return `Context compacted: ${tokens(c.tokens_before)} → ${tokens(c.tokens_after)} tokens`
  return `Context compacted: ${c.replaced} messages replaced`
}

/** buildTrace turns spans (and what the run page knows beside them)
 * into the tree the waterfall draws. */
export function buildTrace(input: TraceInput): TraceModel {
  const { rootRunId } = input
  const now = input.now ?? Date.now()
  const chain = input.chain?.length ? input.chain : rootRunId ? [rootRunId] : []
  const head = chain.at(0)
  const canon = (run: string | undefined) =>
    run !== undefined && chain.includes(run) ? head : run

  const spans = [...input.spans].sort(
    (a, b) => ms(a.start) - ms(b.start) || (a.span_id < b.span_id ? -1 : 1)
  )
  // A child run in flight takes its lane when it starts, not when its
  // first span arrives: it stands in the list as a span of its own.
  const liveRuns = [...(input.liveRuns ?? [])].sort(
    (a, b) => a.runId.length - b.runId.length || a.start - b.start
  )
  const lanes =
    head !== undefined
      ? assignLanes(
          [
            ...spans.map((s) => ({ ...s, run_id: canon(s.run_id ?? head) })),
            ...liveRuns.map(
              (r) =>
                ({
                  run_id: r.runId,
                  start: new Date(r.start).toISOString(),
                }) as Span
            ),
          ],
          head
        )
      : new Map<string, Lane>()
  const laneOf = (run: string | undefined) =>
    (head !== undefined && lanes.get(canon(run ?? head) ?? "")) || LANES[0]

  // 1. One node per span, nested by parent_span_id.
  const bySpan = new Map<string, TraceNode>()
  for (const s of spans) bySpan.set(s.span_id, nodeOf(s, laneOf(s.run_id)))
  const top: TraceNode[] = []
  for (const s of spans) {
    const n = bySpan.get(s.span_id)!
    const p = s.parent_span_id ? bySpan.get(s.parent_span_id) : undefined
    if (p && p !== n) adopt(p, n)
    else top.push(n)
  }

  // 1b. A model call's attempts (a retry, a fallback to another model)
  // are spans under it. They are part of the call, not rows: the call's
  // bar carries ↻ and its detail lists them.
  for (const n of [...bySpan.values()]) {
    if (n.base !== "model") continue
    const attempts = n.children.filter(
      (c) =>
        c.base === "other" &&
        c.children.length === 0 &&
        c.span?.attrs["weft.attempt.index"] !== undefined
    )
    if (attempts.length === 0) continue
    n.children = n.children.filter((c) => !attempts.includes(c))
    for (const c of attempts) {
      n.aliases.push(c.id)
      n.spans.push(...c.spans)
      bySpan.set(c.id, n)
    }
    if (attempts.length > 1) n.retry = true
  }

  let roots = top
  let primary: TraceNode | undefined
  const agentOfRun = new Map<string, TraceNode>()
  for (const n of bySpan.values())
    if (n.base === "agent" && n.runId && !agentOfRun.has(n.runId))
      agentOfRun.set(n.runId, n)

  if (head !== undefined) {
    // 2. The chain's root spans become one root.
    const heads = top.filter(
      (n) =>
        n.base === "agent" && n.runId !== undefined && chain.includes(n.runId)
    )
    primary = heads.at(0)
    if (primary) {
      for (const other of heads.slice(1)) {
        for (const c of other.children) adopt(primary, c)
        other.children = []
        primary.aliases.push(other.id)
        primary.spans.push(...other.spans)
        primary.end = Math.max(primary.end, other.end)
        // The chain's state is its newest run's.
        primary.failed = other.failed
        bySpan.set(other.id, primary)
      }
    } else if (input.root) {
      primary = synthetic(
        `run:${head}`,
        "agent",
        "agent",
        input.root.label,
        laneOf(head),
        input.root.started,
        now
      )
      primary.secondary = shortModel(input.root.secondary)
      primary.model = input.root.secondary
      primary.runId = head
    }
    if (primary) {
      primary.aliases.push(`run:${head}`)
      primary.runId = head
      for (const id of chain) agentOfRun.set(id, primary)
      // 2b. A child run with no agent span yet (it is in flight, or its
      // root span is still on its way) gets a row from its run row, so
      // its calls have their agent to hang under.
      const standIns: [TraceNode, LiveRun][] = []
      for (const r of liveRuns) {
        if (agentOfRun.has(r.runId)) continue
        const n = synthetic(
          `run:${r.runId}`,
          "agent",
          "agent",
          r.label,
          laneOf(r.runId),
          r.start,
          Math.max(r.end ?? now, r.start)
        )
        n.secondary = shortModel(r.model)
        n.model = r.model
        n.runId = r.runId
        n.startedBy = r.parentRunId
        n.handed = true
        n.callId = r.callId
        n.step = r.step
        n.running = r.end === undefined
        n.failed = !!r.failed
        agentOfRun.set(r.runId, n)
        standIns.push([n, r])
      }
      for (const [n, r] of standIns)
        adopt(agentOfRun.get(r.parentRunId) ?? primary, n)
      // 3. Spans whose parent has not arrived (a live run reports a span
      // when it ends, the root last) go under their run's agent.
      for (const n of top) {
        if (n === primary || heads.includes(n)) continue
        // An agent whose handoff span is missing hangs under its parent run.
        const owner =
          n.base === "agent"
            ? agentOfRun.get(n.span?.parent_run_id ?? "")
            : agentOfRun.get(n.runId ?? "")
        adopt(owner && owner !== n ? owner : primary, n)
      }
      roots = [primary]
    }

    // 4. A handoff and the agent it started are one row.
    for (const n of [...bySpan.values()]) {
      if (n.base !== "handoff" || n.children.length !== 1) continue
      const child = n.children[0]
      if (child.base !== "agent") continue
      const parent = n.parent
      child.handoff = n.span
      child.handed = true
      child.aliases.push(n.id)
      child.spans.push(...n.spans)
      child.callId = n.callId
      child.step = n.step
      n.children = []
      if (parent) {
        parent.children = parent.children.map((c) => (c === n ? child : c))
        child.parent = parent
      } else {
        child.parent = undefined
        roots = roots.map((r) => (r === n ? child : r))
      }
      bySpan.set(n.id, child)
    }
  }

  // 5. Waits on a person: the pending call's row becomes the hatched
  // interval; a wait with no such row gets a row of its own.
  const waits = [...(input.waits ?? [])].sort((a, b) => a.from - b.from)
  const drawn = new Set<TraceWait>()
  for (const n of new Set(bySpan.values())) {
    if (n.span?.attrs["weft.tool.pending"] !== true) continue
    const wait =
      waits.find(
        (w) => !drawn.has(w) && w.run !== undefined && w.run === n.runId
      ) ?? waits.find((w) => !drawn.has(w) && Math.abs(w.from - n.start) < 5000)
    n.kind = "waiting"
    n.label = "Waiting for approval"
    n.secondary = n.tool
    n.lane = LANES[0]
    if (wait) {
      drawn.add(wait)
      n.wait = wait
      n.end = wait.to ?? now
      n.running = wait.to === null
      n.note =
        wait.decided ?? (wait.to === null ? "Waiting on a person" : undefined)
    } else {
      n.note =
        "This call did not run: it was waiting on a person when the run ended"
    }
  }
  if (primary)
    waits.forEach((w, i) => {
      if (drawn.has(w)) return
      const n = synthetic(
        `wait:${i}`,
        "tool",
        "waiting",
        "Waiting for approval",
        LANES[0],
        w.from,
        w.to ?? now
      )
      n.wait = w
      n.running = w.to === null
      n.note = w.decided
      n.runId = w.run
      adopt(primary, n)
    })

  // 6. Calls in flight.
  if (primary)
    for (const c of input.live ?? []) {
      // A call that started a child run is that run's row, not a second one.
      if (
        c.kind === "tool" &&
        (agentOfRun.has(`${c.runId}/${c.step ?? ""}/${c.callId ?? ""}`) ||
          liveRuns.some(
            (r) => r.parentRunId === c.runId && r.callId === c.callId
          ))
      )
        continue
      const n = synthetic(
        `live:${c.runId}:${c.kind}:${c.step ?? ""}:${c.callId ?? ""}`,
        c.kind,
        c.kind,
        c.label,
        laneOf(c.runId),
        c.start,
        Math.max(now, c.start)
      )
      if (c.kind === "model") {
        n.label = "Model call"
        n.secondary = shortModel(c.label)
        n.model = c.label
      } else {
        n.secondary = c.secondary
        n.tool = c.label
      }
      n.running = true
      n.runId = c.runId
      n.step = c.step
      n.callId = c.callId
      adopt(agentOfRun.get(c.runId) ?? primary, n)
    }

  // 7. Compaction markers, at the model call of their step.
  if (primary)
    (input.compactions ?? []).forEach((c, i) => {
      const owner = agentOfRun.get(c.run_id ?? head ?? "") ?? primary
      const at =
        owner.children.find((k) => k.base === "model" && k.step === c.step)
          ?.start ?? owner.start
      const n = synthetic(
        `compaction:${i}`,
        "marker",
        "marker",
        compactionLabel(c),
        owner.lane,
        at,
        at
      )
      n.runId = owner.runId
      n.step = c.step
      n.note = c.reason
      adopt(owner, n)
    })

  nameModelCalls(new Set(bySpan.values()))

  if (primary && input.running) {
    primary.running = true
    primary.end = Math.max(primary.end, now)
  }
  if (primary && head !== undefined) {
    const c = input.costs?.get(head)
    if (c) primary.cost = c
  }

  // 8. Order, depth, index.
  const nodes: TraceNode[] = []
  const byId = new Map<string, TraceNode>()
  const order = (a: TraceNode, b: TraceNode) =>
    a.start - b.start ||
    // A marker sits just before the call it happened at.
    Number(b.kind === "marker") - Number(a.kind === "marker") ||
    (num(a.span?.attrs["weft.tool.seq"]) ?? 0) -
      (num(b.span?.attrs["weft.tool.seq"]) ?? 0) ||
    (a.id < b.id ? -1 : 1)
  const walk = (n: TraceNode, depth: number) => {
    n.depth = depth
    nodes.push(n)
    byId.set(n.id, n)
    for (const a of n.aliases) byId.set(a, n)
    liveIds(n).forEach((a) => stood.push([a, n]))
    if (n !== primary && n.base === "agent" && n.runId) {
      const c = input.costs?.get(n.runId)
      if (c) n.cost = c
    }
    n.children.sort(order)
    // An agent spans its calls: a child that outlasts its parent (clock
    // skew, a wait drawn on a pending call) stretches it.
    for (const c of n.children) walk(c, depth + 1)
    if (n.base === "agent" && n.children.length > 0)
      n.end = Math.max(n.end, ...n.children.map((c) => c.end))
  }
  // A row chosen while its call was in flight stays chosen when the
  // call's span arrives: the span answers to the id its stand-in had.
  const stood: [string, TraceNode][] = []
  roots.sort(order)
  for (const r of roots) walk(r, 0)
  for (const [a, n] of stood) if (!byId.has(a)) byId.set(a, n)

  let t0 = Infinity
  let t1 = -Infinity
  for (const n of nodes) {
    if (n.start < t0) t0 = n.start
    if (n.end > t1) t1 = n.end
  }
  if (!Number.isFinite(t0)) {
    t0 = input.root?.started ?? now
    t1 = t0
  }
  if (input.running) t1 = Math.max(t1, now)

  let slowest: TraceNode | undefined
  let leaves = 0
  for (const n of nodes) {
    if (n.children.length > 0 || n.running) continue
    if (n.base !== "model" && n.base !== "tool" && n.base !== "other") continue
    if (n.kind === "waiting") continue
    leaves++
    if (!slowest || n.end - n.start > slowest.end - slowest.start) slowest = n
  }

  return {
    roots,
    nodes,
    byId,
    t0,
    t1: Math.max(t1, t0),
    waits,
    slowestId:
      leaves > 1 && slowest && slowest.end > slowest.start
        ? slowest.id
        : undefined,
    running: !!input.running,
  }
}

// ── Rows ────────────────────────────────────────────────────────────

/** The kinds the legend filters on. */
export type LegendKind = "model" | "tool" | "waiting" | "handoff" | "failed"

/** isKind: does the row count as `kind` for the legend filter? */
export function isKind(n: TraceNode, kind: LegendKind): boolean {
  switch (kind) {
    case "failed":
      return n.failed
    case "waiting":
      return n.kind === "waiting"
    case "handoff":
      return n.handed || n.base === "handoff"
    default:
      return n.base === kind && n.kind !== "waiting"
  }
}

/** searchText is what the search field matches against, lower-cased. */
export function searchText(n: TraceNode): string {
  const parts = [
    n.label,
    n.secondary,
    SPAN_KINDS[n.kind === "marker" ? "other" : n.kind].label,
  ]
  for (const s of n.spans)
    parts.push(
      s.name,
      s.status_message,
      str(s.attrs["error.type"]),
      str(s.attrs["gen_ai.tool.call.id"])
    )
  parts.push(n.note, n.tool, n.model)
  return parts.filter(Boolean).join("\n").toLowerCase()
}

export interface RowFilter {
  /** Ids of rows whose children are hidden. */
  collapsed: ReadonlySet<string>
  query?: string
  kinds?: ReadonlySet<LegendKind>
}

export interface TraceRow {
  node: TraceNode
  expandable: boolean
  expanded: boolean
}

/** visibleRows is the list the waterfall draws, top to bottom. A search
 * or a kind filter keeps the rows that match and their ancestors, and
 * ignores what is collapsed. */
export function visibleRows(model: TraceModel, f: RowFilter): TraceRow[] {
  const query = f.query?.trim().toLowerCase() ?? ""
  const kinds = f.kinds && f.kinds.size > 0 ? f.kinds : undefined
  if (!query && !kinds) {
    const rows: TraceRow[] = []
    const walk = (n: TraceNode) => {
      const expandable = n.children.length > 0
      const expanded = expandable && !f.collapsed.has(n.id)
      rows.push({ node: n, expandable, expanded })
      if (expanded) n.children.forEach(walk)
    }
    model.roots.forEach(walk)
    return rows
  }
  const keep = new Set<TraceNode>()
  for (const n of model.nodes) {
    if (query && !searchText(n).includes(query)) continue
    if (kinds && ![...kinds].some((k) => isKind(n, k))) continue
    for (let a: TraceNode | undefined = n; a && !keep.has(a); a = a.parent)
      keep.add(a)
  }
  return model.nodes
    .filter((n) => keep.has(n))
    .map((n) => {
      const expandable = n.children.some((c) => keep.has(c))
      return { node: n, expandable, expanded: expandable }
    })
}

/** defaultCollapsed: rows deeper than two levels start folded, so a
 * subagent shows as one row with its summary bar. */
export function defaultCollapsed(model: TraceModel): Set<string> {
  const ids = new Set<string>()
  for (const n of model.nodes)
    if (n.depth >= 2 && n.children.length > 0) ids.add(n.id)
  return ids
}

/** allExpandable: every row that has children. */
export function allExpandable(model: TraceModel): Set<string> {
  return new Set(
    model.nodes.filter((n) => n.children.length > 0).map((n) => n.id)
  )
}

/** ancestorIds: the rows to unfold so `id` is on screen. */
export function ancestorIds(model: TraceModel, id: string): string[] {
  const out: string[] = []
  for (let n = model.byId.get(id)?.parent; n; n = n.parent) out.push(n.id)
  return out
}

/** failingNode: the step a failed run failed at: the failed call that
 * ended last, else the deepest failed agent. */
export function failingNode(model: TraceModel): TraceNode | undefined {
  let call: TraceNode | undefined
  let agent: TraceNode | undefined
  for (const n of model.nodes) {
    if (!n.failed) continue
    if (n.base === "agent") {
      if (!agent || n.depth >= agent.depth) agent = n
    } else if (!call || n.end >= call.end) call = n
  }
  return call ?? agent
}

/** waitingNode: the row of the wait that is still open. */
export function waitingNode(model: TraceModel): TraceNode | undefined {
  return (
    model.nodes.find((n) => n.kind === "waiting" && n.wait?.to === null) ??
    [...model.nodes].reverse().find((n) => n.kind === "waiting")
  )
}

/** landingSelection: what a run page opens with. A failed run opens on
 * the failing step, a waiting run on the waiting row, anything else
 * with nothing selected. */
export function landingSelection(
  model: TraceModel,
  status: string
): string | undefined {
  if (status === "failed") return failingNode(model)?.id
  if (status === "waiting") return waitingNode(model)?.id
  return undefined
}

// ── Figures ─────────────────────────────────────────────────────────

export interface TraceStats {
  /** Real milliseconds from the start to `at`. */
  elapsed: number
  /** …of which waiting on a person. */
  waited: number
  tokensIn: number
  tokensOut: number
  modelCalls: number
  toolCalls: number
  /** Calls that ended with an error. */
  failed: number
}

/** statsAt counts what had happened by the real time `at`: calls that
 * had started, tokens of the model calls that had ended. With `at` at
 * or past the end it is the whole trace. */
export function statsAt(model: TraceModel, at: number): TraceStats {
  const s: TraceStats = {
    elapsed: Math.max(0, Math.min(at, model.t1) - model.t0),
    waited: 0,
    tokensIn: 0,
    tokensOut: 0,
    modelCalls: 0,
    toolCalls: 0,
    failed: 0,
  }
  for (const w of model.waits) {
    const to = Math.min(at, w.to ?? model.t1)
    if (to > w.from) s.waited += to - w.from
  }
  for (const n of model.nodes) {
    if (n.start > at || n.kind === "marker") continue
    const ended = n.end <= at && !n.running
    if (n.base === "model") {
      s.modelCalls++
      if (ended) {
        s.tokensIn += n.tokensIn ?? 0
        s.tokensOut += n.tokensOut ?? 0
      }
    } else if (n.kind !== "waiting" && (n.base === "tool" || n.handed))
      s.toolCalls++
    else if (n.base === "handoff") s.toolCalls++
    if (ended && n.failed && n.base !== "agent") s.failed++
  }
  return s
}

export interface Segment {
  start: number
  end: number
  style: "solid" | "dim" | "failed" | "hatch"
  lane: Lane
}

/** summarySegments: an agent row's bar, its children on one line. */
export function summarySegments(n: TraceNode): Segment[] {
  return n.children
    .filter((c) => c.kind !== "marker")
    .map((c) => ({
      start: c.start,
      end: c.end,
      lane: c.lane,
      style:
        c.kind === "waiting"
          ? "hatch"
          : c.failed && c.base !== "agent"
            ? "failed"
            : c.base === "model"
              ? "solid"
              : "dim",
    }))
}

/** gist: the row in one line, for the hover tooltip. */
export function gist(n: TraceNode): string {
  if (n.kind === "marker") return n.label
  const what = n.handed
    ? "Handoff to"
    : n.kind === "waiting"
      ? "Waiting on"
      : n.base === "agent"
        ? "Agent"
        : n.base === "model"
          ? "Model call"
          : n.base === "tool" || n.base === "handoff"
            ? "Tool call"
            : "Span"
  const tail = n.failed
    ? ` · failed${n.spans[0]?.attrs["error.type"] ? ` (${String(n.spans[0].attrs["error.type"])})` : ""}`
    : n.running && n.kind !== "waiting"
      ? " · running"
      : ""
  if (n.kind === "waiting") return `${n.label}: ${n.tool ?? "a call"}${tail}`
  if (n.base === "model")
    return `${n.label}${n.model ? ` · ${n.model}` : ""}${tail}`
  return `${what} ${n.label}${n.model ? ` · ${n.model}` : ""}${tail}`
}
