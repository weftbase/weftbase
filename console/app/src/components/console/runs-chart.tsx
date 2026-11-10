import * as React from "react"

import type { OutcomeBucket } from "@/lib/api/types"
import { count } from "@/lib/format"
import { cn } from "@/lib/utils"

// Runs over time, split by outcome: one stacked bar per bucket. The
// colours are the status colours (tokens in styles.css, stepped so
// neighbours in the stack stay apart for colour-blind readers); the
// stack order is fixed, segments are split by a 2px gap, and the legend,
// the hover readout and a table for screen readers carry the same
// numbers, so colour is never the only cue.

type Key = "succeeded" | "cancelled" | "waiting" | "failed" | "open"

/** Bottom of the stack first. */
const SERIES: { key: Key; label: string; fill: string }[] = [
  { key: "succeeded", label: "Succeeded", fill: "bg-chart-succeeded" },
  { key: "cancelled", label: "Cancelled", fill: "bg-chart-cancelled" },
  { key: "waiting", label: "Waiting", fill: "bg-chart-waiting" },
  { key: "failed", label: "Failed", fill: "bg-chart-failed" },
  { key: "open", label: "In progress", fill: "bg-chart-open" },
]

/** A bucket's runs per series. "open" is what has no outcome yet:
 * running, interrupted, queued. */
export function outcomeCounts(b: OutcomeBucket): Record<Key, number> {
  return {
    succeeded: b.succeeded,
    cancelled: b.cancelled,
    waiting: b.waiting,
    failed: b.failed,
    open: Math.max(
      0,
      b.runs - b.succeeded - b.cancelled - b.waiting - b.failed
    ),
  }
}

const timeFmt = (bucketMs: number) =>
  new Intl.DateTimeFormat(
    "en-GB",
    bucketMs >= 86_400_000
      ? { day: "numeric", month: "short" }
      : bucketMs >= 3_600_000
        ? { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }
        : { hour: "2-digit", minute: "2-digit" }
  )

/** niceMax rounds a maximum up to 1, 2 or 5 × 10^n, at least 4. */
export function niceMax(n: number): number {
  if (n <= 4) return 4
  const p = 10 ** Math.floor(Math.log10(n))
  const f = n / p
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p
}

export function RunsChart({
  series,
  bucketMs,
  className,
}: {
  /** One bucket per interval, oldest first; `t` is its start. */
  series: OutcomeBucket[]
  bucketMs: number
  className?: string
}) {
  const [hover, setHover] = React.useState<number | null>(null)
  const fmt = timeFmt(bucketMs)
  const counts = series.map(outcomeCounts)
  const max = niceMax(Math.max(0, ...series.map((b) => b.runs)))
  const at = (i: number) => {
    const b = series.at(i)
    return b ? fmt.format(new Date(b.t)) : ""
  }
  const total = series.reduce((a, b) => a + b.runs, 0)
  // "In progress" is in the legend only when the window has any.
  const shown = SERIES.filter(
    (s) => s.key !== "open" || counts.some((c) => c.open > 0)
  )
  const h = hover === null ? undefined : counts.at(hover)

  return (
    <figure className={cn("flex flex-col gap-2", className)}>
      <div className="flex min-h-4 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {shown.map((s) => (
          <span key={s.key} className="inline-flex items-center gap-1.5">
            <span aria-hidden className={cn("size-2 rounded-[2px]", s.fill)} />
            {s.label}
            {h && (
              <span className="font-mono text-foreground tabular-nums">
                {count(h[s.key])}
              </span>
            )}
          </span>
        ))}
        <span className="ml-auto font-mono tabular-nums" aria-live="off">
          {h && hover !== null
            ? `from ${at(hover)}`
            : `${count(total)} ${total === 1 ? "run" : "runs"}`}
        </span>
      </div>
      <div className="flex gap-2">
        <div
          aria-hidden
          className="flex w-8 flex-none flex-col justify-between text-right font-mono text-[11px] leading-none text-subtle-foreground"
        >
          <span>{count(max)}</span>
          <span>{count(max / 2)}</span>
          <span>0</span>
        </div>
        <div className="relative h-28 min-w-0 flex-1">
          <div
            aria-hidden
            className="absolute inset-0 flex flex-col justify-between"
          >
            <span className="h-px bg-chart-grid" />
            <span className="h-px bg-chart-grid" />
            <span className="h-px bg-border" />
          </div>
          <div
            className="absolute inset-0 flex items-end gap-0.5"
            onMouseLeave={() => setHover(null)}
          >
            {counts.map((c, i) => {
              const parts = SERIES.filter((s) => c[s.key] > 0)
              return (
                <div
                  key={i}
                  onMouseEnter={() => setHover(i)}
                  className={cn(
                    "flex h-full min-w-0 flex-1 flex-col-reverse justify-start gap-0.5",
                    hover === i && "bg-muted"
                  )}
                >
                  {parts.map((s, j) => (
                    <span
                      key={s.key}
                      className={cn(
                        "w-full flex-none",
                        s.fill,
                        j === parts.length - 1 && "rounded-t-[3px]"
                      )}
                      style={{ height: `max(2px, ${(c[s.key] / max) * 100}%)` }}
                    />
                  ))}
                </div>
              )
            })}
          </div>
        </div>
      </div>
      <div
        aria-hidden
        className="flex justify-between pl-10 font-mono text-[11px] leading-none text-subtle-foreground"
      >
        <span>{at(0)}</span>
        <span>{at(Math.floor(series.length / 2))}</span>
        <span>now</span>
      </div>
      <table className="sr-only">
        <caption>Runs per interval, by outcome</caption>
        <thead>
          <tr>
            <th>From</th>
            {SERIES.map((s) => (
              <th key={s.key}>{s.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {counts.map((c, i) => (
            <tr key={i}>
              <td>{at(i)}</td>
              {SERIES.map((s) => (
                <td key={s.key}>{c[s.key]}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  )
}
