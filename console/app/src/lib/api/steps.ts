// Readers of a step document (GET /v1/runs/{id}/steps/{n}).

import type { Badge, ModelRequest, StepDoc } from "./types"

/** stepRequest: the step's request record, or undefined when the server
 * sent a badge in its place (no record: derived, a hole, a gap). */
export function stepRequest(
  s: Pick<StepDoc, "request"> | undefined
): ModelRequest | undefined {
  const r = s?.request
  return r && "body" in r && typeof r.body === "object" && r.body !== null
    ? r
    : undefined
}

/** stepRequestBadge: why the step has no request record, when it has
 * none and the server said why. */
export function stepRequestBadge(
  s: Pick<StepDoc, "request"> | undefined
): Badge | undefined {
  const r = s?.request
  if (!r || stepRequest(s)) return undefined
  return (r as Badge).badge || (r as Badge).reason ? (r as Badge) : undefined
}
