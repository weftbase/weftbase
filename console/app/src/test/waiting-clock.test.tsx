// A run waiting on a person: nothing reports, yet the wait goes on.
// Its row's label, the detail's "Waiting for" and the elapsed time must
// follow the clock, every tick, without a refetch.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest"

import { SpanDetail } from "@/components/console/run/span-detail"
import {
  TraceWaterfall,
  useTraceView,
} from "@/components/console/trace-waterfall"
import type { TraceInput } from "@/components/console/trace-waterfall"
import { viewOf } from "@/lib/api/chain"
import type { RunDetail, Span } from "@/lib/api/types"

import runFail from "./fixtures/run-fail.json"

const T0 = Date.parse("2026-01-01T00:00:00Z")
const at = (ms: number) => new Date(T0 + ms).toISOString()
const span = (
  id: string,
  parent: string,
  op: string,
  a: number,
  b: number,
  extra: Record<string, unknown> = {}
): Span => ({
  trace_id: "t",
  span_id: id,
  parent_span_id: parent,
  name: op,
  kind: "internal",
  start: at(a),
  end: at(b),
  status: "ok",
  status_message: "",
  service: "svc",
  attrs: { "gen_ai.operation.name": op, ...extra },
  events: [],
  run_id: "r",
})
const spans = [
  span("a", "", "invoke_agent", 0, 1000, { "gen_ai.agent.name": "refunds" }),
  span("b", "a", "chat", 0, 800, { "weft.step.index": 0 }),
  span("w", "a", "execute_tool", 900, 1000, {
    "gen_ai.tool.name": "issue_refund",
    "weft.step.index": 0,
    "weft.tool.pending": true,
  }),
]
// Parked: the run is over, its wait is not (to: null), nothing runs.
const input: TraceInput = {
  spans,
  rootRunId: "r",
  waits: [{ run: "r", from: T0 + 1000, to: null }],
  running: false,
}
const run = { ...(runFail as unknown as RunDetail), id: "r" }
const chain = viewOf("r", [run], spans, [], [])

function Page() {
  const view = useTraceView(input)
  const node = view.model.byId.get("w")!
  return (
    <>
      <output data-testid="elapsed">{view.stats.elapsed}</output>
      <output data-testid="waiting">{view.waitingMs}</output>
      <output data-testid="ticking">{String(view.ticking)}</output>
      <TraceWaterfall view={view} selected="w" />
      <SpanDetail
        node={node}
        model={view.model}
        chain={chain}
        onSelect={() => {}}
        onClose={() => {}}
      />
    </>
  )
}

beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
      unobserve() {}
    }
  )
})
beforeEach(() => {
  vi.useFakeTimers()
  // 12.5 s after the pending call started (the row starts with it).
  vi.setSystemTime(T0 + 13_400)
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

it("ticks the wait's row, its detail and the elapsed time while it waits", async () => {
  const qc = new QueryClient({
    defaultOptions: { queries: { enabled: false } },
  })
  render(
    <QueryClientProvider client={qc}>
      <Page />
    </QueryClientProvider>
  )
  const row = () => screen.getByRole("treeitem", { selected: true }).textContent
  const detail = () =>
    screen.getByText(/^Waiting for$/).nextSibling?.textContent
  const elapsed = () => Number(screen.getByTestId("elapsed").textContent)
  expect(row()).toContain("waiting 12.5s")
  expect(detail()).toBe("12.5s")
  const e0 = elapsed()
  const waiting = () => Number(screen.getByTestId("waiting").textContent)
  expect(screen.getByTestId("ticking").textContent).toBe("true")
  expect(waiting()).toBe(12_400)
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000)
  })
  expect(row()).toContain("waiting 17.5s")
  expect(detail()).toBe("17.5s")
  expect(elapsed() - e0).toBeGreaterThanOrEqual(4800)
  // The Duration tile's "… waiting" reads this.
  expect(waiting()).toBe(17_400)
})
