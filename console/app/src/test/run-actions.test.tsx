// The run page's actions: one steer per send, Cancel wherever a run can
// be cancelled (with its error on screen), only web addresses as "Open
// in app", and a delete that leaves no stale cache behind.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api/errors"
import type * as HttpModule from "@/lib/api/http"
import { RunActions, appUrl } from "@/components/console/run/actions"
import type { DisplayStatus } from "@/lib/api/status"

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
  useRouter: () => ({ state: { location: { href: "/" } } }),
}))
vi.mock("@/components/console/compare/dialog", () => ({
  CompareDialog: () => null,
}))
const { steer, cancel, deleteRun } = vi.hoisted(() => ({
  steer: vi.fn(() => new Promise(() => {})), // never answers: in flight
  cancel: vi.fn(),
  deleteRun: vi.fn(),
}))
vi.mock("@/lib/api/http", async (orig) => {
  const m = await orig<typeof HttpModule>()
  return { ...m, api: { ...m.api, steer, cancel, deleteRun } }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const mount = (
  status: DisplayStatus,
  extra: Partial<React.ComponentProps<typeof RunActions>> = {},
  qc = new QueryClient()
) =>
  render(
    <QueryClientProvider client={qc}>
      <RunActions
        runId="run_1"
        status={status}
        hasChildren={false}
        {...extra}
      />
    </QueryClientProvider>
  )

describe("Send message", () => {
  it("sends one steer for two Ctrl+Enter presses while the first is in flight", async () => {
    mount("running")
    fireEvent.click(screen.getByRole("button", { name: /Send message/ }))
    const box = await screen.findByLabelText("Message")
    fireEvent.change(box, { target: { value: "check the refund" } })
    await act(async () => {
      fireEvent.keyDown(box, { key: "Enter", ctrlKey: true })
    })
    await act(async () => {
      fireEvent.keyDown(box, { key: "Enter", ctrlKey: true }) // or key repeat
    })
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: /Sending/ })
        .disabled
    ).toBe(true)
    expect(steer).toHaveBeenCalledTimes(1)
  })
})

describe("Cancel", () => {
  it.each(["queued", "running", "waiting"] as const)(
    "is offered while the run is %s",
    (status) => {
      mount(status)
      expect(screen.getByRole("button", { name: /^Cancel$/ })).toBeTruthy()
    }
  )
  it.each(["succeeded", "failed", "cancelled", "interrupted"] as const)(
    "is not offered once the run is %s",
    (status) => {
      mount(status)
      expect(screen.queryByRole("button", { name: /^Cancel$/ })).toBeNull()
    }
  )
  it("offers Send message only while the run is running", () => {
    mount("waiting")
    expect(screen.queryByRole("button", { name: /Send message/ })).toBeNull()
  })
  it("says why a cancel failed", async () => {
    cancel.mockRejectedValueOnce(
      new ApiError(409, "already_finished", "The run has already finished.")
    )
    mount("waiting")
    fireEvent.click(screen.getByRole("button", { name: /^Cancel$/ }))
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toMatch(/already finished/)
  })
})

describe("Open in app", () => {
  it("is a link only for http and https addresses", () => {
    expect(appUrl("https://shop.example/orders/1")).toBe(
      "https://shop.example/orders/1"
    )
    expect(appUrl("http://localhost:3000/x")).toBe("http://localhost:3000/x")
    expect(appUrl("javascript:alert(1)")).toBeUndefined()
    expect(appUrl("data:text/html,<b>x</b>")).toBeUndefined()
    expect(appUrl("/relative")).toBeUndefined()
    expect(appUrl(42)).toBeUndefined()
    expect(appUrl(undefined)).toBeUndefined()
  })
})

describe("Delete", () => {
  it("drops the chain's runs, the session, the id lookups and refreshes the rest", async () => {
    deleteRun.mockResolvedValueOnce(undefined)
    const qc = new QueryClient()
    for (const key of [
      ["run", "run_1"],
      ["run", "run_0"],
      ["run", "run_other"],
      ["jump", "runs", "run_1"],
    ])
      qc.setQueryData(key, { id: "x" })
    const spy = vi.spyOn(qc, "invalidateQueries")
    mount("succeeded", { chainIds: ["run_0", "run_1"], sessionId: "ses_1" }, qc)
    fireEvent.pointerDown(
      screen.getByRole("button", { name: "More actions" }),
      {
        button: 0,
        ctrlKey: false,
      }
    )
    fireEvent.click(await screen.findByRole("menuitem", { name: /Delete run/ }))
    fireEvent.click(await screen.findByRole("button", { name: "Delete run" }))
    await waitFor(() => expect(navigate).toHaveBeenCalled())
    expect(qc.getQueryData(["run", "run_1"])).toBeUndefined()
    expect(qc.getQueryData(["run", "run_0"])).toBeUndefined()
    expect(qc.getQueryData(["jump", "runs", "run_1"])).toBeUndefined()
    expect(qc.getQueryData(["run", "run_other"])).toBeDefined()
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey))
    for (const k of [
      ["runs"],
      ["stats"],
      ["sessions"],
      ["session", "ses_1"],
      ["approvals"],
    ])
      expect(keys).toContain(JSON.stringify(k))
  })
})
