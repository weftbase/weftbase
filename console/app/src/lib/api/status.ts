// The status of a run as the Console shows it: the server's
// `display_status` (queued, running, waiting, succeeded, failed,
// cancelled, interrupted), which every run row carries. Nothing in the
// Console derives a status from `status`, `pending` or `error_type`.
// A row without it (a golden older than the field) reads its trace
// status word; an unknown word reads interrupted.

import type { RunRow, SessionRow } from "./types"

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

/** displayStatus is the status to show for a run row. */
export function displayStatus(
  r: Pick<RunRow, "status"> & Partial<Pick<RunRow, "display_status">>
): DisplayStatus {
  if (isDisplay(r.display_status)) return r.display_status
  return isDisplay(r.status) ? r.status : "interrupted"
}

/** sessionStatus: a session's status is its newest turn's outcome, in
 * the server's words for a turn (outcomeStatus). */
export function sessionStatus(s: Pick<SessionRow, "status">): DisplayStatus {
  return outcomeStatus(s.status)
}

/** outcomeStatus maps one turn outcome of a session row (`status`,
 * `outcomes[]`) onto a display status: parked is waiting on a person, a
 * park already decided is running again, and a turn a newer message
 * ended reads cancelled. An unknown word reads interrupted. */
export function outcomeStatus(outcome: string): DisplayStatus {
  if (outcome === "parked") return "waiting"
  if (outcome === "resumed") return "running"
  if (outcome === "ended") return "cancelled"
  return isDisplay(outcome) ? outcome : "interrupted"
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

/** The statuses GET /v1/runs?display_status= filters on in the Runs
 * list (queued runs live in the ledger only). */
export const FILTERABLE: readonly Exclude<DisplayStatus, "queued">[] = [
  "running",
  "waiting",
  "failed",
  "succeeded",
  "cancelled",
  "interrupted",
]
