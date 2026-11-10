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

/** Why an approval was cancelled (its `reason`), in a sentence. */
export const CANCEL_REASON: Record<string, string> = {
  user: "The run was cancelled while this call waited for a decision.",
  version_retired:
    "The agent version was retired while this call waited for a decision.",
}

/** approvalReason: an approval's reason for a line of text. A
 * cancellation's reason is a code the server picks from two. */
export function approvalReason(
  a: Pick<Approval, "status" | "reason">
): string | undefined {
  if (!a.reason) return undefined
  if (a.status !== "cancelled") return a.reason
  if (a.reason === "user") return "the run was cancelled"
  if (a.reason === "version_retired") return "its version was retired"
  return a.reason.replace(/_/g, " ")
}

/** The word and tone of an approval's status; `title` says more where
 * the word alone does not (a cancelled approval's reason). */
export function approvalState(a: Pick<Approval, "status" | "reason">): {
  label: string
  tone: ApprovalTone
  title?: string
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
      return {
        label:
          a.reason === "version_retired"
            ? "Cancelled · version retired"
            : "Cancelled",
        tone: "neutral",
        title: a.reason ? (CANCEL_REASON[a.reason] ?? a.reason) : undefined,
      }
    default:
      return { label: a.status, tone: "neutral" }
  }
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
