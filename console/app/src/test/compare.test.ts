// Compare two runs: the pure logic of lib/compare.ts on real bodies.
//
// The compare-*.json fixtures are CAPTURES from the demo server
// (examples/demo, GET /v1/diff and GET /v1/runs/{id}/spans?tree=1&chain=1),
// not goldens: the server has no golden of these runs. The one exception
// is compare-diff-replay.json, a copy of
// api/http/v1/testdata/v1/diff-replay.golden.json (a replay's kept row).
//   compare-diff-failed    a support run that succeeded in 3 steps (A)
//                          against one that failed after 4 (B)
//   compare-diff-same      two support runs of the same scenario
//   compare-diff-refunds   two refunds runs up to the approval they park on
//   compare-diff-resume    their resumes: approved (A), denied (B)
//   compare-spans-support  the spans of compare-diff-failed's run A
import { describe, expect, it } from "vitest"

import type { DiffDoc, SpansDoc } from "@/lib/api/types"
import {
  buildCompare,
  itemsOf,
  pairChains,
  stepFacts,
  summaryOf,
} from "@/lib/compare"

import failedJson from "./fixtures/compare-diff-failed.json"
import refundsJson from "./fixtures/compare-diff-refunds.json"
import replayJson from "./fixtures/compare-diff-replay.json"
import resumeJson from "./fixtures/compare-diff-resume.json"
import sameJson from "./fixtures/compare-diff-same.json"
import spansJson from "./fixtures/compare-spans-support.json"

const failed = failedJson as unknown as DiffDoc
const same = sameJson as unknown as DiffDoc
const refunds = refundsJson as unknown as DiffDoc
const resume = resumeJson as unknown as DiffDoc
const replay = replayJson as unknown as DiffDoc
const spans = (spansJson as unknown as SpansDoc).spans

describe("stepFacts", () => {
  const facts = stepFacts(spans, failed.a.run_id)

  it("reads each step's duration, model and subagent tools from the spans", () => {
    expect([...facts.keys()].sort()).toEqual([0, 1, 2])
    // Step 0: the model call's start to its tool call's end.
    expect(facts.get(0)?.durationMs).toBe(1251)
    expect(facts.get(0)?.model).toBe("gpt-4o-mini")
    expect([...(facts.get(0)?.handoffs ?? [])]).toEqual([])
    expect([...(facts.get(1)?.handoffs ?? [])]).toEqual(["billing"])
  })

  it("leaves the subagent's own steps out", () => {
    // The billing child run has steps 0 and 1 of its own, with another
    // model: none of it is the parent's.
    expect([...facts.values()].map((f) => f.model)).toEqual([
      "gpt-4o-mini",
      "gpt-4o-mini",
      "gpt-4o-mini",
    ])
  })
})

describe("buildCompare", () => {
  it("names a step as the waterfall names a model call", () => {
    const facts = stepFacts(spans, failed.a.run_id)
    const m = buildCompare([{ doc: failed }], (id) =>
      id === failed.a.run_id ? facts : undefined
    )
    expect(m.rows.map((r) => r.a?.name ?? null)).toEqual([
      "Call lookup_order",
      "Hand off to billing",
      "Reply",
      null,
    ])
    expect(m.rows.map((r) => r.b?.name)).toEqual(
      Array(4).fill("Call update_address")
    )
    expect(m.rows[0].a?.durationMs).toBe(1251)
    expect(m.rows[0].b?.durationMs).toBeUndefined()
  })

  it("knows a lone subagent call from the step's mark when there are no spans", () => {
    const m = buildCompare([{ doc: failed }])
    expect(m.rows[1].a?.name).toBe("Hand off to billing")
  })

  it("takes what differs from the server and says it in words", () => {
    const m = buildCompare([{ doc: failed }])
    expect(m.rows.map((r) => r.kind)).toEqual([
      "changed",
      "changed",
      "changed",
      "only_b",
    ])
    expect(m.rows[0].what).toEqual(["tool", "result", "tokens"])
    // Step 2: A replied, B called a tool again.
    expect(m.rows[2].what).toEqual(["tool", "result", "reply", "tokens"])
    expect(m.rows[3].what).toEqual([])
    expect(m.differing).toBe(4)
    expect(summaryOf(m)).toBe("4 of 4 steps differ, from step #0.")
  })

  it("marks the call that differs, and its result", () => {
    const m = buildCompare([{ doc: failed }])
    const [a] = m.rows[0].a?.calls ?? []
    const [b] = m.rows[0].b?.calls ?? []
    expect(a.mark).toBe("tool")
    expect(b.mark).toBe("tool")
    expect(a.result).toMatchObject({ isError: false, differs: true })
    expect(b.result).toMatchObject({ isError: true, differs: true })
    // B's call where A has none.
    expect(m.rows[2].b?.calls[0].mark).toBe("only")
    expect(m.rows[2].a?.textDiffers).toBe(true)
  })

  it("tells other arguments from another tool", () => {
    const m = buildCompare([{ doc: refunds }])
    expect(m.rows.map((r) => r.what)).toEqual([
      ["arguments", "result"],
      ["arguments"],
    ])
    expect(m.rows[0].a?.calls[0].mark).toBe("args")
    // The parked step: the call has no result yet, on either side.
    expect(m.rows[1].a?.parked).toBe(true)
    expect(m.rows[1].a?.calls[0].result).toBeUndefined()
    // Same tokens on both sides: the server did not flag usage.
    expect(m.rows[1].a?.tokensDiffer).toBe(false)
  })

  it("finds nothing in two runs of one scenario", () => {
    const m = buildCompare([{ doc: same }])
    expect(m.rows.map((r) => r.kind)).toEqual(["same", "same", "same"])
    expect(m.rows.flatMap((r) => r.a?.calls.map((c) => c.mark))).toEqual([
      undefined,
      undefined,
    ])
    expect(m.differing).toBe(0)
    expect(summaryOf(m)).toBe("The two runs took the same 3 steps.")
  })

  it("compares a chain run by run", () => {
    const m = buildCompare([{ doc: refunds }, { doc: resume }])
    expect(m.rows.map((r) => r.key)).toEqual(["0:0", "0:1", "1:0", "1:1"])
    expect(m.rows.map((r) => r.kind)).toEqual([
      "changed",
      "changed",
      "changed",
      "only_a",
    ])
    // Approved: the refund is made. Denied: the model says so.
    expect(m.rows[2].a?.name).toBe("Call issue_refund, notify_customer")
    expect(m.rows[2].b?.name).toBe("Reply")
    expect(itemsOf(m, false).map((i) => i.type)).toEqual([
      "row",
      "row",
      "resumed",
      "row",
      "row",
    ])
  })

  it("numbers the steps through the chain, as the run's page does", () => {
    // The resume's first step follows the parked run's last: no #0 twice.
    const m = buildCompare([{ doc: refunds }, { doc: resume }])
    expect(m.rows.map((r) => r.no)).toEqual([0, 1, 2, 3])
    expect(m.rows.map((r) => r.a?.no)).toEqual([0, 1, 2, 3])
    expect(m.rows.map((r) => r.b?.no)).toEqual([0, 1, 2, undefined])
    expect(summaryOf(m)).toBe("4 of 4 steps differ, from step #0.")
    // The page says where each run's steps start in its chain (chainStep):
    // a side whose earlier run took more steps keeps its own number.
    const before = (id: string) =>
      id === resume.a.run_id ? 2 : id === resume.b.run_id ? 5 : 0
    const n = buildCompare(
      [{ doc: refunds }, { doc: resume }],
      undefined,
      before
    )
    expect(n.rows.map((r) => r.no)).toEqual([0, 1, 2, 3])
    expect(n.rows[2].b?.no).toBe(5)
  })

  it("names the span that selects a step's row on the run page", () => {
    const facts = stepFacts(spans, failed.a.run_id)
    const m = buildCompare([{ doc: failed }], (id) =>
      id === failed.a.run_id ? facts : undefined
    )
    const id = m.rows[0].a?.spanId
    expect(id).toBe(
      spans.find(
        (s) =>
          s.run_id === failed.a.run_id &&
          s.attrs["weft.step.index"] === 0 &&
          s.attrs["gen_ai.operation.name"] === "chat" &&
          s.attrs["weft.attempt.index"] === undefined
      )?.span_id
    )
    expect(id).toBeTruthy()
    // No spans, no selection: the link opens the run.
    expect(m.rows[0].b?.spanId).toBeUndefined()
  })

  it("shows a run alone where the other chain has none", () => {
    // What the page does when only A was resumed: the resume against
    // itself, its B side dropped.
    const m = buildCompare([{ doc: same, only: "a" }])
    expect(m.rows.map((r) => r.kind)).toEqual(["only_a", "only_a", "only_a"])
    expect(m.rows.every((r) => r.b === null && r.what.length === 0)).toBe(true)
  })

  it("keeps a replay's kept step out of the differences (golden)", () => {
    const m = buildCompare([{ doc: replay }])
    expect(m.rows.map((r) => r.kind)).toEqual(["kept", "same"])
    expect(m.rows[0].b).toBeNull()
    // Each side under its own ordinal: the replay's step 0 is the
    // source's step 1.
    expect([m.rows[1].a?.step, m.rows[1].b?.step]).toEqual([1, 0])
    // Usage differs without counting as a change.
    expect(m.rows[1].a?.tokensDiffer).toBe(true)
    expect(m.kept).toBe(1)
    expect(m.aligned).toMatch(/^B was taken from the other run at its step #1/)
  })
})

describe("itemsOf", () => {
  it("folds the steps that do not differ, unless all are asked for", () => {
    const m = buildCompare([{ doc: same }])
    const folded = itemsOf(m, false)
    expect(folded).toHaveLength(1)
    expect(folded[0]).toMatchObject({ type: "fold" })
    expect(folded[0].type === "fold" && folded[0].rows).toHaveLength(3)
    expect(itemsOf(m, true).map((i) => i.type)).toEqual(["row", "row", "row"])
  })

  it("never folds a step that differs", () => {
    const m = buildCompare([{ doc: failed }])
    expect(itemsOf(m, false).every((i) => i.type === "row")).toBe(true)
  })
})

describe("pairChains", () => {
  it("pairs run with run and leaves the longer chain's rest alone", () => {
    expect(pairChains(["a0"], ["b0"])).toEqual([{ a: "a0", b: "b0" }])
    expect(pairChains(["a0", "a1"], ["b0"])).toEqual([
      { a: "a0", b: "b0" },
      { a: "a1", b: "a1", only: "a" },
    ])
    expect(pairChains(["a0"], ["b0", "b1"])).toEqual([
      { a: "a0", b: "b0" },
      { a: "b1", b: "b1", only: "b" },
    ])
  })
})
