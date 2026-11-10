// The streams wired into TanStack Query. A screen mounts one hook and its
// queries stay current; nothing polls. No hook opens a connection: they
// all listen to the two streams hub.ts keeps for the whole app.
//
// Pattern (CONVENTIONS.md, "Queries and streams"):
// - a `run` frame carries the whole row, so it is patched straight into
//   every cached list and into ["run", id];
// - anything a row cannot patch (a new row's place in a filtered list,
//   the stats, a run's spans and events) is invalidated, at most once per
//   second, and TanStack Query refetches what is on screen;
// - when a stream reopens without a resume, everything it feeds is
//   invalidated once.

import { useQueryClient } from "@tanstack/react-query"
import type { InfiniteData, QueryClient } from "@tanstack/react-query"
import * as React from "react"

import { subscribeApprovals, subscribeRuns } from "./hub"
import type { StreamState } from "./stream"
import type { OutboxEvent, RunDetail, RunRow, RunsList } from "./types"

type Key = readonly unknown[]

/** throttled returns a function that invalidates the keys it was given,
 * at most once per `ms` (trailing), until `stop`. */
function throttled(qc: QueryClient, ms = 1000) {
  const pending = new Map<string, Key>()
  let timer: ReturnType<typeof setTimeout> | undefined
  const flush = () => {
    timer = undefined
    for (const key of pending.values())
      void qc.invalidateQueries({ queryKey: key })
    pending.clear()
  }
  return {
    invalidate(...keys: Key[]) {
      for (const k of keys) pending.set(JSON.stringify(k), k)
      timer ??= setTimeout(flush, ms)
    },
    stop() {
      if (timer) clearTimeout(timer)
      pending.clear()
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

/** useRunsLive keeps every runs list, count and the stats current, and
 * every cached run's row. Mounted once, by the shell (read its state
 * with `useLiveState()`). */
export function useRunsLive(): StreamState {
  const qc = useQueryClient()
  return useHub(
    (onState) => {
      const t = throttled(qc)
      const close = subscribeRuns({
        onState,
        onData({ run: row }) {
          const known = patchRun(qc, row)
          // A new top-level run belongs at the top of some lists and in
          // the counts; a known one may have changed status (it may now
          // match, or stop matching, a filtered list).
          if (!known && row.parent_run_id !== "") return
          t.invalidate(["runs"], ["stats"], ["sessions"])
        },
        onStale: () => t.invalidate(["runs"], ["stats"], ["sessions"]),
      })
      return () => {
        t.stop()
        close()
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
      return () => {
        t.stop()
        close()
      }
    },
    [qc, id, enabled, keys]
  )
}

/** useSessionLive keeps one session current: `["session", id, …]`
 * refetches (at most once a second) whenever one of its runs, or a
 * child of one, moves. */
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
      return () => {
        t.stop()
        close()
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
          t.invalidate(["approvals"], ["runs"])
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
