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
  AgentDetail,
  AgentVersion,
  AgentsSummary,
  Approval,
  ApprovalsPage,
  Conversation,
  DiffDoc,
  EventsPage,
  LedgerRun,
  LogsPage,
  Meta,
  PricingTable,
  RequestsPage,
  RunDetail,
  RunsCursor,
  RunsList,
  RunsPage,
  RunsQuery,
  SearchPage,
  SessionDetail,
  SessionsPage,
  SessionsQuery,
  SignInResponse,
  SpansDoc,
  Stats,
  StatsBy,
  StatsTotal,
  StepDoc,
  StoredManifest,
  ToolsDoc,
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

/** How long a request may go unanswered (headers). */
export const TIMEOUT_MS = 20_000

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
  // No request waits for ever: a button that sent one must come back
  // with an answer or with an error to show.
  const ctl = new AbortController()
  let timedOut = false
  const timer = setTimeout(() => {
    timedOut = true
    ctl.abort()
  }, TIMEOUT_MS)
  const cancel = () => ctl.abort()
  signal?.addEventListener("abort", cancel)
  let res: Response
  try {
    res = await fetch(buildUrl(path, query), {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: ctl.signal,
    })
  } catch (e) {
    if (!timedOut) throw e
    throw new ApiError(
      0,
      "timeout",
      method === "GET"
        ? `The server did not answer within ${TIMEOUT_MS / 1000} s.`
        : `The server did not answer within ${TIMEOUT_MS / 1000} s. The request may still have gone through: look before sending it again.`
    )
  } finally {
    clearTimeout(timer)
    signal?.removeEventListener("abort", cancel)
  }
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
const SEARCH_KEYS = [
  "q",
  "model",
  "tool",
  "since",
  "until",
  "sort",
  "min_cost",
  "min_tokens",
] as const

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
    // chains: a park and its resumes are one row. A page may hold fewer
    // rows than `limit`, and a search's `total` counts rows before the
    // fold.
    const { tag, ...rest } = q
    const query: Query = { ...rest, chains: 1, ...cursor }
    const eq = tag?.indexOf("=") ?? -1
    if (tag && eq > 0) query[`tag.${tag.slice(0, eq)}`] = tag.slice(eq + 1)
    if (isSearch(q)) {
      const p = await get<SearchPage>("/v1/runs", query)
      return {
        total: p.total,
        total_exact: p.total_exact,
        runs: p.runs,
        next: p.next_offset === null ? null : { offset: p.next_offset },
        facets: p.facets,
      }
    }
    const p = await get<RunsPage>("/v1/runs", query)
    return {
      total: p.total,
      total_exact: p.total_exact,
      runs: p.runs,
      next:
        p.next_before === null
          ? null
          : { before: p.next_before, before_id: p.next_before_id ?? undefined },
    }
  },
  /** The runs whose id is, or starts with, the text: child runs and the
   * resumes of a parked run included, so any id a person pastes is
   * found (the command menu's lookup). */
  findRuns: async (idPrefix: string, limit = 5) =>
    (
      await get<RunsPage>("/v1/runs", {
        id_prefix: idPrefix,
        parent: "*",
        limit,
      })
    ).runs,
  /** Start a run; answers its ledger row (queued). */
  admit: (body: AdmitRequest) => post<LedgerRun>("/v1/runs", body),
  run: (id: string) => get<RunDetail>(run(id)),
  /** The timeline of a logical run: the spans of every run of its chain
   * (a park and its resumes) and their descendants, with the waits. */
  runTimeline: (id: string) =>
    get<SpansDoc>(`${run(id)}/spans`, { tree: 1, chain: 1 }),
  events: (id: string, after?: number, limit?: number) =>
    get<EventsPage>(`${run(id)}/events`, { after, limit }),
  /** step: the conversation as the model saw it at that step. */
  transcript: (id: string, step?: number) =>
    get<Transcript>(`${run(id)}/transcript`, { step }),
  step: (id: string, n: number) => get<StepDoc>(`${run(id)}/steps/${n}`),
  requests: (id: string, step?: number) =>
    get<RequestsPage>(`${run(id)}/requests`, { step }),
  /** One page of a run's logs from index `from` (a page's `next_from`
   * feeds back in); `severity` keeps the lines at or above a level. */
  logsFrom: (
    id: string,
    query: { from?: number; limit?: number; severity?: string }
  ) => get<LogsPage>(`${run(id)}/logs`, query),
  /** The export is a download: give this URL to an <a download>. */
  exportUrl: (id: string, format: ExportFormat = "json", tree = false) =>
    buildUrl(`${run(id)}/export`, { format, tree: tree ? 1 : undefined }),
  /** Delete a run and what it recorded. Cannot be undone. */
  deleteRun: (id: string) => request<void>("DELETE", run(id)),
  cancel: (id: string, reason?: string) =>
    post<LedgerRun>(`${run(id)}/cancel`, { reason }),
  steer: (id: string, text: string) =>
    post<{ run_id: string; steered: number }>(`${run(id)}/steer`, {
      messages: [{ role: "user", content: [{ type: "text", text }] }],
    }),

  // Approvals: tool calls waiting on, or decided by, a person
  approvals: (
    q: {
      run?: string
      status?: string
      agent?: string
      tool?: string
      limit?: number
      offset?: number
    } = {}
  ) => get<ApprovalsPage>("/v1/approvals", { ...q }),
  approval: (id: string) => get<Approval>(`/v1/approvals/${seg(id)}`),
  /** Decide one approval. args_sha256 is the approval's own value, sent
   * back: 409 args_changed when the arguments are no longer those. */
  decideApproval: (
    id: string,
    body: {
      decision: "approve" | "deny" | "resolve"
      reason?: string
      content?: string
      args_sha256: string
    }
  ) => post<Approval>(`/v1/approvals/${seg(id)}/decision`, body),

  // Sessions
  sessions: (q: SessionsQuery = {}) =>
    get<SessionsPage>("/v1/sessions", { ...q }),
  session: (id: string) => get<SessionDetail>(session(id)),
  /** The session a public id (the browser-safe handle an app shows)
   * stands for. */
  publicSession: (publicId: string) =>
    get<{ session_id: string }>(`/v1/public/${seg(publicId)}`),
  /** The session as a person reads it, one entry per turn, oldest
   * first; paged by turn (limit at most 100). */
  conversation: (
    id: string,
    /** tail: the newest page, in one request, whatever the offset. */
    page: { offset?: number; limit?: number; tail?: boolean } = {}
  ) => get<Conversation>(`${session(id)}/conversation`, { ...page }),
  sessionExportUrl: (id: string) =>
    buildUrl(`${session(id)}/export`, { format: "jsonl" }),
  /** Delete a session, its turns and what they recorded. Cannot be undone. */
  deleteSession: (id: string) => request<unknown>("DELETE", session(id)),

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
    get<Stats>("/v1/stats", { by, since, parent: opts.all ? "*" : null }),
  /** Top-level runs by display status: the window's total and 24 buckets. */
  statsTotal: (since: string) =>
    get<StatsTotal>("/v1/stats", { by: "total", since }),

  // Agents
  agents: async () => (await get<{ agents: Agent[] }>("/v1/agents")).agents,
  /** Every agent with its newest version's shape and its runs over the
   * window (since: "1h", "24h", "7d"), and the inventory's totals. */
  agentsSummary: (since?: string) =>
    get<AgentsSummary>("/v1/agents", { include: "summary", since }),
  agent: (name: string) => get<AgentDetail>(`/v1/agents/${seg(name)}`),
  /** Every tool once, with the agent versions that have it. */
  tools: (since?: string) => get<ToolsDoc>("/v1/tools", { since }),
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

  // Compare
  /** Two runs' steps, aligned by step ordinal, with what differs. */
  diff: (a: string, b: string) => get<DiffDoc>("/v1/diff", { a, b }),

  // Sign-in
  /** A fresh one-time sign-in link for the Console at `origin`. The
   * link is a credential: show it once, never log or store it. */
  signInLink: (origin: string) =>
    post<{ link: string; token: string; expires_at: string }>(
      "/v1/auth/links",
      { origin }
    ),
}
