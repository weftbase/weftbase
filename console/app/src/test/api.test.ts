// The API layer against the server's own response bodies. The fixtures
// in ./fixtures are copies of the goldens in api/http/v1/testdata/v1
// (refresh them with `cp`; never edit them by hand). Each test feeds a
// real body through the client or a helper and checks what the Console
// reads from it.
import { QueryClient } from "@tanstack/react-query"
import type { InfiniteData } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ApiError, isNotFound, isUnauthorized } from "@/lib/api/errors"
import { api, isSearch } from "@/lib/api/http"
import { patchRun } from "@/lib/api/live"
import type {
  RunDetail,
  RunRow,
  RunsList,
  SessionDetail,
  SessionsPage,
  SpansDoc,
  TraceDetail,
  TracesPage,
  Transcript,
} from "@/lib/api/types"
import { cost } from "@/lib/format"
import { assignLanes } from "@/lib/lanes"
import { displayStatus, sessionStatus, statusFilter } from "@/lib/api/status"
import { baseKind, spanKind, spanLabel } from "@/lib/span-kind"

import meta from "./fixtures/meta.json"
import runDetail from "./fixtures/run-children-holes.json"
import runFail from "./fixtures/run-fail.json"
import runs from "./fixtures/runs.json"
import search from "./fixtures/search.json"
import sessionOrders from "./fixtures/session-orders.json"
import sessions from "./fixtures/sessions.json"
import pricing from "./fixtures/settings-pricing.json"
import spansTree from "./fixtures/spans-tree.json"
import trace from "./fixtures/trace.json"
import traces from "./fixtures/traces.json"
import transcriptAsOf from "./fixtures/transcript-as-of-step.json"

const answer = (body: unknown, status = 200) =>
  vi.stubGlobal("fetch", () =>
    Promise.resolve(new Response(JSON.stringify(body), { status }))
  )

afterEach(() => vi.unstubAllGlobals())

describe("runs", () => {
  it("reads a cursor page", async () => {
    answer(runs)
    const page = await api.runs({})
    expect(page.total).toBe(4)
    expect(page.runs.map((r) => r.id)).toEqual([
      "r_stale",
      "r_sub",
      "r_fail",
      "r_ok",
    ])
    // The fixture is the last page.
    expect(page.next).toBeNull()
    const r = page.runs[1]
    expect(r.agent).toBe("researcher")
    expect(r.model).toEqual({ provider: "wefttest", name: "script" })
    expect(r.prompt_preview).toBe("Where is order 42?")
    expect(r.usage.input_tokens + r.usage.output_tokens).toBe(18)
    expect(r.children_count).toBe(1)
  })

  it("turns a time cursor into the next page's parameters", async () => {
    answer({
      ...runs,
      next_before: "2020-01-01T09:00:00.123Z",
      next_before_id: "r_ok",
    })
    const page = await api.runs({})
    expect(page.next).toEqual({
      before: "2020-01-01T09:00:00.123Z",
      before_id: "r_ok",
    })
  })

  it("reads a search page (offset-paged) in the same shape", async () => {
    expect(isSearch({ status: "failed", agent: "a" })).toBe(false)
    expect(isSearch({ q: "refund" })).toBe(true)
    expect(isSearch({ since: "2020-01-01T00:00:00Z" })).toBe(true)
    answer(search)
    const page = await api.runs({ q: "order" })
    expect(page.total).toBe(search.total)
    expect(page.runs.length).toBe(search.runs.length)
    expect(page.next).toEqual(
      search.next_offset === null ? null : { offset: search.next_offset }
    )
  })

  it("never shows an unknown cost as zero", () => {
    const rows = runs.runs as unknown as RunRow[]
    expect(rows[0].cost_usd).toBeNull()
    expect(cost(rows[0])).toBe("unknown")
    expect(cost(rows[1])).toBe("$0")
  })

  it("derives the state the Console shows", () => {
    const rows = runs.runs as unknown as RunRow[]
    expect(rows.map(displayStatus)).toEqual([
      "interrupted",
      "succeeded",
      "failed",
      "succeeded",
    ])
    const ok = rows[3]
    // Parked: succeeded, with a call pending and nothing resumed it.
    expect(displayStatus({ ...ok, pending: 1 })).toBe("waiting")
    expect(displayStatus({ ...ok, pending: 1, resumed_by: "r_next" })).toBe(
      "succeeded"
    )
    expect(displayStatus({ ...rows[2], error_type: "cancelled" })).toBe(
      "cancelled"
    )
    expect(statusFilter("waiting")).toBe("parked")
    expect(statusFilter("failed")).toBe("failed")
  })

  it("reads a run with its children, holes and compactions", () => {
    const run = runDetail as unknown as RunDetail
    expect(Array.isArray(run.children)).toBe(true)
    expect(run.children.length).toBeGreaterThan(0)
    expect(run.children[0].parent_run_id).toBe(run.id)
    expect(Array.isArray(run.holes)).toBe(true)
    expect(Array.isArray(run.compactions)).toBe(true)
    const failed = runFail as unknown as RunDetail
    expect(displayStatus(failed)).toBe("failed")
    expect(failed.err).not.toBe("")
  })

  it("reads a transcript as of a step", () => {
    const t = transcriptAsOf as unknown as Transcript
    expect(t.step).toBe(2)
    expect(t.messages?.[0].role).toBe("user")
    expect(t.batches[0].input).toBe(true)
    expect(t.compacted_at?.replaced).toBe(2)
  })
})

describe("errors", () => {
  it("throws the server's error shape", async () => {
    answer({ error: { code: "not_found", message: "no such run r_x" } }, 404)
    const err = await api.run("r_x").catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(isNotFound(err)).toBe(true)
    expect((err as ApiError).code).toBe("not_found")
    expect((err as ApiError).message).toBe("no such run r_x")
  })

  it("knows a 401 and keeps the request id of a 5xx", async () => {
    answer({ error: { code: "unauthorized", message: "no credential" } }, 401)
    expect(isUnauthorized(await api.meta().catch((e: unknown) => e))).toBe(true)
    answer(
      { error: { code: "internal", message: "boom", request_id: "req_1" } },
      500
    )
    const err = (await api.meta().catch((e: unknown) => e)) as ApiError
    expect(err.requestId).toBe("req_1")
  })
})

describe("sessions, traces, meta", () => {
  it("reads sessions and a session's turns", () => {
    const page = sessions as unknown as SessionsPage
    expect(page.sessions.length).toBe(page.total)
    for (const s of page.sessions) {
      expect(typeof s.id).toBe("string")
      expect(typeof s.turns).toBe("number")
      expect(typeof s.last_seen).toBe("string")
      expect(() => sessionStatus(s)).not.toThrow()
    }
    const s = sessionOrders as unknown as SessionDetail
    expect(s.runs.length).toBe(s.turns)
    expect(s.runs.every((r) => r.session_id === s.id)).toBe(true)
  })

  it("reads traces and a trace", () => {
    const page = traces as unknown as TracesPage
    expect(page.traces[0].trace_id).toMatch(/^[0-9a-f]{32}$/)
    expect(typeof page.traces[0].span_count).toBe("number")
    expect(Array.isArray(page.traces[0].run_ids)).toBe(true)
    const t = trace as unknown as TraceDetail
    expect(t.spans.length).toBeGreaterThan(0)
    expect(Array.isArray(t.run_ids)).toBe(true)
  })

  it("reads meta and pricing", () => {
    expect(meta.principal.scope).toHaveProperty("project")
    expect(meta.principal.scope).toHaveProperty("env")
    expect(typeof meta.weft_version).toBe("string")
    expect(meta).toHaveProperty("retention")
    expect(pricing.models[0]).toHaveProperty("model_glob")
    expect(typeof pricing.models[0].input).toBe("number")
  })
})

describe("spans", () => {
  const doc = spansTree as unknown as SpansDoc

  it("classifies spans by what weft wrote on them", () => {
    expect(doc.spans.map(baseKind)).toEqual(["agent", "agent"])
    expect(spanLabel(doc.spans[0])).toBe("a")
    const tool = {
      ...doc.spans[0],
      attrs: {
        "gen_ai.operation.name": "execute_tool",
        "gen_ai.tool.name": "search",
      },
    }
    expect(spanKind(tool)).toBe("tool")
    expect(spanKind(tool, true)).toBe("waiting")
    expect(spanKind({ ...tool, status: "error" })).toBe("failed")
    expect(
      baseKind({
        ...tool,
        attrs: { ...tool.attrs, "weft.tool.origin": "subagent" },
      })
    ).toBe("handoff")
    expect(
      baseKind({ ...tool, attrs: { "gen_ai.operation.name": "chat" } })
    ).toBe("model")
    expect(baseKind({ ...tool, attrs: {} })).toBe("other")
  })

  it("gives the root run the first hue and each subagent the next", () => {
    const lanes = assignLanes(doc.spans, "tg_root")
    expect(lanes.get("tg_root")?.index).toBe(1)
    expect(lanes.get("tg_root/0/call_a")?.index).toBe(2)
    // A lane follows the agent: the same spans in another order agree.
    const again = assignLanes([...doc.spans].reverse(), "tg_root")
    expect(again.get("tg_root/0/call_a")?.index).toBe(2)
  })
})

describe("live patches", () => {
  const rows = runs.runs as unknown as RunRow[]
  const list = (): RunsList => ({ total: 4, runs: rows, next: null })

  it("patches a run frame into every cached list and the run itself", () => {
    const qc = new QueryClient()
    qc.setQueryData<InfiniteData<RunsList>>(["runs", "list", {}], {
      pages: [list()],
      pageParams: [undefined],
    })
    qc.setQueryData<RunsList>(["runs", "page", { limit: 10 }], list())
    qc.setQueryData(["run", "r_stale"], {
      ...rows[0],
      children: [],
      holes: [],
      compactions: [],
    })

    const frame: RunRow = { ...rows[0], status: "succeeded", steps: 3 }
    expect(patchRun(qc, frame)).toBe(true)

    const inf = qc.getQueryData<InfiniteData<RunsList>>(["runs", "list", {}])
    expect(inf?.pages[0].runs[0]).toMatchObject({
      id: "r_stale",
      status: "succeeded",
      steps: 3,
    })
    expect(inf?.pages[0].runs[1]).toBe(rows[1])
    expect(
      qc.getQueryData<RunsList>(["runs", "page", { limit: 10 }])?.runs[0].steps
    ).toBe(3)
    const detail = qc.getQueryData<RunDetail>(["run", "r_stale"])
    expect(detail?.status).toBe("succeeded")
    expect(detail?.children).toEqual([])
  })

  it("reports a run no list shows, and leaves the lists alone", () => {
    const qc = new QueryClient()
    const data = { pages: [list()], pageParams: [undefined] }
    qc.setQueryData(["runs", "list", {}], data)
    expect(patchRun(qc, { ...rows[0], id: "r_new" })).toBe(false)
    expect(qc.getQueryData(["runs", "list", {}])).toBe(data)
  })
})
