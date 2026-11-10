// The conversation's logic, without React: what a turn's one-line
// summary says, which one-line markers a turn gets (waits on a person,
// approvals, grants, steered messages, compactions, forks, handoffs),
// what is said in words when a turn has no reply, and the list of items
// the page draws top to bottom. Tested on the server's own documents
// (src/test/session.test.ts).

import type { ConversationView } from "@/lib/api/sessions"
import { outcomeStatus } from "@/lib/api/status"
import type { DisplayStatus } from "@/lib/api/status"
import type {
  Actor,
  Approval,
  ConversationTurn,
  SessionEntry,
  SessionRow,
  WaitDoc,
} from "@/lib/api/types"
import { cost, dateTime, duration, shortId, tokens } from "@/lib/format"

// ── The summary line ────────────────────────────────────────────────

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/** A duration for a sentence: one decimal under a minute ("4.1s"). */
export function shortDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return duration(ms)
}

/** An open turn is still being worked on (or waited on). */
export const isOpen = (t: Pick<ConversationTurn, "display_status">) =>
  t.display_status === "running" ||
  t.display_status === "waiting" ||
  t.display_status === "queued"

/** turnElapsed: first start to last end; an open turn is measured to now. */
export function turnElapsed(t: ConversationTurn, now: number): number {
  if (!isOpen(t)) return t.duration_ms
  return Math.max(t.duration_ms, now - Date.parse(t.started))
}

/** summaryParts is the collapsed line of a turn:
 * "3 model calls · 2 tools · 4.1s · $0.012". Zero tool calls are left
 * out; an unknown cost reads "cost unknown", never $0. */
export function summaryParts(t: ConversationTurn, now: number): string[] {
  // A turn still going has no finished model call and no cost yet: say
  // what is known so far, not "0 model calls · cost unknown".
  const open = isOpen(t)
  const parts: string[] = []
  if (t.model_calls > 0 || !open)
    parts.push(plural(t.model_calls, "model call"))
  if (t.tool_calls > 0) parts.push(plural(t.tool_calls, "tool"))
  parts.push(shortDuration(turnElapsed(t, now)))
  const c = cost(t)
  if (c !== "unknown") parts.push(c)
  else if (!open) parts.push("cost unknown")
  return parts
}

/** summaryTitle: the figures the line leaves out, for its tooltip. */
export function summaryTitle(t: ConversationTurn): string {
  const lines = [
    `${tokens(t.usage.input_tokens)} tokens in, ${tokens(t.usage.output_tokens)} out`,
  ]
  if (t.waiting_ms > 0)
    lines.push(`${shortDuration(t.waiting_ms)} of it waiting on a person`)
  if (t.runs.length > 1)
    lines.push(`${t.runs.length} runs (it parked and resumed)`)
  return lines.join(" · ")
}

// ── What a turn without a reply says ────────────────────────────────

export interface TurnNote {
  tone: "danger" | "warning" | "muted"
  /** One sentence. */
  text: string
  /** The server's own error text, when there is one. */
  detail?: string
}

const ERROR_KIND: Record<string, string> = {
  max_steps: "it reached its step limit",
  model_error: "a model call failed",
  tool_error: "a tool call failed",
  timeout: "it ran out of time",
  usage_limit: "it reached its usage limit",
  loop_detected: "it was repeating itself",
}

const CANCEL_CAUSE: Record<string, string> = {
  user: "by a person",
  "thread.closed": "because the session was closed",
  "weftbase.cancel": "from the server",
  deadline: "because its deadline passed",
}

/** endedNote: a newer message ended this turn (interrupt | rollback). */
function endedNote(t: ConversationTurn): TurnNote | undefined {
  if (!t.ended_kind && !t.ended_by) return undefined
  const by = t.ended_by_turn ? `turn ${t.ended_by_turn}` : "a newer message"
  if (t.ended_kind === "rollback")
    return {
      tone: "muted",
      text: `Rolled back by ${by}: what this turn did is not part of the conversation that followed.`,
    }
  return {
    tone: "muted",
    text: `Interrupted by ${by}: the agent stopped here and went on with the newer message.`,
  }
}

/** turnNote: what stands where the reply would be, in words. null when
 * the turn has its reply and nothing more needs saying. */
export function turnNote(t: ConversationTurn): TurnNote | null {
  const ended = endedNote(t)
  if (ended) return ended
  switch (t.display_status) {
    case "succeeded":
      return t.reply === null
        ? { tone: "muted", text: "The turn finished without a text reply." }
        : null
    case "failed": {
      const why = t.error_type ? ERROR_KIND[t.error_type] : undefined
      return {
        tone: "danger",
        text: why
          ? `No reply: the run failed because ${why}.`
          : `No reply: the run failed${t.error_type ? ` (${t.error_type})` : ""}.`,
        detail: t.err || undefined,
      }
    }
    case "cancelled": {
      const who = t.cancel_cause
        ? (CANCEL_CAUSE[t.cancel_cause] ?? `(${t.cancel_cause})`)
        : undefined
      return {
        tone: "muted",
        text: `No reply: the run was cancelled${who ? ` ${who}` : ""}.`,
      }
    }
    case "interrupted":
      return {
        tone: "muted",
        text: "No reply: the run stopped reporting without an end.",
      }
    case "waiting":
      return null // the open wait's marker says it, with the decision
    case "running":
      return { tone: "muted", text: "The agent is working on it…" }
    case "queued":
      return { tone: "muted", text: "Queued: waiting for a worker." }
  }
}

/** offPath: the turn was rolled back, so the conversation went on
 * without it. Drawn dimmed and folded. */
export const offPath = (t: ConversationTurn) => t.ended_kind === "rollback"

// ── Markers ─────────────────────────────────────────────────────────

export type MarkerKind =
  | "wait"
  | "approval"
  | "grant"
  | "message"
  | "compaction"
  | "fork"
  | "handoff"
  | "label"
  | "other"

export interface Fact {
  label: string
  value: string
  /** An id, a hash, a number: drawn in mono. */
  mono?: boolean
}

export interface Marker {
  key: string
  kind: MarkerKind
  /** The one line. */
  text: string
  /** When it happened (RFC 3339). */
  at: string
  /** Who / how / when and whatever else the record carries. */
  facts: Fact[]
  /** The exact arguments of the calls decided, for the expanded view. */
  args?: { tool: string; value: unknown }[]
  /** Another session this marker points to (a fork, a handoff). */
  session?: string
  /** An open wait: the run whose calls a person can decide now. */
  pendingRun?: string
  tone?: "warning" | "danger"
}

const str = (v: unknown): string | undefined =>
  typeof v === "string" && v !== "" ? v : undefined
const num = (v: unknown): number | undefined =>
  typeof v === "number" ? v : undefined

/** actorName: "operator op_1", "key …VCWT9". */
export function actorName(a: Actor | undefined): string | undefined {
  if (!a) return undefined
  const id = str(a.name) ?? (a.id ? shortId(a.id) : undefined)
  // An operator whose id is the word "operator" is said once.
  return (
    [a.kind, id === a.kind ? undefined : id].filter(Boolean).join(" ") ||
    undefined
  )
}

const VERB: Record<string, string> = {
  approve: "approved",
  approved: "approved",
  deny: "denied",
  denied: "denied",
  resolve: "answered for the tool",
  resolved: "answered for the tool",
  expired: "expired",
  cancelled: "cancelled",
  pending: "pending",
}
const verb = (word: string | undefined) =>
  word ? (VERB[word] ?? word) : "decided"

const unique = <T>(xs: T[]) => [...new Set(xs)]

/** waitMarker: one wait on a person, from the park until the resume. */
export function waitMarker(w: WaitDoc, now: number): Marker {
  const approvals = w.approvals ?? []
  const tools = unique(approvals.map((a) => a.tool))
  const what =
    approvals.length > 1 ? `${approvals.length} approvals` : "approval"
  const from = Date.parse(w.from)
  const facts: Fact[] = []
  const args = approvals.map((a) => ({ tool: a.tool, value: a.args }))
  if (w.to === null) {
    return {
      key: `wait:${w.run}`,
      kind: "wait",
      tone: "warning",
      text: `Waiting ${shortDuration(Math.max(0, now - from))} for ${what}${tools.length ? ` of ${tools.join(", ")}` : ""}`,
      at: w.from,
      facts: [{ label: "Asked", value: dateTime(w.from) }],
      pendingRun: w.run,
    }
  }
  const outcomes = unique(approvals.map((a) => verb(a.outcome ?? a.status)))
  const who = unique(
    (w.decided_by ?? [])
      .map(actorName)
      .filter((x): x is string => x !== undefined)
  )
  const decided =
    approvals.length === 0
      ? "decided"
      : approvals.length === 1
        ? `${approvals[0].tool} ${outcomes[0]}`
        : outcomes.length === 1
          ? `all ${outcomes[0]}`
          : approvals
              .map((a) => `${a.tool} ${verb(a.outcome ?? a.status)}`)
              .join(", ")
  for (const a of approvals) approvalFacts(a, approvals.length > 1, facts)
  facts.push(
    { label: "Asked", value: dateTime(w.from) },
    { label: "Resumed", value: dateTime(w.to) }
  )
  return {
    key: `wait:${w.run}`,
    kind: "wait",
    tone: approvals.some((a) => a.status === "denied") ? "danger" : undefined,
    text: `Waited ${shortDuration(Date.parse(w.to) - from)} for ${what} · ${decided}${who.length ? ` by ${who.join(", ")}` : ""}`,
    at: w.from,
    facts,
    args,
  }
}

function approvalFacts(a: Approval, named: boolean, out: Fact[]) {
  const p = named ? `${a.tool}: ` : ""
  if (!named) out.push({ label: "Tool", value: a.tool, mono: true })
  out.push({ label: `${p}Decision`, value: verb(a.outcome ?? a.status) })
  const who = actorName(a.actor)
  if (who) out.push({ label: `${p}Decided by`, value: who, mono: true })
  if (a.reason) out.push({ label: `${p}Reason`, value: a.reason })
  if (a.content)
    out.push({ label: `${p}Answer given to the agent`, value: a.content })
  if (a.decided_at)
    out.push({ label: `${p}Decided`, value: dateTime(a.decided_at) })
}

const FIELD_LABEL: Record<string, string> = {
  tool: "Tool",
  via: "How",
  scope: "Scope",
  outcome: "Outcome",
  reason: "Reason",
  state: "State",
  input: "Kind",
  uses: "Uses",
  name: "Name",
  target: "Entry",
  from_entry: "From entry",
  to_entry: "To entry",
  child_session: "Session",
  new_session: "New session",
  call_id: "Call",
  grant_id: "Grant",
  receipt_id: "Receipt",
  request_entry: "Request entry",
  key_id: "Key",
  args_sha256: "Arguments hash",
  messages_before: "Messages before",
  messages_after: "Messages after",
  tokens_before: "Tokens before",
  tokens_after: "Tokens after",
  replaced: "Messages replaced",
  entries: "Entries",
  hash: "Summary hash",
  always: "For every later call",
  wrapper_call: "Delegating call",
  who: "Who",
  text: "Message",
  prompt: "Prompt",
  stop: "Stopped because",
  content: "Answer given to the agent",
}

const MONO =
  /(_id|_entry|_sha256|^hash|^target|_session|^entries|^uses|tokens_|messages_|^replaced)$|^(call_id|key_id)/

function valueText(v: unknown): string {
  if (typeof v === "string") return v
  if (typeof v === "number" || typeof v === "boolean") return String(v)
  if (v && typeof v === "object") {
    const o = v as Record<string, unknown>
    // {provider, name} and {input_tokens, output_tokens} read better flat.
    if (typeof o.name === "string")
      return [o.provider, o.name].filter(Boolean).join(" ")
    if (typeof o.input_tokens === "number")
      return `${tokens(o.input_tokens)} in, ${tokens(num(o.output_tokens) ?? 0)} out`
  }
  return JSON.stringify(v)
}

/** markFacts: who, how, when first, then every other field the record
 * carries, so the expanded marker hides nothing. */
export function markFacts(m: SessionEntry): Fact[] {
  const facts: Fact[] = []
  const who = str(m.content?.who) ?? m.actor
  if (who)
    facts.push({
      label:
        m.actor_of === "resume" ? "Decided by (the resume's deciders)" : "Who",
      value: who,
    })
  const via = str(m.fields.via)
  if (via) facts.push({ label: "How", value: via })
  facts.push({ label: "When", value: dateTime(m.fact_at ?? m.at) })
  for (const [k, v] of Object.entries(m.content ?? {})) {
    if (k === "who" || v === "" || v === null || v === undefined) continue
    facts.push({ label: FIELD_LABEL[k] ?? k, value: valueText(v) })
  }
  for (const [k, v] of Object.entries(m.fields)) {
    if (k === "via" || v === "" || v === null || v === undefined) continue
    facts.push({
      label: FIELD_LABEL[k] ?? k.replace(/_/g, " "),
      value: valueText(v),
      mono: MONO.test(k),
    })
  }
  if (m.entry) facts.push({ label: "Entry", value: m.entry, mono: true })
  if (m.content_mark === "stripped")
    facts.push({
      label: "Content",
      value: "removed before it reached this server",
    })
  return facts
}

const RECEIPT: Record<string, string> = {
  steer: "Message steered into the turn",
  queue: "Message queued until the turn ended",
  reject: "Message refused: the session was busy",
  interrupt: "Message interrupted the turn",
  rollback: "Message rolled the turn back",
}

/** markText: the one line of a thread mark. */
export function markText(
  m: SessionEntry,
  request?: SessionEntry
): { kind: MarkerKind; text: string; tone?: Marker["tone"]; session?: string } {
  const f = m.fields
  const tool = str(f.tool) ?? str(request?.fields.tool)
  switch (m.kind) {
    case "approval.request":
      return {
        kind: "approval",
        tone: "warning",
        text: `Approval requested${tool ? ` for ${tool}` : ""}`,
      }
    case "approval.decision": {
      const who = str(m.content?.who) ?? (m.actor_of ? undefined : m.actor)
      const reason = str(m.content?.reason)
      const outcome = str(f.outcome)
      return {
        kind: "approval",
        tone: outcome === "deny" ? "danger" : undefined,
        text:
          `${tool ?? "Call"} ${verb(outcome)}` +
          (who ? ` by ${who}` : "") +
          (f.always === true ? ", and for every later call" : "") +
          (reason ? ` · ${reason}` : ""),
      }
    }
    case "approval.refused":
      return {
        kind: "approval",
        tone: "danger",
        text: `A decision was refused${str(f.reason) ? `: ${str(f.reason)!.replace(/_/g, " ")}` : ""}`,
      }
    case "grant.mint":
      return {
        kind: "grant",
        text: `Grant given: ${tool ?? "a tool"} may run without asking${f.scope === "session" ? ", for this session" : ""}`,
      }
    case "grant.use":
      return { kind: "grant", text: `Grant used${tool ? ` for ${tool}` : ""}` }
    case "grant.revoke": {
      const uses = num(f.uses)
      return {
        kind: "grant",
        text:
          `Grant revoked${tool ? `: ${tool}` : ""}` +
          (uses !== undefined ? `, after ${plural(uses, "use")}` : ""),
      }
    }
    case "grant.expire":
      return { kind: "grant", text: `Grant expired${tool ? `: ${tool}` : ""}` }
    case "receipt": {
      const state = str(f.state)
      const base = RECEIPT[str(f.input) ?? ""] ?? "Message received"
      return {
        kind: "message",
        text: state && state !== "delivered" ? `${base} (${state})` : base,
      }
    }
    case "interrupt":
      return { kind: "message", text: "The turn was interrupted" }
    case "rewind":
      return {
        kind: "fork",
        text: `Rewound${str(f.to_entry) ? ` to entry ${str(f.to_entry)}` : ""}`,
      }
    case "compaction": {
      const before = num(f.tokens_before)
      const after = num(f.tokens_after)
      return {
        kind: "compaction",
        text:
          before !== undefined && after !== undefined
            ? `Context compacted: ${tokens(before)} → ${tokens(after)} tokens`
            : "Context compacted",
      }
    }
    case "uncompact":
      return { kind: "compaction", text: "A compaction was undone" }
    case "pin":
      return {
        kind: "compaction",
        text: "An entry was pinned: compaction keeps it",
      }
    case "branch":
      return {
        kind: "fork",
        text: `Branched here${str(f.to_entry) ? `: the conversation went on from entry ${str(f.to_entry)}` : ""}`,
      }
    case "fork": {
      const to = str(f.new_session)
      return {
        kind: "fork",
        text: to ? "Forked here into another session" : "Forked here",
        session: to,
      }
    }
    case "label":
      return {
        kind: "label",
        text: `Labelled${str(f.name) ? ` “${str(f.name)}”` : ""}`,
      }
    case "pool.delegate": {
      const state = str(f.state)
      return {
        kind: "handoff",
        text: `Handed to another session${state ? ` (${state})` : ""}`,
        session: str(f.child_session),
      }
    }
    case "pool.retire":
      return {
        kind: "handoff",
        text: "The session it was handed to was retired",
        session: str(f.child_session),
      }
  }
  return { kind: "other", text: m.kind.replace(/[._]/g, " ") }
}

const markKey = (m: SessionEntry) => `mark:${m.entry ?? ""}:${m.kind}:${m.at}`

function fromMark(m: SessionEntry, request?: SessionEntry): Marker {
  return {
    key: markKey(m),
    ...markText(m, request),
    at: m.fact_at ?? m.at,
    facts: markFacts(m),
  }
}

/** markMarkers turns thread marks into markers. An approval's request
 * and its decision are one marker (the decision's); a request with no
 * decision stands alone; `skipCalls` are the calls a wait already tells
 * (the ledger's record), so they are not told twice; a grant used
 * several times is one line. */
export function markMarkers(
  marks: readonly SessionEntry[],
  skipCalls: ReadonlySet<string> = new Set()
): Marker[] {
  const requests = new Map<string, SessionEntry>()
  const decided = new Set<string>()
  for (const m of marks) {
    if (m.kind === "approval.request" && m.entry) requests.set(m.entry, m)
    if (m.kind === "approval.decision") {
      const r = str(m.fields.request_entry)
      if (r) decided.add(r)
    }
  }
  const out: Marker[] = []
  const uses = new Map<string, Marker & { n: number }>()
  for (const m of marks) {
    const call = str(m.fields.call_id)
    if (m.kind.startsWith("approval.") && call && skipCalls.has(call)) continue
    if (m.kind === "approval.request" && m.entry && decided.has(m.entry))
      continue
    if (m.kind === "grant.use") {
      const id = str(m.fields.grant_id) ?? ""
      const seen = uses.get(id)
      if (seen) {
        seen.n++
        seen.text = seen.text.replace(
          /^Grant used( \d+ times)?/,
          `Grant used ${seen.n} times`
        )
        continue
      }
      const first = { ...fromMark(m), n: 1 }
      uses.set(id, first)
      out.push(first)
      continue
    }
    out.push(fromMark(m, requests.get(str(m.fields.request_entry) ?? "")))
  }
  return out
}

/** turnMarkers: everything that happened inside a turn besides the
 * model's work, in time order: its waits on a person and its marks. */
export function turnMarkers(t: ConversationTurn, now: number): Marker[] {
  const waits = t.waits ?? []
  const told = new Set(
    waits.flatMap((w) => (w.approvals ?? []).map((a) => a.call_id))
  )
  return [
    ...waits.map((w) => waitMarker(w, now)),
    ...markMarkers(t.marks ?? [], told),
  ].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

// ── The page's items ────────────────────────────────────────────────

export type Item =
  | { type: "older"; key: string; count: number }
  | { type: "gap"; key: string; text: string }
  | { type: "mark"; key: string; marker: Marker }
  | {
      type: "turn"
      key: string
      turn: ConversationTurn
      /** Its position among the session's turns (1-based), for a
       * session whose runs carry no turn number. */
      ordinal: number
      markers: Marker[]
      note: TurnNote | null
      offPath: boolean
    }

/** turnNumber: the number a turn is known by. */
export const turnNumber = (t: ConversationTurn, ordinal: number) =>
  t.turn > 0 ? t.turn : ordinal

const range = (a: number, b: number) =>
  a === b ? `Turn ${a} is` : `Turns ${a}–${b} are`

/** gapText explains missing turn numbers between two turns (prev
 * undefined: before the first one). A resume that took a number of its
 * own is told as what it is. */
export function gapText(
  prev: ConversationTurn | undefined,
  next: ConversationTurn
): string | undefined {
  if (next.turn <= 0 || (prev && prev.turn <= 0)) return undefined
  const from = (prev?.turn ?? 0) + 1
  const to = next.turn - 1
  if (to < from) return undefined
  const missing = to - from + 1
  if (prev && prev.runs.length - 1 >= missing)
    return `${range(from, to)} turn ${prev.turn} continuing after its wait, shown above as one turn.`
  return `${range(from, to)} not in this server's records (deleted, or never reported).`
}

/** buildItems lays the conversation out top to bottom: a "load older"
 * row when earlier turns are not loaded, each turn with its markers,
 * one muted line per gap in turn numbers, and the session's own marks
 * (filed under no turn) where they fall in time. */
export function buildItems(c: ConversationView, now: number): Item[] {
  const items: Item[] = []
  if (c.offset > 0) items.push({ type: "older", key: "older", count: c.offset })
  const loose = markMarkers(c.sessionMarks)
    .map((marker) => ({ marker, t: Date.parse(marker.at) }))
    .sort((a, b) => a.t - b.t)
  // Marks older than the first loaded turn belong with the turns that
  // are not loaded.
  const first = c.turns[0] ? Date.parse(c.turns[0].started) : -Infinity
  let li = 0
  if (c.offset > 0) while (li < loose.length && loose[li].t < first) li++

  c.turns.forEach((turn, i) => {
    const started = Date.parse(turn.started)
    for (; li < loose.length && loose[li].t < started; li++)
      items.push({
        type: "mark",
        key: loose[li].marker.key,
        marker: loose[li].marker,
      })
    const prev = i === 0 ? undefined : c.turns[i - 1]
    const gap = i === 0 && c.offset > 0 ? undefined : gapText(prev, turn)
    if (gap) items.push({ type: "gap", key: `gap:${turn.run_id}`, text: gap })
    items.push({
      type: "turn",
      key: turn.run_id,
      turn,
      ordinal: c.offset + i + 1,
      markers: turnMarkers(turn, now),
      note: turnNote(turn),
      offPath: offPath(turn),
    })
  })
  for (; li < loose.length; li++)
    items.push({
      type: "mark",
      key: loose[li].marker.key,
      marker: loose[li].marker,
    })
  return items
}

// ── The list's outcome strip ────────────────────────────────────────

export interface OutcomeStrip {
  /** One per turn shown, oldest first. */
  dots: DisplayStatus[]
  /** Older turns the strip leaves out. */
  earlier: number
  /** "3 turns: 2 succeeded, 1 failed". */
  title: string
}

export const STRIP_MAX = 12

/** exchanges: a session's turns as a person counts them (a resume is
 * part of the turn it continues). */
export const exchanges = (s: Pick<SessionRow, "turns" | "resumes">) =>
  Math.max(0, s.turns - (s.resumes ?? 0))

/** outcomeStrip reads a session row's `outcomes` (one word per run,
 * oldest first) as one dot per turn: a park that was decided ("resumed")
 * is folded into the run that continued it. */
export function outcomeStrip(
  s: Pick<SessionRow, "turns" | "resumes" | "outcomes">,
  max = STRIP_MAX
): OutcomeStrip {
  const words = s.outcomes ?? []
  const turns = words.filter(
    (w, i) => w !== "resumed" || i === words.length - 1
  )
  const all = turns.map(outcomeStatus)
  const dots = all.slice(-max)
  const total = exchanges(s)
  const counts = new Map<DisplayStatus, number>()
  for (const d of all) counts.set(d, (counts.get(d) ?? 0) + 1)
  const parts = [...counts].map(([k, n]) => `${n} ${k}`)
  return {
    dots,
    earlier: Math.max(0, total - dots.length),
    title: `${plural(total, "turn")}${parts.length ? `: ${parts.join(", ")}` : ""}${
      all.length < total ? ` (the newest ${all.length})` : ""
    }`,
  }
}

/** sessionTitle: the title, else the first message, else what is known. */
export function sessionTitle(
  s: Pick<SessionRow, "title" | "first_prompt" | "content_off"> | undefined
): string {
  if (!s) return "Session"
  return (
    s.title ||
    s.first_prompt ||
    (s.content_off ? "Content not recorded" : "Untitled session")
  )
}
