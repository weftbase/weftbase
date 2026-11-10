// The SSE helper: frames, the native resume, and the fresh reconnect
// after an HTTP error. EventSource is faked; the frames are real ones
// (a run row from the goldens).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { openStream } from "@/lib/api/stream"
import type { StreamFrame } from "@/lib/api/types"

import runs from "./fixtures/runs.json"

class FakeEventSource {
  static CLOSED = 2
  static instances: FakeEventSource[] = []
  readyState = 0
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, (e: MessageEvent<string>) => void>()
  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, fn)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  emit(type: string, data: string, id = "") {
    this.listeners.get(type)?.({
      data,
      lastEventId: id,
    } as MessageEvent<string>)
  }
  /** A dropped connection: the browser retries by itself. */
  drop() {
    this.readyState = 0
    this.onerror?.()
  }
  /** An HTTP error: the browser gives up on this object. */
  fail() {
    this.readyState = 2
    this.onerror?.()
  }
}

const last = () => FakeEventSource.instances.at(-1)!

beforeEach(() => {
  FakeEventSource.instances = []
  vi.stubGlobal("EventSource", FakeEventSource)
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("openStream", () => {
  it("delivers run frames with their id and skips broken ones", () => {
    const frames: StreamFrame[] = []
    const stop = openStream(
      "/v1/runs/stream",
      { all: 1 },
      { onFrame: (f) => frames.push(f) }
    )
    last().open()
    last().emit("run", JSON.stringify({ run: runs.runs[0] }), "41")
    last().emit("run", "{not json", "42")
    last().emit("ping", "{}")
    expect(frames).toHaveLength(1)
    expect(frames[0]).toMatchObject({ type: "run", id: "41" })
    expect(frames[0].type === "run" && frames[0].data.run.id).toBe("r_stale")
    stop()
    expect(last().closed).toBe(true)
  })

  it("treats the browser's own reconnect as a resume", () => {
    const states: string[] = []
    const reopened: boolean[] = []
    openStream(
      "/v1/runs/stream",
      { all: 1 },
      {
        onFrame() {},
        onState: (s) => states.push(s),
        onReopen: (r) => reopened.push(r),
      }
    )
    last().open()
    last().drop()
    last().open()
    expect(FakeEventSource.instances).toHaveLength(1)
    expect(states).toEqual(["connecting", "live", "reconnecting", "live"])
    // Last-Event-ID went with the retry: nothing was missed.
    expect(reopened).toEqual([true])
  })

  it("reconnects with backoff after an HTTP error and asks for a refetch", () => {
    const reopened: boolean[] = []
    const stop = openStream(
      "/v1/runs/stream",
      { all: 1 },
      {
        onFrame() {},
        onReopen: (r) => reopened.push(r),
      }
    )
    last().open()
    last().fail()
    expect(FakeEventSource.instances).toHaveLength(1)
    vi.advanceTimersByTime(1000)
    expect(FakeEventSource.instances).toHaveLength(2)
    last().fail()
    vi.advanceTimersByTime(1999)
    expect(FakeEventSource.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeEventSource.instances).toHaveLength(3)
    last().open()
    // A new connection has no last id: frames may have been missed.
    expect(reopened).toEqual([false])
    stop()
    last().fail()
    vi.advanceTimersByTime(60_000)
    expect(FakeEventSource.instances).toHaveLength(3)
  })
})
