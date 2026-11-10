// The conversation's logic, without React: what a turn's one-line
// summary says, which one-line markers a turn gets (waits on a person
// with what was decided, grants, steered messages, compactions, forks,
// handoffs), the number a turn is shown and linked by,
// what is said in words when a turn has no reply, and the list of items
// the page draws top to bottom. Tested on the server's own documents
// (src/test/session.test.ts).

import { actorText } from "@/lib/actor"
import type { ActorLike } from "@/lib/actor"
import type { ConversationView } from "@/lib/api/sessions"
import { outcomeStatus } from "@/lib/api/status"
import type { DisplayStatus } from "@/lib/api/status"
import type {
  Approval,
  ConversationTurn,
  SessionEntry,
  SessionRow,
  WaitDoc,
} from "@/lib/api/types"
import { cost, dateTime, duration, tokens } from "@/lib/format"

// ── The summary line ────────────────────────────────────────────────

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

/** A duration for a sentence: one decimal under a minute ("4.1s"). */
function shortDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return duration(ms)
}

/** An open turn is still being worked on (or waited on). */
const isOpen = (t: Pick<ConversationTurn, "display_status">) =>
  t.display_status === "running" ||
  t.display_status === "waiting" ||
  t.display_status === "queued"

/** turnElapsed: first start to last end; an open turn is measured to now. */
function turnElapsed(t: ConversationTurn, now: number): number {
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

/** endedNote: a newer message ended this turn (interrupt | rollback).
 * `endedBy` is the position of that message's turn, when it is known. */
function endedNote(
  t: ConversationTurn,
  endedBy?: number
): TurnNote | undefined {
  if (!t.ended_kind && !t.ended_by) return undefined
  const by = endedBy ? `turn ${endedBy}` : "a newer message"
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
export function turnNote(
  t: ConversationTurn,
  endedBy?: number
): TurnNote | null {
  const ended = endedNote(t, endedBy)
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
 * without it (the server's `off_path`). Drawn dimmed and folded. */
export const offPath = (t: ConversationTurn) => t.off_path === true

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
  /** The decision of a call (or, "asked", its request still open): the
   * wait it ended tells it, in the wait's own line. */
  decides?: "decided" | "asked"
  /** The tool that call is of, when the record names it. */
  tool?: string
}

/** What the markers need from outside the turn. */
export interface MarkerContext {
  /** The signed-in principal: its own decisions read "you". */
  me?: ActorLike
  /** The tool each grant covers, by grant id (the mint can be in an
   * earlier turn than the use). */
  grants?: ReadonlyMap<string, string>
}

const str = (v: unknown): string | undefined =>
  typeof v === "string" && v !== "" ? v : undefined
const num = (v: unknown): number | undefined =>
  typeof v === "number" ? v : undefined

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

/** A line starts with a capital, except a tool's name. */
const startLine = (s: string) => (s.startsWith("call ") ? `C${s.slice(1)}` : s)

const midLine = (s: string) => (s.startsWith("Call ") ? `c${s.slice(1)}` : s)

/** A wait this short is not worth saying: a grant answered, nobody was
 * asked. */
const WAIT_WORTH_SAYING_MS = 100

/** waitMarker: one wait on a person, from the park until the resume, in
 * one line with what was decided: "Waited 3.0s · refund approved by
 * you". `decisions` are the thread's decision markers the wait covers
 * (a session whose ledger wait carries no approvals); a wait too short
 * to notice reads as its decision alone, and is undefined when there is
 * nothing to say about it. */
export function waitMarker(
  w: WaitDoc,
  now: number,
  ctx: MarkerContext = {},
  decisions: readonly Marker[] = []
): Marker | undefined {
  const approvals = w.approvals ?? []
  const from = Date.parse(w.from)
  if (w.to === null) {
    const tools = unique([
      ...approvals.map((a) => a.tool),
      ...decisions.flatMap((d) => (d.tool ? [d.tool] : [])),
    ])
    const what =
      approvals.length > 1 ? `${approvals.length} approvals` : "approval"
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
  const ms = Date.parse(w.to) - from
  const waited =
    ms >= WAIT_WORTH_SAYING_MS ? `Waited ${shortDuration(ms)}` : undefined
  const facts: Fact[] = []
  let told: string | undefined
  let tone: Marker["tone"]
  let kind: MarkerKind = "wait"
  if (approvals.length > 0) {
    const outcomes = unique(approvals.map((a) => verb(a.outcome ?? a.status)))
    const who = unique(
      (w.decided_by ?? [])
        .map((a) => actorText(a, ctx.me))
        .filter((x): x is string => x !== undefined)
    )
    const reason = approvals.length === 1 ? approvals[0].reason : undefined
    told =
      (approvals.length === 1
        ? `${approvals[0].tool} ${outcomes[0]}`
        : outcomes.length === 1
          ? `all ${approvals.length} calls ${outcomes[0]}`
          : approvals
              .map((a) => `${a.tool} ${verb(a.outcome ?? a.status)}`)
              .join(", ")) +
      (who.length ? ` by ${who.join(", ")}` : "") +
      (reason ? ` · ${reason}` : "")
    if (approvals.some((a) => a.status === "denied")) tone = "danger"
    for (const a of approvals)
      approvalFacts(a, approvals.length > 1, facts, ctx)
  } else if (decisions.length > 0) {
    told = decisions.map((d) => midLine(d.text)).join(", ")
    tone = decisions.find((d) => d.tone)?.tone
    if (!waited) kind = decisions[0].kind
    for (const d of decisions)
      for (const f of d.facts)
        if (!facts.some((x) => x.label === f.label && x.value === f.value))
          facts.push(f)
  }
  if (!waited && !told) return undefined
  if (waited)
    facts.push(
      { label: "Asked", value: dateTime(w.from) },
      { label: "Resumed", value: dateTime(w.to) }
    )
  const args = [
    ...approvals.map((a) => ({ tool: a.tool, value: a.args })),
    ...decisions.flatMap((d) => d.args ?? []),
  ]
  return {
    key: `wait:${w.run}`,
    kind,
    tone,
    text:
      waited && told
        ? `${waited} · ${told}`
        : (waited && `${waited} for approval`) || startLine(told ?? ""),
    at: w.from,
    facts,
    args: args.length ? args : undefined,
  }
}

function approvalFacts(
  a: Approval,
  named: boolean,
  out: Fact[],
  ctx: MarkerContext
) {
  const p = named ? `${a.tool}: ` : ""
  if (!named) out.push({ label: "Tool", value: a.tool, mono: true })
  out.push({ label: `${p}Decision`, value: verb(a.outcome ?? a.status) })
  const who = actorText(a.actor, ctx.me)
  if (who) out.push({ label: `${p}Decided by`, value: who })
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
function markFacts(m: SessionEntry, ctx: MarkerContext = {}): Fact[] {
  const facts: Fact[] = []
  // A resume's deciders come as one list, kept as written.
  const who = m.actor_of
    ? m.actor
    : actorText(str(m.content?.who) ?? m.actor, ctx.me)
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

/** What a message that arrived during a turn did, by the receipt's
 * `input`. A receipt of any other kind (a plain "send" that was
 * accepted) is the thread's bookkeeping: the message is the next turn,
 * and no line is drawn for it. */
const RECEIPT: Record<string, string> = {
  steer: "Message steered into the turn",
  queue: "Message queued until the turn ended",
  reject: "Message refused: the session was busy",
  interrupt: "Message interrupted the turn",
  rollback: "Message rolled the turn back",
}

/** receiptText: the line of a message's receipt, in its newest state. */
function receiptText(input: string, state: string | undefined, text?: string) {
  const base = RECEIPT[input]
  return (
    (state && state !== "delivered" ? `${base} (${state})` : base) +
    (text ? `: “${text}”` : "")
  )
}

const viaGrant = (m: SessionEntry) =>
  m.fields.via === "grant" || str(m.fields.grant_id) !== undefined

/** markText: the one line of a thread mark. */
function markText(
  m: SessionEntry,
  request?: SessionEntry,
  ctx: MarkerContext = {}
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
      const who = actorText(
        str(m.content?.who) ?? (m.actor_of ? undefined : m.actor),
        ctx.me
      )
      const reason = str(m.content?.reason)
      const outcome = str(f.outcome)
      // A grant answered: nobody was asked this time. The reason is the
      // grant's, and is told where the grant was given.
      if (viaGrant(m) && outcome !== "deny") {
        const granted = tool ?? ctx.grants?.get(str(f.grant_id) ?? "")
        return {
          kind: "grant",
          text: `${granted ?? "call"} allowed by a grant${who ? ` from ${who}` : ""}`,
        }
      }
      return {
        kind: "approval",
        tone: outcome === "deny" ? "danger" : undefined,
        text:
          `${tool ?? "call"} ${verb(outcome)}` +
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
    case "grant.mint": {
      const reason = str(m.content?.reason)
      return {
        kind: "grant",
        text:
          `Grant given: ${tool ?? "a tool"} may run without asking${f.scope === "session" ? ", for this session" : ""}` +
          (reason ? ` · ${reason}` : ""),
      }
    }
    case "grant.use": {
      const granted = tool ?? ctx.grants?.get(str(f.grant_id) ?? "")
      return { kind: "grant", text: `${granted ?? "call"} allowed by a grant` }
    }
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
    case "receipt":
      return {
        kind: "message",
        text: receiptText(
          str(f.input) ?? "",
          str(f.state),
          str(m.content?.text)
        ),
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

function fromMark(
  m: SessionEntry,
  request: SessionEntry | undefined,
  ctx: MarkerContext
): Marker {
  return {
    key: markKey(m),
    ...markText(m, request, ctx),
    at: m.fact_at ?? m.at,
    facts: markFacts(m, ctx),
  }
}

/** markMarkers turns thread marks into markers, each thing told once:
 * an approval's request and its decision are one marker (the
 * decision's), and so are a grant's use and the decision it gave; a
 * request with no decision stands alone; `skipCalls` are the calls a
 * wait already tells (the ledger's record); the same grant allowing the
 * same tool several times is one line; a message's receipts (queued,
 * then delivered) are one line in its newest state, and the receipt of
 * a plain message is not a line at all. */
export function markMarkers(
  marks: readonly SessionEntry[],
  skipCalls: ReadonlySet<string> = new Set(),
  ctx: MarkerContext = {}
): Marker[] {
  const requests = new Map<string, SessionEntry>()
  const requestOfCall = new Map<string, SessionEntry>()
  const decided = new Set<string>()
  const decidedCalls = new Set<string>()
  for (const m of marks) {
    const call = str(m.fields.call_id)
    if (m.kind === "approval.request") {
      if (m.entry) requests.set(m.entry, m)
      if (call) requestOfCall.set(call, m)
    }
    if (m.kind === "approval.decision") {
      const r = str(m.fields.request_entry)
      if (r) decided.add(r)
      if (call) decidedCalls.add(call)
    }
  }
  const out: Marker[] = []
  const allowed = new Map<string, Marker & { n: number; line: string }>()
  const receipts = new Map<string, { marker: Marker; text?: string }>()
  for (const m of marks) {
    const call = str(m.fields.call_id)
    if (m.kind.startsWith("approval.") && call && skipCalls.has(call)) continue
    if (m.kind === "grant.use" && call && skipCalls.has(call)) continue
    if (
      m.kind === "approval.request" &&
      ((m.entry && decided.has(m.entry)) || (call && decidedCalls.has(call)))
    )
      continue
    // The decision the grant gave says it.
    if (m.kind === "grant.use" && call && decidedCalls.has(call)) continue

    if (m.kind === "receipt") {
      const input = str(m.fields.input) ?? ""
      if (!(input in RECEIPT)) continue
      const id = str(m.fields.receipt_id)
      const seen = id ? receipts.get(id) : undefined
      if (seen) {
        const state = str(m.fields.state)
        seen.marker.text = receiptText(input, state, seen.text)
        seen.marker.facts.push({
          label: state ? `${state[0].toUpperCase()}${state.slice(1)}` : "Then",
          value: dateTime(m.fact_at ?? m.at),
        })
        continue
      }
      const marker = fromMark(m, undefined, ctx)
      if (id) receipts.set(id, { marker, text: str(m.content?.text) })
      out.push(marker)
      continue
    }

    const request =
      requests.get(str(m.fields.request_entry) ?? "") ??
      (call ? requestOfCall.get(call) : undefined)
    const marker = fromMark(m, request, ctx)
    marker.tool = str(m.fields.tool) ?? str(request?.fields.tool)
    if (m.kind === "approval.request") marker.decides = "asked"
    if (m.kind === "approval.decision" || m.kind === "grant.use") {
      marker.decides = "decided"
      if (marker.tool && request?.content?.args !== undefined)
        marker.args = [{ tool: marker.tool, value: request.content.args }]
      if (marker.kind === "grant") {
        const seen = allowed.get(marker.text)
        if (seen) {
          seen.n++
          seen.text = `${seen.line}, ${seen.n} times`
          continue
        }
        const first = { ...marker, n: 1, line: marker.text }
        allowed.set(marker.text, first)
        out.push(first)
        continue
      }
    }
    out.push(marker)
  }
  return out.map((m) => ({ ...m, text: startLine(m.text) }))
}

/** A decision is told by the wait it ended when it falls inside it
 * (the two clocks are the thread's and the ledger's). */
const WAIT_SLACK_MS = 250

/** saidByNote: marks that only repeat what the turn's closing sentence
 * says (turnNote): that a newer message interrupted it or rolled it
 * back. */
function saidByNote(t: ConversationTurn, m: SessionEntry): boolean {
  if (!t.ended_kind && !t.ended_by) return false
  if (m.kind === "interrupt") return true
  return (
    m.kind === "rewind" &&
    t.ended_kind === "rollback" &&
    m.fields.reason === "rollback"
  )
}

/** turnMarkers: everything that happened inside a turn besides the
 * model's work, in time order: its waits on a person and its marks,
 * a wait and the decision that ended it in one line. */
export function turnMarkers(
  t: ConversationTurn,
  now: number,
  ctx: MarkerContext = {}
): Marker[] {
  const waits = t.waits ?? []
  const told = new Set(
    waits.flatMap((w) => (w.approvals ?? []).map((a) => a.call_id))
  )
  let marks = markMarkers(
    (t.marks ?? []).filter((m) => !saidByNote(t, m)),
    told,
    ctx
  )
  const out: Marker[] = []
  for (const w of waits) {
    const from = Date.parse(w.from) - WAIT_SLACK_MS
    const to = (w.to === null ? Infinity : Date.parse(w.to)) + WAIT_SLACK_MS
    const wants = w.to === null ? "asked" : "decided"
    const mine =
      (w.approvals ?? []).length > 0
        ? []
        : marks.filter((m) => {
            const at = Date.parse(m.at)
            return m.decides === wants && at >= from && at <= to
          })
    marks = marks.filter((m) => !mine.includes(m))
    const marker = waitMarker(w, now, ctx, mine)
    if (marker) out.push(marker)
  }
  return [...out, ...marks].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

// ── The page's items ────────────────────────────────────────────────

export type Item =
  | { type: "older"; key: string; count: number }
  | {
      /** The session is a fork: said once, above its first turn. */
      type: "origin"
      key: string
      session: string
      /** The origin's turn the fork was made after, by the thread's
       * number (the page asks the origin for its position). */
      turn?: number
    }
  | { type: "gap"; key: string; text: string }
  | { type: "mark"; key: string; marker: Marker }
  | {
      type: "turn"
      key: string
      turn: ConversationTurn
      /** Its position among the session's turns as a person counts
       * them (1-based): the number shown. */
      position: number
      markers: Marker[]
      note: TurnNote | null
      offPath: boolean
      /** The position of the turn whose message ended this one. */
      endedBy?: number
    }

/** turnPosition: where a turn stands in its session, counted as a
 * person counts (1-based): the server's `index` when it sends one, else
 * its place in the page (`ordinal`: the page's offset + its place + 1). */
const turnPosition = (t: ConversationTurn, ordinal: number) =>
  t.index ?? ordinal

/** turnParam: the number a turn is linked by (`?turn=`, `#turn-N`): the
 * thread's own turn number, which the run carries too (the run page's
 * breadcrumb links with it); its position when the runs carry none. */
export const turnParam = (t: ConversationTurn, position: number) =>
  t.turn > 0 ? t.turn : position

/** positionOfTurn: the position of the turn the thread numbers `raw`,
 * among the turns a view has loaded. */
export function positionOfTurn(
  c: Pick<ConversationView, "turns" | "offset">,
  raw: number
): number | undefined {
  const i = c.turns.findIndex((t) => t.turn === raw)
  return i < 0 ? undefined : turnPosition(c.turns[i], c.offset + i + 1)
}

/** forkOrigin splits a fork's "<origin session>#<entry>". */
export function forkOrigin(v: string): { session: string; entry?: string } {
  const i = v.lastIndexOf("#")
  return i < 0
    ? { session: v }
    : { session: v.slice(0, i), entry: v.slice(i + 1) }
}

/** buildItems lays the conversation out top to bottom: a "load older"
 * row when earlier turns are not loaded, where a fork came from, each
 * turn with its markers, the session's own marks (filed under no turn)
 * where they fall in time, and one line per turn number the server
 * says was skipped and why. A jump in the thread's turn numbers with no
 * reason given is not mentioned: turns are numbered by position. */
export function buildItems(
  c: ConversationView,
  now: number,
  me?: ActorLike
): Item[] {
  const items: Item[] = []
  if (c.offset > 0) items.push({ type: "older", key: "older", count: c.offset })
  const origin = c.session?.forked_from_session
  if (origin && c.offset === 0) {
    const first = c.turns.at(0)?.turn ?? 0
    items.push({
      type: "origin",
      key: "origin",
      session: forkOrigin(origin).session,
      turn: first > 1 ? first - 1 : undefined,
    })
  }
  const grants = new Map<string, string>()
  for (const m of [...c.sessionMarks, ...c.turns.flatMap((t) => t.marks ?? [])])
    if (m.kind === "grant.mint") {
      const id = str(m.fields.grant_id)
      const tool = str(m.fields.tool)
      if (id && tool) grants.set(id, tool)
    }
  const ctx: MarkerContext = { me, grants }
  const loose = markMarkers(c.sessionMarks, undefined, ctx)
    .map((marker) => ({ marker, t: Date.parse(marker.at) }))
    .sort((a, b) => a.t - b.t)
  // Marks older than the first loaded turn belong with the turns that
  // are not loaded.
  const first = c.turns[0] ? Date.parse(c.turns[0].started) : -Infinity
  let li = 0
  if (c.offset > 0) while (li < loose.length && loose[li].t < first) li++

  const position = (i: number) => turnPosition(c.turns[i], c.offset + i + 1)
  const positions = new Map<number, number>()
  c.turns.forEach((t, i) => {
    if (t.turn > 0) positions.set(t.turn, position(i))
  })
  const reasons = c.skipped.filter((s) => s.reason)

  c.turns.forEach((turn, i) => {
    const started = Date.parse(turn.started)
    for (; li < loose.length && loose[li].t < started; li++)
      items.push({
        type: "mark",
        key: loose[li].marker.key,
        marker: loose[li].marker,
      })
    // The numbers skipped just before this turn (none is claimed before
    // the first turn of a page that starts later).
    const after = i === 0 ? (c.offset > 0 ? Infinity : 0) : c.turns[i - 1].turn
    for (const s of reasons)
      if (turn.turn > 0 && s.turn > after && s.turn < turn.turn)
        items.push({ type: "gap", key: `skipped:${s.turn}`, text: s.reason! })
    const endedBy =
      turn.ended_by_turn === undefined
        ? undefined
        : positions.get(turn.ended_by_turn)
    items.push({
      type: "turn",
      key: turn.run_id,
      turn,
      position: position(i),
      markers: turnMarkers(turn, now, ctx),
      note: turnNote(turn, endedBy),
      offPath: offPath(turn),
      endedBy,
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

const STRIP_MAX = 12

/** outcomeStrip reads a session row's `outcomes` (one word per run,
 * oldest first) as one dot per turn: a park that was decided ("resumed")
 * is folded into the run that continued it. */
export function outcomeStrip(
  s: Pick<SessionRow, "turns" | "outcomes">,
  max = STRIP_MAX
): OutcomeStrip {
  const words = s.outcomes ?? []
  const turns = words.filter(
    (w, i) => w !== "resumed" || i === words.length - 1
  )
  const all = turns.map(outcomeStatus)
  const dots = all.slice(-max)
  // `turns` is the server's count of turns (a resume is not one).
  const total = s.turns
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
