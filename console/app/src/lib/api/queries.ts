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
//   ["diff", a, b]
//   ["approvals", "pages", query]  infinite, pages of ApprovalsPage
//   ["session", id, "conversation", "window"]  the windowed conversation

import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"

import { api } from "./http"
import { loadSessions, refreshConversation } from "./sessions"
import type { ConversationView, SessionsFilter } from "./sessions"
import type { RunsCursor, RunsQuery, StatsBy } from "./types"

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
  /** The step-aligned diff of two runs (the Compare page). */
  diff: (a: string, b: string) =>
    queryOptions({
      queryKey: ["diff", a, b],
      queryFn: () => api.diff(a, b),
      retry: false,
    }),
  /** The Approvals inbox: pages on demand ("Load more"), by offset.
   * Under ["approvals"], so the outbox stream keeps every loaded page
   * current. */
  approvalsList: (
    query: { status?: string; agent?: string; tool?: string; run?: string } = {}
  ) =>
    infiniteQueryOptions({
      queryKey: ["approvals", "pages", query],
      queryFn: ({ pageParam }) =>
        api.approvals({ ...query, limit: APPROVALS_PAGE, offset: pageParam }),
      initialPageParam: 0,
      getNextPageParam: (last) => last.next_offset ?? undefined,
    }),
  /** A session's conversation as one list: the newest turns, kept
   * current, with the earlier pages the person loaded (see
   * refreshConversation in sessions.ts). Every refetch reads the tail
   * only. */
  conversationWindow: (id: string) =>
    queryOptions({
      queryKey: conversationKey(id),
      queryFn: ({ client }) =>
        refreshConversation(id, () =>
          client.getQueryData<ConversationView>(conversationKey(id))
        ),
    }),
}

/** Rows per page of the Approvals inbox. */
export const APPROVALS_PAGE = 50
/** The key of q.conversationWindow(id). */
export const conversationKey = (id: string) =>
  ["session", id, "conversation", "window"] as const

/** Keys a write to a run makes stale (cancel, steer, resume). */
export const afterRunWrite = (id: string) =>
  [["run", id], ["runs"], ["stats"], ["sessions"], ["session"]] as const
