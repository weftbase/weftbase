import { Badge } from "@/components/ui/badge"
import type { ApprovalStatus, RunStatus, SessionState } from "@/lib/api/types"

// Status pills (StatusBadge README). Every pill carries its word; the
// status colours mean one thing each and are used for nothing else.

type Variant = "succeeded" | "running" | "waiting" | "failed" | "neutral"

const RUN: Record<RunStatus, { v: Variant; long: string; short: string }> = {
  succeeded: { v: "succeeded", long: "Succeeded", short: "Succeeded" },
  running: { v: "running", long: "Running", short: "Running" },
  queued: { v: "running", long: "Queued", short: "Queued" },
  waiting: { v: "waiting", long: "Waiting for approval", short: "Waiting" },
  failed: { v: "failed", long: "Failed", short: "Failed" },
  cancelled: { v: "neutral", long: "Cancelled", short: "Cancelled" },
}

export function RunStatusBadge({
  status,
  short = false,
}: {
  status: RunStatus
  short?: boolean
}) {
  const s = RUN[status]
  return <Badge variant={s.v}>{short ? s.short : s.long}</Badge>
}

const APPROVAL: Record<ApprovalStatus, { v: Variant; label: string }> = {
  pending: { v: "waiting", label: "Pending" },
  approved: { v: "succeeded", label: "Approved" },
  denied: { v: "failed", label: "Denied" },
  expired: { v: "neutral", label: "Expired" },
}

export function ApprovalStatusBadge({ status }: { status: ApprovalStatus }) {
  const s = APPROVAL[status]
  return <Badge variant={s.v}>{s.label}</Badge>
}

const SESSION: Record<SessionState, { v: Variant; label: string }> = {
  active: { v: "running", label: "Active" },
  idle: { v: "neutral", label: "Idle" },
  waiting: { v: "waiting", label: "Waiting for approval" },
  closed: { v: "neutral", label: "Closed" },
}

export function SessionStateBadge({ state }: { state: SessionState }) {
  const s = SESSION[state]
  return <Badge variant={s.v}>{s.label}</Badge>
}
