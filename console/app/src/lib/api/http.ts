// The /v1 transport. The only file that calls fetch.
//
// Rules it keeps (WEFTBASE-ARCHITECTURE §4.4, §7.4):
// - Same-origin, relative URLs resolved against the shell's <base href>,
//   so the Console works under any mount prefix.
// - Auth is the HttpOnly wb_session cookie. No token is ever put in a URL,
//   and none is read from or kept in storage.
// - Unsafe methods carry the Weftbase-CSRF: 1 header.
// - Ids are percent-encoded once per path segment: child run ids contain
//   "/" (<parent>/<step>/<call>).

import type { ApiClient } from "./client"
import { ApiError } from "./errors"

type Query = Record<string, string | number | undefined>

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
    if (v !== undefined && v !== "") url.searchParams.set(k, String(v))
  }
  return url.toString()
}

async function request<T>(
  method: "GET" | "POST" | "DELETE",
  path: string,
  query?: Query,
  body?: unknown
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (method !== "GET") headers["Weftbase-CSRF"] = "1"
  if (body !== undefined) headers["Content-Type"] = "application/json"
  const res = await fetch(buildUrl(path, query), {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    let code = "http_error"
    let message = res.statusText
    try {
      const j = (await res.json()) as {
        error?: { code?: string; message?: string }
      }
      code = j.error?.code ?? code
      message = j.error?.message ?? message
    } catch {
      // not JSON: keep the status text
    }
    throw new ApiError(res.status, code, message)
  }
  return (await res.json()) as T
}

const get = <T>(path: string, query?: Query) => request<T>("GET", path, query)

export const httpClient: ApiClient = {
  kind: "http",
  meta: () => get("/v1/meta"),
  stats: (env) => get("/v1/stats", { env }),
  listRuns: (env, f) => get("/v1/runs", { env, ...f }),
  getRun: (env, id) =>
    get(`/v1/runs/${seg(id)}`, { env, include: "steps,transcript" }),
  listSessions: (env, f) => get("/v1/sessions", { env, ...f }),
  getSession: (env, id) =>
    get(`/v1/sessions/${seg(id)}`, { env, include: "timeline,entries" }),
  listTraces: (env, q) => get("/v1/traces", { env, q }),
  getTrace: (env, id) => get(`/v1/traces/${seg(id)}`, { env }),
  listAgents: (env) => get("/v1/agents", { env }),
  getAgent: (env, name) =>
    get(`/v1/agents/${seg(name)}`, { env, include: "versions" }),
  listApprovals: (env, f) => get("/v1/approvals", { env, ...f }),
  getApproval: (env, id) => get(`/v1/approvals/${seg(id)}`, { env }),
  decide: (env, id, d) =>
    request("POST", `/v1/approvals/${seg(id)}/decision`, { env }, d),
  pricing: () => get("/v1/settings/pricing"),
}
