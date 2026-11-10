// URL search schemas. Every view, filter and selection lives in the URL,
// so a link to any screen opens exactly that screen. Validators are plain
// functions (no schema library) and drop anything they do not know, a
// credential included.
//
// Pattern for a screen (CONVENTIONS.md, "URL state"):
//   export const Route = createFileRoute("/runs/")({ validateSearch: validateRuns, … })
//   const search = Route.useSearch()
//   const set = useSetSearch(Route.fullPath)   // set({ status: "failed" })

import { useNavigate } from "@tanstack/react-router"

import { FILTERABLE } from "./api/status"

/** A status the runs list can filter on. */
export type RunFilterStatus = (typeof FILTERABLE)[number]

type Raw = Record<string, unknown>

export const str = (v: unknown): string | undefined =>
  typeof v === "string" && v.trim() !== ""
    ? v
    : typeof v === "number"
      ? String(v)
      : undefined

export function oneOf<T extends string>(
  v: unknown,
  xs: readonly T[]
): T | undefined {
  return typeof v === "string" && (xs as readonly string[]).includes(v)
    ? (v as T)
    : undefined
}

export const num = (v: unknown): number | undefined => {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN
  return Number.isInteger(n) && n >= 0 ? n : undefined
}

/** Time windows, as the server's `since` takes them (a duration back
 * from now). "all" sends no bound. */
export const RANGES = ["1h", "24h", "7d", "30d"] as const
export type Range = (typeof RANGES)[number]
export const RANGE_LABEL: Record<Range, string> = {
  "1h": "Last hour",
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  "30d": "Last 30 days",
}
const RANGE_MS: Record<Range, number> = {
  "1h": 3_600_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
  "30d": 2_592_000_000,
}
/** sinceOf is the RFC 3339 start of a window, rounded down to the minute
 * so the query key (and the cache entry) is stable while a page is open. */
export function sinceOf(range: Range, now = Date.now()): string {
  const t = Math.floor((now - RANGE_MS[range]) / 60_000) * 60_000
  return new Date(t).toISOString()
}

/** useSetSearch merges a patch into a route's search; undefined removes
 * a key. Filters replace the history entry, they do not push one. */
export function useSetSearch<T extends object>(from: string) {
  const navigate = useNavigate()
  return (patch: Partial<T>, opts: { push?: boolean } = {}) =>
    void navigate({
      to: from,
      search: ((prev: Raw) => ({ ...prev, ...patch })) as never,
      replace: !opts.push,
    } as never)
}

// ── Overview ────────────────────────────────────────────────────────
/** The Overview's windows (its stats are bucketed per window). */
export const WINDOWS = ["1h", "24h", "7d"] as const
export type Window = (typeof WINDOWS)[number]
/** What the Overview's Breakdown table groups by. */
export const BREAKDOWNS = ["agent", "model", "tool", "error"] as const
export type Breakdown = (typeof BREAKDOWNS)[number]
export interface OverviewSearch {
  range?: Window
  by?: Breakdown
}
export const validateOverview = (s: Raw): OverviewSearch => ({
  range: oneOf(s.range, WINDOWS),
  by: oneOf(s.by, BREAKDOWNS),
})

// ── Runs ────────────────────────────────────────────────────────────
export const RUNS_TABS = ["runs", "traces"] as const
export interface RunsSearch {
  tab?: "traces"
  q?: string
  status?: RunFilterStatus
  agent?: string
  range?: Range
  // The "+ Filter" menu:
  tool?: string
  model?: string
  /** An error kind (the server's error_type): max_steps, model_error, … */
  error?: string
  /** USD at least. */
  min_cost?: number
  min_tokens?: number
  /** One metadata match, "key=value". */
  tag?: string
  session?: string
  /** Subagent runs too, not only top-level ones. */
  sub?: true
  sort?: RunsSort
}
/** The columns the server can order by; "started" is the default. */
export const RUNS_SORTS = ["duration", "tokens", "cost"] as const
export type RunsSort = (typeof RUNS_SORTS)[number]
const amount = (v: unknown): number | undefined => {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN
  return Number.isFinite(n) && n > 0 ? n : undefined
}
const tag = (v: unknown): string | undefined => {
  const t = str(v)
  return t && t.indexOf("=") > 0 && !t.endsWith("=") ? t : undefined
}
export const validateRuns = (s: Raw): RunsSearch => ({
  tab: oneOf(s.tab, ["traces"] as const),
  q: str(s.q),
  status: oneOf(s.status, FILTERABLE),
  agent: str(s.agent),
  range: oneOf(s.range, RANGES),
  tool: str(s.tool),
  model: str(s.model),
  error: str(s.error),
  min_cost: amount(s.min_cost),
  min_tokens: num(s.min_tokens) || undefined,
  tag: tag(s.tag),
  session: str(s.session),
  sub: s.sub === true || s.sub === "true" || s.sub === 1 ? true : undefined,
  sort: oneOf(s.sort, RUNS_SORTS),
})

export const RUN_TABS = ["timeline", "transcript", "logs", "details"] as const
export interface RunSearch {
  tab?: (typeof RUN_TABS)[number]
  /** The span selected in the waterfall. */
  span?: string
  /** The step the transcript is shown as of. */
  step?: number
}
export const validateRun = (s: Raw): RunSearch => ({
  tab: oneOf(s.tab, RUN_TABS),
  span: str(s.span),
  step: num(s.step),
})

// ── Sessions ────────────────────────────────────────────────────────
/** What GET /v1/sessions can filter on: the newest turn's status, a
 * turn waiting on a person, or any failed turn. */
export const SESSION_STATES = [
  "running",
  "waiting",
  "failed",
  "succeeded",
  "interrupted",
] as const
export interface SessionsSearch {
  q?: string
  state?: (typeof SESSION_STATES)[number]
  agent?: string
}
export const validateSessions = (s: Raw): SessionsSearch => ({
  q: str(s.q),
  state: oneOf(s.state, SESSION_STATES),
  agent: str(s.agent),
})

export interface SessionSearch {
  /** The turn whose waterfall is open. */
  turn?: number
  span?: string
}
export const validateSession = (s: Raw): SessionSearch => ({
  turn: num(s.turn),
  span: str(s.span),
})

// ── Traces, agents, approvals ───────────────────────────────────────
export interface TraceSearch {
  span?: string
}
export const validateTrace = (s: Raw): TraceSearch => ({ span: str(s.span) })

export interface AgentsSearch {
  tab?: "tools"
  range?: Window
}
export const validateAgents = (s: Raw): AgentsSearch => ({
  tab: oneOf(s.tab, ["tools"] as const),
  range: oneOf(s.range, WINDOWS),
})

export interface AgentSearch {
  /** The version number whose capabilities are shown (default: newest). */
  v?: number
}
export const validateAgent = (s: Raw): AgentSearch => ({ v: num(s.v) })

/** pending, decided (any outcome), or one outcome. */
export const APPROVAL_STATUSES = [
  "pending",
  "decided",
  "approved",
  "denied",
  "resolved",
  "expired",
  "cancelled",
] as const
export interface ApprovalsSearch {
  /** The approval open in the detail. */
  id?: string
  status?: (typeof APPROVAL_STATUSES)[number]
  agent?: string
  tool?: string
  /** Only one run's approvals. */
  run?: string
}
export const validateApprovals = (s: Raw): ApprovalsSearch => ({
  id: str(s.id),
  status: oneOf(s.status, APPROVAL_STATUSES),
  agent: str(s.agent),
  tool: str(s.tool),
  run: str(s.run),
})
