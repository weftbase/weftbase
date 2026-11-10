// Reading sessions: the list (with its search, which also finds a
// session by the app's public id or by its own id) and the conversation
// (paged by turn; the page opens on the newest turns and older ones are
// read on demand; a refetch reads the newest turns only).

import { api } from "./http"
import type {
  Conversation,
  ConversationTurn,
  SessionEntry,
  SessionFacts,
  SessionRow,
  SessionsPage,
  SessionsQuery,
} from "./types"

/** What the Sessions list can filter on. */
export interface SessionsFilter {
  q?: string
  state?: "running" | "waiting" | "failed" | "succeeded" | "interrupted"
  agent?: string
}

export const SESSIONS_PAGE = 50

/** toSessionsQuery maps the list's filters onto GET /v1/sessions.
 * "waiting" is the server's awaiting=1 (a turn waits on a person) and
 * "failed" its failed=1 (any turn failed, not only the newest). */
export function toSessionsQuery(f: SessionsFilter): SessionsQuery {
  return {
    q: f.q,
    agent: f.agent,
    awaiting: f.state === "waiting" ? true : undefined,
    failed: f.state === "failed" ? true : undefined,
    status:
      f.state && f.state !== "waiting" && f.state !== "failed"
        ? f.state
        : undefined,
    limit: SESSIONS_PAGE,
  }
}

/** An id typed whole: one word, no spaces. */
const oneWord = (q: string | undefined): q is string => !!q && !/\s/.test(q)

/** loadSessions reads one page of the list. On the first page of a
 * one-word search it also asks for the session whose public id is that
 * word and for the sessions whose own id starts with it, and puts those
 * first: pasting either id finds the session. */
export async function loadSessions(
  f: SessionsFilter,
  cursor?: { before: string; before_id?: string }
): Promise<SessionsPage> {
  const query = toSessionsQuery(f)
  if (cursor || !oneWord(f.q)) return api.sessions({ ...query, ...cursor })
  const byId = { ...query, q: undefined, limit: 5 }
  const [page, pub, own] = await Promise.all([
    api.sessions(query),
    api.sessions({ ...byId, public_id: f.q }),
    api.sessions({ ...byId, id_prefix: f.q }),
  ])
  return mergeSessions(page, [...pub.sessions, ...own.sessions])
}

/** mergeSessions puts `first` at the head of a page, each session once. */
export function mergeSessions(
  page: SessionsPage,
  first: readonly SessionRow[]
): SessionsPage {
  const seen = new Set<string>()
  const sessions: SessionRow[] = []
  for (const s of [...first, ...page.sessions]) {
    if (seen.has(s.id)) continue
    seen.add(s.id)
    sessions.push(s)
  }
  return {
    ...page,
    sessions,
    total: page.total + (sessions.length - page.sessions.length),
  }
}

// ── The conversation ────────────────────────────────────────────────

export const TURNS_PAGE = 20

/** The turns from `offset` to the newest, as one document. */
export interface ConversationView {
  sessionId: string
  publicId?: string
  agent: string
  title?: string
  status: Conversation["status"]
  /** The first loaded turn's index; turns before it are not loaded. */
  offset: number
  /** Where the live window starts: a refetch reads the turns from here
   * on; the loaded turns before it are finished and kept as read. */
  tailOffset: number
  /** The session's own row (totals, cost, times, lineage); absent from
   * a server that does not send it. */
  session?: SessionFacts
  /** The sessions this one handed work to. */
  delegated: SessionFacts[]
  totalTurns: number
  turns: ConversationTurn[]
  sessionMarks: SessionEntry[]
  marksCapped: boolean
}

function viewOf(pages: Conversation[]): ConversationView {
  const first = pages[0]
  const last = pages[pages.length - 1]
  return {
    sessionId: first.session_id,
    publicId: first.public_id || undefined,
    agent: last.agent,
    title: last.title,
    status: last.status,
    offset: first.offset,
    // The newest page as it was read: the last one's first turn.
    tailOffset: last.offset,
    session: last.session,
    delegated: last.delegated ?? [],
    totalTurns: last.total_turns,
    turns: pages.flatMap((p) => p.turns),
    sessionMarks: last.session_marks ?? [],
    marksCapped: pages.some((p) => p.marks_capped),
  }
}

/** loadConversation reads the turns from `from` to the newest. "tail"
 * is the newest TURNS_PAGE turns, in one request (`tail=1`). */
export async function loadConversation(
  id: string,
  from: number | "tail"
): Promise<ConversationView> {
  if (from === "tail")
    return viewOf([await api.conversation(id, { tail: true })])
  let page = await api.conversation(id, { offset: from, limit: TURNS_PAGE })
  const pages = [page]
  while (page.next_offset !== null && page.turns.length > 0) {
    page = await api.conversation(id, { offset: page.next_offset, limit: 100 })
    pages.push(page)
  }
  return viewOf(pages)
}

// ── The windowed conversation ───────────────────────────────────────
//
// A long session is not read again from its first loaded turn each time
// something moves. The view is one list with a live window at its end
// (the newest TURNS_PAGE turns, where turns still change): a refetch
// reads that page alone (`tail=1`) and replaces that part; what was
// loaded before it (turns that left the window, and the "earlier turns"
// pages) stays as it was read.

/** mergeTail puts freshly read pages (from some turn to the newest) at
 * the end of what is loaded: `prev`'s turns before the pages' first turn
 * are kept as they are, the rest is the pages'. Without `prev`, or when
 * the pages do not join it (they start after its end, or before its
 * start), the view is the pages alone. */
export function mergeTail(
  prev: ConversationView | undefined,
  pages: Conversation[]
): ConversationView {
  const fresh = viewOf(pages)
  if (
    !prev ||
    fresh.offset < prev.offset ||
    fresh.offset > prev.offset + prev.turns.length
  )
    return fresh
  return {
    ...fresh,
    publicId: fresh.publicId ?? prev.publicId,
    offset: prev.offset,
    turns: [...prev.turns.slice(0, fresh.offset - prev.offset), ...fresh.turns],
    marksCapped: fresh.marksCapped,
  }
}

/** mergeEarlier puts pages of earlier turns at the top of the view: the
 * turns before the view's first one, each once. Pages that do not reach
 * the view's first turn (a hole would open) change nothing. */
export function mergeEarlier(
  view: ConversationView,
  pages: Conversation[]
): ConversationView {
  const first = pages.at(0)
  if (!first || first.offset >= view.offset) return view
  const earlier: ConversationTurn[] = []
  for (const p of pages)
    p.turns.forEach((t, i) => {
      // Each index once, in order, up to the view's first turn.
      if (p.offset + i === first.offset + earlier.length) earlier.push(t)
    })
  const upTo = view.offset - first.offset
  if (earlier.length < upTo) return view
  return {
    ...view,
    offset: first.offset,
    turns: [...earlier.slice(0, upTo), ...view.turns],
    marksCapped: view.marksCapped || pages.some((p) => p.marks_capped),
  }
}

/** refreshConversation is the windowed read: always one request, the
 * newest page (`tail=1`), merged into what `cached()` holds when the
 * answer is in (so earlier turns loaded meanwhile are not lost). A
 * session that shrank (turns deleted), or one that grew by more than a
 * page since the last read, is the newest page alone again. */
export async function refreshConversation(
  id: string,
  cached: () => ConversationView | undefined
): Promise<ConversationView> {
  const page = await api.conversation(id, { tail: true })
  const prev = cached()
  return mergeTail(
    prev && page.total_turns >= prev.totalTurns ? prev : undefined,
    [page]
  )
}

/** loadEarlier reads the turns [from, to): the pages "earlier turns"
 * adds at the top (mergeEarlier). They are read once and never again. */
export async function loadEarlier(
  id: string,
  from: number,
  to: number
): Promise<Conversation[]> {
  const pages: Conversation[] = []
  let offset = Math.max(0, from)
  while (offset < to) {
    const page = await api.conversation(id, {
      offset,
      limit: Math.min(100, to - offset),
    })
    pages.push(page)
    if (page.turns.length === 0) break
    offset = page.offset + page.turns.length
  }
  return pages
}
