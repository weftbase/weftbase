// A sign-in link opened in a tab already on the Console (only the hash
// changes) that fails, used or expired, says so: on the signed-out
// screen as its alert, on the signed-in shell as a toast.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api/errors"
import type * as HttpModule from "@/lib/api/http"
import { AuthGate } from "@/components/console/auth-gate"

vi.mock("@/components/console/app-shell", () => ({
  AppShell: () => <p>shell</p>,
}))
const { meta, signIn, toastError } = vi.hoisted(() => ({
  meta: vi.fn(),
  signIn: vi.fn(),
  toastError: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: { error: toastError } }))
vi.mock("@/lib/api/http", async (orig) => {
  const m = await orig<typeof HttpModule>()
  return { ...m, api: { ...m.api, meta, signIn } }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const used = () =>
  new ApiError(
    401,
    "invalid_token",
    "This sign-in link was already used or has expired."
  )

const openLink = async (token: string) =>
  act(async () => {
    window.location.hash = `#signin=${token}`
    window.dispatchEvent(new HashChangeEvent("hashchange"))
    await new Promise((r) => setTimeout(r, 20))
  })

const mount = () => {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthGate />
    </QueryClientProvider>
  )
}

it("says why a link opened on the signed-out screen did not sign in", async () => {
  meta.mockRejectedValue(new ApiError(401, "unauthorized", "no session"))
  signIn.mockRejectedValue(used())
  mount()
  await screen.findByText("You are signed out")
  await openLink("used-token")
  expect(signIn).toHaveBeenCalledWith("used-token")
  expect(screen.getByRole("alert").textContent).toMatch(/already used/)
})

it("says it on the signed-in shell too", async () => {
  meta.mockResolvedValue({ principal: { kind: "operator", id: "op_1" } })
  signIn.mockRejectedValue(used())
  mount()
  await screen.findByText("shell")
  await openLink("used-token-2")
  expect(toastError).toHaveBeenCalledTimes(1)
  expect(toastError.mock.calls[0][1].description).toMatch(/already used/)
  expect(screen.getByText("shell")).toBeTruthy()
})
