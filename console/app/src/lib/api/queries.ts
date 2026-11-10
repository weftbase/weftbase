// TanStack Query options per resource. Screens use these, never the
// client directly, so keys and caching stay in one place. live.ts patches
// and invalidates the same keys from the SSE streams.
//
// Key shapes (live.ts depends on them):
//   ["meta"]  ["pricing"]  ["agents"]  ["agent", name, "versions"]
//   ["runs", "list", query]      infinite, pages of RunsList
//   ["runs", "page", query]      one RunsList (overview, counts)
//   ["run", id]  ["run", id, "spans" | "events" | "transcript" | …]
//   ["sessions", "list", query]  ["session", id]  ["session", id, …]
//   ["sessions", "pages", filter]  infinite, pages of SessionsPage
//   ["traces", "list", query]    ["trace", id]
//   ["stats", by, since]  ["stats", "total", since]
//   ["approvals", "list", query]  ["approvals", "one", id]
//   ["agents", "summary", since]  ["agent", name]  ["tools", since]

import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"

import { api } from "./http"
import { loadConversation, loadSessions } from "./sessions"
import type { SessionsFilter } from "./sessions"
import type { RunsCursor, RunsQuery, SessionsQuery, StatsBy } from "./types"

export const q = {
  meta: () =>
    queryOptions({
      queryKey: ["meta"],
      queryFn: ({ signal }) => api.meta(signal),
      staleTime: 60_000,
      retry: false,
    }),
  pricing: () =>
    queryOptions({
      queryKey: ["pricing"],
      queryFn: () => api.pricing(),
      staleTime: 300_000,
    }),

  /** The Runs list: pages on demand ("Load more"). */
  runs: (query: RunsQuery) =>
    infiniteQueryOptions({
      queryKey: ["runs", "list", query],
      queryFn: ({ pageParam }) => api.runs(query, pageParam),
      initialPageParam: undefined as RunsCursor | undefined,
      getNextPageParam: (last) => last.next ?? undefined,
    }),
  /** One short page of runs: a "latest" list, a count (limit 1 + total). */
  runsPage: (query: RunsQuery) =>
    queryOptions({
      queryKey: ["runs", "page", query],
      queryFn: () => api.runs(query),
    }),
  run: (id: string) =>
    queryOptions({ queryKey: ["run", id], queryFn: () => api.run(id) }),
  spans: (id: string) =>
    queryOptions({
      queryKey: ["run", id, "spans"],
      queryFn: () => api.spans(id, true),
    }),
  events: (id: string) =>
    queryOptions({
      queryKey: ["run", id, "events"],
      queryFn: () => api.events(id),
    }),
  transcript: (id: string, step?: number) =>
    queryOptions({
      queryKey: ["run", id, "transcript", step ?? "all"],
      queryFn: () => api.transcript(id, step),
    }),
  step: (id: string, n: number) =>
    queryOptions({
      queryKey: ["run", id, "step", n],
      queryFn: () => api.step(id, n),
    }),
  requests: (id: string, step?: number) =>
    queryOptions({
      queryKey: ["run", id, "requests", step ?? "all"],
      queryFn: () => api.requests(id, step),
    }),
  manifest: (hash: string) =>
    queryOptions({
      queryKey: ["manifest", hash],
      queryFn: () => api.manifest(hash),
      staleTime: Infinity,
      retry: false,
    }),
  logs: (id: string) =>
    queryOptions({
      queryKey: ["run", id, "logs"],
      queryFn: () => api.logs(id),
    }),

  sessions: (query: SessionsQuery) =>
    queryOptions({
      queryKey: ["sessions", "list", query],
      queryFn: () => api.sessions(query),
    }),
  /** The Sessions list: pages on demand; its search also finds a
   * session by public id or by its own id. */
  sessionsList: (filter: SessionsFilter) =>
    infiniteQueryOptions({
      queryKey: ["sessions", "pages", filter],
      queryFn: ({ pageParam }) => loadSessions(filter, pageParam),
      initialPageParam: undefined as
        { before: string; before_id?: string } | undefined,
      getNextPageParam: (last) =>
        last.next_before
          ? {
              before: last.next_before,
              before_id: last.next_before_id ?? undefined,
            }
          : undefined,
    }),
  session: (id: string) =>
    queryOptions({ queryKey: ["session", id], queryFn: () => api.session(id) }),
  /** A session's turns from `from` to the newest ("tail": the newest
   * page). */
  conversation: (id: string, from: number | "tail") =>
    queryOptions({
      queryKey: ["session", id, "conversation", from],
      queryFn: () => loadConversation(id, from),
    }),
  timeline: (id: string) =>
    queryOptions({
      queryKey: ["session", id, "timeline"],
      queryFn: () => api.timeline(id),
    }),
  entries: (id: string) =>
    queryOptions({
      queryKey: ["session", id, "entries"],
      queryFn: () => api.entries(id),
    }),

  traces: (query: { notRun?: boolean; limit?: number } = {}) =>
    queryOptions({
      queryKey: ["traces", "list", query],
      queryFn: () => api.traces(query),
    }),
  trace: (id: string) =>
    queryOptions({ queryKey: ["trace", id], queryFn: () => api.trace(id) }),

  /** since: "1h", "24h", "7d", … back from now. */
  stats: (by: StatsBy, since: string) =>
    queryOptions({
      queryKey: ["stats", by, since],
      queryFn: () => api.stats(by, since),
      staleTime: 15_000,
    }),

  /** Model and tool breakdowns count subagent runs too (all). */
  statsAll: (by: StatsBy, since: string) =>
    queryOptions({
      queryKey: ["stats", by, since, "all"],
      queryFn: () => api.stats(by, since, { all: true }),
      staleTime: 15_000,
    }),
  statsTotal: (since: string) =>
    queryOptions({
      queryKey: ["stats", "total", since],
      queryFn: () => api.statsTotal(since),
      staleTime: 15_000,
    }),

  approvals: (
    query: {
      status?: string
      agent?: string
      tool?: string
      run?: string
      limit?: number
    } = {}
  ) =>
    queryOptions({
      queryKey: ["approvals", "list", query],
      queryFn: () => api.approvals(query),
    }),
  approval: (id: string) =>
    queryOptions({
      queryKey: ["approvals", "one", id],
      queryFn: () => api.approval(id),
    }),

  agentsSummary: (since?: string) =>
    queryOptions({
      queryKey: ["agents", "summary", since ?? "24h"],
      queryFn: () => api.agentsSummary(since),
    }),
  agent: (name: string) =>
    queryOptions({
      queryKey: ["agent", name],
      queryFn: () => api.agent(name),
      retry: false,
    }),
  tools: (since?: string) =>
    queryOptions({
      queryKey: ["tools", since ?? "24h"],
      queryFn: () => api.tools(since),
    }),
  agents: () =>
    queryOptions({ queryKey: ["agents"], queryFn: () => api.agents() }),
  agentVersions: (name: string) =>
    queryOptions({
      queryKey: ["agent", name, "versions"],
      queryFn: () => api.agentVersions(name),
    }),
}

/** Keys a write to a run makes stale (cancel, steer, resume). */
export const afterRunWrite = (id: string) =>
  [["run", id], ["runs"], ["stats"], ["sessions"], ["session"]] as const
