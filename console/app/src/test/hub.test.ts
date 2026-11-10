// The live hub (lib/api/hub.ts): the regression test for the Console
// that stopped answering. Chrome allows six connections per host, and
// streams per screen, per tab and per page kept in the back/forward
// cache used them all up. The rules tested here:
// - however many screens listen, the app holds two EventSources;
// - a page that is left (pagehide) holds none, and one that is restored
//   (pageshow, persisted) reopens them and tells its screens to refetch;
// - several tabs hold two between them, and the tab that takes over
//   when the holder leaves tells everyone to refetch.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type * as HubModule from "@/lib/api/hub"
import type { RunFrame } from "@/lib/api/types"

import runs from "./fixtures/runs.json"

class FakeEventSource {
  static CLOSED = 2
  static all: FakeEventSource[] = []
  static open = () => FakeEventSource.all.filter((e) => !e.closed)
  readyState = 0
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, (e: MessageEvent<string>) => void>()
  constructor(readonly url: string) {
    FakeEventSource.all.push(this)
  }
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, fn)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(type: string, data: unknown, id = "1") {
    this.listeners.get(type)?.({
      data: JSON.stringify(data),
      lastEventId: id,
    } as MessageEvent<string>)
  }
}

/** navigator.locks, enough of it: one holder per name, a queue, abort
 * while queued. Shared by every "tab" of a test. */
function fakeLocks() {
  const held = new Map<string, boolean>()
  const queue = new Map<string, (() => void)[]>()
  return {
    request(
      name: string,
      opts: { signal?: AbortSignal },
      cb: () => Promise<void>
    ): Promise<void> {
      return new Promise<void>((resolve, reject) => {
        const run = () => {
          if (opts.signal?.aborted) return next()
          held.set(name, true)
          void cb().then(() => {
            held.set(name, false)
            resolve()
            next()
          })
        }
        const next = () => queue.get(name)?.shift()?.()
        opts.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError"))
        )
        if (held.get(name)) queue.set(name, [...(queue.get(name) ?? []), run])
        else run()
      })
    },
  }
}

type Hub = typeof HubModule
/** A tab: its own copy of the module, the same globals. */
async function tab(): Promise<Hub> {
  vi.resetModules()
  return await import("@/lib/api/hub")
}

const frame: RunFrame = { run: runs.runs[0] as RunFrame["run"] }
const tick = () => new Promise((r) => setTimeout(r, 0))

beforeEach(() => {
  FakeEventSource.all = []
  vi.stubGlobal("EventSource", FakeEventSource)
})
afterEach(() => {
  window.dispatchEvent(new Event("pagehide"))
  vi.unstubAllGlobals()
})

describe("one tab", () => {
  beforeEach(() => vi.stubGlobal("BroadcastChannel", undefined))

  it("holds two streams however many screens listen", async () => {
    const hub = await tab()
    const got: string[] = []
    const stops = [
      hub.subscribeRuns({ onData: (f) => got.push(`list ${f.run.id}`) }),
      hub.subscribeRuns({ onData: (f) => got.push(`run ${f.run.id}`) }),
      hub.subscribeRuns({ onData: () => {} }),
      hub.subscribeApprovals({ onData: (e) => got.push(e.type) }),
    ]
    expect(FakeEventSource.open().map((e) => new URL(e.url).pathname)).toEqual([
      "/v1/runs/stream",
      "/v1/events",
    ])
    FakeEventSource.all[0].emit("run", frame)
    FakeEventSource.all[1].emit("approval.requested", {
      type: "approval.requested",
    })
    expect(got).toEqual([
      `list ${frame.run.id}`,
      `run ${frame.run.id}`,
      "approval.requested",
    ])
    stops.forEach((stop) => stop())
    expect(FakeEventSource.open()).toHaveLength(0)
  })

  it("holds none once the page is left, and refetches when restored", async () => {
    const hub = await tab()
    const stale = vi.fn()
    hub.subscribeRuns({ onData: () => {}, onStale: stale })
    hub.subscribeApprovals({ onData: () => {}, onStale: stale })
    expect(FakeEventSource.open()).toHaveLength(2)
    stale.mockClear()

    // Navigating away: Chrome may keep this page in its back/forward
    // cache, connections and all, unless they are closed here.
    window.dispatchEvent(new Event("pagehide"))
    expect(FakeEventSource.open()).toHaveLength(0)

    // Back: the same page, the same screens, new streams.
    const back = new Event("pageshow") as PageTransitionEvent
    Object.defineProperty(back, "persisted", { value: true })
    window.dispatchEvent(back)
    expect(FakeEventSource.open()).toHaveLength(2)
    expect(stale).toHaveBeenCalled()
  })
})

describe("several tabs", () => {
  it("hold two streams between them", async () => {
    vi.stubGlobal("navigator", { locks: fakeLocks() })
    const a = await tab()
    const b = await tab()
    const c = await tab()
    const got: string[] = []
    const staleB = vi.fn()
    const stopA = a.subscribeRuns({ onData: () => got.push("a") })
    b.subscribeRuns({ onData: () => got.push("b"), onStale: staleB })
    c.subscribeRuns({ onData: () => got.push("c") })
    c.subscribeApprovals({ onData: () => got.push("c approval") })
    await tick()
    expect(FakeEventSource.open()).toHaveLength(2)

    // The holder's frames reach every tab.
    FakeEventSource.open()[0].emit("run", frame)
    FakeEventSource.open()[1].emit("approval.decided", {
      type: "approval.decided",
    })
    await tick()
    expect(got.sort()).toEqual(["a", "b", "c", "c approval"])

    // The holder goes: another tab takes over, still two streams, and
    // the tabs are told they may have missed something.
    staleB.mockClear()
    stopA()
    await tick()
    expect(FakeEventSource.open()).toHaveLength(2)
    expect(FakeEventSource.all).toHaveLength(4)
    await tick()
    expect(staleB).toHaveBeenCalled()
  })
})
