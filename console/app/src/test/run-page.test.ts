// The run page's logic that is not the waterfall's drawing: a child run
// in flight, a model call's attempts, the steps and logs of a chain.
import { afterEach, describe, expect, it, vi } from "vitest"

import { wholeOf } from "@/components/console/run/transcript-tab"
import { liveRunOf } from "@/components/console/run/use-run"
import { buildTrace } from "@/components/console/trace-waterfall/model"
import {
  chainStep,
  chainSteps,
  loadLogsRound,
  locateStep,
  logFrontier,
  treeRunIds,
} from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { api } from "@/lib/api/http"
import type {
  LogsPage,
  RunDetail,
  RunRow,
  Span,
  Transcript,
} from "@/lib/api/types"
import { offset } from "@/lib/format"

const T0 = Date.parse("2026-01-01T00:00:00Z")
const iso = (ms: number) => new Date(T0 + ms).toISOString()

function span(
  id: string,
  parent: string,
  op: string,
  name: string,
  from: number,
  to: number,
  run: string,
  attrs: Record<string, unknown> = {}
): Span {
  return {
    trace_id: "t",
    span_id: id,
    parent_span_id: parent,
    name: op ? `${op} ${name}` : name,
    kind: "internal",
    start: iso(from),
    end: iso(to),
    status: "ok",
    status_message: "",
    service: "svc",
    attrs: {
      ...(op ? { "gen_ai.operation.name": op } : {}),
      ...(op === "invoke_agent" ? { "gen_ai.agent.name": name } : {}),
      ...(op === "execute_tool" ? { "gen_ai.tool.name": name } : {}),
      ...attrs,
    },
    events: [],
    run_id: run,
  }
}

const CHILD = "r/1/call_b"
const row = (o: Partial<RunRow>): RunRow =>
  ({
    id: CHILD,
    parent_run_id: "r",
    parent_call_id: "call_b",
    agent: "billing",
    model: { provider: "acme", name: "acme-1" },
    started: iso(1500),
    finished: null,
    last_seen: iso(1600),
    status: "running",
    display_status: "running",
    steps: 0,
    ...o,
  }) as RunRow

describe("a child run in flight", () => {
  // The parent has reported two calls; its root span and the handoff's
  // tool span come when they end.
  const parent = [
    span("m0", "root", "chat", "gpt", 0, 800, "r", { "weft.step.index": 0 }),
    span("m1", "root", "chat", "gpt", 900, 1500, "r", {
      "weft.step.index": 1,
    }),
  ]
  const input = {
    rootRunId: "r",
    root: { label: "support", started: T0 },
    running: true,
    now: T0 + 3000,
  }

  it("takes a row from its run row, with its calls under it", () => {
    const m = buildTrace({
      ...input,
      spans: [
        ...parent,
        // The child's first model call has ended: its span names an
        // agent span that is not stored yet.
        span("c0", "childroot", "chat", "acme-1", 1500, 2000, CHILD, {
          "weft.step.index": 0,
        }),
      ],
      liveRuns: [liveRunOf(row({}))],
      live: [
        // The parent's handoff call, as its events tell it …
        {
          runId: "r",
          kind: "tool",
          label: "billing",
          start: T0 + 1500,
          step: 1,
          callId: "call_b",
        },
        // … and the child's tool call in flight.
        {
          runId: CHILD,
          kind: "tool",
          label: "list_charges",
          start: T0 + 2000,
          step: 0,
          callId: "call_l",
        },
      ],
    })
    const child = m.byId.get(`run:${CHILD}`)!
    expect(child).toMatchObject({
      base: "agent",
      label: "billing",
      handed: true,
      running: true,
      step: 1,
      callId: "call_b",
    })
    expect(child.parent).toBe(m.roots[0])
    expect(child.children.map((c) => c.label)).toEqual([
      "Call list_charges",
      "list_charges",
    ])
    expect(child.children[1].running).toBe(true)
    // The handoff is one row: the parent's call in flight is not a second.
    expect(m.nodes.filter((n) => n.label === "billing")).toHaveLength(1)
    // Its own lane, from the start.
    expect(child.lane.index).toBe(2)
    expect(m.byId.get("m1")!.label).toBe("Hand off to billing")
  })

  it("gives way to the agent span, which answers to the same id", () => {
    const m = buildTrace({
      ...input,
      spans: [
        ...parent,
        span("h", "root", "execute_tool", "billing", 1500, 2600, "r", {
          "weft.step.index": 1,
          "weft.tool.origin": "subagent",
          "gen_ai.tool.call.id": "call_b",
        }),
        span("childroot", "h", "invoke_agent", "billing", 1500, 2600, CHILD),
      ],
      liveRuns: [liveRunOf(row({}))],
    })
    const child = m.byId.get(`run:${CHILD}`)!
    expect(child.id).toBe("childroot")
    expect(child.handoff?.span_id).toBe("h")
    expect(m.nodes.filter((n) => n.label === "billing")).toHaveLength(1)
    // A call chosen while in flight stays chosen when its span arrives.
    expect(m.byId.get("live:r:model:1:")!.id).toBe("m1")
  })

  it("is over when its row says so, though its span is still on its way", () => {
    const r = liveRunOf(row({ display_status: "failed", finished: iso(2100) }))
    expect(r).toMatchObject({ end: T0 + 2100, failed: true, step: 1 })
    const m = buildTrace({ ...input, spans: parent, liveRuns: [r] })
    expect(m.byId.get(`run:${CHILD}`)).toMatchObject({
      running: false,
      failed: true,
      end: T0 + 2100,
    })
  })
})

describe("a model call's attempts", () => {
  it("are part of the call's row, not rows of their own", () => {
    const m = buildTrace({
      rootRunId: "r",
      spans: [
        span("root", "", "invoke_agent", "a", 0, 2000, "r"),
        span("m", "root", "chat", "gpt", 0, 1600, "r", {
          "weft.step.index": 0,
        }),
        {
          ...span("a1", "m", "", "attempt", 0, 800, "r", {
            "weft.attempt.index": 1,
          }),
          status: "error",
        },
        span("a2", "m", "", "attempt", 800, 1600, "r", {
          "weft.attempt.index": 2,
        }),
      ],
    })
    expect(m.nodes.map((n) => n.id)).toEqual(["root", "m"])
    const call = m.byId.get("m")!
    expect(call.retry).toBe(true)
    expect(call.failed).toBe(false)
    expect(m.byId.get("a1")).toBe(call)
    expect(call.spans.map((s) => s.span_id)).toEqual(["m", "a1", "a2"])
  })
})

const run = (id: string, steps: number, children: RunRow[] = []) =>
  ({ id, steps, children, started: iso(0) }) as unknown as RunDetail
const chainOf = (runs: RunDetail[], spans: Span[] = []) =>
  ({ runs, last: runs.at(-1)!, run: runs[0], spans }) as unknown as ChainView

describe("the steps of a chain", () => {
  // Parked after 2 steps, resumed for 1, parked again, resumed for 1.
  const chain = chainOf([run("a", 2), run("b", 1), run("c", 1)])

  it("are numbered through its runs", () => {
    expect(chainSteps(chain)).toBe(4)
    expect(chainStep(chain, "a", 1)).toBe(1)
    expect(chainStep(chain, "b", 0)).toBe(2)
    expect(chainStep(chain, "c", 0)).toBe(3)
    // A child run keeps its own numbering.
    expect(chainStep(chain, "a/1/call", 0)).toBeUndefined()
  })

  it("map back to the run that took them", () => {
    expect(locateStep(chain, 0)).toMatchObject({ run: { id: "a" }, step: 0 })
    expect(locateStep(chain, 1)).toMatchObject({ run: { id: "a" }, step: 1 })
    expect(locateStep(chain, 2)).toMatchObject({ run: { id: "b" }, step: 0 })
    expect(locateStep(chain, 3)).toMatchObject({ run: { id: "c" }, step: 0 })
  })

  it("read as one conversation", () => {
    const msg = (text: string) => ({
      role: "user" as const,
      content: [{ type: "text" as const, text }],
    })
    const doc = (
      ...batches: [input: boolean, ...texts: string[]][]
    ): Transcript => ({
      batches: batches.map(([input, ...texts], index) => ({
        index,
        step: 0,
        input,
        messages: texts.map(msg),
      })),
    })
    const text = (docs: (Transcript | undefined)[]) =>
      wholeOf(docs).map((m) => (m.content[0] as { text: string }).text)
    // The resume's input repeats what the parked run recorded.
    expect(
      text([
        doc([true, "q"], [false, "call"]),
        doc([true, "q", "call"], [false, "result", "done"]),
      ])
    ).toEqual(["q", "call", "result", "done"])
    // Nothing recorded for the parked run: the resume's input is all there is.
    expect(text([doc(), doc([true, "q", "call"], [false, "done"])])).toEqual([
      "q",
      "call",
      "done",
    ])
  })
})

describe("the logs of a tree", () => {
  afterEach(() => vi.restoreAllMocks())

  it("are read for every run: the chain, its children, and what the spans name", () => {
    const chain = chainOf(
      [run("a", 1, [row({ id: "a/0/x", started: iso(50) })]), run("b", 1)],
      [
        span("1", "", "chat", "m", 10, 20, "a"),
        span("2", "", "chat", "m", 30, 40, "a/0/x/0/y"),
        span("3", "", "chat", "m", 60, 70, "a/0/x"),
      ]
    )
    expect(treeRunIds(chain)).toEqual(["a", "b", "a/0/x/0/y", "a/0/x"])
  })

  const page = (from: number, n: number, more: boolean): LogsPage => ({
    logs: Array.from({ length: n }, (_, i) => ({
      index: from + i,
      time: iso((from + i) * 100),
      severity: "INFO",
      severity_number: 9,
      body: `line ${from + i}`,
      attrs: {},
    })),
    next_from: more ? from + n : undefined,
    holes: [],
  })

  it("a round reads one page of each run and says where each stands", async () => {
    const calls: string[] = []
    vi.spyOn(api, "logsFrom").mockImplementation((id, q) => {
      calls.push(`${id}@${q.from ?? 0}/${q.severity ?? "-"}`)
      return Promise.resolve(
        id === "a" ? page(q.from ?? 0, 2, true) : page(0, 1, false)
      )
    })
    const first = await loadLogsRound(
      { a: { from: 0, at: null }, b: { from: 0, at: null } },
      "warn",
      2
    )
    expect(calls).toEqual(["a@0/warn", "b@0/warn"])
    expect(first.lines.map((l) => `${l.run}:${l.index}`)).toEqual([
      "a:0",
      "a:1",
      "b:0",
    ])
    expect(first.more).toEqual({ a: { from: 2, at: iso(100) } })
    // Lines after the point every run is read to are held back.
    expect(logFrontier(first.more)).toBe(T0 + 100)
    expect(logFrontier({})).toBe(Infinity)
    expect(logFrontier({ a: { from: 0, at: null } })).toBe(-Infinity)

    const second = await loadLogsRound(first.more, undefined, 2)
    expect(calls.at(-1)).toBe("a@2/-")
    expect(second.lines.map((l) => l.index)).toEqual([2, 3])
  })

  it("a run whose logs cannot be read does not stop the others", async () => {
    vi.spyOn(api, "logsFrom").mockImplementation((id) =>
      id === "a"
        ? Promise.reject(new Error("boom"))
        : Promise.resolve(page(0, 1, false))
    )
    const r = await loadLogsRound({
      a: { from: 0, at: null },
      b: { from: 0, at: null },
    })
    expect(r.lines).toHaveLength(1)
    expect(r.more).toEqual({})
    expect(r.failed).toBeInstanceOf(Error)
  })
})

describe("offset", () => {
  it("reads the start itself as +0ms", () => {
    expect(offset(0)).toBe("+0ms")
    expect(offset(0.4)).toBe("+0ms")
    expect(offset(820)).toBe("+820ms")
    expect(offset(2350)).toBe("+2.35s")
  })
})
