// The /v1 client. With stream.ts, the only code that opens a network
// connection (an ESLint rule forbids fetch and EventSource elsewhere).
//
// Rules it keeps:
// - Same-origin, relative URLs resolved against the shell's <base href>,
//   so the Console works under any mount prefix.
// - Auth is the HttpOnly wb_session cookie. No credential is ever put in
//   a URL, and none is read from or kept in storage. The one-time sign-in
//   token travels in a POST body (auth.ts).
// - Unsafe methods carry the Weftbase-CSRF: 1 header.
// - Ids are percent-encoded once per path segment: a child run id
//   contains "/" (<parent>/<step>/<call>).

import { ApiError } from "./errors"
import type {
  AdmitRequest,
  Agent,
  AgentVersion,
  EventsPage,
  LedgerRun,
  LogsPage,
  Meta,
  PricingTable,
  RequestsPage,
  ResumeDecision,
  RunDetail,
  RunsCursor,
  RunsList,
  RunsPage,
  RunsQuery,
  SearchPage,
  SessionDetail,
  SessionEntries,
  SessionTimeline,
  SessionsPage,
  SessionsQuery,
  SignInResponse,
  SpansDoc,
  Stats,
  StatsBy,
  StepDoc,
  StoredManifest,
  TraceDetail,
  TracesPage,
  Transcript,
} from "./types"

/** undefined and "" are left out; null sends the key with an empty value
 * (the server reads `parent=` as "top-level runs only"). */
export type Query = Record<string, string | number | boolean | null | undefined>

/** seg percent-encodes one path segment, "/" included. */
export function seg(id: string): string {
  return encodeURIComponent(id)
}

/** buildUrl is the one place a request URL is made. */
export function buildUrl(path: string, query: Query = {}): string {
  const base =
    typeof document === "undefined" ? "http://localhost/" : document.baseURI
  const url = new URL(path.replace(/^\//, ""), base)
  for (const [k, v] of Object.entries(query)) {
    if (v === null) url.searchParams.set(k, "")
    else if (v !== undefined && v !== "") url.searchParams.set(k, String(v))
  }
  return url.toString()
}

type Method = "GET" | "POST" | "DELETE"

async function request<T>(
  method: Method,
  path: string,
  query?: Query,
  body?: unknown,
  signal?: AbortSignal
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (method !== "GET") headers["Weftbase-CSRF"] = "1"
  if (body !== undefined) headers["Content-Type"] = "application/json"
  const res = await fetch(buildUrl(path, query), {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
  if (!res.ok) {
    let code = "http_error"
    let message = res.statusText || `HTTP ${res.status}`
    let requestId: string | undefined
    try {
      const j = (await res.json()) as {
        error?: { code?: string; message?: string; request_id?: string }
      }
      code = j.error?.code ?? code
      message = j.error?.message ?? message
      requestId = j.error?.request_id
    } catch {
      // not JSON: keep the status text
    }
    throw new ApiError(res.status, code, message, requestId)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

const get = <T>(path: string, query?: Query, signal?: AbortSignal) =>
  request<T>("GET", path, query, undefined, signal)
const post = <T>(path: string, body?: unknown) =>
  request<T>("POST", path, undefined, body ?? {})

const run = (id: string) => `/v1/runs/${seg(id)}`
const session = (id: string) => `/v1/sessions/${seg(id)}`

/** The parameters that make GET /v1/runs a search (offset-paged). */
const SEARCH_KEYS = ["q", "model", "tool", "since", "until", "sort"] as const

export function isSearch(q: RunsQuery): boolean {
  return SEARCH_KEYS.some((k) => q[k] !== undefined && q[k] !== "")
}

export type ExportFormat = "json" | "jsonl" | "otlp" | "wefttest"

export const api = {
  // Auth
  signIn: (token: string) =>
    request<SignInResponse>("POST", "/v1/auth/sessions", undefined, { token }),
  signOut: () => request<void>("DELETE", "/v1/auth/sessions/current"),

  // Server
  meta: (signal?: AbortSignal) => get<Meta>("/v1/meta", undefined, signal),
  pricing: () => get<PricingTable>("/v1/settings/pricing"),
  manifest: (hash: string) => get<StoredManifest>(`/v1/manifests/${seg(hash)}`),

  // Runs
  /** One page of runs, newest first. The server pages a plain list by a
   * time cursor and a search by offset; both come back as a RunsList. */
  async runs(q: RunsQuery = {}, cursor?: RunsCursor): Promise<RunsList> {
    const query: Query = { ...q, ...cursor }
    if (isSearch(q)) {
      const p = await get<SearchPage>("/v1/runs", query)
      return {
        total: p.total,
        runs: p.runs,
        next: p.next_offset === null ? null : { offset: p.next_offset },
        facets: p.facets,
      }
    }
    const p = await get<RunsPage>("/v1/runs", query)
    return {
      total: p.total,
      runs: p.runs,
      next:
        p.next_before === null
          ? null
          : { before: p.next_before, before_id: p.next_before_id ?? undefined },
    }
  },
  /** Start a run; answers its ledger row (queued). */
  admit: (body: AdmitRequest) => post<LedgerRun>("/v1/runs", body),
  run: (id: string) => get<RunDetail>(run(id)),
  runStatus: (id: string) => get<LedgerRun>(`${run(id)}/status`),
  /** tree: every descendant run's spans too, each tagged with its run_id. */
  spans: (id: string, tree = true) =>
    get<SpansDoc>(`${run(id)}/spans`, { tree: tree ? 1 : undefined }),
  events: (id: string, after?: number, limit?: number) =>
    get<EventsPage>(`${run(id)}/events`, { after, limit }),
  /** step: the conversation as the model saw it at that step. */
  transcript: (id: string, step?: number) =>
    get<Transcript>(`${run(id)}/transcript`, { step }),
  step: (id: string, n: number) => get<StepDoc>(`${run(id)}/steps/${n}`),
  requests: (id: string, step?: number) =>
    get<RequestsPage>(`${run(id)}/requests`, { step }),
  logs: (id: string, from?: number, limit?: number) =>
    get<LogsPage>(`${run(id)}/logs`, { from, limit }),
  /** The export is a download: give this URL to an <a download>. */
  exportUrl: (id: string, format: ExportFormat = "json") =>
    buildUrl(`${run(id)}/export`, { format }),
  cancel: (id: string, reason?: string) =>
    post<LedgerRun>(`${run(id)}/cancel`, { reason }),
  steer: (id: string, text: string) =>
    post<{ run_id: string; steered: number }>(`${run(id)}/steer`, {
      messages: [{ role: "user", content: [{ type: "text", text }] }],
    }),
  /** Decide a parked run's pending calls; answers the new run. */
  resume: (id: string, decisions: ResumeDecision[]) =>
    post<LedgerRun>(`${run(id)}/resume`, { decisions }),

  // Sessions
  sessions: (q: SessionsQuery = {}) =>
    get<SessionsPage>("/v1/sessions", { ...q }),
  session: (id: string) => get<SessionDetail>(session(id)),
  timeline: (id: string) => get<SessionTimeline>(`${session(id)}/timeline`),
  entries: (id: string, offset?: number, limit?: number) =>
    get<SessionEntries>(`${session(id)}/entries`, { offset, limit }),
  sessionExportUrl: (id: string) =>
    buildUrl(`${session(id)}/export`, { format: "jsonl" }),

  // Traces
  /** notRun: only traces that belong to no weft run. */
  traces: (q: { notRun?: boolean; before?: string; limit?: number } = {}) =>
    get<TracesPage>("/v1/traces", {
      not_run: q.notRun ? 1 : undefined,
      before: q.before,
      limit: q.limit,
    }),
  trace: (id: string) => get<TraceDetail>(`/v1/traces/${seg(id)}`),

  // Stats
  /** since: a duration back from now ("1h", "24h", "7d") or RFC 3339.
   * Counts top-level runs only, like the Runs list, unless all is set. */
  stats: (by: StatsBy, since: string, opts: { all?: boolean } = {}) =>
    get<Stats>("/v1/stats", { by, since, parent: opts.all ? undefined : null }),

  // Agents
  agents: async () => (await get<{ agents: Agent[] }>("/v1/agents")).agents,
  agentVersions: async (name: string) =>
    (
      await get<{ agent: string; versions: AgentVersion[] | null }>(
        `/v1/agents/${seg(name)}/versions`
      )
    ).versions ?? [],
  /** Move a label to a version. expected is what the label holds now
   * ("" for a new label); a mismatch is a 409. */
  promote: (
    name: string,
    label: string,
    body: { version: string; expected: string; reason?: string }
  ) => post<AgentVersion>(`/v1/agents/${seg(name)}/labels/${seg(label)}`, body),
}
