// Compare two runs: GET /v1/diff turned into the rows the Compare page
// draws. Pure (no React, no network), tested on captured bodies
// (src/test/compare.test.ts).
//
// The server decides what differs: a row's `changed`, `changes`,
// `unknown` and `differs` are taken as they come. This file only adds
// what a person needs to read a row:
//   - the row's name, as the waterfall names a model call ("Call
//     lookup_order", "Hand off to billing", "Reply");
//   - inside a column the server flagged, which call it is (another
//     tool, the same tool with other arguments, a call one side lacks);
//   - a step's duration and model, which /v1/diff does not carry, read
//     from the run's spans;
//   - "outcome" when the two steps ended differently (ok / error /
//     parked), which /v1/diff reports per side but does not compare.
//
// A run that parked and was resumed is one logical run: the page diffs
// the two chains run by run and hands the documents here as segments.

import type {
  DiffColumn,
  DiffDoc,
  DiffRow,
  DiffSide,
  Hole,
  Span,
} from "./api/types"

/** One /v1/diff document of the comparison. `only`: one side has no run
 * at this place of its chain; the document is that run against itself
 * and only its own side is kept. */
export interface CompareSegment {
  doc: DiffDoc
  only?: "a" | "b"
}

export type CallMark = "tool" | "args" | "only"

export interface CompareCall {
  name: string
  args: unknown
  /** How it differs from the call at the same place on the other side:
   * another tool, other arguments, or no call there. undefined: same. */
  mark?: CallMark
  /** A call to a subagent. */
  handoff: boolean
  /** Absent while the call has no result (parked, running, failed). */
  result?: { content: string; isError: boolean; differs: boolean }
}

export interface CompareStep {
  runId: string
  /** The step's ordinal in its own run. */
  step: number
  name: string
  /** ok | parked | error | … */
  status: string
  failed: boolean
  parked: boolean
  calls: CompareCall[]
  /** null: not recorded. */
  text: string | null
  textDiffers: boolean
  inputTokens: number
  outputTokens: number
  tokensDiffer: boolean
  /** From the run's spans; undefined when they do not say. */
  durationMs?: number
  model?: string
  holes: Hole[]
}

/** same: nothing differs. changed: something does. only_a / only_b: one
 * run has no step here. kept: a step the replaying run did not run again.
 * unknown: could not be compared. */
export type RowKind =
  "same" | "changed" | "only_a" | "only_b" | "kept" | "unknown"

/** What differs in a row, in the order it is listed. */
export type Difference =
  | "outcome"
  | "tool"
  | "arguments"
  | "result"
  | "reply"
  | "system prompt"
  | "tokens"

export interface CompareRow {
  key: string
  /** Which run of the chain (0: the first). */
  segment: number
  /** The aligned step ordinal, as the server numbers it. */
  step: number
  kind: RowKind
  what: Difference[]
  /** Columns that could not be compared, in words. */
  unknown: string[]
  a: CompareStep | null
  b: CompareStep | null
  /** The side that kept the source's step (two replays of one run). */
  keptBy?: "a" | "b"
}

export interface CompareModel {
  rows: CompareRow[]
  /** Rows that differ (changed, or only one side has the step). */
  differing: number
  same: number
  kept: number
  /** How many runs each side's chain has compared here. */
  segments: number
  /** The server's alignment note for a replay, in words. */
  aligned?: string
  holes: Hole[]
}

// ── Steps from spans ────────────────────────────────────────────────

export interface StepFacts {
  durationMs?: number
  model?: string
  /** The tools of the step that are subagents. */
  handoffs: Set<string>
}

const num = (v: unknown) => (typeof v === "number" ? v : undefined)
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined)

/** stepFacts reads what /v1/diff does not carry from a run's spans: per
 * step of `runId`, the time from its model call's start to the end of
 * its last tool call, the model that answered, and which of its tools
 * are subagents. */
export function stepFacts(
  spans: readonly Span[],
  runId: string
): Map<number, StepFacts> {
  const out = new Map<number, StepFacts>()
  const bounds = new Map<number, [number, number]>()
  for (const s of spans) {
    if (s.run_id !== undefined && s.run_id !== runId) continue
    const step = num(s.attrs["weft.step.index"])
    const op = str(s.attrs["gen_ai.operation.name"])
    if (step === undefined || !op || op === "invoke_agent") continue
    const f = out.get(step) ?? { handoffs: new Set<string>() }
    out.set(step, f)
    if (op === "execute_tool") {
      const tool = str(s.attrs["gen_ai.tool.name"])
      if (tool && s.attrs["weft.tool.origin"] === "subagent")
        f.handoffs.add(tool)
    } else {
      f.model =
        str(s.attrs["gen_ai.response.model"]) ??
        str(s.attrs["gen_ai.request.model"]) ??
        f.model
    }
    const from = Date.parse(s.start)
    const to = Date.parse(s.end)
    if (Number.isNaN(from) || Number.isNaN(to)) continue
    const b = bounds.get(step)
    bounds.set(
      step,
      b ? [Math.min(b[0], from), Math.max(b[1], to)] : [from, to]
    )
  }
  for (const [step, [from, to]] of bounds) {
    const f = out.get(step)
    if (f) f.durationMs = Math.max(0, to - from)
  }
  return out
}

// ── Rows ────────────────────────────────────────────────────────────

const same = (x: unknown, y: unknown) => JSON.stringify(x) === JSON.stringify(y)

const list = (names: string[]) =>
  `${names.slice(0, 2).join(", ")}${names.length > 2 ? ` +${names.length - 2}` : ""}`

/** stepName names a step by what the model did, as the waterfall names
 * a model call (trace-waterfall/model.ts, nameModelCalls). */
export function stepName(
  calls: readonly CompareCall[],
  side: DiffSide
): string {
  if (calls.length > 0) {
    const names = calls.map((c) => c.name)
    return calls.every((c) => c.handoff)
      ? `Hand off to ${list(names)}`
      : `Call ${list(names)}`
  }
  return side.text ? "Reply" : "Model call"
}

function callsOf(
  side: DiffSide,
  other: DiffSide | null,
  flagged: { calls: boolean; results: boolean },
  facts: StepFacts | undefined
): CompareCall[] {
  // Results come in call order; a call without one (parked, running)
  // is skipped, so walk both lists together by name.
  let next = 0
  const lone = side.marks.includes("subagent") && side.tool_calls.length === 1
  return side.tool_calls.map((c, i) => {
    const r = side.tool_results.at(next)
    const result = r && r.name === c.name ? r : undefined
    if (result) next++
    const theirs = other?.tool_calls.at(i)
    let mark: CallMark | undefined
    if (flagged.calls && other) {
      if (!theirs) mark = "only"
      else if (theirs.name !== c.name) mark = "tool"
      else if (!same(theirs.args, c.args)) mark = "args"
    }
    const theirResult = other?.tool_results.at(next - 1)
    return {
      name: c.name,
      args: c.args,
      mark,
      handoff: facts ? facts.handoffs.has(c.name) : lone,
      result: result && {
        content: result.content,
        isError: result.is_error,
        differs:
          flagged.results &&
          !!other &&
          (!theirResult ||
            theirResult.content !== result.content ||
            theirResult.is_error !== result.is_error),
      },
    }
  })
}

function stepOf(
  runId: string,
  step: number,
  side: DiffSide,
  other: DiffSide | null,
  row: Pick<DiffRow, "changes" | "differs">,
  facts: StepFacts | undefined
): CompareStep {
  const has = (c: DiffColumn) => row.changes.includes(c)
  const calls = callsOf(
    side,
    other,
    { calls: has("tool_calls"), results: has("tool_results") },
    facts
  )
  return {
    runId,
    step,
    name: stepName(calls, side),
    status: side.status,
    failed: side.status === "error",
    parked: side.status === "parked",
    calls,
    text: side.text,
    textDiffers: has("text") && !!other,
    inputTokens: side.usage.input_tokens,
    outputTokens: side.usage.output_tokens,
    tokensDiffer: (has("usage") || row.differs.includes("usage")) && !!other,
    durationMs: facts?.durationMs,
    model: facts?.model,
    holes: side.holes,
  }
}

const COLUMN: Record<DiffColumn, string> = {
  system: "system prompt",
  tool_calls: "tool calls",
  tool_results: "tool results",
  text: "reply",
  usage: "tokens",
  missing: "step",
}

function differences(
  row: DiffRow,
  a: CompareStep | null,
  b: CompareStep | null
): Difference[] {
  const out: Difference[] = []
  if (a && b && a.status !== b.status) out.push("outcome")
  if (row.changes.includes("tool_calls")) {
    const marks = [...(a?.calls ?? []), ...(b?.calls ?? [])].map((c) => c.mark)
    const tool = marks.includes("tool") || marks.includes("only")
    if (tool) out.push("tool")
    if (marks.includes("args") || !tool) out.push("arguments")
  }
  if (row.changes.includes("tool_results")) out.push("result")
  if (row.changes.includes("text")) out.push("reply")
  if (row.changes.includes("system")) out.push("system prompt")
  if (row.changes.includes("usage")) out.push("tokens")
  return out
}

/** Facts of every run of a side, by run id. */
export type FactsOf = (runId: string) => Map<number, StepFacts> | undefined

/** buildCompare turns the diff documents of two (chains of) runs into
 * rows. `facts` supplies what the runs' spans add (stepFacts). */
export function buildCompare(
  segments: readonly CompareSegment[],
  facts: FactsOf = () => undefined
): CompareModel {
  const rows: CompareRow[] = []
  const holes: Hole[] = []
  let aligned: string | undefined
  segments.forEach(({ doc, only }, segment) => {
    holes.push(...doc.holes)
    if (doc.aligned && !only) aligned = alignedNote(doc)
    const fa = facts(doc.a.run_id)
    const fb = facts(doc.b.run_id)
    for (const r of doc.steps) {
      // A one-sided segment is a run against itself: keep its own side.
      const sa = only === "b" ? null : r.a
      const sb = only === "a" ? null : r.b
      const flags = only ? { changes: [], differs: [] } : r
      const a =
        sa &&
        stepOf(
          doc.a.run_id,
          r.a_step ?? r.step,
          sa,
          sb,
          flags,
          fa?.get(r.a_step ?? r.step)
        )
      const b =
        sb &&
        stepOf(
          doc.b.run_id,
          r.b_step ?? r.step,
          sb,
          sa,
          flags,
          fb?.get(r.b_step ?? r.step)
        )
      const what = only ? [] : differences(r, a, b)
      let kind: RowKind
      if (r.kept && !only) kind = "kept"
      else if (!a && !b) kind = "unknown"
      else if (!b) kind = "only_a"
      else if (!a) kind = "only_b"
      else if (r.changed || what.length > 0) kind = "changed"
      else if (r.unknown.length > 0) kind = "unknown"
      else kind = "same"
      rows.push({
        key: `${segment}:${r.step}`,
        segment,
        step: r.step,
        kind,
        what,
        unknown: only ? [] : r.unknown.map((c) => COLUMN[c]),
        a,
        b,
        keptBy: only ? undefined : r.kept_by,
      })
    }
  })
  const n = (...kinds: RowKind[]) =>
    rows.filter((r) => kinds.includes(r.kind)).length
  return {
    rows,
    differing: n("changed", "only_a", "only_b"),
    same: n("same"),
    kept: n("kept"),
    segments: segments.length,
    aligned,
    holes,
  }
}

function alignedNote(doc: DiffDoc): string | undefined {
  const al = doc.aligned
  if (!al) return undefined
  if (al.replay === "both")
    return `Both runs were taken from ${al.source}: A from its step #${al.a_from}, B from its step #${al.b_from}. Steps are numbered as in that run.`
  const [side, from] = al.replay === "a" ? ["A", al.a_from] : ["B", al.b_from]
  return `${side} was taken from the other run at its step #${from}: the steps before it were kept, not run again.`
}

// ── What the page lists ─────────────────────────────────────────────

export type CompareItem =
  | { type: "row"; row: CompareRow }
  /** A stretch of steps that are the same, folded to one line. */
  | { type: "fold"; key: string; rows: CompareRow[] }
  /** Between two runs of a chain: the run waited on a person here. */
  | { type: "resumed"; key: string }

const quiet = (r: CompareRow) => r.kind === "same" || r.kind === "kept"

/** itemsOf lists the rows in order. Unless `all`, each stretch of steps
 * that do not differ is folded into one line, so the differences stand
 * out. */
export function itemsOf(model: CompareModel, all: boolean): CompareItem[] {
  const out: CompareItem[] = []
  let fold: CompareRow[] = []
  const flush = () => {
    if (fold.length > 0)
      out.push({ type: "fold", key: `fold:${fold[0].key}`, rows: fold })
    fold = []
  }
  model.rows.forEach((row, i) => {
    if (i > 0 && model.rows[i - 1].segment !== row.segment) {
      flush()
      out.push({ type: "resumed", key: `resumed:${row.segment}` })
    }
    if (!all && quiet(row)) fold.push(row)
    else {
      flush()
      out.push({ type: "row", row })
    }
  })
  flush()
  return out
}

/** pairChains lines two chains up run by run: the i-th run of one with
 * the i-th of the other. Where one chain is shorter, the other's run is
 * compared with itself and shown alone (`only`). */
export function pairChains(
  a: readonly string[],
  b: readonly string[]
): { a: string; b: string; only?: "a" | "b" }[] {
  const out: { a: string; b: string; only?: "a" | "b" }[] = []
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a.at(i)
    const y = b.at(i)
    if (x !== undefined && y !== undefined) out.push({ a: x, b: y })
    else if (x !== undefined) out.push({ a: x, b: x, only: "a" })
    else if (y !== undefined) out.push({ a: y, b: y, only: "b" })
  }
  return out
}

/** summaryOf: the one line over the rows. */
export function summaryOf(model: CompareModel): string {
  const total = model.rows.length
  if (total === 0) return "Neither run recorded a step."
  const steps = (n: number) => `${n} ${n === 1 ? "step" : "steps"}`
  if (model.differing === 0) {
    const unknown = model.rows.filter((r) => r.kind === "unknown").length
    return unknown > 0
      ? `No difference found; ${steps(unknown)} could not be compared.`
      : `The two runs took the same ${steps(total)}.`
  }
  const first = model.rows.find(
    (r) => r.kind === "changed" || r.kind === "only_a" || r.kind === "only_b"
  )
  return `${model.differing} of ${steps(total)} differ${model.differing === 1 ? "s" : ""}${first ? `, from step #${first.step}` : ""}.`
}
