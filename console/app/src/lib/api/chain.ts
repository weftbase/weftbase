// A chain: one logical run across parks. A run that waits on a person
// ends ("parked"); the decision starts a second run that continues it.
// The Console shows the chain as ONE run with the waits between its
// runs, whichever run's id is in the address.
//
// The server does the stitching: the run carries `chain` (the runs, the
// totals, the waits) and GET /v1/runs/{id}/spans?tree=1&chain=1 answers
// with every run's spans. loadChain reads both into a ChainView; the
// run page and the waterfall take that and never look at resumed_from /
// resumed_by.

import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query"

import { api } from "./http"
import { displayStatus } from "./status"
import type { DisplayStatus } from "./status"
import type {
  Actor,
  Approval,
  CostFields,
  Hole,
  LogRow,
  LogsPage,
  PosEvent,
  RunDetail,
  RunRow,
  Span,
  Usage,
  WaitDoc,
} from "./types"

/** One wait on a person, in milliseconds since the epoch. */
export interface ChainWait {
  /** The run that parked. */
  run: string
  resumedBy?: string
  from: number
  /** null while the wait lasts. */
  to: number | null
  /** The calls waited on, as the server records them. */
  approvals: Approval[]
  /** Who decided, as the server names them (print with actorText). */
  decidedBy: Actor[]
}

export interface ChainTotals extends Pick<
  CostFields,
  "cost_usd" | "cost_known" | "cost_reason"
> {
  started: string
  finished: string | null
  /** First start to last end; null while the chain is open (the page
   * measures to now). */
  durationMs: number | null
  waitingMs: number
  steps: number
  usage: Usage
}

export interface ChainView {
  /** The run the address names. */
  run: RunDetail
  /** Every run of the chain, oldest first (one when nothing parked). */
  runs: RunDetail[]
  /** The newest run: its state is the chain's state. */
  last: RunDetail
  status: DisplayStatus
  spans: Span[]
  holes: Hole[]
  waits: ChainWait[]
  totals: ChainTotals
}

const ms = (t: string) => Date.parse(t)

export function waitOf(w: WaitDoc): ChainWait {
  return {
    run: w.run,
    resumedBy: w.resumed_by,
    from: ms(w.from),
    to: w.to ? ms(w.to) : null,
    approvals: w.approvals ?? [],
    decidedBy: w.decided_by ?? [],
  }
}

/** viewOf assembles a ChainView from what the server returned. */
export function viewOf(
  id: string,
  runs: RunDetail[],
  spans: Span[],
  holes: Hole[],
  waits: WaitDoc[]
): ChainView {
  const first = runs[0]
  const last = runs[runs.length - 1]
  const chain = (runs.find((r) => r.id === id) ?? last).chain
  const status = chain
    ? displayStatus({
        status: last.status,
        display_status: chain.display_status as DisplayStatus,
      })
    : displayStatus(last)
  const open =
    status === "running" || status === "waiting" || status === "queued"
  return {
    run: runs.find((r) => r.id === id) ?? last,
    runs,
    last,
    status,
    spans,
    holes,
    waits: waits.map(waitOf),
    totals: chain
      ? {
          started: chain.started,
          finished: chain.finished,
          durationMs: open ? null : chain.duration_ms,
          waitingMs: open
            ? waits.reduce((n, w) => n + (w.to ? ms(w.to) - ms(w.from) : 0), 0)
            : chain.waiting_ms,
          steps: chain.steps,
          usage: chain.usage,
          cost_usd: chain.cost_known ? chain.cost_usd : null,
          cost_known: chain.cost_known,
          cost_reason: chain.cost_known
            ? last.cost_reason
            : "one of this run's parts has no known price",
        }
      : {
          started: first.started,
          finished: last.finished,
          durationMs:
            open || !last.finished
              ? null
              : ms(last.finished) - ms(first.started),
          waitingMs: 0,
          steps: last.steps,
          usage: last.usage,
          cost_usd: last.cost_usd,
          cost_known: last.cost_known,
          cost_reason: last.cost_reason,
        },
  }
}

/** approvalsOf: every approval of the chain, once each, oldest first. */
export function approvalsOf(chain: ChainView): Approval[] {
  const byId = new Map<string, Approval>()
  for (const w of chain.waits) for (const a of w.approvals) byId.set(a.id, a)
  for (const r of chain.runs)
    for (const a of r.approvals ?? []) byId.set(a.id, a)
  return [...byId.values()].sort((a, b) =>
    a.requested_at < b.requested_at ? -1 : 1
  )
}

/** loadChain reads the run at `id` with the rest of its chain. */
export async function loadChain(id: string): Promise<ChainView> {
  const run = await api.run(id)
  const ids = run.chain?.runs.map((r) => r.id) ?? [id]
  const [doc, ...runs] = await Promise.all([
    // A queued run is in the ledger only: it has no spans to read yet.
    run.ledger_only
      ? Promise.resolve({ spans: [], holes: [], waits: null })
      : api.runTimeline(id),
    ...ids.map((rid) => (rid === id ? Promise.resolve(run) : api.run(rid))),
  ])
  return viewOf(
    id,
    runs,
    doc.spans,
    doc.holes ?? [],
    doc.waits ?? run.chain?.waits ?? []
  )
}

/** The chain of a run. Under ["run", id, …], so the run's stream keeps
 * it current (live.ts). */
export const chainQuery = (id: string) =>
  queryOptions({
    queryKey: ["run", id, "chain"],
    queryFn: () => loadChain(id),
  })

// ── Steps of a chain ────────────────────────────────────────────────
// Each run of a chain counts its steps from 0. For the reader the chain
// is one run, so its steps are numbered through: the resume's step 0
// follows the parked run's last.

/** chainStep: the step of the chain that is `step` of run `runId`;
 * undefined for a run that is not of the chain (a child run keeps its
 * own numbering). */
export function chainStep(
  chain: ChainView,
  runId: string | undefined,
  step: number | undefined
): number | undefined {
  if (step === undefined) return undefined
  let before = 0
  for (const r of chain.runs) {
    if (r.id === runId) return before + step
    before += r.steps
  }
  return undefined
}

/** locateStep: the run of the chain that holds the chain's step `n`,
 * and the step's number in that run. Past the end: the newest run. */
export function locateStep(
  chain: ChainView,
  n: number
): { run: RunDetail; step: number } {
  let before = 0
  for (const r of chain.runs) {
    if (n < before + r.steps) return { run: r, step: n - before }
    before += r.steps
  }
  const last = chain.last
  return { run: last, step: Math.max(0, n - (before - last.steps)) }
}

/** chainSteps: how many steps the chain has taken, over all its runs. */
export const chainSteps = (chain: ChainView) =>
  chain.runs.reduce((n, r) => n + r.steps, 0)

// ── The runs of a tree ──────────────────────────────────────────────

/** treeRunIds: every run of the chain and every run they started, at
 * any depth: the chain's own first, then the others in the order they
 * began. A descendant is known by its row (a direct child) or by its
 * spans (`tree=1` tags each span with its run). */
export function treeRunIds(chain: ChainView, extra: readonly RunRow[] = []) {
  const own = chain.runs.map((r) => r.id)
  const seen = new Set(own)
  const first = new Map<string, number>()
  const note = (id: string | undefined, t: string) => {
    if (!id || seen.has(id)) return
    const at = ms(t)
    const had = first.get(id)
    if (had === undefined || at < had) first.set(id, at)
  }
  for (const r of chain.runs) for (const c of r.children) note(c.id, c.started)
  for (const c of extra) note(c.id, c.started)
  for (const s of chain.spans) note(s.run_id, s.start)
  return [...own, ...[...first].sort((a, b) => a[1] - b[1]).map(([id]) => id)]
}

// ── Runs in flight ──────────────────────────────────────────────────
// A run reports a span when the call ends, and its root span last. So a
// child run that is still going is known by its row (the parent's
// `children`) and its calls in flight by its events, like the parent's.

export interface InFlight {
  /** The rows of the child runs that are going, at any depth. */
  runs: RunRow[]
  /** Their events, by run id. */
  events: Record<string, PosEvent[]>
}

const going = (r: RunRow) => {
  const s = displayStatus(r)
  return s === "running" || s === "queued"
}

/** runningChildren: the direct children of the chain that are going. */
export const runningChildren = (chain: ChainView) =>
  chain.runs.flatMap((r) => r.children).filter(going)

// One page holds a run's events for any run a person would watch live;
// a longer run shows the calls its newest 5,000 events leave open.
const EVENTS_LIMIT = 5000
const MAX_IN_FLIGHT = 12
const MAX_DEPTH = 3

/** loadInFlight reads the events of the child runs that are going, and
 * looks under each for the runs it started in turn. Two requests per
 * run in flight; nothing for one that is over. */
export async function loadInFlight(children: RunRow[]): Promise<InFlight> {
  const out: InFlight = { runs: [], events: {} }
  let level = children.filter(going)
  for (let depth = 0; depth < MAX_DEPTH && level.length > 0; depth++) {
    level = level.slice(0, Math.max(0, MAX_IN_FLIGHT - out.runs.length))
    const read = await Promise.all(
      level.map(async (row) => {
        const [events, detail] = await Promise.all([
          api.events(row.id, undefined, EVENTS_LIMIT).catch(() => undefined),
          api.run(row.id).catch(() => undefined),
        ])
        return { row, events: events?.events ?? [], detail }
      })
    )
    const next: RunRow[] = []
    for (const r of read) {
      out.runs.push(r.row)
      out.events[r.row.id] = r.events
      // Every run a child started, over or not: one whose root span has
      // not arrived still needs its row.
      for (const c of r.detail?.children ?? []) {
        if (going(c)) next.push(c)
        else out.runs.push(c)
      }
    }
    level = next
  }
  return out
}

/** The child runs in flight under the run at `id`. Under ["run", id, …]:
 * the runs stream refetches it when one of them moves (live.ts). */
export const inFlightQuery = (id: string, children: RunRow[]) =>
  queryOptions({
    queryKey: ["run", id, "in-flight", children.map((c) => c.id).join(" ")],
    queryFn: () => loadInFlight(children),
  })

// ── The logs of a tree ──────────────────────────────────────────────
// GET /v1/runs/{id}/logs answers for one run, a page at a time. The Logs
// tab shows every run of the tree in one time order, so the pages are
// read in rounds: a round reads the next page of the runs that are
// furthest behind, a few requests at a time. A "Load more" is one round.

export interface LogLine extends LogRow {
  /** The run that wrote the line. */
  run: string
}

/** Where the reading of a run's logs stands: the next index to read and
 * the time of the last line read (null: nothing read yet). A run read to
 * its end has no cursor. */
export type LogCursors = Record<string, { from: number; at: string | null }>

export interface LogsRound {
  lines: LogLine[]
  /** The runs that have more to read after this round. */
  more: LogCursors
  /** Why lines may be missing, as the server says it (the first run's
   * that says anything). */
  note?: Pick<LogsPage, "reason" | "fix">
  /** A run is still going: its newest lines arrive when its spans end. */
  partial?: string
  /** The first run whose logs could not be read. */
  failed?: unknown
}

export const LOGS_PAGE = 500
export const LOGS_RUNS_PER_ROUND = 12
const LOGS_AT_ONCE = 4

/** inBatches runs `fn` over `items`, at most `n` at a time. */
async function inBatches<T, TOut>(
  items: readonly T[],
  n: number,
  fn: (item: T) => Promise<TOut>
): Promise<TOut[]> {
  const out: TOut[] = []
  for (let i = 0; i < items.length; i += n)
    out.push(...(await Promise.all(items.slice(i, i + n).map(fn))))
  return out
}

/** loadLogsRound reads one more page of the runs in `cursors` that are
 * furthest behind (never read first, then the oldest last line). */
export async function loadLogsRound(
  cursors: LogCursors,
  severity?: string,
  limit = LOGS_PAGE
): Promise<LogsRound> {
  const order = Object.entries(cursors).sort(([, a], [, b]) =>
    a.at === b.at
      ? 0
      : a.at === null
        ? -1
        : b.at === null
          ? 1
          : a.at < b.at
            ? -1
            : 1
  )
  const now = order.slice(0, LOGS_RUNS_PER_ROUND)
  const out: LogsRound = {
    lines: [],
    more: Object.fromEntries(order.slice(LOGS_RUNS_PER_ROUND)),
  }
  const pages = await inBatches(now, LOGS_AT_ONCE, ([id, c]) =>
    api.logsFrom(id, { from: c.from || undefined, limit, severity }).then(
      (page) => ({ id, page, error: undefined }),
      (error: unknown) => ({ id, page: undefined, error })
    )
  )
  for (const { id, page, error } of pages) {
    if (!page) {
      out.failed ??= error
      continue
    }
    for (const l of page.logs) out.lines.push({ ...l, run: id })
    if (page.next_from !== undefined && page.logs.length > 0)
      out.more[id] = { from: page.next_from, at: page.logs.at(-1)!.time }
    if (page.reason && !out.note)
      out.note = { reason: page.reason, fix: page.fix }
    if (page.partial)
      out.partial ??= page.partial_reason ?? "The run is still going."
  }
  return out
}

/** The logs of the runs `ids`, read in rounds. NOT under ["run", id]:
 * the runs stream invalidates that at most once a second while a run
 * is going, and each invalidation would start every round again. The
 * Logs tab asks for a refetch itself when the run has moved. */
export const treeLogsQuery = (
  id: string,
  ids: readonly string[],
  severity?: string
) =>
  infiniteQueryOptions({
    queryKey: ["logs", id, ids.join(" "), severity ?? "all"],
    queryFn: ({ pageParam }) => loadLogsRound(pageParam, severity),
    initialPageParam: Object.fromEntries<LogCursors[string]>(
      ids.map((r) => [r, { from: 0, at: null }])
    ),
    getNextPageParam: (last) =>
      Object.keys(last.more).length > 0 ? last.more : undefined,
  })

/** logFrontier: lines after this time are not shown yet: a run with
 * more to read may still have lines before them. Infinity when every
 * run is read to its end; -Infinity while one has not been read at all. */
export function logFrontier(more: LogCursors | undefined): number {
  let t = Infinity
  for (const c of Object.values(more ?? {}))
    t = Math.min(t, c.at === null ? -Infinity : ms(c.at))
  return t
}
