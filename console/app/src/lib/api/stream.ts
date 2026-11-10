// Live streams over SSE. With http.ts, the only code that opens a network
// connection.
//
// The server (api/http/v1/live.go) sends `id: <seq>` on every run, record
// and attempt frame, a `ping` every 15 s, and `overflow` before it drops
// a subscriber that fell behind. It resumes from the Last-Event-ID
// request header: it backfills what the client missed, then goes live.
//
// EventSource cannot set request headers, and does not need to: on its
// automatic reconnect the browser itself sends Last-Event-ID (the last
// `id:` it received), which is the header the server reads. So:
// - a dropped connection resumes with no gap ("resumed");
// - an `overflow` frame is followed by the same resume, and the caller is
//   also told to refetch, since the gap may exceed what can be backfilled;
// - an HTTP error (401 after the session ended, 5xx at a restart) closes
//   an EventSource for good. We open a new one with backoff; that one has
//   no last id, so the caller is told it is a fresh stream and refetches.
// Auth is the same-origin session cookie; nothing goes in the URL.

import { buildUrl, seg } from "./http"
import type { Query } from "./http"
import type { StreamFrame } from "./types"

export type StreamState = "connecting" | "live" | "reconnecting"

export interface StreamHandlers {
  onFrame: (frame: StreamFrame) => void
  /** The connection state changed (for a "Live" pill). */
  onState?: (state: StreamState) => void
  /** The stream is open again after a break. resumed: the server
   * backfilled the gap; false: frames may have been missed, refetch. */
  onReopen?: (resumed: boolean) => void
}

const FRAMES = ["run", "record", "attempt", "backfill_error"] as const
const BACKOFF_MS = [1000, 2000, 5000, 10_000, 30_000]

/** Frame kinds the server filters on (`kinds=`). Deltas (token-by-token
 * model output) are off unless asked for. */
export type FrameKind = "event" | "delta" | "messages" | "run" | "attempt"

const DEDUP_MAX = 8192

/** Delivery is at least once (a resume's backfill overlaps the live
 * frames behind it), so records are deduplicated on (run, kind, pos),
 * the key the server names. Bounded: the oldest keys are dropped. */
function dedup() {
  const keys = new Set<string>()
  return {
    dup(r: { run_id: string; kind: string; pos: number }): boolean {
      const k = `${r.run_id}\u0000${r.kind}\u0000${r.pos}`
      if (keys.has(k)) return true
      keys.add(k)
      if (keys.size > DEDUP_MAX) {
        for (const old of keys) {
          keys.delete(old)
          if (keys.size <= DEDUP_MAX / 2) break
        }
      }
      return false
    },
  }
}

/** openStream subscribes to one SSE route until the returned function is
 * called. It never throws: a stream that cannot connect keeps retrying. */
export function openStream(
  path: string,
  query: Query,
  handlers: StreamHandlers
): () => void {
  if (typeof EventSource === "undefined") return () => {}
  let source: EventSource | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let closed = false
  let failures = 0
  let opened = false
  const seen = dedup()

  const connect = (fresh: boolean) => {
    handlers.onState?.(opened ? "reconnecting" : "connecting")
    const es = new EventSource(buildUrl(path, query))
    source = es
    // fresh: this EventSource replaces one the browser gave up on, so it
    // carries no last id and its first open is not a resume.
    let resumes = !fresh
    es.onopen = () => {
      failures = 0
      handlers.onState?.("live")
      if (opened) handlers.onReopen?.(resumes)
      opened = true
      resumes = true
    }
    es.onerror = () => {
      if (closed) return
      if (es.readyState === EventSource.CLOSED) {
        // An HTTP error: the browser will not retry this one.
        es.close()
        const wait = BACKOFF_MS[Math.min(failures, BACKOFF_MS.length - 1)]
        failures++
        handlers.onState?.("reconnecting")
        timer = setTimeout(() => connect(true), wait)
      } else {
        // The browser is reconnecting with Last-Event-ID.
        handlers.onState?.("reconnecting")
      }
    }
    for (const type of FRAMES) {
      es.addEventListener(type, (e) => {
        const msg = e as MessageEvent<string>
        let frame: StreamFrame
        try {
          frame = {
            type,
            id: msg.lastEventId,
            data: JSON.parse(msg.data),
          }
        } catch {
          return // a frame that is not JSON is skipped, never fatal
        }
        if (frame.type === "record" && seen.dup(frame.data)) return
        handlers.onFrame(frame)
      })
    }
    // The subscriber fell behind and the server is about to end this
    // stream. The browser reconnects with the last id and the server
    // backfills, but frames can be lost past the server's buffer: tell
    // the caller to refetch.
    es.addEventListener("overflow", () => handlers.onReopen?.(false))
  }
  connect(false)

  return () => {
    closed = true
    if (timer) clearTimeout(timer)
    source?.close()
  }
}

const kinds = (k: FrameKind[] | undefined) => (k ? k.join(",") : undefined)

/** Every run of the project. The lists only need `run` frames. */
export function streamRuns(
  handlers: StreamHandlers,
  opts: { agent?: string; kinds?: FrameKind[] } = {}
): () => void {
  return openStream(
    "/v1/runs/stream",
    opts.agent
      ? { agent: opts.agent, kinds: kinds(opts.kinds) }
      : { all: 1, kinds: kinds(opts.kinds) },
    handlers
  )
}

/** One run: its row, its records and its model-call attempts. */
export function streamRun(
  id: string,
  handlers: StreamHandlers,
  opts: { kinds?: FrameKind[] } = {}
): () => void {
  return openStream(
    `/v1/runs/${seg(id)}/stream`,
    { kinds: kinds(opts.kinds) },
    handlers
  )
}

/** One session: the frames of all its runs. */
export function streamSession(
  id: string,
  handlers: StreamHandlers,
  opts: { kinds?: FrameKind[] } = {}
): () => void {
  return openStream(
    `/v1/sessions/${seg(id)}/stream`,
    { kinds: kinds(opts.kinds) },
    handlers
  )
}
