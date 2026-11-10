// The /v1 contract as of the review round (B's changes): logical runs
// by default, partial pages, the 409 codes in words, 503 as a store that
// is down, the server's session status, cancelled approvals with their
// reason, the outbox's event names and the waiting page's fallback.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ErrorState } from "@/components/console/empty-state"
import { chainRefetch } from "@/components/console/run/use-run"
import { approvalReason, approvalState } from "@/lib/approval-view"
import { ApiError, actionError, isUnavailable } from "@/lib/api/errors"
import { api } from "@/lib/api/http"
import { OUTBOX_EVENTS } from "@/lib/api/hub"
import { sessionStatus } from "@/lib/api/status"
import type { RunRow, SessionRow, StatsTotal } from "@/lib/api/types"
import { runsCount } from "@/lib/runs"
import { figuresOf } from "@/lib/stats"

import statsTotal from "./fixtures/stats-total.json"

vi.mock("@tanstack/react-router", () => ({
  useRouter: () => ({ invalidate: () => {} }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("GET /v1/runs", () => {
  let urls: string[]
  beforeEach(() => {
    urls = []
    vi.stubGlobal(
      "fetch",
      vi.fn((u: string) => {
        urls.push(u)
        return Promise.resolve(
          new Response(
            JSON.stringify({
              total: 0,
              total_exact: true,
              runs: [],
              next_before: null,
              next_before_id: null,
            }),
            { status: 200 }
          )
        )
      })
    )
  })
  it("asks for logical runs without a chains parameter (the default)", async () => {
    await api.runs({ agent: "support" })
    await api.runs({ q: "refund" })
    for (const u of urls)
      expect(new URL(u).searchParams.has("chains")).toBe(false)
  })
})

describe("a page the server cut", () => {
  const r = (id: string) => ({ id }) as RunRow
  it("says that more results are not shown", () => {
    const c = runsCount(
      [
        {
          total: 900,
          total_exact: false,
          partial: true,
          runs: [r("a"), r("b")],
        },
      ],
      false
    )
    expect(c?.text).toBe("2 runs")
    expect(c?.more).toMatch(/more results not shown/)
  })
  it("says nothing more when it was not cut", () => {
    expect(
      runsCount([{ total: 1, total_exact: true, runs: [r("a")] }], false)?.more
    ).toBeUndefined()
  })
})

describe("409 codes in words", () => {
  const e = (code: string) => new ApiError(409, code, "server words")
  it.each([
    ["already_finished", /already finished/],
    ["already_resumed", /already continued|resumed/],
    ["not_parked", /not waiting/],
    ["not_running", /not running/],
    ["session_busy", /session.*(going|turn)/],
    ["session_settling", /previous turn/],
  ])("%s", (code, re) => {
    expect(actionError(e(code))).toMatch(re)
  })
  it("keeps the server's words for anything else", () => {
    expect(actionError(new ApiError(400, "bad_request", "no agent"))).toBe(
      "no agent"
    )
  })
})

describe("503 unavailable", () => {
  it("reads as a store that is down, with a retry", () => {
    const err = new ApiError(
      503,
      "unavailable",
      "the control store could not be read: x"
    )
    expect(isUnavailable(err)).toBe(true)
    const onRetry = vi.fn()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ErrorState error={err} what="The run" onRetry={onRetry} />
      </QueryClientProvider>
    )
    expect(screen.getByText(/store is unavailable/i)).toBeTruthy()
    expect(screen.queryByText(/not found/i)).toBeNull()
    screen.getByRole("button", { name: /Try again/ }).click()
    expect(onRetry).toHaveBeenCalled()
  })
})

describe("a session's status", () => {
  it("is the server's display_status", () => {
    expect(
      sessionStatus({
        status: "parked",
        display_status: "cancelled",
      } as SessionRow)
    ).toBe("cancelled")
  })
})

describe("a cancelled approval", () => {
  it("says why: the run was cancelled, or its version retired", () => {
    expect(
      approvalState({ status: "cancelled", reason: "user" }).title
    ).toMatch(/run was cancelled/)
    const retired = approvalState({
      status: "cancelled",
      reason: "version_retired",
    })
    expect(retired.label).toMatch(/version retired/)
    expect(retired.title).toMatch(/retired/)
  })
})

describe("the outbox events followed", () => {
  it("are the server's names", () => {
    expect(OUTBOX_EVENTS).toContain("run.cancelled")
    expect(OUTBOX_EVENTS).not.toContain("run.canceled")
    expect(OUTBOX_EVENTS).toContain("approval.cancelled")
    expect(OUTBOX_EVENTS).toContain("session.deleted")
  })
})

describe("a waiting run page", () => {
  it("looks again every 30 s only while the outbox stream is down", () => {
    const waiting = { status: "waiting", last: {} } as never
    expect(chainRefetch(waiting, "live")).toBe(false)
    expect(chainRefetch(waiting, "reconnecting")).toBe(30_000)
    expect(chainRefetch(waiting, "connecting")).toBe(30_000)
    expect(
      chainRefetch({ status: "succeeded", last: {} } as never, "reconnecting")
    ).toBe(false)
  })
})

describe("stats by=total", () => {
  it("count the queued runs in the runs tile", () => {
    const t = (statsTotal as unknown as StatsTotal).total
    expect(typeof t.queued).toBe("number")
    expect(
      figuresOf({ ...t, queued: 3, waiting: 0, running: 0 }).runs.hint
    ).toBe("3 queued")
  })
})

describe("a cancelled approval's reason in a line", () => {
  it("is words, not the server's code", () => {
    expect(approvalReason({ status: "cancelled", reason: "user" })).toBe(
      "the run was cancelled"
    )
    expect(
      approvalReason({ status: "cancelled", reason: "version_retired" })
    ).toBe("its version was retired")
    expect(approvalReason({ status: "denied", reason: "too much" })).toBe(
      "too much"
    )
  })
})
