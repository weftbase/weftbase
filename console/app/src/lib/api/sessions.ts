// Reading sessions: the list (with its search, which also finds a
// session by the app's public id or by its own id) and the conversation
// (paged by turn; the page opens on the newest turns and older ones are
// read on demand).

import { api } from "./http"
import type {
  Conversation,
  ConversationTurn,
  SessionEntry,
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
    totalTurns: last.total_turns,
    turns: pages.flatMap((p) => p.turns),
    sessionMarks: last.session_marks ?? [],
    marksCapped: pages.some((p) => p.marks_capped),
  }
}

/** loadConversation reads the turns from `from` to the newest. "tail"
 * is the newest TURNS_PAGE turns: one request when the session has no
 * more than that, two otherwise (the first one says how many there are). */
export async function loadConversation(
  id: string,
  from: number | "tail"
): Promise<ConversationView> {
  let offset = from === "tail" ? 0 : from
  let page = await api.conversation(id, { offset, limit: TURNS_PAGE })
  if (from === "tail" && page.next_offset !== null) {
    offset = Math.max(0, page.total_turns - TURNS_PAGE)
    page = await api.conversation(id, { offset, limit: TURNS_PAGE })
  }
  const pages = [page]
  while (page.next_offset !== null && page.turns.length > 0) {
    page = await api.conversation(id, { offset: page.next_offset, limit: 100 })
    pages.push(page)
  }
  return viewOf(pages)
}
