import {
  CircleCheckIcon,
  CircleDashedIcon,
  CircleDotDashedIcon,
  CircleSlashIcon,
  CircleXIcon,
  HourglassIcon,
  LoaderIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { STATUS_HINT, STATUS_LABEL } from "@/lib/api/status"
import type { DisplayStatus } from "@/lib/api/status"
import { cn } from "@/lib/utils"

// Run and session states (lib/run-state.ts). Every state has an icon and
// a word as well as a colour; the dot alone is used only in table rows,
// where it carries a tooltip and a screen-reader label.

const STYLE: Record<
  DisplayStatus,
  { icon: LucideIcon; dot: string; text: string; ground: string }
> = {
  queued: {
    icon: CircleDotDashedIcon,
    dot: "border border-neutral bg-neutral-muted",
    text: "text-neutral-foreground",
    ground: "bg-neutral-muted",
  },
  running: {
    icon: LoaderIcon,
    dot: "bg-lane-1 live-pulse",
    text: "text-foreground",
    ground: "bg-neutral-muted",
  },
  succeeded: {
    icon: CircleCheckIcon,
    dot: "bg-success",
    text: "text-success-foreground",
    ground: "bg-success-muted",
  },
  failed: {
    icon: CircleXIcon,
    dot: "bg-danger",
    text: "text-danger-foreground",
    ground: "bg-danger-muted",
  },
  waiting: {
    icon: HourglassIcon,
    dot: "bg-warning",
    text: "text-warning-foreground",
    ground: "bg-warning-muted",
  },
  cancelled: {
    icon: CircleSlashIcon,
    dot: "bg-neutral",
    text: "text-neutral-foreground",
    ground: "bg-neutral-muted",
  },
  interrupted: {
    icon: CircleDashedIcon,
    dot: "border border-neutral bg-transparent",
    text: "text-neutral-foreground",
    ground: "bg-neutral-muted",
  },
}

/** StatusDot: the 8px state mark of a table row. */
export function StatusDot({
  state,
  className,
}: {
  state: DisplayStatus
  className?: string
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          role="img"
          aria-label={STATUS_LABEL[state]}
          className={cn(
            "inline-block size-2 flex-none rounded-full",
            STYLE[state].dot,
            className
          )}
        />
      </TooltipTrigger>
      <TooltipContent side="right">
        {STATUS_LABEL[state]}: {STATUS_HINT[state]}
      </TooltipContent>
    </Tooltip>
  )
}

/** StatusBadge: icon + word, for headers and detail rows. */
export function StatusBadge({
  state,
  className,
}: {
  state: DisplayStatus
  className?: string
}) {
  const s = STYLE[state]
  return (
    <span
      title={STATUS_HINT[state]}
      className={cn(
        "inline-flex h-5 flex-none items-center gap-1 rounded-sm px-1.5 text-xs font-medium",
        s.ground,
        s.text,
        className
      )}
    >
      <s.icon
        aria-hidden
        className={cn("size-3", state === "running" && "animate-spin")}
      />
      {STATUS_LABEL[state]}
    </span>
  )
}

/** LivePill: the stream is connected and the view follows it. */
export function LivePill({
  state,
}: {
  state: "connecting" | "live" | "reconnecting"
}) {
  return (
    <span
      className="inline-flex h-5 items-center gap-1.5 rounded-sm border px-1.5 text-xs text-muted-foreground"
      title={
        state === "live"
          ? "Updates arrive as they happen."
          : "The live stream is reconnecting; the view refreshes when it is back."
      }
    >
      <span
        aria-hidden
        className={cn(
          "size-1.5 rounded-full",
          state === "live" ? "live-pulse bg-success" : "bg-neutral"
        )}
      />
      {state === "live"
        ? "Live"
        : state === "connecting"
          ? "Connecting"
          : "Reconnecting"}
    </span>
  )
}
