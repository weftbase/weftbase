import type { SparkPoint } from "@/lib/stats"
import { cn } from "@/lib/utils"

// A sparkline: one thin bar per bucket, oldest first, the failed part of
// each in the danger colour on top. It shows the shape only; the row's
// own cells carry the numbers and `label` says what it draws.
export function Sparkline({
  points,
  label,
  className,
}: {
  points: readonly SparkPoint[]
  /** "Runs per hour, last 24 hours": the title and the accessible name. */
  label: string
  className?: string
}) {
  const max = Math.max(0, ...points.map((p) => p.total))
  if (max === 0)
    return (
      <span
        role="img"
        aria-label={`${label}: none`}
        title={`${label}: none`}
        className={cn("inline-block h-4 w-[94px] border-b", className)}
      />
    )
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className={cn(
        "inline-flex h-4 w-[94px] items-end gap-px border-b",
        className
      )}
    >
      {points.map((p, i) => {
        const ok = p.total - p.failed
        return (
          <span
            key={i}
            className="flex h-full min-w-0 flex-1 flex-col justify-end"
          >
            {p.failed > 0 && (
              <span
                className="w-full bg-chart-failed"
                style={{ height: `max(2px, ${(p.failed / max) * 100}%)` }}
              />
            )}
            {ok > 0 && (
              <span
                className="w-full bg-chart-ok"
                style={{ height: `max(2px, ${(ok / max) * 100}%)` }}
              />
            )}
          </span>
        )
      })}
    </span>
  )
}
