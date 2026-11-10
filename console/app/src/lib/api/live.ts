// The streams wired into TanStack Query. A screen mounts one hook and its
// queries stay current; nothing polls. No hook opens a connection: they
// all listen to the two streams hub.ts keeps for the whole app.
//
// Pattern (CONVENTIONS.md, "Queries and streams"):
// - a `run` frame carries the whole row, so it is patched straight into
//   every cached list and into ["run", id];
// - a list is refetched only when what it holds may have changed: a run
//   appeared or changed status (it may now match, or stop matching, a
//   filter). A step of a run already listed is a patch, not a refetch.
//   At most once every LIST_MS;
// - the stats are aggregates over a window: refetched at most once every
//   STATS_MS while runs move, and when the window gets the focus back;
// - a run page's or session page's own data is invalidated at most once
//   per second while its runs move;
// - the outbox's run events say what the trace store never sees (a run
//   queued, then cancelled or failed by its TTL; a run another client
//   admitted): they refresh the lists, the stats and that run's page;
// - when a stream reopens without a resume, everything it feeds is
//   invalidated once.
// Filter options (q.options) are not live: they read a 30-day window
// and change rarely.

import { useQueryClient } from "@tanstack/react-query"
import type { InfiniteData, QueryClient } from "@tanstack/react-query"
import * as React from "react"

import { subscribeApprovals, subscribeRunEvents, subscribeRuns } from "./hub"
import { forget } from "./queries"
import { displayStatus } from "./status"
import type { StreamState } from "./stream"
import type { OutboxEvent, RunDetail, RunRow, RunsList } from "./types"

type Key = readonly unknown[]

/** How often a runs list or the sessions list may refetch while runs
 * move. */
export const LIST_MS = 5000
/** How often the stats may refetch while runs move. */
export const STATS_MS = 30_000

/** prefixOf: does `key` start with `prefix` (TanStack's partial match
 * for arrays of plain values and objects)? */
const startsWith = (key: Key, prefix: Key) =>
  prefix.every((p, i) => JSON.stringify(key[i]) === JSON.stringify(p))

/** throttled collects what to invalidate and does it at most once per
 * `ms` (trailing), in one pass, so a query several reasons name is
 * refetched once; until `stop`. */
function throttled(qc: QueryClient, ms = 1000) {
  let matchers: ((key: Key) => boolean)[] = []
  let timer: ReturnType<typeof setTimeout> | undefined
  const flush = () => {
    timer = undefined
    const now = matchers
    matchers = []
    void qc.invalidateQueries({
      predicate: (q) => now.some((m) => m(q.queryKey)),
    })
  }
  const add = (m: (key: Key) => boolean) => {
    matchers.push(m)
    timer ??= setTimeout(flush, ms)
  }
  return {
    invalidate(...keys: Key[]) {
      for (const k of keys) add((key) => startsWith(key, k))
    },
    /** Invalidate the queries `predicate` picks. */
    invalidateWhere(predicate: (key: Key) => boolean) {
      add(predicate)
    },
    stop() {
      if (timer) clearTimeout(timer)
      matchers = []
    },
  }
}

const swap = (runs: RunRow[], row: RunRow): RunRow[] | undefined => {
  const i = runs.findIndex((r) => r.id === row.id)
  if (i < 0) return undefined
  const next = runs.slice()
  next[i] = { ...runs[i], ...row }
  return next
}

/** patchRun writes a run row into every cached list that already shows
 * it, and into the run's own entry. It answers whether any list had it. */
export function patchRun(qc: QueryClient, row: RunRow): boolean {
  let found = false
  qc.setQueriesData<InfiniteData<RunsList> | RunsList>(
    { queryKey: ["runs"] },
    (data) => {
      if (!data) return data
      if ("pages" in data) {
        let hit = false
        const pages = data.pages.map((p) => {
          const runs = swap(p.runs, row)
          if (!runs) return p
          hit = true
          return { ...p, runs }
        })
        if (!hit) return data
        found = true
        return { ...data, pages }
      }
      const runs = swap(data.runs, row)
      if (!runs) return data
      found = true
      return { ...data, runs }
    }
  )
  qc.setQueryData<RunDetail>(["run", row.id], (d) => (d ? { ...d, ...row } : d))
  return found
}

function useHub(
  open: (onState: (s: StreamState) => void) => () => void,
  deps: React.DependencyList
): StreamState {
  const [state, setState] = React.useState<StreamState>("connecting")
  React.useEffect(() => open(setState), deps)
  return state
}

/** moves remembers the status each run was last seen in and answers
 * whether a row is news: a run not seen yet, or a new status. Bounded:
 * the oldest runs are forgotten. */
export function movesTracker(max = 4096) {
  const last = new Map<string, string>()
  return (id: string, status: string): boolean => {
    const was = last.get(id)
    if (was === status) return false
    last.delete(id)
    last.set(id, status)
    if (last.size > max)
      for (const old of last.keys()) {
        last.delete(old)
        if (last.size <= max / 2) break
      }
    return true
  }
}

/** The status a ledger event moves its run to, in display words. */
const EVENT_STATUS: Record<string, string> = {
  "run.queued": "queued",
  "run.started": "running",
  "run.parked": "waiting",
  "run.finished": "succeeded",
  "run.failed": "failed",
  "run.cancelled": "cancelled",
}

/** listedLedgerOnly: does a cached runs list show the run as a row the
 * trace store does not hold (ledger_only: queued, or cancelled before
 * it started)? No frame will ever patch such a row. */
function listedLedgerOnly(qc: QueryClient, id: string): boolean {
  const has = (runs: RunRow[]) =>
    runs.some((r) => r.id === id && r.ledger_only === true)
  return qc
    .getQueriesData<InfiniteData<RunsList> | RunsList>({ queryKey: ["runs"] })
    .some(([, d]) =>
      !d ? false : "pages" in d ? d.pages.some((p) => has(p.runs)) : has(d.runs)
    )
}

/** reshuffles: the runs lists a run that changed status to `status` may
 * have entered or left. A list without a status filter keeps its rows
 * (the patch shows the new status): only filtered lists and the
 * overview's failed runs (on a failure) can change. (Its expired
 * approvals change with an approval event: useApprovalsLive.) */
export function reshuffles(key: Key, status: string): boolean {
  if (key[0] !== "runs") return false
  if (key[1] === "failed") return status === "failed"
  if (key[1] === "expired") return false
  const q = key[2] as { display_status?: string; approvals?: string } | null
  return !!q && typeof q === "object" && !!(q.display_status || q.approvals)
}

/** approvalMoves: the runs lists an approval event can change by
 * itself: those filtered on approvals (a request makes a pending one,
 * a decision or an expiry ends it), and the overview's expired ones on
 * an expiry. */
export function approvalMoves(key: Key, type: string): boolean {
  if (key[0] !== "runs") return false
  if (key[1] === "expired") return type === "approval.expired"
  const f = (key[2] as { approvals?: string } | null)?.approvals
  if (!f) return false
  return type === "approval.requested" ? f === "pending" : true
}

/** takesNew: the runs lists a run seen for the first time may belong
 * in: every list and page of runs (it may go on top), but the
 * overview's failed runs and expired approvals only when it is one. */
export function takesNew(key: Key, status: string): boolean {
  if (key[0] !== "runs") return false
  return key[1] === "list" || key[1] === "page" || reshuffles(key, status)
}

/** useRunsLive keeps every runs list, count and the stats current, and
 * every cached run's row. Mounted once, by the shell (read its state
 * with `useLiveState()`). */
export function useRunsLive(): StreamState {
  const qc = useQueryClient()
  return useHub(
    (onState) => {
      const lists = throttled(qc, LIST_MS)
      const stats = throttled(qc, STATS_MS)
      const news = movesTracker()
      const all = () => {
        lists.invalidate(["runs"], ["sessions"])
        stats.invalidate(["stats"])
      }
      const closeRuns = subscribeRuns({
        onState,
        onData({ run: row }) {
          const known = patchRun(qc, row)
          // A child run is in no list but a known one (subagent runs).
          if (!known && row.parent_run_id !== "") return
          // A step of a run is in the patch; a new run, or a run whose
          // status changed, may enter or leave a list and moves a count.
          const status = displayStatus(row)
          if (!news(row.id, status)) return
          lists.invalidateWhere((k) =>
            known ? reshuffles(k, status) : takesNew(k, status)
          )
          if (row.session_id) lists.invalidate(["sessions"])
          stats.invalidate(["stats"])
        },
        onStale: all,
      })
      // The run row arrives once the trace store hears of a run; the
      // ledger knows earlier (queued) and of runs that never start.
      const closeEvents = subscribeRunEvents({
        onData(e) {
          if (e.type === "run.deleted" || e.type === "session.deleted") {
            forgetDeleted(qc, e)
            return
          }
          const id = typeof e.data?.run_id === "string" ? e.data.run_id : ""
          const status = EVENT_STATUS[e.type]
          // A row a list shows whose trace may never come (queued, then
          // cancelled or failed by its TTL): no frame will patch it, so
          // the lists are read again. A run with a trace gets its frames.
          if (id && listedLedgerOnly(qc, id)) {
            all()
            return
          }
          if (!status) return
          if (status === "queued") {
            lists.invalidateWhere((k) => takesNew(k, status))
            lists.invalidate(["sessions"])
            stats.invalidate(["stats"])
            return
          }
          lists.invalidateWhere((k) => reshuffles(k, status))
          stats.invalidate(["stats"])
        },
        onStale: all,
      })
      return () => {
        lists.stop()
        stats.stop()
        closeRuns()
        closeEvents()
      }
    },
    [qc]
  )
}

/** rootOf: the top-level run a (child) run id belongs to. A child's id
 * is `<parent>/<step>/<call>`. */
const rootOf = (id: string) => {
  const i = id.indexOf("/")
  return i < 0 ? id : id.slice(0, i)
}

/** useRunLive keeps one run current: whenever it, one of its child
 * runs or the run that continues it moves, everything under
 * `["run", key]` for each of `keys` refetches (at most once a second).
 * `id` is the run to follow; `keys` default to it. */
export function useRunLive(
  id: string,
  opts: { enabled?: boolean; keys?: readonly string[] } = {}
): StreamState {
  const qc = useQueryClient()
  const enabled = opts.enabled ?? true
  const keys = (opts.keys ?? [id]).join("\u0000")
  return useHub(
    (onState) => {
      if (!enabled) return () => {}
      const t = throttled(qc)
      const stale = () =>
        t.invalidate(...keys.split("\u0000").map((k) => ["run", k]))
      const close = subscribeRuns({
        onState,
        onData({ run: row }) {
          if (rootOf(row.id) === id || row.resumed_from === id) stale()
        },
        onStale: stale,
      })
      // A transition the ledger alone knows (queued, cancelled before it
      // started, failed by its queue TTL), of this run or its chain.
      const mine = new Set(keys.split("\u0000").concat(id))
      const closeEvents = subscribeRunEvents({
        onData(e) {
          const d = e.data ?? {}
          if (
            [e.resource_id, d.run_id, d.chain_run_id, d.resume_of].some(
              (v) => typeof v === "string" && mine.has(v)
            )
          )
            stale()
        },
        onStale: stale,
      })
      return () => {
        t.stop()
        close()
        closeEvents()
      }
    },
    [qc, id, enabled, keys]
  )
}

/** useOutboxState: the state of the outbox stream (GET /v1/events),
 * for a page that falls back to a slow timer while it is down. */
export function useOutboxState(): StreamState {
  return useHub((onState) => subscribeRunEvents({ onState, onData() {} }), [])
}

/** forgetDeleted: a run or session deleted (here or elsewhere) leaves
 * no cache behind (run.deleted / session.deleted, which name what went). */
function forgetDeleted(qc: QueryClient, e: OutboxEvent) {
  const d = e.data ?? {}
  const ids = Array.isArray(d.run_ids)
    ? d.run_ids.filter((x): x is string => typeof x === "string")
    : []
  const session = typeof d.session_id === "string" ? d.session_id : undefined
  if (e.type === "session.deleted" && session) forget(qc, { session })
  else forget(qc, { runs: ids, session })
}

/** useSessionLive keeps one session current: `["session", id, …]`
 * refetches (at most once a second) whenever one of its runs, or a
 * child of one, moves, and on a ledger event of one of its runs (a
 * queued turn cancelled, or failed by its TTL, never has a trace). */
export function useSessionLive(id: string): StreamState {
  const qc = useQueryClient()
  return useHub(
    (onState) => {
      const t = throttled(qc)
      // A child run's row may not name the session: it is known by its
      // parent's id.
      const mine = new Set<string>()
      const close = subscribeRuns({
        onState,
        onData({ run: row }) {
          if (row.session_id === id) mine.add(rootOf(row.id))
          else if (!mine.has(rootOf(row.id))) return
          t.invalidate(["session", id])
        },
        onStale: () => t.invalidate(["session", id]),
      })
      const closeEvents = subscribeRunEvents({
        onData(e) {
          if (e.type === "session.deleted") return // forgetDeleted
          if (e.data?.session_id === id) t.invalidate(["session", id])
        },
        onStale: () => t.invalidate(["session", id]),
      })
      return () => {
        t.stop()
        close()
        closeEvents()
      }
    },
    [qc, id]
  )
}

/** useApprovalsLive keeps the approvals inbox and its count current from
 * the outbox stream. onRequested is called for an approval asked for
 * while this page is open (the stream starts at the tail). Mounted once
 * by the shell. */
export function useApprovalsLive(
  onRequested?: (event: OutboxEvent) => void
): StreamState {
  const qc = useQueryClient()
  const cb = React.useRef(onRequested)
  cb.current = onRequested
  return useHub(
    (onState) => {
      const t = throttled(qc)
      const close = subscribeApprovals({
        onState,
        onData(e) {
          // A decision moves the run (its frames patch the lists); what
          // an approval changes by itself is the inbox, the lists
          // filtered on approvals and the overview's expired ones.
          t.invalidate(["approvals"])
          t.invalidateWhere((k) => approvalMoves(k, e.type))
          if (e.type === "approval.requested") cb.current?.(e)
        },
        onStale: () => t.invalidate(["approvals"]),
      })
      return () => {
        t.stop()
        close()
      }
    },
    [qc]
  )
}
