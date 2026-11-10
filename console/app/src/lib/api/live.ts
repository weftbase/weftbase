// The streams wired into TanStack Query. A screen mounts one hook and its
// queries stay current; nothing polls.
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

import { streamEvents, streamRun, streamRuns, streamSession } from "./stream"
import type { StreamState } from "./stream"
import type {
  OutboxEvent,
  RunDetail,
  RunRow,
  RunsList,
  StreamFrame,
} from "./types"

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

function useStream(
  open: (onState: (s: StreamState) => void) => () => void,
  deps: React.DependencyList
): StreamState {
  const [state, setState] = React.useState<StreamState>("connecting")
  React.useEffect(() => open(setState), deps)
  return state
}

/** useRunsLive keeps every runs list, count and the stats current. Mount
 * it on a screen that lists runs (the shell mounts it for the nav count). */
export function useRunsLive(): StreamState {
  const qc = useQueryClient()
  return useStream(
    (onState) => {
      const t = throttled(qc)
      const close = streamRuns(
        {
          onState,
          onFrame(f) {
            if (f.type !== "run") return
            const row = f.data.run
            const known = patchRun(qc, row)
            // A new top-level run belongs at the top of some lists and in
            // the counts; a known one may have changed status (it may now
            // match, or stop matching, a filtered list).
            if (!known && row.parent_run_id !== "") return
            t.invalidate(["runs"], ["stats"], ["sessions"])
          },
          onReopen(resumed) {
            if (!resumed) t.invalidate(["runs"], ["stats"], ["sessions"])
          },
        },
        { kinds: ["run"] }
      )
      return () => {
        t.stop()
        close()
      }
    },
    [qc]
  )
}

/** useRunLive keeps one run current: its row is patched, and its spans,
 * events, transcript, steps and logs refetch as records arrive. onFrame
 * gets every frame as well (the waterfall appends rows from it). */
export function useRunLive(
  id: string,
  opts: { enabled?: boolean; onFrame?: (f: StreamFrame) => void } = {}
): StreamState {
  const qc = useQueryClient()
  const enabled = opts.enabled ?? true
  const onFrame = React.useRef(opts.onFrame)
  onFrame.current = opts.onFrame
  return useStream(
    (onState) => {
      if (!enabled) return () => {}
      const t = throttled(qc)
      const close = streamRun(id, {
        onState,
        onFrame(f) {
          onFrame.current?.(f)
          if (f.type === "run") {
            const row = f.data.run
            patchRun(qc, row)
            // A child's row changes the parent's children and totals.
            if (row.id !== id) t.invalidate(["run", id])
            return
          }
          t.invalidate(["run", id])
        },
        onReopen(resumed) {
          if (!resumed) t.invalidate(["run", id])
        },
      })
      return () => {
        t.stop()
        close()
      }
    },
    [qc, id, enabled]
  )
}

/** useSessionLive keeps one session current: its turns' rows are patched
 * and the session, its timeline and entries refetch as records arrive. */
export function useSessionLive(
  id: string,
  opts: { enabled?: boolean; onFrame?: (f: StreamFrame) => void } = {}
): StreamState {
  const qc = useQueryClient()
  const enabled = opts.enabled ?? true
  const onFrame = React.useRef(opts.onFrame)
  onFrame.current = opts.onFrame
  return useStream(
    (onState) => {
      if (!enabled) return () => {}
      const t = throttled(qc)
      const close = streamSession(id, {
        onState,
        onFrame(f) {
          onFrame.current?.(f)
          if (f.type === "run") patchRun(qc, f.data.run)
          t.invalidate(["session", id])
        },
        onReopen(resumed) {
          if (!resumed) t.invalidate(["session", id])
        },
      })
      return () => {
        t.stop()
        close()
      }
    },
    [qc, id, enabled]
  )
}

const APPROVAL_EVENTS = ["approval.requested", "approval.decided"] as const

/** useApprovalsLive keeps the approvals inbox and its count current from
 * the outbox stream. onRequested is called for an approval asked for
 * while this page is open (the stream first replays the stored events;
 * those are not news). Mounted once by the shell. */
export function useApprovalsLive(
  onRequested?: (event: OutboxEvent) => void
): StreamState {
  const qc = useQueryClient()
  const cb = React.useRef(onRequested)
  cb.current = onRequested
  return useStream(
    (onState) => {
      const t = throttled(qc)
      const opened = Date.now()
      const close = streamEvents(APPROVAL_EVENTS, {
        onState,
        onEvent(e) {
          const at = Date.parse(e.created_at)
          // The replay of what is already stored changes nothing on screen.
          if (!(at >= opened - 5000)) return
          t.invalidate(["approvals"], ["runs"])
          if (e.type === "approval.requested") cb.current?.(e)
        },
        onReopen(resumed) {
          if (!resumed) t.invalidate(["approvals"])
        },
      })
      return () => {
        t.stop()
        close()
      }
    },
    [qc]
  )
}
