// Number, time and money formats. One place, so every screen agrees.

import type { Cost } from "./api/types"

const nf = new Intl.NumberFormat("en-US")

export function tokens(n: number): string {
  return nf.format(n)
}

/** duration renders milliseconds the way the artboard does: "4.2 s",
 * "820 ms", "2 min 4 s". null (still going) is an em dash. */
export function duration(ms: number | null): string {
  if (ms === null) return "—"
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return s ? `${m} min ${s} s` : `${m} min`
}

/** cost never prints "$0.00" for a price it does not know. */
export function cost(c: Cost): string {
  if (c === null) return "unknown"
  if (c === 0) return "$0"
  if (c < 0.01) return `$${c.toFixed(4)}`
  return `$${c.toFixed(2)}`
}

/** ago is "now", "2 min ago", "3 h ago" or "4 d ago". */
export function ago(isoTime: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - Date.parse(isoTime)) / 1000))
  if (s < 45) return "now"
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  return `${Math.round(h / 24)} d ago`
}

/** dateTime is an absolute local time for titles and detail rows. */
export function dateTime(isoTime: string): string {
  return new Date(isoTime).toLocaleString("en-GB", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })
}

export function json(v: unknown): string {
  return JSON.stringify(v, null, 2)
}
