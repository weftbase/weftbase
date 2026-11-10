// The conversation's logic on the server's own documents.
//
// conversation-*.json and sessions-demo.json are bodies of
// GET /v1/sessions/{id}/conversation and GET /v1/sessions captured,
// unedited, from `go run ./examples/demo` (the refunds conversation is
// one the demo plays by itself; the failed and the waiting ones were
// started through POST /v1/runs with the demo's scripted prompts).
// session-timeline.json is the server's golden: it is where grants,
// compactions, steered messages and labels come from, since the demo's
// sessions have no thread records.
import { afterEach, describe, expect, it, vi } from "vitest"

import {
  actorName,
  buildItems,
  exchanges,
  gapText,
  markMarkers,
  offPath,
  outcomeStrip,
  sessionTitle,
  summaryParts,
  summaryTitle,
  turnMarkers,
  turnNote,
  waitMarker,
} from "@/components/console/session/model"
import {
  loadConversation,
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

import failedDoc from "./fixtures/conversation-failed.json"
import refundsDoc from "./fixtures/conversation-refunds.json"
import waitingDoc from "./fixtures/conversation-waiting.json"
import timelineDoc from "./fixtures/session-timeline.json"
import sessionsDemo from "./fixtures/sessions-demo.json"
import sessionsGolden from "./fixtures/sessions.json"

const refunds = refundsDoc as unknown as Conversation
const failed = failedDoc as unknown as Conversation
const waiting = waitingDoc as unknown as Conversation
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
    totalTurns: c.total_turns,
    turns: c.turns,
    sessionMarks: c.session_marks ?? [],
    marksCapped: false,
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
      "Waited 5.0s for approval · issue_refund approved by key …RMAVCWT9",
    ])
    expect(first.note).toBeNull()
  })

  it("opens to who, how and when, with the exact arguments", () => {
    const m = waitMarker(refunds.turns[0].waits![0], NOW)
    const fact = (label: string) => m.facts.find((f) => f.label === label)
    expect(fact("Tool")?.value).toBe("issue_refund")
    expect(fact("Decision")?.value).toBe("approved")
    expect(fact("Decided by")?.value).toBe("key …RMAVCWT9")
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

  it("names who decided once", () => {
    expect(actorName({ kind: "operator", id: "operator" })).toBe("operator")
    expect(actorName({ kind: "operator", id: "op_01HZX3K9QW7N" })).toBe(
      "operator …X3K9QW7N"
    )
    expect(actorName({ kind: "key", id: "k", name: "ci" })).toBe("key ci")
    expect(actorName(undefined)).toBeUndefined()
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
    )
    expect(denied.text).toContain("issue_refund denied by key")
    expect(denied.tone).toBe("danger")
    expect(denied.facts.find((f) => f.label === "Reason")?.value).toBe(
      "too late"
    )
    const two = waitMarker(
      { ...w, approvals: [a, { ...a, id: "appr_2", tool: "send_email" }] },
      NOW
    )
    expect(two.text).toContain("for 2 approvals · all approved by key")
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
    expect(turnNote(interrupted)?.text).toMatch(/^Interrupted by turn 5:/)
    expect(offPath(interrupted)).toBe(false)
    const rolled = turn({
      display_status: "cancelled",
      reply: null,
      ended_kind: "rollback",
      ended_by_turn: 5,
    })
    expect(turnNote(rolled)?.text).toMatch(/^Rolled back by turn 5:/)
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
    expect(texts).toContain("Message steered into the turn")
    const m = markers.find((x) => x.kind === "message")!
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

  it("a grant used several times is one line", () => {
    const base = timeline.marks[0]
    const use = (n: number) => ({
      ...base,
      kind: "grant.use",
      entry: `u${n}`,
      at: `2020-01-01T09:00:1${n}.000Z`,
      fields: { grant_id: "e32", tool: "send_email" },
    })
    const out = markMarkers([use(1), use(2), use(3)])
    expect(out.map((m) => m.text)).toEqual([
      "Grant used 3 times for send_email",
    ])
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

  it("explains a gap in turn numbers in one line", () => {
    const [a, b, c] = failed.turns
    expect(gapText(a, b)).toBeUndefined()
    expect(gapText(a, { ...c, turn: 3 })).toBe(
      "Turn 2 is not in this server's records (deleted, or never reported)."
    )
    expect(gapText(a, { ...c, turn: 5 })).toBe(
      "Turns 2–4 are not in this server's records (deleted, or never reported)."
    )
    expect(gapText(undefined, { ...c, turn: 2 })).toMatch(/^Turn 1 is not/)
    // A resume that took a turn number of its own is not a hole.
    expect(gapText(refunds.turns[0], { ...refunds.turns[1], turn: 3 })).toBe(
      "Turn 2 is turn 1 continuing after its wait, shown above as one turn."
    )
    // Runs that carry no turn number make no claim.
    expect(gapText({ ...a, turn: 0 }, { ...b, turn: 0 })).toBeUndefined()
    const items = buildItems(
      view(failed, { turns: [a, { ...c, turn: 4 }] }),
      NOW
    )
    expect(items.map((i) => i.type)).toEqual(["turn", "gap", "turn"])
  })
})

describe("the list", () => {
  it("draws one dot per turn and folds a decided park", () => {
    const s = demo.sessions.find((x) => x.resumes === 1)!
    expect(s.outcomes).toEqual(["resumed", "succeeded", "succeeded"])
    expect(exchanges(s)).toBe(2)
    const strip = outcomeStrip(s)
    expect(strip.dots).toEqual(["succeeded", "succeeded"])
    expect(strip.earlier).toBe(0)
    expect(strip.title).toBe("2 turns: 2 succeeded")
  })

  it("shows a failed turn inside a session that ended well", () => {
    const s = demo.sessions.find(
      (x) => x.failed_turns === 1 && x.status === "succeeded"
    )!
    expect(outcomeStrip(s).dots).toEqual(["succeeded", "failed", "succeeded"])
    expect(sessionStatus(s)).toBe("succeeded")
    expect(outcomeStrip(s).title).toBe("3 turns: 2 succeeded, 1 failed")
  })

  it("reads waiting and cancelled from the server's words", () => {
    const parked = demo.sessions.find((x) => x.status === "parked")!
    expect(sessionStatus(parked)).toBe("waiting")
    expect(outcomeStrip(parked).dots.at(-1)).toBe("waiting")
    const cancelled = demo.sessions.find((x) => x.status === "cancelled")!
    expect(outcomeStrip(cancelled).dots).toEqual(["succeeded", "cancelled"])
    expect(outcomeStatus("ended")).toBe("cancelled")
    expect(outcomeStatus("resumed")).toBe("running")
    expect(outcomeStatus("a new word")).toBe("interrupted")
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
    const v = await loadConversation(refunds.session_id, "tail")
    expect(urls.length).toBe(1)
    expect(v.offset).toBe(0)
    expect(v.turns.length).toBe(2)
    expect(v.totalTurns).toBe(2)
  })

  it("a long one opens on its newest turns", async () => {
    const page = (offset: number, n: number, total: number): Conversation => ({
      ...refunds,
      total_turns: total,
      offset,
      next_offset: offset + n < total ? offset + n : null,
      turns: Array.from({ length: n }, (_, i) => ({
        ...refunds.turns[1],
        turn: offset + i + 1,
        run_id: `run_${offset + i + 1}`,
      })),
    })
    serve((u) => {
      const offset = Number(u.searchParams.get("offset") ?? 0)
      const limit = Number(u.searchParams.get("limit"))
      return page(offset, Math.min(limit, 26 - offset), 26)
    })
    const v = await loadConversation("s", "tail")
    expect(urls.length).toBe(2)
    expect(v.offset).toBe(6)
    expect(v.turns.map((t) => t.turn)).toEqual(
      Array.from({ length: 20 }, (_, i) => i + 7)
    )
    // Pinned to a turn, it reads from there to the newest.
    const pinned = await loadConversation("s", 0)
    expect(pinned.turns.length).toBe(26)
    expect(pinned.offset).toBe(0)
  })
})
