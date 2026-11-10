import { Link } from "@tanstack/react-router"

import { useNow } from "@/components/console/relative-time"
import { Status } from "@/components/console/status"
import type { ChainView } from "@/lib/api/chain"
import {
  DASH,
  between,
  cost,
  count,
  duration,
  shortId,
  tokens,
} from "@/lib/format"
import { cn } from "@/lib/utils"

// The totals of the two runs, one line each with the figures in columns,
// so two numbers that differ sit one over the other. A compact table in
// one bordered container; no cards.

const COLS =
  "grid grid-cols-[6.5rem_minmax(0,1.4fr)_repeat(5,minmax(0,1fr))] items-center"

export function SideTag({ side }: { side: "a" | "b" }) {
  return (
    <span className="inline-flex size-4 flex-none items-center justify-center rounded-sm border font-mono text-xs leading-none font-medium text-muted-foreground uppercase">
      {side}
    </span>
  )
}

/** RunLink: a run named by its agent and the end of its id. */
export function RunLink({
  chain,
  className,
}: {
  chain: ChainView
  className?: string
}) {
  return (
    <Link
      to="/runs/$runId"
      params={{ runId: chain.run.id }}
      title={`Open run ${chain.run.id}`}
      className={cn(
        "inline-flex min-w-0 items-center gap-1.5 rounded-sm outline-none hover:underline focus-visible:outline-2 focus-visible:outline-ring",
        className
      )}
    >
      <span className="truncate text-foreground">{chain.run.agent}</span>
      <span className="flex-none font-mono text-xs text-muted-foreground">
        {shortId(chain.run.id)}
      </span>
    </Link>
  )
}

export function CompareTotals({ a, b }: { a: ChainView; b: ChainView }) {
  const now = useNow()
  return (
    <div
      role="table"
      aria-label="Totals of the two runs"
      className="flex-none overflow-hidden rounded-alert border bg-card"
    >
      <div role="row" className={cn(COLS, "h-7 border-b")}>
        {["", "Run", "Status", "Duration", "Tokens", "Cost", "Steps"].map(
          (h, i) => (
            <div
              key={i}
              role="columnheader"
              className={cn("truncate px-3 table-head", i === 1 && "px-0")}
            >
              {h}
            </div>
          )
        )}
      </div>
      {(
        [
          ["a", a],
          ["b", b],
        ] as const
      ).map(([side, chain]) => {
        const t = chain.totals
        const used = t.usage.input_tokens + t.usage.output_tokens
        const waited =
          t.waitingMs > 0 ? `of which ${duration(t.waitingMs)} waiting` : ""
        return (
          <div
            key={side}
            role="row"
            className={cn(COLS, "h-8 border-b last:border-b-0")}
          >
            <div role="rowheader" className="flex items-center gap-1.5 px-3">
              <SideTag side={side} />
            </div>
            <div role="cell" className="flex min-w-0 text-sm">
              <RunLink chain={chain} />
            </div>
            <div role="cell" className="min-w-0 px-3">
              <Status state={chain.status} />
            </div>
            {[
              [duration(t.durationMs ?? between(t.started, null, now)), waited],
              [
                used > 0 ? tokens(used) : DASH,
                used > 0
                  ? `${tokens(t.usage.input_tokens)} input, ${tokens(t.usage.output_tokens)} output`
                  : "",
              ],
              [used > 0 ? cost(t) : DASH, t.cost_reason ?? ""],
              [count(t.steps), ""],
            ].map(([value, title], i) => (
              <div
                key={i}
                role="cell"
                title={title || undefined}
                className="truncate px-3 font-mono text-xs tabular-nums"
              >
                {value}
              </div>
            ))}
          </div>
        )
      })}
    </div>
  )
}
