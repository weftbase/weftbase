import * as React from "react"

import { cn } from "@/lib/utils"

export interface Stat {
  label: string
  /** Already formatted (lib/format.ts). "unknown" and "—" are values. */
  value: React.ReactNode
  /** One muted word or figure after the value ("in 1.2k · out 340"). */
  hint?: React.ReactNode
  /** Why the value is what it is (a cost's reason); shown on hover. */
  title?: string
  tone?: "default" | "danger" | "warning"
}

// The stat strip: one bordered container, equal cells split by hairlines,
// a small muted label over a plain larger value. No cards inside it and
// none around it. Used on the overview (4 cells) and the run page (6).
export function StatStrip({
  stats,
  className,
}: {
  stats: Stat[]
  className?: string
}) {
  return (
    <dl
      className={cn(
        "grid flex-none overflow-hidden rounded-alert border bg-card max-sm:grid-cols-2",
        className
      )}
      style={{ gridTemplateColumns: `repeat(${stats.length}, minmax(0, 1fr))` }}
    >
      {stats.map((s, i) => (
        <div
          key={s.label}
          title={s.title}
          className={cn(
            "flex min-w-0 flex-col gap-0.5 px-3 py-2",
            i > 0 &&
              "border-l max-sm:border-l-0 max-sm:even:border-l max-sm:[&:nth-child(n+3)]:border-t"
          )}
        >
          <dt className="truncate text-xs text-muted-foreground">{s.label}</dt>
          <dd className="flex min-w-0 items-baseline gap-2">
            <span
              className={cn(
                "truncate stat",
                s.tone === "danger" && "text-danger-foreground",
                s.tone === "warning" && "text-warning-foreground"
              )}
            >
              {s.value}
            </span>
            {s.hint && (
              <span className="truncate text-xs text-subtle-foreground">
                {s.hint}
              </span>
            )}
          </dd>
        </div>
      ))}
    </dl>
  )
}
