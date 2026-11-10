// The trace waterfall's pure logic, on the server's golden span trees
// (fixtures are copies of api/http/v1/testdata/v1) and on synthetic
// trees where a golden has no such case (a chain with a wait, 1,000+
// rows).
import { describe, expect, it } from "vitest"

import {
  GAP_PLAY_MS,
  advance,
  stepFrom,
  stepTargets,
  wallDuration,
} from "@/components/console/trace-waterfall/clock"
import {
  ancestorIds,
  buildStructure,
  buildTrace,
  defaultCollapsed,
  failingNode,
  landingSelection,
  statsAt,
  summarySegments,
  visibleRows,
  withNow,
} from "@/components/console/trace-waterfall/model"
import {
  makeScale,
  niceStep,
  tickLabel,
  ticks,
  zoomAt,
} from "@/components/console/trace-waterfall/scale"
import { liveCalls } from "@/components/console/run/use-run"
import type { PosEvent, Span, SpansDoc, TraceDetail } from "@/lib/api/types"

import eventsOk from "./fixtures/events-ok.json"
import spansSub from "./fixtures/spans-sub.json"
import spansTree from "./fixtures/spans-tree.json"
import trace from "./fixtures/trace.json"

const sub = spansSub as unknown as SpansDoc
const T0 = Date.parse("2026-01-01T00:00:00Z")
const iso = (ms: number) => new Date(T0 + ms).toISOString()

let seq = 0
function span(
  op: "invoke_agent" | "chat" | "execute_tool" | "",
  name: string,
  from: number,
  to: number,
  o: Partial<Span> & { run?: string; attrs?: Record<string, unknown> } = {}
): Span {
  const id = o.span_id ?? `s${String(++seq).padStart(5, "0")}`
  return {
    trace_id: "t",
    span_id: id,
    parent_span_id: o.parent_span_id ?? "",
    name: op ? `${op} ${name}` : name,
    kind: "internal",
    start: iso(from),
    end: iso(to),
    status: o.status ?? "ok",
    status_message: o.status_message ?? "",
    service: "svc",
    attrs: {
      ...(op ? { "gen_ai.operation.name": op } : {}),
      ...(op === "execute_tool" ? { "gen_ai.tool.name": name } : {}),
      ...(op === "invoke_agent" ? { "gen_ai.agent.name": name } : {}),
      ...o.attrs,
    },
    events: [],
    run_id: o.run,
  }
}

/** A parked run and the run that resumed it, 10 minutes later. */
function chainSpans() {
  const WAIT = 600_000
  const a = span("invoke_agent", "refunds", 0, 2000, {
    span_id: "a",
    run: "r1",
  })
  const a1 = span("chat", "m", 0, 800, {
    span_id: "a1",
    parent_span_id: "a",
    run: "r1",
    attrs: {
      "weft.step.index": 0,
      "gen_ai.usage.input_tokens": 100,
      "gen_ai.usage.output_tokens": 10,
    },
  })
  const a2 = span("execute_tool", "issue_refund", 2000, 2000, {
    span_id: "a2",
    parent_span_id: "a",
    run: "r1",
    status: "unset",
    attrs: {
      "weft.tool.pending": true,
      "weft.step.index": 0,
      "gen_ai.tool.call.id": "c1",
    },
  })
  const b = span("invoke_agent", "refunds", 2000 + WAIT, 5000 + WAIT, {
    span_id: "b",
    run: "r2",
  })
  const b1 = span("execute_tool", "issue_refund", 2000 + WAIT, 3000 + WAIT, {
    span_id: "b1",
    parent_span_id: "b",
    run: "r2",
    attrs: {
      "weft.step.index": 0,
      "weft.tool.approved": true,
      "gen_ai.tool.call.id": "c1",
    },
  })
  const b2 = span("chat", "m", 3000 + WAIT, 5000 + WAIT, {
    span_id: "b2",
    parent_span_id: "b",
    run: "r2",
    attrs: {
      "weft.model.tool_calls": 0,
      "weft.step.index": 0,
      "gen_ai.usage.input_tokens": 200,
      "gen_ai.usage.output_tokens": 20,
    },
  })
  return {
    spans: [b2, a, b, a1, b1, a2],
    waits: [
      {
        run: "r1",
        from: T0 + 2000,
        to: T0 + 2000 + WAIT,
        decided: "issue_refund approved by operator op_1",
      },
    ],
    WAIT,
  }
}

/** A run that looks an order up, hands off to a billing agent (a child
 * run under the tool call), and answers: the demo's shape. */
function handoffSpans(): Span[] {
  const child = "p/1/call_1"
  return [
    span("invoke_agent", "support", 0, 5000, { span_id: "p0", run: "p" }),
    span("chat", "gpt", 0, 800, {
      span_id: "p1",
      parent_span_id: "p0",
      run: "p",
      attrs: {
        "gen_ai.request.model": "claude-sonnet-4-6",
        "weft.step.index": 0,
      },
    }),
    span("execute_tool", "lookup_order", 800, 1200, {
      span_id: "p2",
      parent_span_id: "p0",
      run: "p",
      attrs: { "weft.step.index": 0 },
    }),
    span("chat", "gpt", 1200, 1800, {
      span_id: "p3",
      parent_span_id: "p0",
      run: "p",
      attrs: { "weft.step.index": 1 },
    }),
    span("execute_tool", "billing", 1800, 3900, {
      span_id: "p4",
      parent_span_id: "p0",
      run: "p",
      attrs: { "weft.step.index": 1, "weft.tool.origin": "subagent" },
    }),
    span("invoke_agent", "billing", 1800, 3900, {
      span_id: "c0",
      parent_span_id: "p4",
      run: child,
    }),
    span("chat", "acme", 1800, 2300, {
      span_id: "c1",
      parent_span_id: "c0",
      run: child,
      attrs: { "weft.step.index": 0 },
    }),
    span("execute_tool", "list_charges", 2300, 3000, {
      span_id: "c2",
      parent_span_id: "c0",
      run: child,
      attrs: { "weft.step.index": 0 },
    }),
    span("chat", "acme", 3000, 3900, {
      span_id: "c3",
      parent_span_id: "c0",
      run: child,
      attrs: { "weft.step.index": 1, "weft.model.tool_calls": 0 },
    }),
    span("chat", "gpt", 3900, 5000, {
      span_id: "p5",
      parent_span_id: "p0",
      run: "p",
      attrs: {
        "gen_ai.response.finish_reasons": ["stop"],
        "weft.step.index": 2,
      },
    }),
  ]
}

describe("buildTrace", () => {
  it("reads a child run's own tree (the golden) as one agent row", () => {
    const m = buildTrace({ spans: sub.spans, rootRunId: "r_sub" })
    expect(m.roots).toHaveLength(1)
    expect(m.roots[0]).toMatchObject({
      base: "agent",
      label: "researcher",
      depth: 0,
    })
    expect(m.roots[0].lane.index).toBe(1)
  })

  it("nests a child run under the handoff and folds the two into one row", () => {
    const spans = handoffSpans()
    const m = buildTrace({ spans, rootRunId: "p" })
    expect(m.roots).toHaveLength(1)
    expect(m.roots[0].base).toBe("agent")
    const handoffs = m.nodes.filter((n) => n.handoff)
    expect(handoffs.map((h) => h.label)).toEqual(["billing"])
    for (const h of handoffs) {
      expect(h.base).toBe("agent")
      // The row answers to the agent span and to the tool span.
      expect(m.byId.get(h.handoff!.span_id)).toBe(h)
      expect(m.byId.get(h.span!.span_id)).toBe(h)
      // The root has the first hue; a subagent takes the next.
      expect(h.lane.index).toBe(2)
      for (const c of h.children) expect(c.lane).toBe(h.lane)
    }
    expect(m.roots[0].lane.index).toBe(1)
    // A model call is named by what it did; the model is the muted word.
    expect(
      m.nodes.filter((n) => n.base === "model").map((n) => n.label)
    ).toEqual([
      "Call lookup_order",
      "Hand off to billing",
      "Call list_charges",
      "Reply",
      "Reply",
    ])
    expect(m.byId.get("p1")).toMatchObject({
      secondary: "sonnet-4-6",
      model: "claude-sonnet-4-6",
    })
    // Every span is on exactly one row.
    const onRows = m.nodes.flatMap((n) => n.spans.map((s) => s.span_id))
    expect(new Set(onRows).size).toBe(spans.length)
    expect(onRows).toHaveLength(spans.length)
    for (const n of m.nodes)
      if (n.parent) expect(n.depth).toBe(n.parent.depth + 1)
    expect(m.nodes.map((n) => n.id)).toEqual([
      "p0",
      "p1",
      "p2",
      "p3",
      "c0",
      "c1",
      "c2",
      "c3",
      "p5",
    ])
  })

  it("keeps two unrelated root spans of a holed tree as rows", () => {
    const doc = spansTree as unknown as SpansDoc
    const m = buildTrace({ spans: doc.spans, rootRunId: "tg_root" })
    expect(m.nodes).toHaveLength(2)
    // The child's handoff span is missing: it hangs under the root run.
    expect(m.roots).toHaveLength(1)
    expect(m.roots[0].children[0].runId).toBe("tg_root/0/call_a")
  })

  it("draws a raw trace with neutral lanes and nothing folded", () => {
    const t = trace as unknown as TraceDetail
    const m = buildTrace({ spans: t.spans })
    expect(m.nodes).toHaveLength(t.spans.length)
    for (const n of m.nodes) {
      expect(n.lane.index).toBe(0)
      expect(n.handoff).toBeUndefined()
    }
  })

  it("folds a chain into one root with the wait as a hatched row", () => {
    const { spans, waits, WAIT } = chainSpans()
    const m = buildTrace({ spans, rootRunId: "r1", chain: ["r1", "r2"], waits })
    expect(m.roots).toHaveLength(1)
    const root = m.roots[0]
    expect(root.id).toBe("a")
    expect(m.byId.get("b")).toBe(root)
    expect(root.children.map((c) => c.id)).toEqual(["a1", "a2", "b1", "b2"])
    const wait = m.byId.get("a2")!
    for (const n of m.nodes) expect(n.lane.index).toBe(n === wait ? 0 : 1)
    expect(wait.kind).toBe("waiting")
    expect(wait).toMatchObject({
      label: "Waiting for approval",
      secondary: "issue_refund",
      tool: "issue_refund",
    })
    // The approved call ran before the resumed run's model call: that
    // call did not ask for it.
    expect(m.byId.get("b2")!.label).toBe("Reply")
    expect(wait.end - wait.start).toBe(WAIT)
    expect(wait.note).toContain("approved by operator op_1")
    expect(root.end - root.start).toBe(5000 + WAIT)
    // The slowest call is a call, never the wait.
    expect(m.slowestId).toBe("b2")
  })

  it("leaves a pending call that never got a decision at zero width", () => {
    const { spans } = chainSpans()
    const m = buildTrace({
      spans: spans.filter((s) => s.run_id === "r1"),
      rootRunId: "r1",
    })
    const wait = m.byId.get("a2")!
    expect(wait.kind).toBe("waiting")
    expect(wait.end).toBe(wait.start)
    expect(wait.note).toMatch(/did not run/)
  })

  it("adds calls in flight and a stand-in root for a live run", () => {
    const now = T0 + 3000
    const m = buildTrace({
      spans: [
        span("chat", "m", 0, 900, {
          span_id: "x1",
          run: "r",
          parent_span_id: "not-yet",
          attrs: { "weft.step.index": 0 },
        }),
      ],
      rootRunId: "r",
      root: { label: "support", started: T0 },
      live: [
        {
          runId: "r",
          kind: "tool",
          label: "lookup",
          start: T0 + 900,
          step: 0,
          callId: "c1",
        },
      ],
      running: true,
      now,
    })
    expect(m.roots[0].label).toBe("support")
    expect(m.roots[0].running).toBe(true)
    expect(m.roots[0].children.map((c) => c.label)).toEqual([
      "Call lookup",
      "lookup",
    ])
    const live = m.roots[0].children[1]
    expect(live.running).toBe(true)
    expect(live.end).toBe(now)
    expect(m.t1).toBe(now)
    // The stand-in keeps answering to its id when the real root arrives.
    expect(m.byId.get("run:r")).toBe(m.roots[0])
  })

  it("places a compaction marker before the call of its step", () => {
    const m = buildTrace({
      spans: [
        span("invoke_agent", "a", 0, 3000, { span_id: "r", run: "r" }),
        span("chat", "m", 0, 1000, {
          parent_span_id: "r",
          run: "r",
          attrs: { "weft.step.index": 0 },
        }),
        span("chat", "m", 1000, 3000, {
          span_id: "second",
          parent_span_id: "r",
          run: "r",
          attrs: { "weft.step.index": 1 },
        }),
      ],
      rootRunId: "r",
      compactions: [
        {
          scope: "run",
          step: 1,
          hash: "h",
          replaced: 12,
          entries: 1,
          tokens_before: 41_000,
          tokens_after: 9000,
        },
      ],
    })
    const kids = m.roots[0].children
    expect(kids[1].kind).toBe("marker")
    expect(kids[1].label).toBe("Context compacted: 41.0k → 9,000 tokens")
    expect(kids[2].id).toBe("second")
  })
})

describe("rows", () => {
  const m = buildTrace({ spans: handoffSpans(), rootRunId: "p" })

  it("starts with rows deeper than two levels folded", () => {
    const folded = defaultCollapsed(m)
    for (const id of folded)
      expect(m.byId.get(id)!.depth).toBeGreaterThanOrEqual(2)
    const rows = visibleRows(m, { collapsed: folded })
    for (const r of rows)
      for (const a of ancestorIds(m, r.node.id))
        expect(folded.has(a)).toBe(false)
    expect(visibleRows(m, { collapsed: new Set() })).toHaveLength(
      m.nodes.length
    )
  })

  it("search keeps the matches and their ancestors, whatever is folded", () => {
    const leaf = m.byId.get("c2")!
    const rows = visibleRows(m, {
      collapsed: defaultCollapsed(m),
      query: leaf.label.toUpperCase(),
    })
    const ids = rows.map((r) => r.node.id)
    expect(ids).toContain(leaf.id)
    for (const a of ancestorIds(m, leaf.id)) expect(ids).toContain(a)
    for (const r of rows) {
      const hit = r.node.label.toLowerCase().includes(leaf.label.toLowerCase())
      const above = rows.some((x) =>
        ancestorIds(m, x.node.id).includes(r.node.id)
      )
      expect(hit || above).toBe(true)
    }
    expect(
      visibleRows(m, { collapsed: new Set(), query: "no such step anywhere" })
    ).toEqual([])
  })

  it("the legend filters by kind and keeps ancestors", () => {
    const rows = visibleRows(m, {
      collapsed: new Set(),
      kinds: new Set(["handoff"]),
    })
    expect(rows.some((r) => r.node.handoff)).toBe(true)
    for (const r of rows)
      expect(r.node.handoff !== undefined || r.expandable).toBe(true)
  })

  it("an agent's summary bar has one segment per child", () => {
    const root = m.roots[0]
    const segs = summarySegments(root)
    expect(segs).toHaveLength(root.children.length)
    for (const s of segs) {
      expect(s.start).toBeGreaterThanOrEqual(root.start)
      expect(s.end).toBeLessThanOrEqual(root.end)
    }
  })

  it("builds and lists a 5,000-row tree quickly", () => {
    const spans: Span[] = [
      span("invoke_agent", "root", 0, 600_000, { span_id: "root", run: "big" }),
    ]
    for (let a = 0; a < 50; a++) {
      const run = `big/${a}/c`
      spans.push(
        span("execute_tool", `agent${a}`, a * 10_000, a * 10_000 + 9000, {
          span_id: `h${a}`,
          parent_span_id: "root",
          run: "big",
          attrs: { "weft.tool.origin": "subagent" },
        })
      )
      spans.push(
        span("invoke_agent", `agent${a}`, a * 10_000, a * 10_000 + 9000, {
          span_id: `g${a}`,
          parent_span_id: `h${a}`,
          run,
        })
      )
      for (let i = 0; i < 98; i++)
        spans.push(
          span(
            i % 2 ? "execute_tool" : "chat",
            `call${i}`,
            a * 10_000 + i * 90,
            a * 10_000 + i * 90 + 80,
            { parent_span_id: `g${a}`, run }
          )
        )
    }
    const t = performance.now()
    const big = buildTrace({ spans, rootRunId: "big" })
    const rows = visibleRows(big, { collapsed: new Set() })
    const found = visibleRows(big, { collapsed: new Set(), query: "call42" })
    const ms = performance.now() - t
    expect(big.nodes).toHaveLength(1 + 50 * 99)
    expect(rows).toHaveLength(big.nodes.length)
    expect(found).toHaveLength(1 + 50 * 2)
    // The root and the first five subagents have hues; later ones are neutral.
    expect(big.roots[0].lane.index).toBe(1)
    expect(big.byId.get("g0")!.lane.index).toBe(2)
    expect(big.byId.get("g4")!.lane.index).toBe(6)
    expect(big.byId.get("g5")!.lane.index).toBe(0)
    expect(ms).toBeLessThan(1500)
  })
})

describe("landing selection", () => {
  const failed = [
    span("invoke_agent", "a", 0, 4000, {
      span_id: "r",
      run: "r",
      status: "error",
    }),
    span("chat", "m", 0, 1000, {
      span_id: "m0",
      parent_span_id: "r",
      run: "r",
    }),
    span("execute_tool", "t", 1000, 2000, {
      span_id: "t0",
      parent_span_id: "r",
      run: "r",
      status: "error",
    }),
    span("chat", "m", 2000, 3000, {
      span_id: "m1",
      parent_span_id: "r",
      run: "r",
    }),
    span("execute_tool", "t", 3000, 4000, {
      span_id: "t1",
      parent_span_id: "r",
      run: "r",
      status: "error",
    }),
  ]
  it("a failed run lands on the failed call that ended last", () => {
    const m = buildTrace({ spans: failed, rootRunId: "r" })
    expect(failingNode(m)!.id).toBe("t1")
    expect(landingSelection(m, "failed")).toBe("t1")
  })
  it("a run that failed with no failed call lands on its agent", () => {
    const m = buildTrace({ spans: [failed[0], failed[1]], rootRunId: "r" })
    expect(landingSelection(m, "failed")).toBe("r")
  })
  it("a waiting run lands on the open wait", () => {
    const { spans } = chainSpans()
    const m = buildTrace({
      spans: spans.filter((s) => s.run_id === "r1"),
      rootRunId: "r1",
      waits: [{ run: "r1", from: T0 + 2000, to: null }],
      now: T0 + 60_000,
    })
    expect(landingSelection(m, "waiting")).toBe("a2")
    expect(m.byId.get("a2")!.end).toBe(T0 + 60_000)
  })
  it("anything else lands on nothing", () => {
    const m = buildTrace({ spans: failed, rootRunId: "r" })
    for (const s of [
      "succeeded",
      "running",
      "cancelled",
      "interrupted",
      "queued",
    ])
      expect(landingSelection(m, s)).toBeUndefined()
  })
})

describe("statsAt", () => {
  const { spans, waits, WAIT } = chainSpans()
  const m = buildTrace({ spans, rootRunId: "r1", chain: ["r1", "r2"], waits })
  it("counts what had happened by the playhead", () => {
    expect(statsAt(m, T0 + 500)).toMatchObject({
      modelCalls: 1,
      toolCalls: 0,
      tokensIn: 0,
      elapsed: 500,
    })
    expect(statsAt(m, T0 + 900)).toMatchObject({
      modelCalls: 1,
      tokensIn: 100,
      tokensOut: 10,
    })
    const mid = statsAt(m, T0 + 2500 + WAIT)
    expect(mid).toMatchObject({ modelCalls: 1, toolCalls: 1, waited: WAIT })
  })
  it("is the whole trace at the end", () => {
    expect(statsAt(m, Infinity)).toMatchObject({
      modelCalls: 2,
      toolCalls: 1,
      tokensIn: 300,
      tokensOut: 30,
      failed: 0,
      elapsed: 5000 + WAIT,
    })
  })
})

describe("scale and ticks", () => {
  it("is linear without a long wait", () => {
    const s = makeScale(1000, 6000, [{ from: 2000, to: 3500 }])
    expect(s.gaps).toEqual([])
    expect(s.length).toBe(5000)
    expect(s.toDisplay(3500)).toBe(2500)
    expect(s.toReal(2500)).toBe(3500)
  })

  it("compresses a wait longer than 2 s and maps both ways", () => {
    const s = makeScale(0, 605_000, [{ from: 2000, to: 602_000 }])
    expect(s.gaps).toHaveLength(1)
    const g = s.gaps[0]
    expect(g.d0).toBe(2000)
    expect(g.d1 - g.d0).toBe(400) // 8% of 5 s of activity
    expect(s.length).toBe(5400)
    expect(s.toDisplay(602_000)).toBe(2400)
    expect(s.toDisplay(605_000)).toBe(5400)
    expect(s.toDisplay(302_000)).toBe(2200)
    for (const t of [0, 1500, 2000, 302_000, 602_000, 604_000, 605_000])
      expect(s.toReal(s.toDisplay(t))).toBeCloseTo(t, 6)
  })

  it("an open wait runs to the end of the axis", () => {
    const s = makeScale(0, 100_000, [{ from: 3000, to: null }])
    expect(s.gaps[0].to).toBe(100_000)
    expect(s.toDisplay(100_000)).toBe(s.length)
  })

  it("picks readable steps", () => {
    expect(niceStep(1)).toBe(1)
    expect(niceStep(3)).toBe(5)
    expect(niceStep(700)).toBe(1000)
    expect(niceStep(11_000)).toBe(15_000)
    expect(niceStep(61_000)).toBe(120_000)
  })

  it("labels ticks", () => {
    expect(tickLabel(0, 500)).toBe("0s")
    expect(tickLabel(500, 500)).toBe("500ms")
    expect(tickLabel(1500, 500)).toBe("1.5s")
    expect(tickLabel(602_500, 500)).toBe("10m 02.5s")
    expect(tickLabel(40_000, 20_000)).toBe("40s")
    expect(tickLabel(90_000, 30_000)).toBe("1m 30s")
    expect(tickLabel(120_000, 60_000)).toBe("2m")
    expect(tickLabel(3_900_000, 300_000)).toBe("1h 05m")
  })

  it("adapts the ticks to the width", () => {
    const s = makeScale(0, 64_000)
    const wide = ticks(s, 0, s.length, 900)
    expect(wide.map((t) => t.label).slice(0, 4)).toEqual([
      "0s",
      "10s",
      "20s",
      "30s",
    ])
    const narrow = ticks(s, 0, s.length, 240)
    expect(narrow.map((t) => t.label)).toEqual(["0s", "30s", "1m"])
    // Zoomed into 2 s: sub-second ticks.
    const zoomed = ticks(s, 10_000, 12_000, 900)
    expect(zoomed[0].label).toBe("10s")
    expect(zoomed[1].label).toBe("10.2s")
  })

  it("puts no tick inside a gap and labels real time after it", () => {
    const s = makeScale(0, 605_000, [{ from: 2000, to: 602_000 }])
    const ts = ticks(s, 0, s.length, 1000)
    for (const t of ts) {
      const inside = t.d > s.gaps[0].d0 + 1e-6 && t.d < s.gaps[0].d1 - 1e-6
      expect(inside).toBe(false)
    }
    expect(ts.some((t) => t.label.startsWith("10m"))).toBe(true)
  })

  it("zooms around the cursor and stays in range", () => {
    const [a, b] = zoomAt(0, 1000, 250, 0.5, 1000)
    expect([a, b]).toEqual([125, 625])
    expect(zoomAt(0, 1000, 500, 4, 1000)).toEqual([0, 1000])
    const [c, d] = zoomAt(900, 1000, 1000, 2, 1000)
    expect(d).toBe(1000)
    expect(d - c).toBe(200)
  })
})

describe("playback clock", () => {
  const plain = makeScale(0, 10_000)
  const gapped = makeScale(0, 605_000, [{ from: 2000, to: 602_000 }])

  it("moves at the speed", () => {
    expect(advance(plain, 0, 1000, 1)).toBe(1000)
    expect(advance(plain, 1000, 500, 4)).toBe(3000)
    expect(advance(plain, 9900, 1000, 1)).toBe(10_000)
  })

  it("crosses a compressed wait in one second, whatever the speed", () => {
    const g = gapped.gaps[0]
    for (const speed of [0.5, 1, 4]) {
      expect(advance(gapped, g.d0, GAP_PLAY_MS, speed)).toBeCloseTo(g.d1, 6)
      expect(advance(gapped, g.d0, GAP_PLAY_MS / 2, speed)).toBeCloseTo(
        (g.d0 + g.d1) / 2,
        6
      )
    }
    // 1 s to the gap, 1 s across it, 0.5 s after it, at 2×.
    expect(advance(gapped, 0, 1000 + 1000 + 500, 2)).toBeCloseTo(g.d1 + 1000, 6)
  })

  it("a ten-minute wait adds one second to the whole playback", () => {
    expect(wallDuration(gapped, 1)).toBe(5000 + GAP_PLAY_MS)
    expect(wallDuration(gapped, 2)).toBe(2500 + GAP_PLAY_MS)
    expect(advance(gapped, 0, wallDuration(gapped, 1), 1)).toBeCloseTo(
      gapped.length,
      6
    )
  })

  it("steps to the previous and next row start, deterministically", () => {
    const targets = stepTargets(plain, [0, 1000, 1000, 4000, 2500])
    expect(targets).toEqual([0, 1000, 2500, 4000])
    expect(stepFrom(targets, 0, 1, plain.length)).toBe(1000)
    expect(stepFrom(targets, 1000, 1, plain.length)).toBe(2500)
    expect(stepFrom(targets, 1700, -1, plain.length)).toBe(1000)
    expect(stepFrom(targets, 1000, -1, plain.length)).toBe(0)
    expect(stepFrom(targets, 4000, 1, plain.length)).toBe(plain.length)
    expect(stepFrom(targets, plain.length, -1, plain.length)).toBe(4000)
    expect(stepFrom(targets, 0, -1, plain.length)).toBe(0)
  })
})

describe("liveCalls", () => {
  const events = (eventsOk as unknown as { events: PosEvent[] }).events
  it("reports nothing in flight for a finished run", () => {
    expect(liveCalls("r_ok", "m", events, [])).toEqual([])
  })
  it("reports the call that has started and has no span yet", () => {
    const upTo = (type: string) =>
      events.slice(0, events.findIndex((e) => e.event.type === type) + 1)
    const afterStep = liveCalls("r_ok", "script", upTo("step_start"), [])
    expect(afterStep).toHaveLength(1)
    expect(afterStep[0]).toMatchObject({
      kind: "model",
      label: "script",
      step: 0,
    })
    if (events.some((e) => e.event.type === "tool_start")) {
      const afterTool = liveCalls("r_ok", "script", upTo("tool_start"), [])
      expect(afterTool).toHaveLength(1)
      expect(afterTool[0].kind).toBe("tool")
    }
  })
})

describe("the clock over a live tree", () => {
  const live = () => {
    const root = span("invoke_agent", "support", 0, 1000, { run: "r" })
    const call = span("chat", "gpt", 0, 400, {
      run: "r",
      parent_span_id: root.span_id,
      attrs: { "weft.step.index": 0 },
    })
    return {
      spans: [root, call],
      rootRunId: "r",
      running: true,
      live: [
        {
          kind: "tool" as const,
          runId: "r",
          label: "lookup",
          start: T0 + 500,
          step: 0,
          callId: "c1",
        },
      ],
      waits: [{ run: "r", from: T0 + 600, to: null }],
    }
  }

  it("draws the same tree as a full build, at any now", () => {
    const input = live()
    const st = buildStructure(input)
    for (const now of [T0 + 700, T0 + 5000, T0 + 9000]) {
      const a = withNow(st, now)
      const b = buildTrace({ ...input, now })
      expect([a.t0, a.t1]).toEqual([b.t0, b.t1])
      expect(a.nodes.map((n) => [n.id, n.start, n.end])).toEqual(
        b.nodes.map((n) => [n.id, n.start, n.end])
      )
    }
  })

  it("moves only the clock: the rows stay the same objects", () => {
    const st = buildStructure(live())
    const a = withNow(st, T0 + 2000)
    const ends = a.nodes.map((n) => n.end)
    const b = withNow(st, T0 + 3000)
    expect(b.nodes).toBe(a.nodes)
    expect(b.t1).toBe(T0 + 3000)
    // The root, the call in flight and the open wait follow now.
    expect(
      b.nodes.map((n) => n.end).filter((e, i) => e !== ends[i]).length
    ).toBe(3)
    // Back in time (clock skew) gives the same answer as before.
    expect(withNow(st, T0 + 2000).nodes.map((n) => n.end)).toEqual(ends)
  })
})

describe("spans that cannot be drawn as recorded", () => {
  it("draws a parent loop's rows at the top level and says so", () => {
    const a = span("invoke_agent", "a", 0, 1000, { run: "r", span_id: "a" })
    const b = span("chat", "m", 100, 200, {
      run: "r",
      span_id: "b",
      parent_span_id: "c",
    })
    const c = span("execute_tool", "t", 300, 400, {
      run: "r",
      span_id: "c",
      parent_span_id: "b",
    })
    for (const input of [
      { spans: [a, b, c], rootRunId: "r" },
      { spans: [b, c] }, // a raw trace
    ]) {
      const m = buildTrace(input)
      expect(visibleRows(m, { collapsed: new Set() }).length).toBe(
        input.spans.length
      )
      expect(m.repaired).toEqual({ cycles: 1, unreadable: 0 })
    }
  })

  it("leaves out a span whose start does not parse; the axis stands", () => {
    const a = span("invoke_agent", "a", 0, 1000, { run: "r", span_id: "a" })
    const bad = {
      ...span("chat", "m", 0, 0, { run: "r", parent_span_id: "a" }),
      start: "garbage",
      end: "",
    }
    const m = buildTrace({ spans: [a, bad], rootRunId: "r" })
    expect(Number.isFinite(m.t0) && Number.isFinite(m.t1)).toBe(true)
    expect(m.t1 - m.t0).toBe(1000)
    expect(m.repaired).toEqual({ cycles: 0, unreadable: 1 })
    expect(buildTrace({ spans: [a], rootRunId: "r" }).repaired).toBeUndefined()
  })

  it("reads an end that does not parse as the start", () => {
    const a = { ...span("chat", "m", 100, 200, { run: "r" }), end: "nope" }
    const m = buildTrace({ spans: [a] })
    expect(m.nodes[0].end).toBe(m.nodes[0].start)
  })
})
