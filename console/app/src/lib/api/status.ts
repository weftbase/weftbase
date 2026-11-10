// The status of a run as the Console shows it. The ONE place that maps
// the server's fields onto a display status; nothing else reads
// `status`, `pending` or `error_type` to decide what a run is.
//
// A trace-store run row's `status` is one of four words: running,
// succeeded, failed, interrupted. A parked run reads status "succeeded"
// with stopped_by "parked" and pending > 0; a cancelled one status
// "failed" with error_type "cancelled" (the rule behind the server's own
// ?status=parked and ?status=cancelled filters, obsdb/db.go). The ledger
// (GET /v1/runs/{id}/status) has its own words: queued, running, parked,
// succeeded, failed, canceled.
//
// The server is gaining a `display_status` field on run rows. When it is
// present it wins; the derivation below is the fallback for a server
// that does not send it yet.

import type { LedgerRun, RunRow, RunStatusFilter, SessionRow } from "./types"

export type DisplayStatus =
  | "queued"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "interrupted"

/** Every display status, in the order filters list them. */
export const DISPLAY_STATUSES: readonly DisplayStatus[] = [
  "running",
  "waiting",
  "failed",
  "succeeded",
  "cancelled",
  "interrupted",
  "queued",
]

const isDisplay = (v: unknown): v is DisplayStatus =>
  typeof v === "string" && (DISPLAY_STATUSES as readonly string[]).includes(v)

type Statused = Pick<RunRow, "status"> &
  Partial<
    Pick<
      RunRow,
      "display_status" | "pending" | "resumed_by" | "error_type" | "stopped_by"
    >
  >

/** displayStatus is the status to show for a run row. */
export function displayStatus(r: Statused): DisplayStatus {
  if (isDisplay(r.display_status)) return r.display_status
  if (r.status === "succeeded" && (r.pending ?? 0) > 0 && !r.resumed_by)
    return "waiting"
  if (r.status === "failed" && r.error_type === "cancelled") return "cancelled"
  return r.status
}

/** ledgerStatus is the same, for a ledger row (admit, cancel, resume). */
export function ledgerStatus(r: Pick<LedgerRun, "status">): DisplayStatus {
  if (r.status === "parked") return "waiting"
  if (r.status === "canceled") return "cancelled"
  return r.status
}

/** sessionStatus: a session's status is its newest turn's; the server
 * already says "parked" there. */
export function sessionStatus(s: Pick<SessionRow, "status">): DisplayStatus {
  return s.status === "parked" ? "waiting" : s.status
}

export const STATUS_LABEL: Record<DisplayStatus, string> = {
  queued: "Queued",
  running: "Running",
  waiting: "Waiting",
  succeeded: "Succeeded",
  failed: "Failed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
}

/** What each status means, for a tooltip. */
export const STATUS_HINT: Record<DisplayStatus, string> = {
  queued: "The run is waiting for a worker.",
  running: "The run is reporting.",
  waiting: "A tool call is waiting on a person.",
  succeeded: "The run finished.",
  failed: "The run ended with an error.",
  cancelled: "The run was cancelled.",
  interrupted: "The run stopped reporting without an end.",
}

/** The statuses GET /v1/runs?status= can filter on (queued lives in the
 * ledger only), with the server's word for each. */
export const FILTERABLE: readonly Exclude<DisplayStatus, "queued">[] = [
  "running",
  "waiting",
  "failed",
  "succeeded",
  "cancelled",
  "interrupted",
]
export function statusFilter(s: DisplayStatus): RunStatusFilter | undefined {
  if (s === "queued") return undefined
  return s === "waiting" ? "parked" : s
}
