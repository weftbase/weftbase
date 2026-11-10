// No request waits for ever (lib/api/http.ts): a write whose answer
// never comes fails after TIMEOUT_MS with a sentence a button can show,
// instead of leaving the button disabled.
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api/errors"
import { TIMEOUT_MS, api } from "@/lib/api/http"

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it("fails a request that gets no answer", async () => {
  // A fetch that only ends when it is aborted: a starved connection.
  vi.stubGlobal(
    "fetch",
    (_url: string, init: RequestInit) =>
      new Promise((_, reject) =>
        init.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError"))
        )
      )
  )
  const sent = api
    .decideApproval("appr_1", { decision: "approve", args_sha256: "abc" })
    .catch((e: unknown) => e)
  await vi.advanceTimersByTimeAsync(TIMEOUT_MS)
  const err = await sent
  expect(err).toBeInstanceOf(ApiError)
  expect((err as ApiError).code).toBe("timeout")
  expect((err as ApiError).message).toMatch(/did not answer within 20 s/)
  expect((err as ApiError).message).toMatch(/may still have gone through/)
})
