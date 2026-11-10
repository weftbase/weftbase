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
//   ["traces", "list", query]    ["trace", id]
//   ["stats", by, since]

import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"

import { api } from "./http"
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
  session: (id: string) =>
    queryOptions({ queryKey: ["session", id], queryFn: () => api.session(id) }),
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
