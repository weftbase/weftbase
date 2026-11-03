// No credential ever travels in a URL (§7.4: the Console signs in with an
// HttpOnly session cookie). Two halves:
// - static: only src/lib/api/ makes requests, and no source puts a token,
//   key or secret into a URL or reads one from the page address;
// - dynamic: every call of the /v1 transport is driven with fetch stubbed,
//   and each URL it builds carries no credential, uses same-origin
//   cookies, sends no Authorization header, and marks unsafe methods
//   with Weftbase-CSRF.
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative, resolve } from "node:path"
import { afterEach, describe, expect, it, vi } from "vitest"

import { httpClient } from "@/lib/api/http"
import { validateRuns, validateRoot } from "@/lib/search"

const SRC = resolve(__dirname, "..")

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    if (statSync(p).isDirectory()) return f === "test" ? [] : sources(p)
    return /\.tsx?$/.test(f) &&
      !/\.test\.tsx?$/.test(f) &&
      f !== "routeTree.gen.ts"
      ? [p]
      : []
  })
}

const files = sources(SRC).map((p) => ({
  path: relative(SRC, p),
  text: readFileSync(p, "utf8"),
}))

describe("no token in URLs: static", () => {
  it("makes requests only from src/lib/api", () => {
    const raw = /\bfetch\(|new EventSource\(|XMLHttpRequest|sendBeacon/
    const callers = files.filter((f) => raw.test(f.text)).map((f) => f.path)
    expect(callers).toEqual(["lib/api/http.ts"])
  })

  it("never puts a credential into a URL", () => {
    const bad =
      /[?&](token|access_token|api_key|key|secret|session|auth)=|searchParams\.(set|append)\(\s*["'](token|access_token|api_key|key|secret|session|auth)["']/i
    expect(files.filter((f) => bad.test(f.text)).map((f) => f.path)).toEqual([])
  })

  it("never reads credentials from the page address or keeps them in storage", () => {
    const bad =
      /location\.(hash|search)|localStorage\.setItem\(\s*["'][^"']*(token|key|secret)/i
    expect(files.filter((f) => bad.test(f.text)).map((f) => f.path)).toEqual([])
  })

  it("drops a token from the URL search instead of keeping it in state", () => {
    expect(validateRoot({ token: "tok_SECRET" })).not.toHaveProperty("token")
    expect(
      Object.values(validateRuns({ token: "tok_SECRET", q: "x" }))
    ).not.toContain("tok_SECRET")
  })
})

describe("no token in URLs: the /v1 transport", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("builds credential-free, same-origin requests for every call", async () => {
    const seen: { url: string; init: RequestInit }[] = []
    vi.stubGlobal("fetch", (url: string, init: RequestInit) => {
      seen.push({ url, init })
      return Promise.resolve(
        new Response("{}", {
          status: 200,
          headers: { "content-type": "application/json" },
        })
      )
    })
    const env = "live" as const
    await Promise.all([
      httpClient.meta(),
      httpClient.stats(env),
      httpClient.listRuns(env, {
        status: "failed",
        q: "refund",
        agent: "billing-refund",
      }),
      httpClient.getRun(env, "run_8f2a1c/2/0"),
      httpClient.listSessions(env, {}),
      httpClient.getSession(env, "ses_1"),
      httpClient.listTraces(env, "chat"),
      httpClient.getTrace(env, "abc"),
      httpClient.listAgents(env),
      httpClient.getAgent(env, "support-triage"),
      httpClient.listApprovals(env, { status: "pending" }),
      httpClient.getApproval(env, "apr_1"),
      httpClient.decide(env, "apr_1", { approve: true, args_sha256: "00" }),
      httpClient.pricing(),
    ])
    expect(seen).toHaveLength(14)
    for (const { url, init } of seen) {
      const u = new URL(url)
      expect(u.pathname.startsWith("/v1/")).toBe(true)
      for (const k of u.searchParams.keys())
        expect(k).not.toMatch(/token|key|secret|session|auth/i)
      expect(url).not.toMatch(/tok_|wb_session|Bearer/i)
      expect(init.credentials).toBe("same-origin")
      const h = new Headers(init.headers)
      expect(h.has("Authorization")).toBe(false)
      if (init.method !== "GET") expect(h.get("Weftbase-CSRF")).toBe("1")
    }
    expect(seen.find((s) => s.url.includes("run_8f2a1c"))?.url).toContain(
      "/v1/runs/run_8f2a1c%2F2%2F0?"
    )
  })
})
