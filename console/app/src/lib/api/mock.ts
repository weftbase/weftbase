// The mock client: the ApiClient interface over mock-data.ts. Decisions
// on approvals change the in-memory store, so the inbox behaves like the
// real one for the length of a page load.

import type { ApiClient } from "./client"
import { ApiError } from "./errors"
import { AGENTS, APPROVALS, PRICES, RUNS, SESSIONS, TRACES } from "./mock-data"
import type { Env, Page, Run, RunStatus, Stats } from "./types"

const page = <T>(items: T[]): Page<T> => ({
  items,
  next_cursor: null,
  total: items.length,
})
const clone = <T>(x: T): T => structuredClone(x)
const notFound = (what: string, id: string) =>
  new ApiError(404, "not_found", `No ${what} with id ${id}.`)

/** The test environment holds a smaller slice of the same data. */
const inEnv = (env: Env, i: number) => env === "live" || i % 3 === 0

function runsFor(env: Env): Run[] {
  return RUNS.filter((_, i) => inEnv(env, i))
}

function matches(q: string | undefined, ...fields: (string | null)[]) {
  if (!q) return true
  const needle = q.toLowerCase()
  return fields.some((f) => f?.toLowerCase().includes(needle))
}

export const mockClient: ApiClient = {
  kind: "mock",
  meta: async () => ({
    version: "v0.21.0",
    commit: "0000000",
    workspace: "[Workspace name]",
    hosting: "self-hosted",
    env: "live",
    data_dir: "/var/lib/weftbase",
    database: "SQLite (weftbase.db)",
    retention_days: 30,
    content_policy: "full",
    started_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
  }),
  stats: async (env) => {
    const runs = runsFor(env)
    const by = Object.fromEntries(
      (
        [
          "queued",
          "running",
          "waiting",
          "succeeded",
          "failed",
          "cancelled",
        ] as RunStatus[]
      ).map((s) => [s, runs.filter((r) => r.status === s).length])
    ) as Stats["runs_by_status"]
    const classes = new Map<string, number>()
    for (const r of runs)
      if (r.error_class)
        classes.set(r.error_class, (classes.get(r.error_class) ?? 0) + 1)
    return {
      window: "24h",
      runs_by_status: by,
      failures_by_error_class: [...classes]
        .map(([error_class, count]) => ({ error_class, count }))
        .sort((a, b) => b.count - a.count),
      cost_today: runs.some((r) => r.cost === null)
        ? null
        : runs.reduce((a, r) => a + (r.cost ?? 0), 0),
      tokens_today: runs.reduce(
        (a, r) => a + r.usage.input_tokens + r.usage.output_tokens,
        0
      ),
      pending_approvals: APPROVALS.filter((a) => a.status === "pending").length,
    }
  },
  listRuns: async (env, f) => {
    const items = runsFor(env).filter(
      (r) =>
        (!f.status || r.status === f.status) &&
        (!f.agent || r.agent === f.agent) &&
        (!f.model || r.model === f.model) &&
        (!f.tool || r.tools.includes(f.tool)) &&
        (!f.error_class || r.error_class === f.error_class) &&
        (!f.tenant || r.tenant === f.tenant) &&
        (!f.session || r.session_id === f.session) &&
        matches(f.q, r.id, r.agent, r.model, r.error_class)
    )
    return page(
      clone(items.slice(0, f.limit ?? items.length)).map(
        ({ steps: _s, transcript: _t, ...rest }) => rest
      )
    )
  },
  getRun: async (_env, id) => {
    const r = RUNS.find((x) => x.id === id)
    if (!r) throw notFound("run", id)
    return clone(r)
  },
  listSessions: async (env, f) =>
    page(
      clone(
        SESSIONS.filter(
          (s, i) =>
            inEnv(env, i) &&
            (!f.state || s.state === f.state) &&
            (!f.agent || s.agent === f.agent) &&
            matches(f.q, s.id, s.agent)
        )
      ).map(({ turn_list: _t, timeline: _l, ...rest }) => rest)
    ),
  getSession: async (_env, id) => {
    const s = SESSIONS.find((x) => x.id === id)
    if (!s) throw notFound("session", id)
    return clone(s)
  },
  listTraces: async (env, q) =>
    page(
      clone(
        TRACES.filter(
          (t, i) => inEnv(env, i) && matches(q, t.id, t.root, t.service)
        )
      ).map(({ spans: _s, run_id: _r, ...rest }) => rest)
    ),
  getTrace: async (_env, id) => {
    const t = TRACES.find((x) => x.id === id)
    if (!t) throw notFound("trace", id)
    return clone(t)
  },
  listAgents: async () => clone(AGENTS),
  getAgent: async (_env, name) => {
    const a = AGENTS.find((x) => x.name === name)
    if (!a) throw notFound("agent", name)
    return clone(a)
  },
  listApprovals: async (_env, f) => {
    const items = APPROVALS.filter(
      (a) =>
        (!f.status ||
          (f.status === "pending"
            ? a.status === "pending"
            : a.status !== "pending")) &&
        (!f.agent || a.agent === f.agent) &&
        (!f.tool || a.tool === f.tool)
    ).sort(
      (a, b) =>
        Number(b.status === "pending") - Number(a.status === "pending") ||
        b.requested_at.localeCompare(a.requested_at)
    )
    return page(clone(items))
  },
  getApproval: async (_env, id) => {
    const a = APPROVALS.find((x) => x.id === id)
    if (!a) throw notFound("approval", id)
    return clone(a)
  },
  decide: async (_env, id, d) => {
    const a = APPROVALS.find((x) => x.id === id)
    if (!a) throw notFound("approval", id)
    if (a.status !== "pending")
      throw new ApiError(
        409,
        "already_decided",
        "This approval was already decided."
      )
    if (d.args_sha256 !== a.args_sha256)
      throw new ApiError(
        409,
        "args_changed",
        "The arguments changed since you looked. Reload to see them."
      )
    a.status = d.approve ? "approved" : "denied"
    a.decided_at = new Date().toISOString()
    a.decided_by = "[Operator name]"
    a.reason = d.reason ?? null
    const run = RUNS.find((r) => r.id === a.run_id)
    if (run) {
      run.status = d.approve ? "running" : "failed"
      const step = run.steps.find((s) => s.approval_id === a.id)
      if (step) {
        step.status = d.approve ? "ok" : "error"
        step.summary = d.approve ? "approved" : `denied: ${d.reason ?? ""}`
      }
    }
    return clone(a)
  },
  pricing: async () => clone(PRICES),
}
