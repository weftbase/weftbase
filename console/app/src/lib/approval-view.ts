// How an approval reads in a list. Pure.

import type { Approval } from "./api/types"

const scalar = (v: unknown): string =>
  typeof v === "string"
    ? v
    : typeof v === "number" || typeof v === "boolean"
      ? String(v)
      : v === null
        ? "null"
        : Array.isArray(v)
          ? `[${v.length}]`
          : "{…}"

/** argsSummary is a call's arguments on one line: "order_id 1077 ·
 * amount_usd 129 · reason duplicate charge". The detail shows them in
 * full; this is only the gist. */
export function argsSummary(args: unknown): string {
  if (args === null || args === undefined) return ""
  if (typeof args !== "object" || Array.isArray(args)) return scalar(args)
  return Object.entries(args as Record<string, unknown>)
    .map(([k, v]) => `${k} ${scalar(v)}`)
    .join(" · ")
}

export type ApprovalTone = "waiting" | "good" | "bad" | "neutral"

/** The word and tone of an approval's status. */
export function approvalState(a: Pick<Approval, "status">): {
  label: string
  tone: ApprovalTone
} {
  switch (a.status) {
    case "pending":
      return { label: "Pending", tone: "waiting" }
    case "approved":
      return { label: "Approved", tone: "good" }
    case "denied":
      return { label: "Denied", tone: "bad" }
    case "resolved":
      return { label: "Answered", tone: "neutral" }
    case "expired":
      return { label: "Expired", tone: "neutral" }
    case "cancelled":
      return { label: "Cancelled", tone: "neutral" }
    default:
      return { label: a.status, tone: "neutral" }
  }
}

/** Who decided: "operator op_…", "API key key_…", or "system" (the
 * server itself: an approval that expired); undefined when nobody has.
 * short keeps the end of a long id (a list cell). */
export function decidedBy(
  a: Pick<Approval, "actor">,
  short = false
): string | undefined {
  if (a.actor?.kind === "system" && !a.actor.id) return "system"
  if (!a.actor?.id) return undefined
  const kind =
    a.actor.kind === "key"
      ? "API key"
      : a.actor.kind === "operator"
        ? "operator"
        : (a.actor.kind ?? "")
  const id =
    short && a.actor.id.length > 12 ? `…${a.actor.id.slice(-6)}` : a.actor.id
  return `${kind} ${id}`.trim()
}

/** The version an approval's run ran, as the run page names it: "v2"
 * and the labels on it now ("v2 staging"). undefined when the approval
 * carries none (a run the server did not admit). */
export function approvalVersion(
  v: Approval["agent_version"]
): { id: string; number: number; labels: string[] } | undefined {
  if (!v?.id || !v.number) return undefined
  return { id: v.id, number: v.number, labels: v.labels ?? [] }
}
