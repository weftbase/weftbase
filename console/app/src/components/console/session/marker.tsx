import { Link } from "@tanstack/react-router"
import {
  ArrowRightIcon,
  ChevronRightIcon,
  CircleIcon,
  CornerDownRightIcon,
  GitForkIcon,
  HourglassIcon,
  KeyRoundIcon,
  MessageSquarePlusIcon,
  ShieldCheckIcon,
  ShrinkIcon,
  TagIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"
import * as React from "react"

import { DecideForm } from "@/components/console/approval/decide"
import { JsonView } from "@/components/console/json-view"
import type { Approval } from "@/lib/api/types"
import { cn } from "@/lib/utils"

import type { Marker, MarkerKind } from "./model"

const ICON: Record<MarkerKind, LucideIcon> = {
  wait: HourglassIcon,
  approval: ShieldCheckIcon,
  grant: KeyRoundIcon,
  message: MessageSquarePlusIcon,
  compaction: ShrinkIcon,
  fork: GitForkIcon,
  handoff: CornerDownRightIcon,
  label: TagIcon,
  other: CircleIcon,
}

/** MarkerLine: one thing that happened in the conversation besides the
 * messages, as one line. Click (or Enter) opens who / how / when under
 * it. */
export function MarkerLine({ marker }: { marker: Marker }) {
  const [open, setOpen] = React.useState(false)
  const Icon = ICON[marker.kind]
  return (
    <div className="min-w-0">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="group/marker flex min-h-6 w-full min-w-0 items-center gap-1.5 rounded-sm py-0.5 pr-1 text-left text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <Icon
          aria-hidden
          className={cn(
            "size-3.5 flex-none text-subtle-foreground",
            marker.tone === "warning" && "text-warning-foreground",
            marker.tone === "danger" && "text-danger-foreground"
          )}
        />
        <span className="min-w-0 truncate" title={marker.text}>
          {marker.text}
        </span>
        <ChevronRightIcon
          aria-hidden
          className={cn(
            "size-3 flex-none text-subtle-foreground opacity-0 transition-transform group-hover/marker:opacity-100 group-focus-visible/marker:opacity-100",
            open && "rotate-90 opacity-100"
          )}
        />
      </button>
      {open && <MarkerFacts marker={marker} />}
    </div>
  )
}

function MarkerFacts({ marker }: { marker: Marker }) {
  return (
    <div className="mt-1 mb-1.5 ml-5 flex flex-col gap-2 rounded-input border bg-card p-2">
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        {marker.facts.map((f, i) => (
          <React.Fragment key={i}>
            <dt className="text-muted-foreground">{f.label}</dt>
            <dd className={cn("min-w-0 break-words", f.mono && "font-mono")}>
              {f.value}
            </dd>
          </React.Fragment>
        ))}
        {marker.session && (
          <>
            <dt className="text-muted-foreground">Open</dt>
            <dd>
              <SessionLink id={marker.session} />
            </dd>
          </>
        )}
      </dl>
      {marker.args?.map((a, i) => (
        <div key={i} className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">
            Arguments of <span className="font-mono">{a.tool}</span>
          </span>
          <JsonView value={a.value} depth={2} />
        </div>
      ))}
    </div>
  )
}

export function SessionLink({ id }: { id: string }) {
  return (
    <Link
      to="/sessions/$sessionId"
      params={{ sessionId: id }}
      className="inline-flex max-w-full items-center gap-1 font-mono text-xs underline-offset-2 hover:underline"
    >
      <span className="truncate">{id}</span>
      <ArrowRightIcon aria-hidden className="size-3 flex-none" />
    </Link>
  )
}

/** OpenWait: the turn waits on a person now. The line, then each
 * pending call with its exact arguments and the decision. */
export function OpenWait({
  marker,
  approvals,
}: {
  marker: Marker
  approvals: readonly Approval[]
}) {
  const pending = approvals.filter((a) => a.status === "pending")
  const decided = approvals.length - pending.length
  return (
    <section
      aria-label="Waiting for a decision"
      className="flex min-w-0 flex-col gap-2 rounded-alert border border-warning/40 bg-warning-muted p-3"
    >
      <p className="flex items-center gap-1.5 text-xs">
        <HourglassIcon
          aria-hidden
          className="size-3.5 flex-none text-warning-foreground"
        />
        <span className="font-medium">{marker.text}</span>
        {decided > 0 && (
          <span className="text-muted-foreground">
            · {decided} of {approvals.length} decided; the turn continues when
            all are
          </span>
        )}
      </p>
      {pending.map((a) => (
        <div key={a.id} className="flex min-w-0 flex-col gap-1.5">
          <span className="text-xs text-muted-foreground">
            The agent wants to call{" "}
            <span className="font-mono font-medium text-foreground">
              {a.tool}
            </span>{" "}
            with these arguments.
          </span>
          <DecideForm approval={a} />
        </div>
      ))}
      {pending.length === 0 && (
        <p className="text-xs text-muted-foreground">
          Every call has a decision; the turn is about to continue.
        </p>
      )}
    </section>
  )
}
