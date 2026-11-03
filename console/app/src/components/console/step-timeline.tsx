import { Link } from "@tanstack/react-router"
import { BotIcon, GitForkIcon, ShieldCheckIcon, WrenchIcon } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import type { Step, StepKind } from "@/lib/api/types"
import { duration } from "@/lib/format"
import { cn } from "@/lib/utils"

// Timeline (design system): the steps of one run. A 31px icon box, the
// title and duration on one line, one detail line below, a 1px line
// joining the icons. A waiting step is amber: a person needs to act.

const ICON: Record<StepKind, LucideIcon> = {
  model: BotIcon,
  tool: WrenchIcon,
  subagent: GitForkIcon,
  approval: ShieldCheckIcon,
}

export function stepTitle(s: Step): string {
  switch (s.kind) {
    case "model":
      return "Model call"
    case "tool":
      return `Tool · ${s.name}`
    case "subagent":
      return `Subagent · ${s.name}`
    case "approval":
      return `Approval · ${s.name}`
  }
}

export function StepTimeline({
  steps,
  runId,
  selected,
  total,
}: {
  steps: Step[]
  /** With a run id, each step links to its selection on the run page. */
  runId?: string
  selected?: number
  /** The run's length in ms; draws a waterfall bar under each step. */
  total?: number
}) {
  return (
    <ol className="flex flex-col">
      {steps.map((s, i) => {
        const Icon = ICON[s.kind]
        const waiting = s.status === "waiting"
        const failed = s.status === "error"
        const body = (
          <>
            {i < steps.length - 1 && (
              <span
                aria-hidden
                className="absolute top-[34px] -bottom-1.5 left-[15px] w-px bg-border"
              />
            )}
            <span
              className={cn(
                "flex size-[31px] flex-none items-center justify-center rounded-input border bg-muted text-muted-foreground",
                waiting && "text-warning-foreground",
                failed && "text-danger-foreground"
              )}
            >
              <Icon className="size-4" />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex justify-between gap-2">
                <span className="truncate text-sm font-medium">
                  {stepTitle(s)}
                </span>
                <span
                  className={cn(
                    "font-mono text-xs text-subtle-foreground",
                    waiting && "text-warning-foreground",
                    failed && "text-danger-foreground"
                  )}
                >
                  {waiting
                    ? "waiting"
                    : failed
                      ? "failed"
                      : duration(s.duration_ms)}
                </span>
              </span>
              <span className="mt-0.5 block truncate text-[13px] text-muted-foreground">
                {s.summary}
              </span>
              {total !== undefined && total > 0 && (
                <span
                  aria-hidden
                  className="mt-2 block h-1 rounded-full bg-muted"
                >
                  <span
                    className={cn(
                      "block h-1 rounded-full bg-chart-3",
                      failed && "bg-danger",
                      waiting && "bg-warning"
                    )}
                    style={{
                      marginLeft: `${(s.offset_ms / total) * 100}%`,
                      width: `${Math.max(1.5, ((s.duration_ms ?? total - s.offset_ms) / total) * 100)}%`,
                    }}
                  />
                </span>
              )}
            </span>
          </>
        )
        const rowClass = cn(
          "relative flex gap-3 pb-[18px]",
          runId && "-mx-2 rounded-md px-2 pt-2 hover:bg-muted/60",
          runId &&
            selected === s.index &&
            "bg-table-row-selected shadow-nav-active hover:bg-table-row-selected"
        )
        return (
          <li key={s.index}>
            {runId ? (
              <Link
                to="/runs/$runId"
                params={{ runId }}
                search={(prev) => ({ ...prev, view: undefined, step: s.index })}
                className={rowClass}
                aria-current={selected === s.index ? "step" : undefined}
              >
                {body}
              </Link>
            ) : (
              <div className={rowClass}>{body}</div>
            )}
          </li>
        )
      })}
    </ol>
  )
}
