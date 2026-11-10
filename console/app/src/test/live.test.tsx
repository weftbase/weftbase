// The live streams wired into the cache must not refetch on every frame
// (C7: the Runs page made 92 /v1/stats requests in 30 s). A step of a
// listed run is a patch; a list refetches when a run appears or changes
// status, at most once every LIST_MS; the stats at most once every
// STATS_MS; filter options never; the outbox's run events (what the
// trace store never sees) refresh the lists and that run's page.
import {
  QueryClient,
  QueryClientProvider,
  QueryObserver,
} from "@tanstack/react-query"
import type { InfiniteData } from "@tanstack/react-query"
import { cleanup, render } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  LIST_MS,
  STATS_MS,
  approvalMoves,
  movesTracker,
  useRunLive,
  useRunsLive,
  useSessionLive,
} from "@/lib/api/live"
import type { HubHandlers } from "@/lib/api/hub"
import type { OutboxEvent, RunFrame, RunRow, RunsList } from "@/lib/api/types"

import runs from "./fixtures/runs.json"

const hub = vi.hoisted(() => ({
  runs: new Set<HubHandlers<RunFrame>>(),
  runEvents: new Set<HubHandlers<OutboxEvent>>(),
}))
vi.mock("@/lib/api/hub", () => ({
  subscribeRuns: (h: HubHandlers<RunFrame>) => {
    hub.runs.add(h)
    return () => hub.runs.delete(h)
  },
  subscribeRunEvents: (h: HubHandlers<OutboxEvent>) => {
    hub.runEvents.add(h)
    return () => hub.runEvents.delete(h)
  },
  subscribeApprovals: () => () => {},
}))

const rows = runs.runs as unknown as RunRow[]
const listed = {
  ...rows[0],
  parent_run_id: "",
  display_status: "running",
} as RunRow
/** A queued run the trace store does not hold yet. */
const queued = {
  ...listed,
  id: "run_queued",
  ledger_only: true,
  display_status: "queued",
} as RunRow

function Shell({ runId }: { runId?: string }) {
  useRunsLive()
  useRunLive(runId ?? "none", { enabled: !!runId })
  return null
}

let qc: QueryClient
let fetched: Record<string, number>

const track = (key: readonly unknown[]) => {
  const name = JSON.stringify(key)
  fetched[name] = 0
  // A query that is on screen: invalidating it refetches it.
  new QueryObserver(qc, {
    queryKey: key,
    queryFn: () => {
      fetched[name]++
      return key[0] === "runs"
        ? {
            pages: [
              {
                total: 1,
                total_exact: true,
                runs: [listed, queued],
                next: null,
              },
            ],
            pageParams: [undefined],
          }
        : {}
    },
    staleTime: Infinity,
  }).subscribe(() => {})
}

const frame = (row: Partial<RunRow>) => {
  for (const h of hub.runs) h.onData({ run: { ...listed, ...row } })
}
const event = (type: string, data: Record<string, unknown>) => {
  for (const h of hub.runEvents)
    h.onData({
      seq: 1,
      id: "evt",
      type,
      resource_kind: "run",
      resource_id: String(data.run_id),
      data,
      created_at: "",
    })
}
const count = (key: readonly unknown[]) => fetched[JSON.stringify(key)]

beforeEach(async () => {
  vi.useFakeTimers()
  qc = new QueryClient()
  fetched = {}
  for (const k of [
    ["runs", "list", {}],
    ["runs", "list", { display_status: "running" }],
    ["runs", "failed", "24h", 5],
    ["stats", "total", "24h"],
    ["options", "agent"],
    ["sessions", "pages", {}],
    ["run", listed.id],
  ])
    track(k)
  await vi.runAllTimersAsync()
  for (const k of Object.keys(fetched)) fetched[k] = 0
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

const mount = (runId?: string) =>
  render(
    <QueryClientProvider client={qc}>
      <Shell runId={runId} />
    </QueryClientProvider>
  )

describe("useRunsLive", () => {
  it("patches a step of a listed run without refetching anything", async () => {
    mount()
    frame({ steps: 1 }) // first sight: news
    await vi.advanceTimersByTimeAsync(STATS_MS)
    for (const k of Object.keys(fetched)) fetched[k] = 0
    for (let i = 2; i < 30; i++) {
      frame({ steps: i })
      await vi.advanceTimersByTimeAsync(1000)
    }
    expect(count(["runs", "list", {}])).toBe(0)
    expect(count(["stats", "total", "24h"])).toBe(0)
    const data = qc.getQueryData<InfiniteData<RunsList>>(["runs", "list", {}])
    expect(data?.pages[0].runs[0].steps).toBe(29)
  })

  it("refetches lists on a status change, the stats slowly, filter options never", async () => {
    mount()
    const statuses = ["running", "waiting", "running", "succeeded"] as const
    for (let i = 0; i < 60; i++) {
      frame({ display_status: statuses[i % 4], steps: i })
      await vi.advanceTimersByTimeAsync(500)
    }
    // 30 s of a status change every 500 ms. A list filtered on the
    // status may gain or lose the run; an unfiltered one keeps it (the
    // patch shows its status); failed runs did not change.
    const filtered = ["runs", "list", { display_status: "running" }]
    expect(count(filtered)).toBeLessThanOrEqual(30_000 / LIST_MS + 1)
    expect(count(filtered)).toBeGreaterThan(0)
    expect(count(["runs", "list", {}])).toBe(0)
    expect(count(["runs", "failed", "24h", 5])).toBe(0)
    expect(count(["stats", "total", "24h"])).toBeLessThanOrEqual(1)
    expect(count(["options", "agent"])).toBe(0)
  })

  it("refreshes the lists on a run event the trace store never sees", async () => {
    mount()
    // A run admitted elsewhere: it belongs at the top.
    event("run.queued", { run_id: "run_new", status: "queued" })
    await vi.advanceTimersByTimeAsync(LIST_MS)
    expect(count(["runs", "list", {}])).toBe(1)
    expect(count(["sessions", "pages", {}])).toBe(1)
    expect(count(["runs", "failed", "24h", 5])).toBe(0)
    // A listed run cancelled before it started: no frame will patch it.
    event("run.canceled", { run_id: queued.id, status: "canceled" })
    await vi.advanceTimersByTimeAsync(LIST_MS)
    expect(count(["runs", "list", {}])).toBe(2)
    // A listed run with a trace: its frames patch it.
    event("run.finished", { run_id: listed.id, status: "succeeded" })
    await vi.advanceTimersByTimeAsync(LIST_MS)
    expect(count(["runs", "list", {}])).toBe(2)
    // A run no list shows that ends: only what it can reshuffle.
    event("run.failed", { run_id: "run_elsewhere", status: "failed" })
    await vi.advanceTimersByTimeAsync(LIST_MS)
    expect(count(["runs", "list", {}])).toBe(2)
    expect(count(["runs", "failed", "24h", 5])).toBe(2)
  })
})

describe("useRunLive", () => {
  it("refetches a run's page on a ledger event of its chain", async () => {
    mount(listed.id)
    event("run.canceled", {
      run_id: "run_resume",
      chain_run_id: listed.id,
      status: "canceled",
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(count(["run", listed.id])).toBe(1)
    event("run.queued", { run_id: "run_someone_else" })
    await vi.advanceTimersByTimeAsync(1000)
    expect(count(["run", listed.id])).toBe(1)
  })
})

describe("movesTracker", () => {
  it("answers news once per status", () => {
    const news = movesTracker(4)
    expect(news("a", "running")).toBe(true)
    expect(news("a", "running")).toBe(false)
    expect(news("a", "succeeded")).toBe(true)
    for (const id of ["b", "c", "d", "e"]) news(id, "running")
    // a was forgotten (bounded): it is news again.
    expect(news("a", "succeeded")).toBe(true)
  })
})

describe("approvalMoves", () => {
  it("names the lists an approval event changes by itself", () => {
    const expired = ["runs", "expired", "24h", 5]
    const pending = ["runs", "list", { approvals: "pending" }]
    const denied = ["runs", "list", { approvals: "denied" }]
    const plain = ["runs", "list", {}]
    expect(approvalMoves(expired, "approval.requested")).toBe(false)
    expect(approvalMoves(expired, "approval.decided")).toBe(false)
    expect(approvalMoves(expired, "approval.expired")).toBe(true)
    expect(approvalMoves(pending, "approval.requested")).toBe(true)
    expect(approvalMoves(denied, "approval.requested")).toBe(false)
    expect(approvalMoves(denied, "approval.decided")).toBe(true)
    expect(approvalMoves(plain, "approval.decided")).toBe(false)
  })
})

describe("useSessionLive", () => {
  function Session() {
    useSessionLive("ses_1")
    return null
  }
  it("hears a queued turn of the session being cancelled (no trace, no frame)", async () => {
    track(["session", "ses_1"])
    await vi.runAllTimersAsync()
    fetched[JSON.stringify(["session", "ses_1"])] = 0
    render(
      <QueryClientProvider client={qc}>
        <Session />
      </QueryClientProvider>
    )
    event("run.cancelled", {
      run_id: "run_q",
      session_id: "ses_1",
      status: "cancelled",
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(count(["session", "ses_1"])).toBe(1)
    event("run.cancelled", {
      run_id: "run_x",
      session_id: "ses_2",
      status: "cancelled",
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(count(["session", "ses_1"])).toBe(1)
  })
})

describe("a delete heard on the outbox", () => {
  it("drops the deleted runs' caches", async () => {
    mount()
    qc.setQueryData(["run", "run_gone"], { id: "run_gone" })
    qc.setQueryData(["run", "run_kept"], { id: "run_kept" })
    for (const h of hub.runEvents)
      h.onData({
        seq: 2,
        id: "evt2",
        type: "run.deleted",
        resource_kind: "run",
        resource_id: "run_gone",
        data: { run_ids: ["run_gone"], ledger_runs: 1, approvals: 0 },
        created_at: "",
      })
    expect(qc.getQueryData(["run", "run_gone"])).toBeUndefined()
    expect(qc.getQueryData(["run", "run_kept"])).toBeDefined()
  })
})
