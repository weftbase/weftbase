import { STATUS_LABEL } from "@/lib/api/status"
import type { DisplayStatus } from "@/lib/api/status"
import type { SessionRow } from "@/lib/api/types"
import { cn } from "@/lib/utils"

import { outcomeStrip } from "./model"

const DOT: Record<DisplayStatus, string> = {
  queued: "border border-neutral bg-neutral-muted",
  running: "bg-lane-1",
  succeeded: "bg-success",
  failed: "bg-danger",
  waiting: "bg-warning",
  cancelled: "bg-neutral",
  interrupted: "border border-neutral",
}

/** OutcomeStrip: one small dot per turn, oldest first, coloured by how
 * the turn ended. "+n" stands for older turns it leaves out. The dots
 * are a picture of the words in its title. */
export function OutcomeStrip({
  session,
  max,
  className,
}: {
  session: Pick<SessionRow, "turns" | "resumes" | "outcomes">
  max?: number
  className?: string
}) {
  const strip = outcomeStrip(session, max)
  if (strip.dots.length === 0) return null
  return (
    <span
      role="img"
      aria-label={strip.title}
      title={strip.title}
      className={cn("inline-flex flex-none items-center gap-0.5", className)}
    >
      {strip.earlier > 0 && (
        <span className="mr-0.5 font-mono text-[10px] leading-none text-subtle-foreground">
          +{strip.earlier}
        </span>
      )}
      {strip.dots.map((d, i) => (
        <span
          key={i}
          data-outcome={d}
          title={STATUS_LABEL[d]}
          className={cn("size-1.5 rounded-full", DOT[d])}
        />
      ))}
    </span>
  )
}
