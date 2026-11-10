import { HourglassIcon } from "lucide-react"

import { DecideForm } from "@/components/console/approval/decide"
import { Button } from "@/components/ui/button"
import type { Approval } from "@/lib/api/types"
import { cn } from "@/lib/utils"

/** ApprovalCallout: the run waits on a person. One sentence, then each
 * pending call with its exact arguments and the decision (DecideForm).
 * It renders nothing when no call is pending. */
export function ApprovalCallout({
  approvals,
  onJump,
  bare = false,
  className,
}: {
  /** The run's approvals; only the pending ones are shown. */
  approvals: readonly Approval[]
  /** "Jump to step": select the waiting row. */
  onJump?: () => void
  /** Inside the detail of the waiting row: no frame, no sentence. */
  bare?: boolean
  className?: string
}) {
  const pending = approvals.filter((a) => a.status === "pending")
  if (pending.length === 0) return null
  const names = [...new Set(pending.map((a) => a.tool))]
  const body = (
    <>
      {pending.map((a) => (
        <div key={a.id} className="flex flex-col gap-1.5">
          {pending.length > 1 && (
            <span className="font-mono text-xs font-medium">{a.tool}</span>
          )}
          <DecideForm approval={a} />
        </div>
      ))}
      {pending.length > 1 && (
        <p className="text-xs text-muted-foreground">
          The run continues when all {pending.length} calls have a decision.
        </p>
      )}
    </>
  )
  if (bare)
    return <div className={cn("flex flex-col gap-3", className)}>{body}</div>
  return (
    <section
      aria-label="Waiting for a decision"
      className={cn(
        "flex flex-none flex-col gap-2 rounded-alert border border-warning/40 bg-warning-muted p-3",
        className
      )}
    >
      <div className="flex items-center gap-2 text-sm">
        <HourglassIcon className="size-3.5 flex-none text-warning-foreground" />
        <p className="min-w-0 flex-1">
          <span className="font-medium">Waiting for a decision.</span> The agent
          wants to call{" "}
          <span className="font-mono text-xs font-medium">
            {names.join(", ")}
          </span>{" "}
          with these arguments.
        </p>
        {onJump && (
          <Button size="sm" variant="ghost" onClick={onJump}>
            Jump to step
          </Button>
        )}
      </div>
      {body}
    </section>
  )
}
