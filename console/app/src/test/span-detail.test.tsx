// The detail of a model call whose step has no request record. The
// server sends a badge ({badge, reason, fix}) in `request`'s place for
// a derived run, a request hole and a call whose record was never
// stored (api/http/v1/steps.go); the detail used to read
// request.body.params and take the whole run page down.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SpanDetail } from "@/components/console/run/span-detail"
import { buildTrace } from "@/components/console/trace-waterfall/model"
import { viewOf } from "@/lib/api/chain"
import { stepRequest, stepRequestBadge } from "@/lib/api/steps"
import type { RunDetail, Span, StepDoc } from "@/lib/api/types"

import step1 from "./fixtures/step-1.json"
import runFail from "./fixtures/run-fail.json"

const at = (ms: number) =>
  new Date(Date.parse("2026-01-01T00:00:00Z") + ms).toISOString()
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

const gap = {
  badge: "gap",
  reason: "the step called the model, but no request record of it was stored",
  fix: "…",
}

afterEach(cleanup)

describe("a step's request", () => {
  it("is the record when the server has one", () => {
    const s = step1 as unknown as StepDoc
    expect(stepRequest(s)?.step).toBe(1)
    expect(stepRequestBadge(s)).toBeUndefined()
  })
  it("is a badge when it has none", () => {
    const s = { request: gap } as StepDoc
    expect(stepRequest(s)).toBeUndefined()
    expect(stepRequestBadge(s)?.badge).toBe("gap")
  })
})

describe("SpanDetail", () => {
  const spans = [
    span("a", "", "invoke_agent", 0, 3000, { "gen_ai.agent.name": "support" }),
    span("b", "a", "chat", 0, 1000, {
      "gen_ai.request.model": "gpt",
      "weft.step.index": 0,
    }),
  ]
  const model = buildTrace({ spans, rootRunId: "r" })
  const node = model.nodes.find((n) => n.base === "model")!
  const run = { ...(runFail as unknown as RunDetail), id: "r" }
  const chain = viewOf("r", [run], spans, [], [])

  const mount = (step: unknown) => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    qc.setQueryData(["run", "r", "step", 0], step)
    qc.setQueryData(["run", "r", "transcript", 0], {
      batches: [],
      messages: [],
    })
    qc.setQueryData(["run", "r", "transcript", "all"], { batches: [] })
    qc.setQueryData(["meta"], { principal: { kind: "local", id: "local" } })
    return render(
      <QueryClientProvider client={qc}>
        <SpanDetail
          node={node}
          model={model}
          chain={chain}
          onSelect={() => {}}
          onClose={() => {}}
        />
      </QueryClientProvider>
    )
  }

  it("draws a model call whose step has a badge for a request", () => {
    expect(node).toBeDefined()
    mount({ ...(step1 as object), step: 0, request: gap })
    expect(screen.getByText(/The request was not recorded/)).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("keeps a detail that throws inside its own container", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {})
    // attempts that is not a list: the body throws while rendering.
    mount({ ...(step1 as object), step: 0, attempts: 7 })
    expect(screen.getByRole("alert").textContent).toMatch(/could not be shown/)
    expect(screen.getByRole("button", { name: "Close detail" })).toBeTruthy()
    err.mockRestore()
  })
})
