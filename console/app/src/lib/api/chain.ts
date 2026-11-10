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

import { queryOptions } from "@tanstack/react-query"

import { api } from "./http"
import { displayStatus } from "./status"
import type { DisplayStatus } from "./status"
import type {
  Approval,
  CostFields,
  Hole,
  RunDetail,
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
  /** Who decided: "operator op_…", "key key_…". */
  decidedBy: string[]
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
    decidedBy: (w.decided_by ?? []).map((a) => `${a.kind} ${a.id}`),
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
