// The shell's one toast: when an approval is already in front of the
// person, and who an expired approval was decided by.
import { QueryClient } from "@tanstack/react-query"
import { describe, expect, it } from "vitest"

import { approvalOnScreen } from "@/components/console/app-shell"
import { chainQuery } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"

const on = (routeId: string, params: Record<string, unknown> = {}) => [
  { routeId: "__root__", params },
  { routeId, params },
]

describe("the approval toast", () => {
  const qc = new QueryClient()

  it("is not raised on the inbox, whatever the run", () => {
    expect(approvalOnScreen(on("/approvals/"), "run_a", qc)).toBe(true)
  })

  it("is not raised on the page of the run that asks", () => {
    const page = on("/runs/$runId", { runId: "run_a" })
    expect(approvalOnScreen(page, "run_a", qc)).toBe(true)
    // A subagent's approval shows on its parent's page.
    expect(approvalOnScreen(page, "run_a/2/call_billing", qc)).toBe(true)
    expect(approvalOnScreen(page, "run_b", qc)).toBe(false)
  })

  it("knows a run that continues the one on the page", () => {
    qc.setQueryData(chainQuery("run_a").queryKey, {
      runs: [{ id: "run_a" }, { id: "run_a2" }],
    } as unknown as ChainView)
    const page = on("/runs/$runId", { runId: "run_a" })
    expect(approvalOnScreen(page, "run_a2", qc)).toBe(true)
    expect(approvalOnScreen(page, "run_c", qc)).toBe(false)
  })

  it("is raised everywhere else", () => {
    expect(approvalOnScreen(on("/runs/"), "run_a", qc)).toBe(false)
    expect(approvalOnScreen(on("/"), "run_a", qc)).toBe(false)
    expect(
      approvalOnScreen(on("/sessions/$sessionId", { sessionId: "s" }), "", qc)
    ).toBe(false)
  })
})
