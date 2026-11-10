// The Overview's numbers, worked out from GET /v1/stats. Pure.
//
// by=total gives the window's runs by display status (the tiles and the
// chart). The Breakdown table reads one grouped answer per segment:
//   agent  top-level runs; `outcomes` (display status) is preferred to
//          the trace store's runs / failed, whose failed counts
//          cancelled runs
//   model  every run that used the model; rates are per model call
//   tool   every run that called the tool; rates are per call
//   error  failed runs by error kind (cancelled is not an error)

import type { OutcomeBucket, Stats, StatsGroup } from "./api/types"
import { DASH, cost, count, percent, tokens, usd } from "./format"
import type { Breakdown, RunsSearch } from "./search"

export interface Figure {
  value: string
  hint?: string
  title?: string
  tone?: "default" | "danger"
}

/** The four tiles: runs, failure rate, cost, tokens. */
export function figuresOf(t: OutcomeBucket): {
  runs: Figure
  failureRate: Figure
  cost: Figure
  tokens: Figure
} {
  const finished = t.succeeded + t.failed
  const open = [
    t.waiting > 0 && `${count(t.waiting)} waiting`,
    t.running > 0 && `${count(t.running)} running`,
  ].filter(Boolean)
  const used = t.input_tokens + t.output_tokens
  return {
    runs: {
      value: count(t.runs),
      hint: open.join(" · ") || undefined,
    },
    failureRate: {
      value: percent(t.failure_rate),
      hint:
        finished > 0
          ? `${count(t.failed)} of ${count(finished)} failed`
          : undefined,
      title:
        "Failed runs out of the runs that finished (succeeded or failed). Cancelled runs are not failures.",
      tone: t.failed > 0 ? "danger" : "default",
    },
    cost: {
      value:
        used === 0
          ? DASH
          : t.cost_known
            ? usd(t.cost_usd)
            : `≥ ${usd(t.cost_usd)}`,
      title: t.cost_known
        ? undefined
        : "Some runs used a model with no price in the pricing table: this is the priced part.",
    },
    tokens: {
      value: used === 0 ? DASH : tokens(used),
      hint:
        used === 0
          ? undefined
          : `${tokens(t.input_tokens)} in · ${tokens(t.output_tokens)} out`,
    },
  }
}

/** One bar of a sparkline: a count and the failed part of it. */
export interface SparkPoint {
  total: number
  failed: number
}

export interface BreakdownRow {
  key: string
  runs: number
  /** Calls of the tool or model (a per-call row). */
  calls?: number
  /** null: not a rate that applies (an error kind is all failures). */
  failureRate: number | null
  /** What the rate counts: "2 of 12 finished runs failed". */
  failureNote?: string
  p50: number | undefined
  p95: number | undefined
  /** undefined: none to count (a tool call has no tokens). */
  tokens: number | undefined
  cost: string
  costTitle?: string
  spark: SparkPoint[]
  /** The Runs list filters this row's numbers link to. */
  link: Partial<RunsSearch>
  /** The same, narrowed to the failures. */
  failedLink: Partial<RunsSearch>
}

export const BREAKDOWN_LABEL: Record<Breakdown, string> = {
  agent: "Agent",
  model: "Model",
  tool: "Tool",
  error: "Error kind",
}

/** What a segment's rows count, where they do not add up to the
 * figures above (a run that used two models is in two rows): the
 * table's caption. The agent and error-kind rows are plain top-level
 * runs and need none. */
export const BREAKDOWN_NOTE: Partial<Record<Breakdown, string>> = {
  model:
    "Every run that used the model, subagent runs included. Failure rate and durations are per model call.",
  tool: "Every run that called the tool, subagent runs included. Failure rate and durations are per call.",
}

const ms = (d: { p50: number; p95: number } | null | undefined) => ({
  p50: d?.p50,
  p95: d?.p95,
})

function agentRow(g: StatsGroup): BreakdownRow {
  const o = g.outcomes
  if (!o) return callRow(g, "runs", { agent: g.key })
  const finished = o.succeeded + o.failed
  const used = o.input_tokens + o.output_tokens
  return {
    key: g.key,
    runs: o.runs,
    failureRate: o.failure_rate,
    failureNote:
      finished > 0
        ? `${count(o.failed)} of ${count(finished)} finished runs failed`
        : "No run has finished",
    ...ms(o.duration_ms),
    tokens: used > 0 ? used : undefined,
    cost:
      used === 0
        ? DASH
        : o.cost_known
          ? usd(o.cost_usd)
          : `≥ ${usd(o.cost_usd)}`,
    costTitle: o.cost_known
      ? undefined
      : "Some of these runs used a model with no price: this is the priced part.",
    spark: (g.outcome_series ?? []).map((b) => ({
      total: b.runs,
      failed: b.failed,
    })),
    link: { agent: g.key },
    failedLink: { agent: g.key, status: "failed" },
  }
}

/** A row whose rate is per call (a model, a tool). */
function callRow(
  g: StatsGroup,
  unit: string,
  link: Partial<RunsSearch>
): BreakdownRow {
  const used = g.input_tokens + g.output_tokens
  return {
    key: g.key,
    runs: g.runs,
    calls: g.calls,
    failureRate: g.calls > 0 ? g.failure_rate : null,
    failureNote:
      g.calls > 0
        ? `${count(g.failed_calls)} of ${count(g.calls)} ${unit} failed`
        : undefined,
    ...ms(g.duration_ms),
    tokens: used > 0 ? used : undefined,
    cost: used > 0 ? cost(g) : DASH,
    costTitle: used > 0 ? g.cost_reason : undefined,
    spark: g.series.map((b) => ({ total: b.calls, failed: b.failed_calls })),
    link,
    failedLink: { ...link, status: "failed" },
  }
}

function errorRow(g: StatsGroup): BreakdownRow {
  const used = g.input_tokens + g.output_tokens
  const link: Partial<RunsSearch> = { error: g.key, status: "failed" }
  return {
    key: g.key,
    runs: g.runs,
    failureRate: null,
    ...ms(g.duration_ms),
    tokens: used > 0 ? used : undefined,
    cost: used > 0 ? cost(g) : DASH,
    costTitle: used > 0 ? g.cost_reason : undefined,
    spark: g.series.map((b) => ({ total: b.runs, failed: b.failed })),
    link,
    failedLink: link,
  }
}

/** breakdownRows turns one grouped stats answer into the table's rows,
 * most runs first. */
export function breakdownRows(by: Breakdown, stats: Stats): BreakdownRow[] {
  const rows = stats.groups
    .filter((g) => g.key !== "" && !(by === "error" && g.key === "cancelled"))
    .map((g) =>
      by === "agent"
        ? agentRow(g)
        : by === "model"
          ? callRow(g, "model calls", { model: g.key, sub: true })
          : by === "tool"
            ? callRow(g, "calls", { tool: g.key, sub: true })
            : errorRow(g)
    )
  return rows.sort((a, b) => b.runs - a.runs || a.key.localeCompare(b.key))
}
