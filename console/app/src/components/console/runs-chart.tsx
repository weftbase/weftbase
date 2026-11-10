import * as React from "react"

import type { StatsBucket } from "@/lib/api/types"
import { count } from "@/lib/format"
import { cn } from "@/lib/utils"

// Runs over time, split by outcome: one stacked bar per bucket, failed on
// top of the rest. Two series only (the server's buckets carry runs and
// failed), neutral and danger, so the split holds for colour-blind
// readers by lightness as well as hue; the legend, the hover readout and
// a table for screen readers carry the same numbers.

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
function niceMax(n: number): number {
  if (n <= 4) return 4
  const p = 10 ** Math.floor(Math.log10(n))
  const f = n / p
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p
}

export function RunsChart({
  series,
  since,
  bucketMs,
  className,
}: {
  series: StatsBucket[]
  /** RFC 3339 start of the first bucket. */
  since: string
  bucketMs: number
  className?: string
}) {
  const [hover, setHover] = React.useState<number | null>(null)
  const start = Date.parse(since)
  const fmt = timeFmt(bucketMs)
  const max = niceMax(Math.max(0, ...series.map((b) => b.runs)))
  const at = (i: number) => fmt.format(new Date(start + i * bucketMs))
  const total = series.reduce((a, b) => a + b.runs, 0)
  const h = hover === null ? undefined : series.at(hover)

  return (
    <figure className={cn("flex flex-col gap-2", className)}>
      <div className="flex h-4 items-center gap-3 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className="size-2 rounded-[2px] bg-chart-ok" />
          Not failed
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className="size-2 rounded-[2px] bg-chart-failed" />
          Failed
        </span>
        <span className="ml-auto font-mono tabular-nums" aria-live="off">
          {h && hover !== null
            ? `${at(hover)} · ${count(h.runs)} runs · ${count(h.failed)} failed`
            : `${count(total)} runs`}
        </span>
      </div>
      <div className="flex gap-2">
        <div
          aria-hidden
          className="flex w-6 flex-none flex-col justify-between text-right font-mono text-[11px] leading-none text-subtle-foreground"
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
            {series.map((b, i) => {
              const ok = b.runs - b.failed
              return (
                <div
                  key={i}
                  onMouseEnter={() => setHover(i)}
                  className={cn(
                    "flex h-full min-w-0 flex-1 flex-col justify-end gap-0.5",
                    hover === i && "bg-muted"
                  )}
                >
                  {b.failed > 0 && (
                    <span
                      className="w-full rounded-t-[3px] bg-chart-failed"
                      style={{
                        height: `max(2px, ${(b.failed / max) * 100}%)`,
                      }}
                    />
                  )}
                  {ok > 0 && (
                    <span
                      className={cn(
                        "w-full bg-chart-ok",
                        b.failed === 0 && "rounded-t-[3px]"
                      )}
                      style={{ height: `max(2px, ${(ok / max) * 100}%)` }}
                    />
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>
      <div
        aria-hidden
        className="flex justify-between pl-8 font-mono text-[11px] leading-none text-subtle-foreground"
      >
        <span>{at(0)}</span>
        <span>{at(Math.floor(series.length / 2))}</span>
        <span>now</span>
      </div>
      <table className="sr-only">
        <caption>Runs per interval</caption>
        <thead>
          <tr>
            <th>From</th>
            <th>Runs</th>
            <th>Failed</th>
          </tr>
        </thead>
        <tbody>
          {series.map((b, i) => (
            <tr key={i}>
              <td>{at(i)}</td>
              <td>{b.runs}</td>
              <td>{b.failed}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </figure>
  )
}
