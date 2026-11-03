// The one API client interface the Console talks to. Screens and hooks
// only see this; they never build URLs or call fetch (an ESLint rule
// forbids fetch outside src/lib/api/).
//
// Two implementations: mockClient (typed in-memory data, the default
// until the /v1 server lands) and httpClient (the /v1 transport). When
// api/openapi/v1.yaml exists, httpClient becomes a thin wrapper over the
// openapi-fetch client generated from it, with the same interface.

import { httpClient } from "./http"
import { mockClient } from "./mock"
import type {
  Agent,
  Approval,
  ApprovalFilters,
  Decision,
  Env,
  Meta,
  Page,
  Price,
  Run,
  RunFilters,
  RunSummary,
  Session,
  SessionFilters,
  SessionSummary,
  Stats,
  Trace,
  TraceSummary,
} from "./types"

export { ApiError, isNotFound } from "./errors"

export interface ApiClient {
  /** "mock" while the Console runs on mock data; the top bar says so. */
  readonly kind: "mock" | "http"
  meta: () => Promise<Meta>
  stats: (env: Env) => Promise<Stats>
  listRuns: (env: Env, f: RunFilters) => Promise<Page<RunSummary>>
  getRun: (env: Env, id: string) => Promise<Run>
  listSessions: (env: Env, f: SessionFilters) => Promise<Page<SessionSummary>>
  getSession: (env: Env, id: string) => Promise<Session>
  listTraces: (env: Env, q?: string) => Promise<Page<TraceSummary>>
  getTrace: (env: Env, id: string) => Promise<Trace>
  listAgents: (env: Env) => Promise<Agent[]>
  getAgent: (env: Env, name: string) => Promise<Agent>
  listApprovals: (env: Env, f: ApprovalFilters) => Promise<Page<Approval>>
  getApproval: (env: Env, id: string) => Promise<Approval>
  decide: (env: Env, id: string, d: Decision) => Promise<Approval>
  pricing: () => Promise<Price[]>
}

/** The client in use. VITE_WEFTBASE_API=http switches to the real /v1
 * server (through the dev proxy, or same-origin when embedded). */
export const api: ApiClient =
  import.meta.env.VITE_WEFTBASE_API === "http" ? httpClient : mockClient
