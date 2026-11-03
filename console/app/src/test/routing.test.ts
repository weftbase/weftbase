// Routing: every screen is a URL, and each URL loads the screen it names
// (on mock data). Runs the real route tree in memory, no DOM.
import { QueryClient } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRouter,
  isNotFound,
} from "@tanstack/react-router"
import { describe, expect, it } from "vitest"

import { RUNS, SESSIONS, TRACES, AGENTS, APPROVALS } from "@/lib/api/mock-data"
import { mountRewrite } from "@/router"
import { routeTree } from "@/routeTree.gen"
import { validateRuns } from "@/lib/search"
import { toFilters } from "@/routes/runs/index"

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
    ["/runs", "/runs/"],
    ["/runs?status=failed&agent=billing-refund", "/runs/"],
    [`/runs/${RUNS[0].id}`, "/runs/$runId"],
    [`/runs/${RUNS[0].id}?view=raw`, "/runs/$runId"],
    ["/sessions", "/sessions/"],
    [`/sessions/${SESSIONS[0].id}`, "/sessions/$sessionId"],
    ["/traces", "/traces/"],
    [
      `/traces/${TRACES[0].id}?span=${TRACES[0].spans[0].id}`,
      "/traces/$traceId",
    ],
    ["/agents", "/agents/"],
    [`/agents/${AGENTS[0].name}?tab=versions`, "/agents/$name"],
    ["/approvals", "/approvals/"],
    [`/approvals/${APPROVALS[0].id}`, "/approvals/$approvalId"],
    ["/audit", "/audit"],
    ["/settings/general", "/settings/general"],
    ["/settings/pricing", "/settings/pricing"],
    ["/settings/appearance", "/settings/appearance"],
  ]
  it.each(cases)("%s loads %s", async (path, routeId) => {
    const r = await leaf(path)
    expect(r.id).toBe(routeId)
    expect(r.status).toBe("success")
  })

  it("redirects /settings to /settings/general", async () => {
    const { router } = await leaf("/settings")
    expect(router.state.location.pathname).toBe("/settings/general")
  })

  it("shows not found for an unknown run", async () => {
    const router = routerAt("/runs/run_missing")
    await router.load()
    expect(router.state.matches.some((m) => isNotFound(m.error))).toBe(true)
  })

  it("keeps filters and the selection in the URL", async () => {
    const { router } = await leaf(
      `/runs?status=waiting&tool=issue_refund&run=${RUNS[2].id}`
    )
    expect(router.state.location.search).toMatchObject({
      status: "waiting",
      tool: "issue_refund",
      run: RUNS[2].id,
    })
  })

  it("carries the environment from page to page", async () => {
    const { router } = await leaf("/runs?env=test")
    await router.navigate({ to: "/agents" })
    expect(router.state.location.search).toMatchObject({ env: "test" })
  })

  it("percent-encodes child run ids with slashes", () => {
    const router = routerAt("/")
    const loc = router.buildLocation({
      to: "/runs/$runId",
      params: { runId: "run_8f2a1c/2/0" },
    })
    expect(loc.href).toBe("/runs/run_8f2a1c%2F2%2F0")
  })

  it("maps URL filters to API filters and drops unknown values", () => {
    const s = validateRuns({
      status: "bogus",
      error: "tool_error",
      agent: "",
      q: "refund",
    })
    expect(s.status).toBeUndefined()
    expect(s.agent).toBeUndefined()
    expect(toFilters(s)).toMatchObject({
      error_class: "tool_error",
      q: "refund",
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
