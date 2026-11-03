// URL search schemas. Every view and selection lives in the URL, so a
// link to any screen opens exactly that screen. Validators are plain
// functions (no schema library) and drop anything they do not know.

import { useSearch } from "@tanstack/react-router"

import type { ApprovalFilters, Env, RunStatus, SessionState } from "./api/types"

type Raw = Record<string, unknown>

const str = (v: unknown): string | undefined =>
  typeof v === "string" && v.trim() !== ""
    ? v
    : typeof v === "number"
      ? String(v)
      : undefined

function oneOf<T extends string>(v: unknown, xs: readonly T[]): T | undefined {
  return typeof v === "string" && (xs as readonly string[]).includes(v)
    ? (v as T)
    : undefined
}

const num = (v: unknown): number | undefined => {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN
  return Number.isInteger(n) && n >= 0 ? n : undefined
}

export const ENVS = ["test", "live"] as const satisfies readonly Env[]

export interface RootSearch {
  env?: Env
}
export const validateRoot = (s: Raw): RootSearch => ({
  env: oneOf(s.env, ENVS),
})

/** useEnv is the environment the top bar selected; live by default. */
export function useEnv(): Env {
  const s: RootSearch = useSearch({ strict: false })
  return s.env ?? "live"
}

export const RUN_TABS = ["running", "waiting", "failed"] as const
export const RUN_STATUSES = [
  "queued",
  "running",
  "waiting",
  "succeeded",
  "failed",
  "cancelled",
] as const satisfies readonly RunStatus[]

export interface RunsSearch {
  status?: RunStatus
  agent?: string
  model?: string
  tool?: string
  error?: string
  tenant?: string
  q?: string
  /** The run open in the side card. */
  run?: string
}
export const validateRuns = (s: Raw): RunsSearch => ({
  status: oneOf(s.status, RUN_STATUSES),
  agent: str(s.agent),
  model: str(s.model),
  tool: str(s.tool),
  error: str(s.error),
  tenant: str(s.tenant),
  q: str(s.q),
  run: str(s.run),
})

export const RUN_VIEWS = ["steps", "transcript", "raw"] as const
export interface RunSearch {
  view?: (typeof RUN_VIEWS)[number]
  step?: number
}
export const validateRun = (s: Raw): RunSearch => ({
  view: oneOf(s.view, RUN_VIEWS),
  step: num(s.step),
})

export const SESSION_STATES = [
  "active",
  "idle",
  "waiting",
  "closed",
] as const satisfies readonly SessionState[]
export interface SessionsSearch {
  state?: SessionState
  agent?: string
  q?: string
}
export const validateSessions = (s: Raw): SessionsSearch => ({
  state: oneOf(s.state, SESSION_STATES),
  agent: str(s.agent),
  q: str(s.q),
})

export interface SessionSearch {
  turn?: number
}
export const validateSession = (s: Raw): SessionSearch => ({
  turn: num(s.turn),
})

export interface TracesSearch {
  q?: string
}
export const validateTraces = (s: Raw): TracesSearch => ({ q: str(s.q) })

export interface TraceSearch {
  span?: string
}
export const validateTrace = (s: Raw): TraceSearch => ({ span: str(s.span) })

export const AGENT_TABS = ["overview", "versions"] as const
export interface AgentSearch {
  tab?: (typeof AGENT_TABS)[number]
}
export const validateAgent = (s: Raw): AgentSearch => ({
  tab: oneOf(s.tab, AGENT_TABS),
})

export interface ApprovalsSearch {
  status?: NonNullable<ApprovalFilters["status"]>
  agent?: string
  tool?: string
}
export const validateApprovals = (s: Raw): ApprovalsSearch => ({
  status: oneOf(s.status, ["pending", "decided"] as const),
  agent: str(s.agent),
  tool: str(s.tool),
})
