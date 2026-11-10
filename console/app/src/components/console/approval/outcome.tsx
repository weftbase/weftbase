import type { ApprovalsOutcome } from "@/lib/api/types"
import { cn } from "@/lib/utils"

// The approval outcome beside a run's status. A run whose approval
// expired or was denied still ends succeeded (the model went on without
// the tool), so the status alone hides that a call was skipped. Pending
// is already the "Waiting" status and approved is the normal path: only
// expired and denied get a chip.

const TEXT: Partial<
  Record<ApprovalsOutcome, { label: string; title: string }>
> = {
  expired: {
    label: "approval expired",
    title:
      "Nobody decided in time: the call was skipped and the run went on without it.",
  },
  denied: {
    label: "approval denied",
    title: "A person denied a call: the run went on without it.",
  },
}

export function approvalOutcomeText(
  outcome: ApprovalsOutcome | undefined
): string | undefined {
  return outcome ? TEXT[outcome]?.label : undefined
}

export function ApprovalOutcome({
  outcome,
  className,
}: {
  outcome: ApprovalsOutcome | undefined
  className?: string
}) {
  const t = outcome ? TEXT[outcome] : undefined
  if (!t) return null
  return (
    <span
      className={cn(
        "flex-none rounded-sm px-1 text-[11px] leading-[18px] font-medium",
        outcome === "expired"
          ? "bg-warning-muted text-warning-foreground"
          : "bg-muted text-muted-foreground",
        className
      )}
      title={t.title}
    >
      {t.label}
    </span>
  )
}
