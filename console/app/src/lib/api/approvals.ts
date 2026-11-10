// Deciding a tool call that waits on a person:
// POST /v1/approvals/{id}/decision, bound to the arguments by the
// approval's own args_sha256 (the server's hash, sent back as it came;
// the Console never computes one). A run continues when every one of
// its approvals has a decision.

import { ApiError } from "./errors"
import { api } from "./http"
import type { Approval } from "./types"

export type Decision = "approve" | "deny" | "resolve"

export interface DecisionInput {
  decision: Decision
  /** Why (deny); the agent reads it. */
  reason?: string
  /** resolve: the result the agent reads instead of running the tool. */
  content?: string
}

/** decide sends one decision and answers the approval as it now is
 * (`resumed_by` names the run that continues, once it was the last). */
export function decide(a: Approval, d: DecisionInput): Promise<Approval> {
  return api.decideApproval(a.id, {
    decision: d.decision,
    reason: d.reason || undefined,
    content: d.content,
    args_sha256: a.args_sha256,
  })
}

/** decisionError turns a refused decision into a sentence. */
export function decisionError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "args_changed")
      return "The arguments of this call are no longer the ones shown, so the decision was not applied. Reload to see the current arguments, then decide again."
    if (e.code === "not_pending")
      return "This call already has a decision: someone else decided it, or the run ended."
    return e.message
  }
  return e instanceof Error ? e.message : String(e)
}

/** The keys a decision makes stale. */
export const afterDecision = [
  ["run"],
  ["runs"],
  ["approvals"],
  ["sessions"],
  ["session"],
  ["stats"],
] as const
