// TanStack Query options per resource. Screens use these, never the
// client directly, so caching and keys stay in one place.

import { queryOptions } from "@tanstack/react-query"

import { api } from "./client"
import type { ApprovalFilters, Env, RunFilters, SessionFilters } from "./types"

export const q = {
  meta: () =>
    queryOptions({
      queryKey: ["meta"],
      queryFn: () => api.meta(),
      staleTime: 60_000,
    }),
  stats: (env: Env) =>
    queryOptions({ queryKey: ["stats", env], queryFn: () => api.stats(env) }),
  runs: (env: Env, f: RunFilters) =>
    queryOptions({
      queryKey: ["runs", env, f],
      queryFn: () => api.listRuns(env, f),
    }),
  run: (env: Env, id: string) =>
    queryOptions({
      queryKey: ["run", env, id],
      queryFn: () => api.getRun(env, id),
    }),
  sessions: (env: Env, f: SessionFilters) =>
    queryOptions({
      queryKey: ["sessions", env, f],
      queryFn: () => api.listSessions(env, f),
    }),
  session: (env: Env, id: string) =>
    queryOptions({
      queryKey: ["session", env, id],
      queryFn: () => api.getSession(env, id),
    }),
  traces: (env: Env, search?: string) =>
    queryOptions({
      queryKey: ["traces", env, search ?? ""],
      queryFn: () => api.listTraces(env, search),
    }),
  trace: (env: Env, id: string) =>
    queryOptions({
      queryKey: ["trace", env, id],
      queryFn: () => api.getTrace(env, id),
    }),
  agents: (env: Env) =>
    queryOptions({
      queryKey: ["agents", env],
      queryFn: () => api.listAgents(env),
    }),
  agent: (env: Env, name: string) =>
    queryOptions({
      queryKey: ["agent", env, name],
      queryFn: () => api.getAgent(env, name),
    }),
  approvals: (env: Env, f: ApprovalFilters) =>
    queryOptions({
      queryKey: ["approvals", env, f],
      queryFn: () => api.listApprovals(env, f),
    }),
  approval: (env: Env, id: string) =>
    queryOptions({
      queryKey: ["approval", env, id],
      queryFn: () => api.getApproval(env, id),
    }),
  pricing: () =>
    queryOptions({
      queryKey: ["pricing"],
      queryFn: () => api.pricing(),
      staleTime: 300_000,
    }),
}

/** Keys a decision makes stale: the inbox, the run and the overview. */
export const afterDecisionKeys = [
  ["approvals"],
  ["approval"],
  ["run"],
  ["runs"],
  ["stats"],
] as const
