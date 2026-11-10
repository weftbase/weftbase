// Number, time and money formats. One place, so every screen agrees.
// The rule: a value the server does not know reads "unknown" or "—",
// never a made-up zero.

import type { CostFields, Usage } from "./api/types"

export const DASH = "—"

const nf = new Intl.NumberFormat("en-US")

/** count is a whole number with thousands separators: "12,480". */
export function count(n: number | null | undefined): string {
  return n === null || n === undefined ? DASH : nf.format(n)
}

/** tokens is compact above 10k: "842", "9,120", "12.4k", "3.1M". */
export function tokens(n: number | null | undefined): string {
  if (n === null || n === undefined) return DASH
  if (n < 10_000) return nf.format(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

/** totalTokens is input + output of a usage. */
export function totalTokens(u: Usage | undefined): number | undefined {
  return u ? u.input_tokens + u.output_tokens : undefined
}

/** duration renders milliseconds: "820ms", "4.2s", "2m 04s", "1h 12m".
 * null or undefined (still going, or not recorded) is a dash. */
export function duration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return DASH
  if (ms < 1) return "<1ms"
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`
  const s = Math.round(ms / 1000)
  if (s < 3600)
    return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, "0")}m`
}

/** between is the milliseconds from start to end; a run that has not
 * finished is measured to `now`. undefined when start is missing. */
export function between(
  start: string | null | undefined,
  end: string | null | undefined,
  now = Date.now()
): number | undefined {
  if (!start) return undefined
  const a = Date.parse(start)
  const b = end ? Date.parse(end) : now
  return Number.isNaN(a) || Number.isNaN(b) ? undefined : Math.max(0, b - a)
}

/** usd formats a known amount: "$0", "$0.0042", "$1.50", "$1,204". */
export function usd(n: number): string {
  if (n === 0) return "$0"
  if (n < 0.01) return `$${n.toFixed(4)}`
  if (n < 1000) return `$${n.toFixed(2)}`
  return `$${nf.format(Math.round(n))}`
}

/** cost renders the server's cost fields. An unknown price is "unknown",
 * never "$0.00"; a partly known sum is "≥ $x". */
export function cost(
  c:
    | Pick<CostFields, "cost_usd" | "cost_known" | "cost_partial">
    | null
    | undefined
): string {
  if (!c) return DASH
  if (c.cost_known && c.cost_usd !== null) return usd(c.cost_usd)
  if (c.cost_partial && typeof c.cost_partial.usd === "number")
    return `≥ ${usd(c.cost_partial.usd)}`
  return "unknown"
}

/** percent renders a 0..1 ratio: "0%", "4.2%", "100%". null is a dash. */
export function percent(r: number | null | undefined): string {
  if (r === null || r === undefined) return DASH
  const p = r * 100
  return `${p === 0 || p >= 10 ? Math.round(p) : p.toFixed(1)}%`
}

/** ago is "now", "42s ago", "5m ago", "3h ago", "4d ago". */
export function ago(isoTime: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - Date.parse(isoTime)) / 1000))
  if (s < 5) return "now"
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
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

/** bytes renders a size: "512 B", "23.4 kB", "1.2 MB". */
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return DASH
  if (n < 1000) return `${n} B`
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)} kB`
  if (n < 1_000_000_000) return `${(n / 1_000_000).toFixed(1)} MB`
  return `${(n / 1_000_000_000).toFixed(1)} GB`
}

/** shortId keeps the end of a long id, where ULIDs and hashes differ. */
export function shortId(id: string, keep = 8): string {
  return id.length <= keep + 4 ? id : `…${id.slice(-keep)}`
}
