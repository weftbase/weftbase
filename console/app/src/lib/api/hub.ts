// The live hub: the app's only two EventSources, shared by every screen
// and, where the browser allows, by every tab.
//
// Why it exists. A browser opens at most six HTTP/1.1 connections to one
// host, for all its tabs together, and an EventSource holds one for as
// long as it is open. Streams per screen or per tab therefore add up to
// a Console in which every request hangs: three tabs with two streams
// each, or one tab after three full-page navigations, because Chrome
// keeps the pages it left in its back/forward cache with their streams
// open. So:
// - two streams for the whole app, never one per component: every run's
//   row (`/v1/runs/stream?all=1&kinds=run`; the server sends a row each
//   time a run moves, which is enough to keep a run or session page
//   current) and the approval events (`/v1/events`). The server has no
//   route that carries both, so two is the floor today;
// - one tab holds them (a Web Lock picks it) and hands every frame to
//   the others over a BroadcastChannel, so ten tabs still use two
//   connections. Without those APIs (an insecure origin) each tab opens
//   its own two;
// - a page that is being left (`pagehide`: unloaded, or put in the
//   back/forward cache) closes its streams and gives the lock up; a
//   page restored from the cache (`pageshow`) joins again and its
//   subscribers refetch;
// - a hidden tab keeps the cache as it is and refetches once when it is
//   shown again.
// A tab that hears nothing from the holder for 45 s (the server pings
// every 15 s) takes the lock over: a frozen tab must not leave the
// others blind.

import { streamEvents, streamRuns } from "./stream"
import type { StreamState } from "./stream"
import type { OutboxEvent, RunFrame } from "./types"

export const APPROVAL_EVENTS = [
  "approval.requested",
  "approval.decided",
] as const

type Topic = "runs" | "events"

export interface HubHandlers<T> {
  onData: (data: T) => void
  onState?: (state: StreamState) => void
  /** Frames may have been missed (a reconnect that could not resume,
   * a tab shown again, a page restored): refetch. */
  onStale?: () => void
}

type Msg =
  | { k: "data"; topic: Topic; data: unknown }
  | { k: "state"; topic: Topic; state: StreamState }
  | { k: "stale"; topic: Topic }
  | { k: "beat" }
  | { k: "hello" }

const SILENCE_MS = 45_000
const WATCH_MS = 15_000

const subs: Record<Topic, Set<HubHandlers<never>>> = {
  runs: new Set(),
  events: new Set(),
}
const states: Record<Topic, StreamState> = {
  runs: "connecting",
  events: "connecting",
}

let started = false
let listening = false
let leading = false
let closeStreams: (() => void) | undefined
let channel: BroadcastChannel | undefined
let release: (() => void) | undefined
let waiting: AbortController | undefined
let heard = 0
let watchdog: ReturnType<typeof setInterval> | undefined
/** Frames arrived while the tab was hidden. */
let dirty = false

const hidden = () =>
  typeof document !== "undefined" && document.visibilityState === "hidden"

const shared = () =>
  typeof BroadcastChannel !== "undefined" &&
  typeof navigator !== "undefined" &&
  "locks" in navigator

function deliver(m: Msg) {
  if (m.k === "state") {
    states[m.topic] = m.state
    for (const h of subs[m.topic]) h.onState?.(m.state)
    return
  }
  if (m.k !== "data" && m.k !== "stale") return
  if (hidden()) {
    dirty = true
    return
  }
  for (const h of subs[m.topic]) {
    if (m.k === "stale") h.onStale?.()
    else (h.onData as (d: unknown) => void)(m.data)
  }
}

function staleAll() {
  dirty = false
  for (const topic of ["runs", "events"] as const)
    for (const h of subs[topic]) h.onStale?.()
}

/** What the holder of the streams does with what they say: its own
 * subscribers first, then the other tabs. */
function emit(m: Msg) {
  deliver(m)
  try {
    channel?.postMessage(m)
  } catch {
    // a frame that cannot be cloned is not worth the stream
  }
}

function lead() {
  leading = true
  const handlers = (topic: Topic) => ({
    onState: (state: StreamState) => emit({ k: "state", topic, state }),
    onReopen: (resumed: boolean) => {
      if (!resumed) emit({ k: "stale", topic })
    },
    onPing: () => emit({ k: "beat" }),
  })
  const stopRuns = streamRuns(
    {
      ...handlers("runs"),
      onFrame: (f) => {
        if (f.type === "run") emit({ k: "data", topic: "runs", data: f.data })
      },
    },
    { kinds: ["run"] }
  )
  const stopEvents = streamEvents(APPROVAL_EVENTS, {
    ...handlers("events"),
    onEvent: (e) => emit({ k: "data", topic: "events", data: e }),
  })
  closeStreams = () => {
    stopRuns()
    stopEvents()
  }
  // The streams are new: whoever was following another tab's may have
  // missed frames between the two.
  emit({ k: "stale", topic: "runs" })
  emit({ k: "stale", topic: "events" })
}

function unlead() {
  leading = false
  closeStreams?.()
  closeStreams = undefined
}

function follow(steal = false) {
  if (!started || leading) return
  const held = () =>
    new Promise<void>((resolve) => {
      release = resolve
      if (started) lead()
      else resolve()
    })
  const ended = () => {
    // Released by stop(), or stolen by a tab that heard nothing from us.
    release = undefined
    const stolen = leading
    unlead()
    if (stolen && started) follow()
  }
  if (steal) {
    waiting?.abort()
    waiting = undefined
    void navigator.locks
      .request(LOCK(), { steal: true }, held)
      .then(ended, ended)
    return
  }
  const ctl = new AbortController()
  waiting = ctl
  void navigator.locks
    .request(LOCK(), { signal: ctl.signal }, held)
    .then(ended, () => {
      // Aborted before it was granted (stop(), or a steal of our own):
      // nothing to end. Rejected while held: another tab stole it.
      if (leading) ended()
    })
}

/** One lock and one channel per mount: two Consoles on one origin under
 * different prefixes are different servers' views. */
const LOCK = () => `weftbase.live:${document.baseURI}`

function start() {
  if (started || typeof window === "undefined") return
  started = true
  if (!listening) {
    listening = true
    window.addEventListener("pagehide", stop)
    window.addEventListener("pageshow", (e) => {
      if (!e.persisted) return
      if (subs.runs.size + subs.events.size > 0) start()
      staleAll()
    })
    document.addEventListener("visibilitychange", () => {
      if (!hidden() && dirty) staleAll()
    })
  }
  if (!shared()) {
    lead()
    return
  }
  channel = new BroadcastChannel(LOCK())
  channel.onmessage = (e: MessageEvent<Msg>) => {
    const m = e.data
    if (m.k === "hello") {
      if (!leading) return
      for (const topic of ["runs", "events"] as const)
        channel?.postMessage({ k: "state", topic, state: states[topic] })
      return
    }
    if (leading) return
    heard = Date.now()
    deliver(m)
  }
  heard = Date.now()
  channel.postMessage({ k: "hello" } satisfies Msg)
  watchdog = setInterval(() => {
    if (leading || hidden()) return
    if (Date.now() - heard > SILENCE_MS) {
      heard = Date.now()
      follow(true)
    }
  }, WATCH_MS)
  follow()
}

function stop() {
  if (!started) return
  started = false
  waiting?.abort()
  waiting = undefined
  if (watchdog) clearInterval(watchdog)
  watchdog = undefined
  unlead()
  release?.()
  release = undefined
  channel?.close()
  channel = undefined
  for (const topic of ["runs", "events"] as const) states[topic] = "connecting"
}

function subscribe<T>(topic: Topic, h: HubHandlers<T>): () => void {
  const set = subs[topic] as Set<HubHandlers<T>>
  set.add(h)
  start()
  h.onState?.(states[topic])
  return () => {
    set.delete(h)
    if (subs.runs.size + subs.events.size === 0) stop()
  }
}

/** Every run's row, each time the run moves (a step, a tool call, its
 * end). Child runs too: their ids start with the parent's. */
export const subscribeRuns = (h: HubHandlers<RunFrame>) => subscribe("runs", h)

/** Approvals asked for and decided, as they happen. */
export const subscribeApprovals = (h: HubHandlers<OutboxEvent>) =>
  subscribe("events", h)
