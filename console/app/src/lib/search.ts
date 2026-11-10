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
export interface OverviewSearch {
  range?: Range
}
export const validateOverview = (s: Raw): OverviewSearch => ({
  range: oneOf(s.range, RANGES),
})

// ── Runs ────────────────────────────────────────────────────────────
export const RUNS_TABS = ["runs", "traces"] as const
export interface RunsSearch {
  tab?: "traces"
  q?: string
  status?: RunFilterStatus
  agent?: string
  range?: Range
}
export const validateRuns = (s: Raw): RunsSearch => ({
  tab: oneOf(s.tab, ["traces"] as const),
  q: str(s.q),
  status: oneOf(s.status, FILTERABLE),
  agent: str(s.agent),
  range: oneOf(s.range, RANGES),
})

export const RUN_TABS = ["timeline", "transcript", "logs", "raw"] as const
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
export interface SessionsSearch {
  q?: string
  state?: RunFilterStatus
  agent?: string
}
export const validateSessions = (s: Raw): SessionsSearch => ({
  q: str(s.q),
  state: oneOf(s.state, FILTERABLE),
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

export const AGENT_TABS = ["versions", "manifest"] as const
export interface AgentSearch {
  tab?: (typeof AGENT_TABS)[number]
}
export const validateAgent = (s: Raw): AgentSearch => ({
  tab: oneOf(s.tab, AGENT_TABS),
})

export interface ApprovalsSearch {
  /** The parked run open in the detail. */
  run?: string
}
export const validateApprovals = (s: Raw): ApprovalsSearch => ({
  run: str(s.run),
})
