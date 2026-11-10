// What a row of a runs list says, worked out once from the server's row.
// Pure: the columns (components/console/run-columns.tsx) only render it.
//
// A park and its resumes are one row (GET /v1/runs?chains=1): the head
// row carries `chain`, and the chain's status, times, steps, usage and
// cost are the row's.

import { displayStatus } from "./api/status"
import type { DisplayStatus } from "./api/status"
import type { RunRow, RunsList, RunsQuery, Usage } from "./api/types"
import { DASH, cost, count, usd } from "./format"
import type { RunsSearch } from "./search"

const isStatus = (v: string): v is DisplayStatus =>
  [
    "queued",
    "running",
    "waiting",
    "succeeded",
    "failed",
    "cancelled",
    "interrupted",
  ].includes(v)

/** The status to show for a row: its chain's when it has one. */
export function rowStatus(r: RunRow): DisplayStatus {
  const s = r.chain?.display_status
  return s && isStatus(s) ? s : displayStatus(r)
}

/** Why the row has that status, in a few words; undefined when the
 * status says it all. For the tooltip of the status. */
export function rowCause(r: RunRow): string | undefined {
  const c = r.chain
  const status = rowStatus(r)
  const errorType = c?.error_type ?? r.error_type
  const stoppedBy = c?.stopped_by ?? r.stopped_by
  const cancelCause = c?.cancel_cause ?? r.cancel_cause
  const err = (c?.err ?? r.err) || undefined
  switch (status) {
    case "failed":
      return [errorType, err].filter(Boolean).join(": ") || undefined
    case "cancelled":
      return cancelCause ? `by ${cancelCause}` : undefined
    case "waiting": {
      const tools = pendingTools(r)
      return tools.length > 0
        ? `${tools.join(", ")} needs a decision`
        : undefined
    }
    case "succeeded":
      return stoppedBy && stoppedBy !== "final_answer" && stoppedBy !== "parked"
        ? `stopped by ${stoppedBy}`
        : undefined
    default:
      return undefined
  }
}

export interface RowTimes {
  started: string | undefined
  /** null: not finished. */
  finished: string | null
  /** The whole span, waits included; undefined while it is open. */
  durationMs: number | undefined
  /** Of durationMs, the time spent waiting on a person. */
  waitingMs: number
  /** When the open wait began (a waiting row). */
  waitingSince: string | undefined
}

export function rowTimes(r: RunRow): RowTimes {
  const c = r.chain
  const started = (c?.started ?? r.started) || undefined
  const finished = c ? c.finished : r.finished
  const open = c?.waits?.find((w) => w.to === null)
  const span =
    started && finished ? Date.parse(finished) - Date.parse(started) : undefined
  return {
    started,
    finished,
    durationMs:
      span === undefined || Number.isNaN(span) ? undefined : Math.max(0, span),
    waitingMs: finished ? (c?.waiting_ms ?? 0) : 0,
    waitingSince: open?.from,
  }
}

export const rowSteps = (r: RunRow): number => r.chain?.steps ?? r.steps
export const rowUsage = (r: RunRow): Usage => r.chain?.usage ?? r.usage

/** Tokens of the row; undefined when it used none (nothing to count is
 * a dash, not "0"). */
export function rowTokens(r: RunRow): number | undefined {
  const u = rowUsage(r)
  const n = u.input_tokens + u.output_tokens
  return n > 0 ? n : undefined
}

/** Whether the row handed work to subagents whose cost is not known. */
const delegatedUnknown = (r: RunRow) =>
  !r.delegated_cost_known &&
  (r.delegated_usage?.input_tokens ?? 0) +
    (r.delegated_usage?.output_tokens ?? 0) >
    0

/** The cost cell: the run's whole bill, its own model calls plus its
 * subagents' (the tokens beside it are the whole run's too). A part
 * with no price makes it "≥ $x"; a run that used no tokens has no cost
 * to state: a dash, never "$0" and never "unknown". */
export function rowCost(r: RunRow): string {
  if (rowTokens(r) === undefined) return DASH
  const c = r.chain
  if (c) return c.cost_known ? usd(c.cost_usd) : `≥ ${usd(c.cost_usd)}`
  if (!r.cost_known || r.cost_usd === null) return cost(r)
  const whole = r.cost_usd + (r.delegated_cost_usd ?? 0)
  return delegatedUnknown(r) ? `≥ ${usd(whole)}` : usd(whole)
}

/** Whether the row's cost is not fully known although it used tokens. */
export function costUnknown(r: RunRow): boolean {
  if (rowTokens(r) === undefined) return false
  if (r.chain) return !r.chain.cost_known
  return !r.cost_known || delegatedUnknown(r)
}

/** One line for under a list: how many of the rows have no full cost,
 * and why. undefined when every cost is known. */
export function unpricedLine(rows: readonly RunRow[]): string | undefined {
  const unknown = rows.filter(costUnknown)
  if (unknown.length === 0) return undefined
  const models = new Set<string>()
  const reasons = new Set<string>()
  for (const r of unknown) {
    const ms = [
      ...(r.unpriced_models ?? []),
      ...(r.delegated_unpriced_models ?? []),
    ]
    ms.forEach((m) => models.add(m))
    const why = r.cost_known ? r.delegated_cost_reason : r.cost_reason
    if (ms.length === 0 && why) reasons.add(why)
  }
  const n = unknown.length
  const head = `${count(n)} ${n === 1 ? "run has" : "runs have"} no full cost`
  if (models.size > 0)
    return `${head}: no price for ${[...models].sort().join(", ")}.`
  if (reasons.size > 0) return `${head}: ${[...reasons].sort().join("; ")}.`
  return `${head}.`
}

const COUNTERS: [keyof RunRow, string, string][] = [
  ["attempts_retried", "retried attempt", "retried attempts"],
  ["fallbacks", "fallback", "fallbacks"],
  ["tool_errors", "tool error", "tool errors"],
  ["denied", "denied call", "denied calls"],
  ["truncated", "truncated output", "truncated outputs"],
  ["repaired", "repaired arguments", "repaired arguments"],
  ["log_warn", "log warning", "log warnings"],
  ["log_error", "log error", "log errors"],
]

/** The row's issue counters as phrases ("4 tool errors"), for the one
 * issues glyph's tooltip. Empty: no glyph. */
export function rowIssues(r: RunRow): string[] {
  const out: string[] = []
  for (const [key, one, many] of COUNTERS) {
    const n = r[key]
    if (typeof n === "number" && n > 0)
      out.push(`${count(n)} ${n === 1 ? one : many}`)
  }
  return out
}

/** "3 subagents", and whether one of them failed. */
export function rowChildren(
  r: RunRow
): { label: string; failed: boolean } | undefined {
  const n = r.children_count ?? 0
  if (n === 0) return undefined
  return {
    label: `${count(n)} ${n === 1 ? "subagent" : "subagents"}`,
    failed: (r.children_failed ?? 0) > 0,
  }
}

/** The version chip: "v7", and the label the run resolved through
 * ("prod") when it came in by one. undefined for a run the server did
 * not admit (a trace only). */
export function rowVersion(
  r: RunRow
): { number: number; label?: string } | undefined {
  const v = r.agent_version
  if (!v?.number) return undefined
  const via = r.resolved_via?.startsWith("label:")
    ? r.resolved_via.slice("label:".length)
    : undefined
  return { number: v.number, label: via }
}

/** runsRows: the rows of the loaded pages, each run once. A search is
 * offset-paged, so a run that moved between two page reads (new runs
 * came in on top) can come back on the next page. */
export function runsRows(pages: readonly Pick<RunsList, "runs">[]): RunRow[] {
  const seen = new Set<string>()
  const out: RunRow[] = []
  for (const p of pages)
    for (const r of p.runs) {
      if (seen.has(r.id)) continue
      seen.add(r.id)
      out.push(r)
    }
  return out
}

/** runsCount: what a list of runs says about its size. The server's
 * total is exact only when it says so (total_exact); otherwise it is an
 * upper bound (counted before a park and its resumes fold into one row),
 * and the list says what it knows: every row once all are loaded, else
 * "at least" the rows it has. */
export function runsCount(
  pages:
    | readonly Pick<RunsList, "total" | "total_exact" | "runs" | "partial">[]
    | undefined,
  more: boolean
): { n: number; text: string; title?: string; more?: string } | undefined {
  const first = pages?.[0]
  if (!pages || !first) return undefined
  const word = (n: number) => (n === 1 ? "run" : "runs")
  if (first.total_exact)
    return {
      n: first.total,
      text: `${count(first.total)} ${word(first.total)}`,
    }
  const loaded = runsRows(pages).length
  // The server stopped reading (its budget, or the deepest offset it
  // pages to): what is loaded is all there is to show.
  if (!more && pages.some((p) => p.partial))
    return {
      n: loaded,
      text: `${count(loaded)} ${word(loaded)}`,
      more: "more results not shown",
      title:
        "The server stopped reading before the end: more runs may match. Narrow the filters to see them.",
    }
  if (!more) return { n: loaded, text: `${count(loaded)} ${word(loaded)}` }
  return {
    n: loaded,
    text: `${count(loaded)}+ runs`,
    title: `At most ${count(first.total)}: the server counts before a wait and what continued it are folded into one run.`,
  }
}

/** toRunsQuery maps the URL's filters onto GET /v1/runs parameters.
 * since: the start of s.range (search.ts sinceOf), worked out by the
 * caller so the query key stays stable. */
export function toRunsQuery(s: RunsSearch, since?: string): RunsQuery {
  return {
    q: s.q,
    display_status: s.status,
    agent: s.agent,
    since: s.range ? since : undefined,
    tool: s.tool,
    model: s.model,
    error_type: s.error,
    min_cost: s.min_cost,
    min_tokens: s.min_tokens,
    tag: s.tag,
    session: s.session,
    parent: s.sub ? "*" : undefined,
    sort: s.sort,
    limit: 50,
  }
}

/** The tools a waiting row waits on a decision for. */
function pendingTools(r: RunRow): string[] {
  const names = new Set<string>()
  for (const w of r.chain?.waits ?? [])
    if (w.to === null)
      for (const a of w.approvals ?? [])
        if (a.status === "pending") names.add(a.tool)
  return [...names]
}
