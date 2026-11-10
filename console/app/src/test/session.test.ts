// The conversation's logic on the server's own documents.
//
// conversation-*.json and sessions-demo.json are bodies of
// GET /v1/sessions/{id}/conversation and GET /v1/sessions captured,
// unedited, from `go run ./examples/demo` (the refunds conversation is
// one the demo plays by itself; the failed and the waiting ones were
// started through POST /v1/runs with the demo's scripted prompts).
// session-timeline.json is the server's golden: it is where grants,
// compactions, steered messages and labels come from, since the demo's
// sessions had no thread records then. conversation-desk.json is a
// capture from the demo too (round 3): the thread-backed `desk` session
// (GET /v1/sessions/{id}/conversation?tail=1, unedited), whose thread
// numbers its turns 1, 2, 4, 6, 8, 9, 10, 11, 12.
import { afterEach, describe, expect, it, vi } from "vitest"

import {
  buildItems,
  markMarkers,
  offPath,
  outcomeStrip,
  positionOfTurn,
  sessionTitle,
  summaryParts,
  summaryTitle,
  turnMarkers,
  turnNote,
  turnParam,
  waitMarker,
} from "@/components/console/session/model"
import { deleteError } from "@/components/console/session/session-menu"
import { ApiError } from "@/lib/api/errors"
import {
  mergeEarlier,
  mergeTail,
  refreshConversation,
  loadSessions,
  mergeSessions,
  toSessionsQuery,
} from "@/lib/api/sessions"
import type { ConversationView } from "@/lib/api/sessions"
import { outcomeStatus, sessionStatus } from "@/lib/api/status"
import type {
  Conversation,
  ConversationTurn,
  SessionTimeline,
  SessionsPage,
} from "@/lib/api/types"

import deskDoc from "./fixtures/conversation-desk.json"
import failedDoc from "./fixtures/conversation-failed.json"
import refundsDoc from "./fixtures/conversation-refunds.json"
import waitingDoc from "./fixtures/conversation-waiting.json"
import timelineDoc from "./fixtures/session-timeline.json"
import sessionsDemo from "./fixtures/sessions-demo.json"
import sessionsGolden from "./fixtures/sessions.json"

const refunds = refundsDoc as unknown as Conversation
const failed = failedDoc as unknown as Conversation
const waiting = waitingDoc as unknown as Conversation
const desk = deskDoc as unknown as Conversation
const timeline = timelineDoc as unknown as SessionTimeline
const demo = sessionsDemo as unknown as SessionsPage
const golden = sessionsGolden as unknown as SessionsPage

const NOW = Date.parse("2026-11-10T11:30:00Z")

const view = (c: Conversation, over: Partial<ConversationView> = {}) =>
  ({
    sessionId: c.session_id,
    agent: c.agent,
    status: c.status,
    offset: c.offset,
    tailOffset: c.offset,
    delegated: [],
    totalTurns: c.total_turns,
    turns: c.turns,
    sessionMarks: c.session_marks ?? [],
    marksCapped: false,
    skipped: c.skipped_turns ?? [],
    session: c.session,
    ...over,
  }) satisfies ConversationView

const turn = (over: Partial<ConversationTurn>): ConversationTurn => ({
  ...refunds.turns[1],
  ...over,
})

afterEach(() => vi.unstubAllGlobals())

describe("the summary line", () => {
  it("says model calls, tools, duration and cost", () => {
    expect(summaryParts(refunds.turns[0], NOW).join(" · ")).toBe(
      "4 model calls · 3 tools · 10.5s · $0.01"
    )
  })

  it("leaves out zero tools and keeps the singular", () => {
    expect(summaryParts(refunds.turns[1], NOW).join(" · ")).toBe(
      "1 model call · 500ms · $0.0033"
    )
    expect(summaryParts(turn({ model_calls: 2, tool_calls: 1 }), NOW)[1]).toBe(
      "1 tool"
    )
  })

  it("never shows an unknown cost as $0", () => {
    const parts = summaryParts(turn({ cost_usd: null, cost_known: false }), NOW)
    expect(parts.at(-1)).toBe("cost unknown")
  })

  it("measures an open turn to now", () => {
    const t = waiting.turns[1]
    expect(t.display_status).toBe("waiting")
    const later = Date.parse(t.started) + 600_000
    expect(summaryParts(t, later)[2]).toBe("10m 00s")
  })

  it("says only what is known of a turn still running", () => {
    const t = turn({
      display_status: "running",
      reply: null,
      finished: null,
      model_calls: 0,
      tool_calls: 1,
      duration_ms: 900,
      cost_usd: null,
      cost_known: false,
    })
    expect(summaryParts(t, Date.parse(t.started) + 1900)).toEqual([
      "1 tool",
      "1.9s",
    ])
  })

  it("puts the wait and the runs in the tooltip", () => {
    expect(summaryTitle(refunds.turns[0])).toBe(
      "2,730 tokens in, 192 out · 5.0s of it waiting on a person · 2 runs (it parked and resumed)"
    )
    expect(summaryTitle(refunds.turns[1])).toBe("1,040 tokens in, 12 out")
  })
})

describe("a park and its resume are one turn", () => {
  it("is one turn with one wait marker", () => {
    const items = buildItems(view(refunds), NOW)
    expect(items.map((i) => i.type)).toEqual(["turn", "turn"])
    const first = items[0]
    if (first.type !== "turn") throw new Error("not a turn")
    expect(first.turn.runs.length).toBe(2)
    expect(first.markers.map((m) => m.text)).toEqual([
      "Waited 5.0s · issue_refund approved by API key …CWT9",
    ])
    expect(first.note).toBeNull()
  })

  it("opens to who, how and when, with the exact arguments", () => {
    const m = waitMarker(refunds.turns[0].waits![0], NOW)!
    const fact = (label: string) => m.facts.find((f) => f.label === label)
    expect(fact("Tool")?.value).toBe("issue_refund")
    expect(fact("Decision")?.value).toBe("approved")
    expect(fact("Decided by")?.value).toBe("API key …CWT9")
    expect(fact("Asked")).toBeDefined()
    expect(fact("Resumed")).toBeDefined()
    expect(m.args).toEqual([
      {
        tool: "issue_refund",
        value: { order_id: "1042", amount_usd: 64, reason: "arrived damaged" },
      },
    ])
    expect(m.pendingRun).toBeUndefined()
  })

  it("says who decided as you when it is the signed-in principal", () => {
    const w = refunds.turns[0].waits![0]
    const me = w.decided_by![0]
    expect(waitMarker(w, NOW, { me })!.text).toBe(
      "Waited 5.0s · issue_refund approved by you"
    )
  })

  it("names the run to decide while the wait lasts", () => {
    const t = waiting.turns[1]
    const [m] = turnMarkers(t, Date.parse(t.waits![0].from) + 5000)
    expect(m.text).toBe("Waiting 5.0s for approval of issue_refund")
    expect(m.pendingRun).toBe(t.run_id)
    expect(m.tone).toBe("warning")
    // The decision is offered by the marker; no sentence stands in for
    // the reply.
    expect(turnNote(t)).toBeNull()
    expect(t.waits![0].approvals![0].status).toBe("pending")
  })

  it("says a denial and several approvals", () => {
    const w = refunds.turns[0].waits![0]
    const a = w.approvals![0]
    const denied = waitMarker(
      {
        ...w,
        approvals: [
          { ...a, status: "denied", outcome: "deny", reason: "too late" },
        ],
      },
      NOW
    )!
    expect(denied.text).toMatch(
      /^Waited 5\.0s · issue_refund denied by API key …\w{4} · too late$/
    )
    expect(denied.tone).toBe("danger")
    expect(denied.facts.find((f) => f.label === "Reason")?.value).toBe(
      "too late"
    )
    const two = waitMarker(
      { ...w, approvals: [a, { ...a, id: "appr_2", tool: "send_email" }] },
      NOW
    )!
    expect(two.text).toContain("· all 2 calls approved by API key")
  })
})

describe("a turn without a reply says why", () => {
  it("failed: in words, with the server's error", () => {
    const t = failed.turns[1]
    expect(t.reply).toBeNull()
    expect(turnNote(t)).toEqual({
      tone: "danger",
      text: "No reply: the run failed because it reached its step limit.",
      detail:
        "weft: run failed at step 4: weft: run exceeded the maximum number of steps",
    })
    expect(turnNote(failed.turns[0])).toBeNull()
  })

  it("an error kind it has no words for is shown as it came", () => {
    expect(
      turnNote(
        turn({ display_status: "failed", error_type: "odd", reply: null })
      )?.text
    ).toBe("No reply: the run failed (odd).")
  })

  it("cancelled, interrupted, running, queued", () => {
    const of = (over: Partial<ConversationTurn>) =>
      turnNote(turn({ reply: null, ...over }))?.text
    expect(of({ display_status: "cancelled", cancel_cause: "user" })).toBe(
      "No reply: the run was cancelled by a person."
    )
    expect(of({ display_status: "cancelled" })).toBe(
      "No reply: the run was cancelled."
    )
    expect(of({ display_status: "interrupted" })).toBe(
      "No reply: the run stopped reporting without an end."
    )
    expect(of({ display_status: "running" })).toBe(
      "The agent is working on it…"
    )
    expect(of({ display_status: "queued" })).toBe(
      "Queued: waiting for a worker."
    )
  })

  it("a turn a newer message ended says which and how", () => {
    const interrupted = turn({
      display_status: "cancelled",
      reply: null,
      ended_kind: "interrupt",
      ended_by: "run_x",
      ended_by_turn: 5,
    })
    // The turn that ended it is named by its position, when it is known.
    expect(turnNote(interrupted, 4)?.text).toMatch(/^Interrupted by turn 4:/)
    expect(turnNote(interrupted)?.text).toMatch(
      /^Interrupted by a newer message:/
    )
    expect(offPath(interrupted)).toBe(false)
    const rolled = turn({
      display_status: "cancelled",
      reply: null,
      ended_kind: "rollback",
      ended_by_turn: 5,
      off_path: true,
    })
    expect(turnNote(rolled, 4)?.text).toMatch(/^Rolled back by turn 4:/)
    expect(offPath(rolled)).toBe(true)
    const item = buildItems(view(refunds, { turns: [rolled] }), NOW).find(
      (i) => i.type === "turn"
    )
    expect(item?.type === "turn" && item.offPath).toBe(true)
  })
})

describe("thread marks (the server's golden timeline)", () => {
  const markers = markMarkers(timeline.marks)
  const texts = markers.map((m) => m.text)

  it("tells an approval once: the request is folded into its decision", () => {
    expect(
      timeline.marks.filter((m) => m.kind === "approval.request").length
    ).toBe(3)
    expect(texts.filter((t) => t.startsWith("Approval requested"))).toEqual([])
    expect(texts).toContain(
      "issue_refund denied by agent-7 · need a photo of the damage"
    )
    expect(texts).toContain("issue_refund approved by agent-7")
    expect(texts).toContain(
      "send_email approved by agent-7, and for every later call"
    )
  })

  it("a request with no decision stands alone", () => {
    const alone = markMarkers(
      timeline.marks.filter((m) => m.kind === "approval.request").slice(0, 1)
    )
    expect(alone.map((m) => m.text)).toEqual([
      "Approval requested for issue_refund",
    ])
    expect(alone[0].tone).toBe("warning")
  })

  it("does not repeat the calls a wait already tells", () => {
    const told = markMarkers(timeline.marks, new Set(["r1", "r2", "e1"]))
    expect(told.some((m) => m.kind === "approval")).toBe(false)
    expect(told.length).toBe(markers.length - 3)
  })

  it("grants given and revoked", () => {
    expect(texts).toContain(
      "Grant given: send_email may run without asking, for this session"
    )
    expect(texts).toContain("Grant revoked: send_email, after 1 use")
  })

  it("a steered message", () => {
    const m = markers.find((x) => x.kind === "message")!
    expect(m.text).toMatch(/^Message steered into the turn/)
    expect(m.facts.find((f) => f.label === "Content")?.value).toBe(
      "removed before it reached this server"
    )
  })

  it("a compaction with its tokens before and after", () => {
    const m = markers.find((x) => x.kind === "compaction")!
    expect(m.text).toBe("Context compacted: 900 → 300 tokens")
    const fact = (label: string) =>
      m.facts.find((f) => f.label === label)?.value
    expect(fact("Messages before")).toBe("9")
    expect(fact("Messages after")).toBe("2")
    expect(fact("Reason")).toBe("threshold")
    expect(fact("summarizer model")).toBe("openai gpt-4o-mini")
    expect(fact("summarizer usage")).toBe("1,800 in, 60 out")
  })

  it("a label and a pin", () => {
    expect(texts).toContain("Labelled “case-closed”")
    expect(texts).toContain("An entry was pinned: compaction keeps it")
  })

  it("every marker opens to who, how and when", () => {
    for (const m of markers)
      expect(m.facts.some((f) => f.label === "When")).toBe(true)
    const decision = markers.find((m) => m.text.includes("denied by agent-7"))!
    expect(decision.facts.slice(0, 3).map((f) => [f.label, f.value])).toEqual([
      ["Who", "agent-7"],
      ["How", "user"],
      ["When", decision.facts[2].value],
    ])
    expect(decision.tone).toBe("danger")
  })

  it("forks, handoffs and kinds it does not know", () => {
    const base = timeline.marks[0]
    const of = (kind: string, fields: Record<string, unknown>) =>
      markMarkers([{ ...base, kind, fields, entry: "e99" }])[0]
    const fork = of("fork", { new_session: "s_trip_2" })
    expect(fork.text).toBe("Forked here into another session")
    expect(fork.session).toBe("s_trip_2")
    const handoff = of("pool.delegate", {
      child_session: "s_child",
      state: "running",
    })
    expect(handoff.text).toBe("Handed to another session (running)")
    expect(handoff.session).toBe("s_child")
    expect(of("branch", { to_entry: "e3" }).text).toBe(
      "Branched here: the conversation went on from entry e3"
    )
    expect(of("receipt", { input: "queue", state: "delivered" }).text).toBe(
      "Message queued until the turn ended"
    )
    expect(of("something.new", {}).text).toBe("something new")
  })

  it("a grant allowing the same tool several times is one line", () => {
    const base = timeline.marks[0]
    const use = (n: number) => ({
      ...base,
      kind: "grant.use",
      entry: `u${n}`,
      at: `2020-01-01T09:00:1${n}.000Z`,
      fields: { grant_id: "e32", tool: "send_email", call_id: `c${n}` },
    })
    const out = markMarkers([use(1), use(2), use(3)])
    expect(out.map((m) => m.text)).toEqual([
      "send_email allowed by a grant, 3 times",
    ])
  })

  it("a plain message's receipt is not a line; a message's receipts are one", () => {
    const base = timeline.marks[0]
    const receipt = (fields: Record<string, unknown>, text?: string) => ({
      ...base,
      kind: "receipt",
      fields,
      content: text ? { text } : undefined,
    })
    expect(
      markMarkers([receipt({ input: "send", state: "accepted" }, "hello")])
    ).toEqual([])
    const out = markMarkers([
      receipt(
        { input: "steer", state: "queued", receipt_id: "r1" },
        "be brief"
      ),
      receipt({ input: "steer", state: "delivered", receipt_id: "r1" }),
    ])
    expect(out.map((m) => m.text)).toEqual([
      "Message steered into the turn: “be brief”",
    ])
    expect(
      markMarkers([
        receipt({ input: "steer", state: "queued", receipt_id: "r1" }, "x"),
      ])[0].text
    ).toBe("Message steered into the turn (queued): “x”")
  })
})

// The demo's thread-backed session, as the server sent it.
describe("the desk conversation (a capture from the demo)", () => {
  const items = buildItems(view(desk), NOW)
  const turns = items.filter((i) => i.type === "turn")
  const lines = (position: number) =>
    turns.find((t) => t.position === position)!.markers.map((m) => m.text)

  it("numbers the turns by position, whatever the thread numbered them", () => {
    expect(desk.turns.map((t) => t.turn)).toEqual([
      1, 2, 4, 6, 8, 9, 10, 11, 12,
    ])
    expect(turns.map((t) => t.position)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9])
    expect(desk.total_turns).toBe(9)
    // Nothing is said about the numbers the thread skipped.
    expect(items.filter((i) => i.type === "gap")).toEqual([])
    // A turn is linked by the thread's number (the one its run carries).
    expect(turns.map((t) => turnParam(t.turn, t.position))).toEqual([
      1, 2, 4, 6, 8, 9, 10, 11, 12,
    ])
    expect(positionOfTurn(view(desk), 10)).toBe(7)
    expect(positionOfTurn(view(desk), 7)).toBeUndefined()
  })

  it("prefers the server's index, and counts from the page's offset without it", () => {
    const indexed = buildItems(
      view(desk, {
        turns: desk.turns.map((t, i) => ({ ...t, index: i + 11 })),
      }),
      NOW
    ).filter((i) => i.type === "turn")
    expect(indexed.map((t) => t.position)).toEqual([
      11, 12, 13, 14, 15, 16, 17, 18, 19,
    ])
    const paged = buildItems(
      view(desk, { offset: 5, turns: desk.turns.slice(5), totalTurns: 9 }),
      NOW
    ).filter((i) => i.type === "turn")
    expect(paged.map((t) => t.position)).toEqual([6, 7, 8, 9])
  })

  it("says a skipped number only when it is not shown elsewhere", () => {
    const built = buildItems(
      view(desk, {
        skipped: [
          { turn: 3, reason: "resumed", into: 2 },
          { turn: 5 },
          { turn: 7, reason: "dropped", note: "The message was dropped." },
        ],
      }),
      NOW
    )
    // resumed and steered numbers are the wait / steered marker on the
    // turn they went into; a number with no reason is not mentioned.
    const at = built.findIndex((i) => i.type === "gap")
    expect(built.filter((i) => i.type === "gap")).toEqual([
      { type: "gap", key: "skipped:7", text: "The message was dropped." },
    ])
    // Just before the turn that follows the skipped number.
    const next = built[at + 1]
    expect(next.type === "turn" && next.turn.turn).toBe(8)
  })

  it("says nothing for a steered number", () => {
    const built = buildItems(
      view(desk, { skipped: [{ turn: 7, reason: "steered", into: 6 }] }),
      NOW
    )
    expect(built.filter((i) => i.type === "gap")).toEqual([])
  })

  it("a wait and the decision that ended it are one line", () => {
    expect(lines(2)).toEqual([
      "Waited 3.0s · add_gift_wrap approved by op_maria",
      "Grant given: add_gift_wrap may run without asking, for this session · gift wrap is free this month",
    ])
    const wait = turns[1].markers[0]
    expect(wait.kind).toBe("wait")
    const labels = wait.facts.map((f) => f.label)
    expect(labels).toEqual(expect.arrayContaining(["Who", "How", "When"]))
    expect(labels).toEqual(expect.arrayContaining(["Asked", "Resumed"]))
  })

  it("a grant's answer is one line, with no wait to speak of", () => {
    expect(lines(3)).toEqual(["add_gift_wrap allowed by a grant from op_maria"])
    expect(turns[2].markers[0].kind).toBe("grant")
    // The mint is in an earlier turn: without it the tool is not known.
    expect(turnMarkers(desk.turns[2], NOW).map((m) => m.text)).toEqual([
      "Call allowed by a grant from op_maria",
    ])
  })

  it("a steered message is one line with its text; receipts of plain messages are none", () => {
    expect(lines(4)).toEqual([
      "Message steered into the turn: “Only this month's orders, please.”",
    ])
    expect(
      desk.turns
        .flatMap((t) => t.marks ?? [])
        .filter((m) => m.kind === "receipt").length
    ).toBe(6)
    expect(
      turns.flatMap((t) => t.markers).filter((m) => m.kind === "message").length
    ).toBe(1)
  })

  it("a turn a newer message ended says so once, by position", () => {
    const interrupted = turns[5]
    expect(interrupted.turn.ended_by_turn).toBe(10)
    expect(interrupted.endedBy).toBe(7)
    expect(interrupted.note?.text).toMatch(/^Interrupted by turn 7:/)
    // The interrupt and rewind marks would say it a second time.
    expect(interrupted.markers).toEqual([])
    const rolled = turns[7]
    expect(rolled.offPath).toBe(true)
    expect(rolled.endedBy).toBe(9)
    expect(rolled.markers).toEqual([])
  })

  it("the fork says where it came from, once, above its first turn", () => {
    const fork = view(desk, {
      session: {
        ...desk.session!,
        forked_from_session: "s_origin#e_1",
      },
      turns: [{ ...desk.turns[1] }],
    })
    expect(buildItems(fork, NOW)[0]).toEqual({
      type: "origin",
      key: "origin",
      session: "s_origin",
      turn: 1,
    })
    expect(buildItems(view(desk), NOW)[0].type).toBe("turn")
  })
})

describe("the conversation's items", () => {
  // The golden's marks, filed under the demo conversation's turns the
  // way the conversation endpoint files them (by run): a composition of
  // two real documents, since no captured conversation has thread marks.
  const withMarks = (): ConversationView => {
    const turns = refunds.turns.map((t, i) => ({
      ...t,
      marks: timeline.marks
        .filter((m) => m.turn === (i === 0 ? 4 : 5))
        .map((m) => ({
          ...m,
          at: t.started,
          fact_at: undefined,
        })),
    }))
    return view(refunds, {
      turns,
      sessionMarks: timeline.marks
        .filter((m) => m.run_id === null)
        .map((m) => ({
          ...m,
          at: refunds.turns[1].started,
          fact_at: undefined,
        })),
    })
  }

  it("puts a turn's markers with the turn, in time order", () => {
    const items = buildItems(withMarks(), NOW)
    const first = items.find((i) => i.type === "turn")
    if (first?.type !== "turn") throw new Error("no turn")
    expect(first.markers.map((m) => m.kind)).toEqual(["compaction", "wait"])
  })

  it("places the session's own marks where they fall in time", () => {
    const items = buildItems(withMarks(), NOW)
    expect(items.map((i) => i.type)).toEqual(["turn", "turn", "mark", "mark"])
    const loose = items.filter((i) => i.type === "mark")
    expect(loose.map((i) => i.type === "mark" && i.marker.kind)).toEqual([
      "compaction",
      "grant",
    ])
  })

  it("offers the earlier turns when the page starts later", () => {
    const items = buildItems(view(failed, { offset: 6, totalTurns: 9 }), NOW)
    expect(items[0]).toEqual({ type: "older", key: "older", count: 6 })
    // No gap is claimed before the first loaded turn.
    expect(items[1].type).toBe("turn")
  })

  it("says nothing about a jump in the thread's turn numbers", () => {
    const [a, , c] = failed.turns
    const items = buildItems(
      view(failed, { turns: [a, { ...c, turn: 4 }] }),
      NOW
    )
    expect(items.map((i) => i.type)).toEqual(["turn", "turn"])
    expect(items.map((i) => i.type === "turn" && i.position)).toEqual([1, 2])
  })
})

describe("the list", () => {
  it("draws one dot per turn and folds a decided park", () => {
    // The server counts turns: a resume is not one (the desk session:
    // 9 turns, 11 runs, two of them resumes).
    const s = desk.session!
    expect(s.runs).toBe(11)
    expect(s.outcomes!.filter((w) => w === "resumed").length).toBe(2)
    const strip = outcomeStrip(s)
    expect(strip.dots.length).toBe(9)
    expect(strip.earlier).toBe(0)
    expect(strip.title).toBe("9 turns: 7 succeeded, 2 cancelled")
  })

  it("shows a failed turn inside a session that ended well", () => {
    const s = demo.sessions.find(
      (x) => x.failed_turns === 1 && x.status === "succeeded"
    )!
    expect(outcomeStrip(s).dots).toEqual(["succeeded", "failed", "succeeded"])
    expect(outcomeStrip(s).title).toBe("3 turns: 2 succeeded, 1 failed")
  })

  it("reads waiting and cancelled from the server's words", () => {
    const parked = demo.sessions.find((x) => x.status === "parked")!
    expect(outcomeStrip(parked).dots.at(-1)).toBe("waiting")
    const cancelled = demo.sessions.find((x) => x.status === "cancelled")!
    expect(outcomeStrip(cancelled).dots).toEqual(["succeeded", "cancelled"])
    expect(outcomeStatus("ended")).toBe("cancelled")
    expect(outcomeStatus("resumed")).toBe("running")
    expect(outcomeStatus("a new word")).toBe("interrupted")
  })

  it("draws the server's display_status as the session's status", () => {
    for (const s of golden.sessions)
      expect(sessionStatus(s)).toBe(s.display_status)
    // The ledger's word wins over the outcome of the newest trace.
    expect(
      sessionStatus({ ...golden.sessions[0], display_status: "queued" })
    ).toBe("queued")
  })

  it("keeps the newest dots of a long session and counts the rest", () => {
    const long = demo.sessions.find((x) => x.turns > 20)!
    const strip = outcomeStrip(long, 12)
    expect(strip.dots.length).toBe(12)
    expect(strip.earlier).toBe(long.turns - 12)
  })

  it("reads the golden's rows too", () => {
    for (const s of golden.sessions) {
      const strip = outcomeStrip(s)
      expect(strip.dots.length).toBe(s.turns)
      expect(strip.dots.at(-1)).toBe(sessionStatus(s))
    }
  })

  it("titles a row by its title, then its first message", () => {
    expect(sessionTitle(demo.sessions[0])).toBe(demo.sessions[0].first_prompt)
    expect(sessionTitle({ title: "Refund #1042", first_prompt: "x" })).toBe(
      "Refund #1042"
    )
    expect(sessionTitle({ content_off: true })).toBe("Content not recorded")
    expect(sessionTitle({})).toBe("Untitled session")
  })

  it("maps the status filter onto the server's parameters", () => {
    expect(toSessionsQuery({ state: "waiting" })).toMatchObject({
      awaiting: true,
      status: undefined,
    })
    expect(toSessionsQuery({ state: "failed" })).toMatchObject({
      failed: true,
      status: undefined,
    })
    expect(toSessionsQuery({ state: "running", agent: "a" })).toMatchObject({
      status: "running",
      agent: "a",
    })
  })
})

describe("reading", () => {
  const urls: string[] = []
  const serve = (answer: (url: URL) => unknown) => {
    urls.length = 0
    vi.stubGlobal("fetch", (input: string) => {
      urls.push(input)
      return Promise.resolve(
        new Response(JSON.stringify(answer(new URL(input))), { status: 200 })
      )
    })
  }

  it("a one-word search also looks the word up as a public id and an id", async () => {
    const [a, b, c] = demo.sessions
    serve((u) =>
      u.searchParams.has("public_id")
        ? { ...demo, total: 1, sessions: [c] }
        : u.searchParams.has("id_prefix")
          ? { ...demo, total: 1, sessions: [b] }
          : { ...demo, total: 2, sessions: [a, b] }
    )
    const page = await loadSessions({ q: "pub_orders" })
    expect(urls.length).toBe(3)
    expect(urls.some((u) => u.includes("public_id=pub_orders"))).toBe(true)
    expect(urls.some((u) => u.includes("id_prefix=pub_orders"))).toBe(true)
    // The id matches first, each session once.
    expect(page.sessions.map((s) => s.id)).toEqual([c.id, b.id, a.id])
    expect(page.total).toBe(3)
  })

  it("a sentence is one search", async () => {
    serve(() => demo)
    await loadSessions({ q: "where is my order" })
    expect(urls.length).toBe(1)
  })

  it("mergeSessions keeps each session once", () => {
    const [a, b] = demo.sessions
    const page = { ...demo, total: 2, sessions: [a, b] }
    expect(mergeSessions(page, [b]).sessions.map((s) => s.id)).toEqual([
      b.id,
      a.id,
    ])
    expect(mergeSessions(page, [b]).total).toBe(2)
  })

  it("a short conversation is one request", async () => {
    serve(() => refunds)
    const v = await refreshConversation(refunds.session_id, () => undefined)
    expect(urls.length).toBe(1)
    expect(urls[0]).toContain("tail=true")
    expect(v.offset).toBe(0)
    expect(v.turns.length).toBe(2)
    expect(v.totalTurns).toBe(2)
  })
})

describe("the windowed conversation", () => {
  const urls: string[] = []
  const serve = (answer: (url: URL) => unknown) => {
    urls.length = 0
    vi.stubGlobal("fetch", (input: string) => {
      urls.push(input)
      return Promise.resolve(
        new Response(JSON.stringify(answer(new URL(input))), { status: 200 })
      )
    })
  }
  const turnOf = (i: number, over: Partial<ConversationTurn> = {}) => ({
    ...refunds.turns[1],
    turn: i,
    run_id: `run_${i}`,
    ...over,
  })
  /** Turns [offset+1 … offset+n] of a session of `total`. */
  const page = (
    offset: number,
    n: number,
    total: number,
    over: Partial<ConversationTurn> = {}
  ): Conversation => ({
    ...refunds,
    total_turns: total,
    offset,
    next_offset: offset + n < total ? offset + n : null,
    turns: Array.from({ length: n }, (_, i) => turnOf(offset + i + 1, over)),
  })
  const numbers = (v: ConversationView) => v.turns.map((t) => t.turn)
  const range = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => from + i)
  const tailOf = (total: number) =>
    page(Math.max(0, total - 20), Math.min(20, total), total)

  it("a new tail replaces the window and keeps what is before it", () => {
    // Loaded: turns 7..26 (the tail of 26), each still "running".
    const before = mergeTail(undefined, [
      page(6, 20, 26, { display_status: "running" }),
    ])
    // Two turns later the tail is 9..28, finished.
    const after = mergeTail(before, [page(8, 20, 28)])
    expect(after.offset).toBe(6)
    expect(after.tailOffset).toBe(8)
    expect(after.totalTurns).toBe(28)
    expect(numbers(after)).toEqual(range(7, 28))
    // Turns 7 and 8 left the window: kept exactly as they were read.
    expect(after.turns[0]).toBe(before.turns[0])
    expect(after.turns[1].display_status).toBe("running")
    // The window's turns are the fresh ones.
    expect(after.turns[2].display_status).toBe(refunds.turns[1].display_status)
  })

  it("a tail that does not join what is loaded stands alone", () => {
    const before = mergeTail(undefined, [page(6, 20, 26)])
    // 40 turns arrived while the page slept: 27..46 were never read.
    const after = mergeTail(before, [page(46, 20, 66)])
    expect(after.offset).toBe(46)
    expect(numbers(after)).toEqual(range(47, 66))
  })

  it("earlier turns go on top, each once, and only without a hole", () => {
    const loaded = mergeTail(undefined, [page(26, 20, 46)])
    const more = mergeEarlier(loaded, [page(6, 20, 46)])
    expect(more.offset).toBe(6)
    expect(more.tailOffset).toBe(26)
    expect(numbers(more)).toEqual(range(7, 46))
    // A page that overlaps the view adds only what is before it.
    const overlap = mergeEarlier(loaded, [page(16, 20, 46)])
    expect(numbers(overlap)).toEqual(range(17, 46))
    // Pages that stop short of the view's first turn would leave a hole.
    expect(mergeEarlier(loaded, [page(0, 20, 46)])).toBe(loaded)
    // Nothing earlier than what is loaded: nothing changes.
    expect(mergeEarlier(more, [page(6, 20, 46)])).toBe(more)
  })

  it("a refetch reads the newest page only, whatever is loaded", async () => {
    let total = 46
    serve(() => tailOf(total))
    let cached: ConversationView | undefined
    cached = await refreshConversation("s", () => cached)
    expect(urls.length).toBe(1)
    expect(numbers(cached)).toEqual(range(27, 46))
    // The person loads 40 earlier turns.
    cached = mergeEarlier(cached, [page(6, 20, 46)])
    cached = mergeEarlier(cached, [page(0, 6, 46)])
    expect(numbers(cached)).toEqual(range(1, 46))
    const early = cached.turns.slice(0, 26)
    // A live tick, with a new turn: one request, for the tail.
    total = 47
    urls.length = 0
    cached = await refreshConversation("s", () => cached)
    expect(urls.length).toBe(1)
    expect(urls[0]).toContain("tail=true")
    expect(urls[0]).not.toContain("offset=")
    expect(numbers(cached)).toEqual(range(1, 47))
    expect(cached.offset).toBe(0)
    expect(cached.tailOffset).toBe(27)
    // The earlier turns are the same objects: not read again.
    cached.turns.slice(0, 26).forEach((t, i) => expect(t).toBe(early[i]))
  })

  it("earlier turns loaded while a refetch is in flight are kept", async () => {
    serve(() => tailOf(46))
    let cached: ConversationView | undefined = mergeTail(undefined, [
      tailOf(46),
    ])
    const flying = refreshConversation("s", () => cached)
    cached = mergeEarlier(cached, [page(6, 20, 46)])
    expect(numbers(await flying)).toEqual(range(7, 46))
  })

  it("a session that shrank is read afresh", async () => {
    serve(() => tailOf(30))
    const cached = mergeEarlier(mergeTail(undefined, [tailOf(46)]), [
      page(6, 20, 46),
    ])
    const v = await refreshConversation("s", () => cached)
    expect(numbers(v)).toEqual(range(11, 30))
  })

  it("an off-path turn is the server's word, folded", () => {
    const t = turnOf(3, { off_path: true })
    expect(offPath(t)).toBe(true)
    expect(offPath(turnOf(3, { ended_kind: "rollback" }))).toBe(false)
  })

  it("a refused delete says what to do", () => {
    expect(
      deleteError(new ApiError(409, "run_active", "session has an active run"))
    ).toBe(
      "A run of this session is still going. Cancel it first, then delete."
    )
    expect(deleteError(new ApiError(500, "internal", "boom"))).toBe("boom")
  })
})
