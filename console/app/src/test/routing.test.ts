// Routing: every screen is a URL, and each URL loads the screen it names.
// Runs the real route tree in memory, no DOM. Routes have no loaders
// (screens fetch with TanStack Query and show their own loading and
// error states), so nothing here touches the network.
import { QueryClient } from "@tanstack/react-query"
import { createMemoryHistory, createRouter } from "@tanstack/react-router"
import { describe, expect, it } from "vitest"

import { validateRuns } from "@/lib/search"
import { mountRewrite } from "@/router"
import { routeTree } from "@/routeTree.gen"
import { toQuery } from "@/routes/runs/index"

function routerAt(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
    context: {
      queryClient: new QueryClient({
        defaultOptions: { queries: { retry: false } },
      }),
    },
  })
}

async function leaf(path: string) {
  const router = routerAt(path)
  await router.load()
  const m = router.state.matches
  return { router, id: m[m.length - 1].routeId, status: m[m.length - 1].status }
}

describe("routes", () => {
  const cases: [string, string][] = [
    ["/", "/"],
    ["/?range=7d", "/"],
    ["/runs", "/runs/"],
    ["/runs?status=failed&agent=billing-refund", "/runs/"],
    ["/runs?tab=traces", "/runs/"],
    ["/runs/r_ok", "/runs/$runId"],
    ["/runs/r_ok?tab=raw&span=0102", "/runs/$runId"],
    ["/sessions", "/sessions/"],
    ["/sessions/s_orders", "/sessions/$sessionId"],
    ["/traces/0102030405060708090a0b0c0d0e0f10", "/traces/$traceId"],
    ["/agents", "/agents/"],
    ["/agents/support-triage?tab=versions", "/agents/$name"],
    ["/approvals", "/approvals/"],
    ["/settings/general", "/settings/general"],
    ["/settings/pricing", "/settings/pricing"],
    ["/settings/appearance", "/settings/appearance"],
  ]
  it.each(cases)("%s loads %s", async (path, routeId) => {
    const r = await leaf(path)
    expect(r.id).toBe(routeId)
    expect(r.status).toBe("success")
  })

  it("has no Audit page and no top-level Traces page", () => {
    const paths = Object.keys(routerAt("/").routesByPath)
    expect(paths).not.toContain("/audit")
    expect(paths).not.toContain("/traces")
    expect(paths).toContain("/traces/$traceId")
  })

  it("redirects /settings to /settings/general", async () => {
    const { router } = await leaf("/settings")
    expect(router.state.location.pathname).toBe("/settings/general")
  })

  it("keeps filters in the URL and drops what it does not know", async () => {
    const { router } = await leaf(
      "/runs?status=waiting&agent=deployer&range=7d&q=refund"
    )
    expect(router.state.matches.at(-1)?.search).toMatchObject({
      status: "waiting",
      agent: "deployer",
      range: "7d",
      q: "refund",
    })
    // What a screen reads is the validator's output: known keys only.
    expect(
      Object.keys(
        validateRuns({ status: "waiting", bogus: "1", range: "2y" })
      ).sort()
    ).toEqual(["agent", "q", "range", "status", "tab"])
    expect(validateRuns({ range: "2y" }).range).toBeUndefined()
  })

  it("percent-encodes child run ids with slashes", () => {
    const router = routerAt("/")
    const loc = router.buildLocation({
      to: "/runs/$runId",
      params: { runId: "run_8f2a1c/2/0" },
    })
    expect(loc.href).toBe("/runs/run_8f2a1c%2F2%2F0")
  })

  it("maps URL filters to API parameters", () => {
    const s = validateRuns({ status: "bogus", agent: "", q: "refund" })
    expect(s.status).toBeUndefined()
    expect(s.agent).toBeUndefined()
    expect(toQuery(s)).toMatchObject({ q: "refund", limit: 50 })
    // "waiting" is the Console's word; the server's filter word is parked.
    expect(toQuery(validateRuns({ status: "waiting" })).status).toBe("parked")
    expect(
      toQuery(validateRuns({ status: "failed" }), "2020-01-01T00:00:00.000Z")
    ).toMatchObject({
      status: "failed",
      since: "2020-01-01T00:00:00.000Z",
    })
  })

  it("mounts under the <base href> prefix", () => {
    const rw = mountRewrite("https://ops.example/weftbase/")
    expect(rw).toBeDefined()
    const inUrl = rw!.input!({
      url: new URL("https://ops.example/weftbase/runs/x"),
    }) as URL
    expect(inUrl.pathname).toBe("/runs/x")
    const outUrl = rw!.output!({
      url: new URL("https://ops.example/runs"),
    }) as URL
    expect(outUrl.pathname).toBe("/weftbase/runs")
    expect(mountRewrite("https://ops.example/")).toBeUndefined()
  })
})
