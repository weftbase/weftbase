// No credential ever travels in a URL or sits in storage (the Console
// signs in with an HttpOnly session cookie). Three parts:
// - static: only src/lib/api/ opens connections; no source puts a token,
//   key or secret into a URL; only src/lib/api/auth.ts reads the page
//   address, and nothing stores a credential;
// - the sign-in link: the token leaves the address bar before the
//   exchange and goes to the server in a POST body;
// - dynamic: every call of the /v1 client and every stream is driven with
//   fetch and EventSource stubbed; each URL carries no credential, uses
//   same-origin cookies, sends no Authorization header, and marks unsafe
//   methods with Weftbase-CSRF.
import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative, resolve } from "node:path"
import { afterEach, describe, expect, it, vi } from "vitest"

import { takeSignInToken, tokenFromPaste } from "@/lib/api/auth"
import { api } from "@/lib/api/http"
import { streamEvents, streamRuns } from "@/lib/api/stream"
import { validateOverview, validateRun, validateRuns } from "@/lib/search"

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
const matching = (re: RegExp) =>
  files.filter((f) => re.test(f.text)).map((f) => f.path)

describe("no token in URLs: static", () => {
  it("opens connections only from src/lib/api", () => {
    expect(
      matching(/\bfetch\(|XMLHttpRequest|sendBeacon|new WebSocket\(/)
    ).toEqual(["lib/api/http.ts"])
    expect(matching(/new EventSource\(/)).toEqual(["lib/api/stream.ts"])
  })

  it("never puts a credential into a URL", () => {
    const bad =
      /[?&](token|access_token|api_key|key|secret|session|auth|signin)=|searchParams\.(set|append)\(\s*["'](token|access_token|api_key|key|secret|session|auth|signin)["']/i
    expect(matching(bad)).toEqual([])
  })

  it("reads the page address for the sign-in link in one file only", () => {
    expect(matching(/location\.(hash|search|href)/)).toEqual([
      "lib/api/auth.ts",
    ])
  })

  it("keeps no credential in storage", () => {
    expect(
      matching(
        /(local|session)Storage\.setItem\(\s*["'`][^"'`]*(token|key|secret|signin|session)/i
      )
    ).toEqual([])
    // Storage is used for preferences only, through two files.
    expect(matching(/(local|session)Storage\./).sort()).toEqual([
      "lib/prefs.ts",
      "lib/theme.ts",
    ])
    expect(matching(/document\.cookie/)).toEqual([])
  })

  it("drops a token from the URL search instead of keeping it in state", () => {
    for (const validate of [validateOverview, validateRuns, validateRun]) {
      const out = validate({
        token: "tok_SECRET",
        signin: "tok_SECRET",
        q: "x",
      })
      expect(Object.keys(out)).not.toContain("token")
      expect(Object.values(out)).not.toContain("tok_SECRET")
    }
  })
})

describe("no token in URLs: the sign-in link", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState(null, "", "/")
  })

  it("takes the token out of the address at once", () => {
    window.history.replaceState(
      null,
      "",
      "/runs?status=failed#signin=tok_SECRET"
    )
    expect(takeSignInToken()).toBe("tok_SECRET")
    expect(window.location.href).not.toContain("tok_SECRET")
    expect(window.location.hash).toBe("")
    expect(window.location.pathname + window.location.search).toBe(
      "/runs?status=failed"
    )
    expect(takeSignInToken()).toBeNull()
  })

  it("accepts a pasted link or a bare token", () => {
    expect(tokenFromPaste(" http://127.0.0.1:7331/#signin=tok_A ")).toBe(
      "tok_A"
    )
    expect(tokenFromPaste("tok_B")).toBe("tok_B")
  })

  it("sends the token in a POST body, never in the URL", async () => {
    const seen: { url: string; init: RequestInit }[] = []
    vi.stubGlobal("fetch", (url: string, init: RequestInit) => {
      seen.push({ url, init })
      return Promise.resolve(
        new Response(
          '{"operator":"operator","expires_at":"2030-01-01T00:00:00Z"}',
          {
            status: 201,
          }
        )
      )
    })
    await api.signIn("tok_SECRET")
    expect(seen).toHaveLength(1)
    expect(seen[0].url).not.toContain("tok_SECRET")
    expect(new URL(seen[0].url).pathname).toBe("/v1/auth/sessions")
    expect(seen[0].init.method).toBe("POST")
    expect(seen[0].init.body).toBe('{"token":"tok_SECRET"}')
    expect(new Headers(seen[0].init.headers).has("Authorization")).toBe(false)
  })
})

describe("no token in URLs: the /v1 client and the streams", () => {
  afterEach(() => vi.unstubAllGlobals())

  const clean = (url: string) => {
    const u = new URL(url)
    expect(u.pathname.startsWith("/v1/")).toBe(true)
    for (const k of u.searchParams.keys())
      expect(k).not.toMatch(/token|key|secret|auth|signin|cookie/i)
    expect(url).not.toMatch(/tok_|wb_session|wb_sk|Bearer/i)
  }

  it("builds credential-free, same-origin requests for every call", async () => {
    const seen: { url: string; init: RequestInit }[] = []
    vi.stubGlobal("fetch", (url: string, init: RequestInit) => {
      seen.push({ url, init })
      return Promise.resolve(new Response("{}", { status: 200 }))
    })
    const child = "run_8f2a1c/2/call_1"
    const calls = [
      api.meta(),
      api.pricing(),
      api.manifest("sha256:abc"),
      api.runs({ display_status: "failed", agent: "billing-refund" }),
      api.runs({ q: "refund", since: "2020-01-01T00:00:00Z" }, { offset: 50 }),
      api.run(child),
      api.events(child, 10),
      api.transcript(child, 2),
      api.step(child, 1),
      api.requests(child, 1),
      api.cancel(child, "operator"),
      api.steer(child, "stop"),
      api.runTimeline(child),
      api.decideApproval("appr_1", { decision: "approve", args_sha256: "ab" }),
      api.sessions({ q: "order", awaiting: true }),
      api.session("s_1"),
      api.traces({ notRun: true }),
      api.trace("abc"),
      api.stats("agent", "24h"),
      api.agents(),
      api.agentVersions("support-triage"),
      api.promote("support-triage", "prod", { version: "agv_1", expected: "" }),
      api.signOut(),
    ]
    await Promise.all(calls)
    expect(seen).toHaveLength(calls.length)
    for (const { url, init } of seen) {
      clean(url)
      expect(init.credentials).toBe("same-origin")
      const h = new Headers(init.headers)
      expect(h.has("Authorization")).toBe(false)
      if (init.method !== "GET") expect(h.get("Weftbase-CSRF")).toBe("1")
    }
    clean(api.exportUrl(child, "jsonl"))
    clean(api.sessionExportUrl("s_1"))
    // A child run id is one path segment.
    expect(seen.map((s) => s.url)).toContain(
      "http://localhost:3000/v1/runs/run_8f2a1c%2F2%2Fcall_1"
    )
    // Top-level runs only for the stats: the empty `parent=`.
    expect(seen.find((s) => s.url.includes("/v1/stats"))?.url).toMatch(
      /[?&]parent=(&|$)/
    )
  })

  it("opens credential-free streams", () => {
    const urls: string[] = []
    class FakeEventSource {
      static CLOSED = 2
      readyState = 0
      constructor(url: string) {
        urls.push(url)
      }
      addEventListener() {}
      close() {}
    }
    vi.stubGlobal("EventSource", FakeEventSource)
    const stops = [
      streamRuns({ onFrame() {} }, { kinds: ["run"] }),
      streamEvents(["approval.requested", "approval.decided"], {
        onEvent() {},
      }),
    ]
    stops.forEach((stop) => stop())
    expect(urls).toHaveLength(2)
    urls.forEach(clean)
    expect(urls[0]).toContain("/v1/runs/stream?all=1&kinds=run")
    expect(urls[1]).toContain("/v1/events?type=approval.requested")
  })
})
