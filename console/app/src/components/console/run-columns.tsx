import { ArrowDownIcon, TriangleAlertIcon } from "lucide-react"
import * as React from "react"

import type { Column } from "@/components/console/data-table"
import { RelativeTime, useNow } from "@/components/console/relative-time"
import { Status } from "@/components/console/status"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import type { RunRow } from "@/lib/api/types"
import { count, duration, tokens } from "@/lib/format"
import {
  rowCause,
  rowChildren,
  rowCost,
  rowIssues,
  rowStatus,
  rowSteps,
  rowTimes,
  rowTokens,
  rowUsage,
  rowVersion,
} from "@/lib/runs"
import type { RunsSort } from "@/lib/search"
import { cn } from "@/lib/utils"

/** A clock that ticks every second, only while something on screen is
 * still going (the shared 10 s clock is too coarse for "elapsed"). */
function useSecond(on: boolean): number {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    if (!on) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [on])
  return on ? Math.max(now, Date.now()) : now
}

const elapsed = (from: string, now: number) => {
  const s = Math.max(0, Math.floor((now - Date.parse(from)) / 1000))
  return s < 60 ? `${s}s` : duration(s * 1000)
}

/** Duration: a finished run's span; a running one's current step and
 * elapsed time; a waiting one's time since it began to wait. */
function Duration({ run }: { run: RunRow }) {
  const status = rowStatus(run)
  const t = rowTimes(run)
  const going = status === "running" || status === "waiting"
  const now = useSecond(going)
  useNow()
  if (status === "running" && t.started) {
    const step = rowSteps(run)
    return (
      <span title="Still running: the step it is on and the time so far">
        {step > 0 && (
          <span className="text-muted-foreground">step {step} · </span>
        )}
        {elapsed(t.started, now)}
      </span>
    )
  }
  if (status === "waiting" && t.waitingSince)
    return (
      <span title="Waiting on a person for this long">
        <span className="text-muted-foreground">waiting </span>
        {elapsed(t.waitingSince, now)}
      </span>
    )
  if (t.durationMs === undefined) return <>—</>
  return (
    <span
      title={
        t.waitingMs > 0
          ? `${duration(t.waitingMs)} of it waiting on a person`
          : undefined
      }
    >
      {duration(t.durationMs)}
    </span>
  )
}

function Title({ run }: { run: RunRow }) {
  const status = rowStatus(run)
  const err = status === "failed" ? run.chain?.err || run.err : ""
  const children = rowChildren(run)
  const issues = rowIssues(run)
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span className="min-w-0 truncate">
        {run.prompt_preview}
        {err && (
          <span className="text-danger-foreground">
            {run.prompt_preview ? " · " : ""}
            {err}
          </span>
        )}
        {!run.prompt_preview && !err && "—"}
      </span>
      {children && (
        <span
          className={cn(
            "flex-none rounded-sm px-1 text-[11px] leading-[18px] font-medium",
            children.failed
              ? "bg-danger-muted text-danger-foreground"
              : "bg-muted text-muted-foreground"
          )}
          title={
            children.failed
              ? `${count(run.children_failed)} of them failed`
              : "Runs this run handed work to"
          }
        >
          {children.label}
        </span>
      )}
      {issues.length > 0 && (
        <Tooltip>
          <TooltipTrigger asChild>
            <TriangleAlertIcon
              role="img"
              aria-label={`Issues: ${issues.join(", ")}`}
              className="size-3.5 flex-none text-warning-foreground"
            />
          </TooltipTrigger>
          <TooltipContent>{issues.join(" · ")}</TooltipContent>
        </Tooltip>
      )}
      {run.derived && (
        <span
          className="flex-none rounded-sm border px-1 text-[11px] leading-4 text-muted-foreground"
          title="Built from OpenTelemetry GenAI spans: no weft records, so steps, messages and logs are not available."
        >
          derived
        </span>
      )}
    </span>
  )
}

function Agent({ run }: { run: RunRow }) {
  const v = rowVersion(run)
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span className="min-w-0 truncate font-medium">{run.agent || "—"}</span>
      {v && (
        <span
          className="flex-none font-mono text-[11px] text-subtle-foreground"
          title={
            v.label
              ? `Version ${v.number}, resolved through the ${v.label} label`
              : `Version ${v.number}`
          }
        >
          v{v.number}
          {v.label && ` ${v.label}`}
        </span>
      )}
    </span>
  )
}

function Tokens({ run }: { run: RunRow }) {
  const n = rowTokens(run)
  if (n === undefined) return <>—</>
  const u = rowUsage(run)
  return (
    <span title={`${count(u.input_tokens)} in · ${count(u.output_tokens)} out`}>
      {tokens(n)}
    </span>
  )
}

export interface SortState {
  /** undefined: newest first (the default). */
  sort: RunsSort | undefined
  onSort: (sort: RunsSort | undefined) => void
}

function SortHeader({
  label,
  by,
  state,
}: {
  label: string
  /** undefined: the default order (started). */
  by: RunsSort | undefined
  state: SortState
}) {
  const active = state.sort === by
  return (
    <button
      type="button"
      aria-pressed={active}
      title={
        active
          ? by
            ? `Sorted by ${label.toLowerCase()}, highest first. Click for newest first.`
            : "Sorted newest first"
          : `Sort by ${label.toLowerCase()}`
      }
      onClick={() => state.onSort(active ? undefined : by)}
      className={cn(
        "-mx-1 inline-flex h-5 items-center gap-0.5 rounded-sm px-1 uppercase outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
        active && "text-foreground"
      )}
    >
      {label}
      {active && <ArrowDownIcon aria-hidden className="size-3" />}
    </button>
  )
}

/** The Runs list's columns: status (dot and word), agent with its
 * version, input, model, steps, duration, tokens, cost, started. With
 * `sort`, the headers of the columns the server can order by are
 * buttons. */
export function runColumns(sort?: SortState): Column<RunRow>[] {
  const head = (label: string, by: RunsSort | undefined) =>
    sort ? <SortHeader label={label} by={by} state={sort} /> : label
  return [
    {
      key: "status",
      header: "Status",
      width: "100px",
      cell: (r) => <Status state={rowStatus(r)} cause={rowCause(r)} />,
    },
    {
      key: "agent",
      header: "Agent",
      width: "minmax(110px, 200px)",
      cell: (r) => <Agent run={r} />,
    },
    {
      key: "input",
      header: "Input",
      width: "minmax(160px, 1fr)",
      muted: true,
      cell: (r) => <Title run={r} />,
    },
    {
      key: "model",
      header: "Model",
      width: "minmax(90px, 150px)",
      muted: true,
      cell: (r) => r.model.name || "—",
    },
    {
      key: "steps",
      header: "Steps",
      width: "52px",
      align: "right",
      mono: true,
      cell: (r) => (rowSteps(r) > 0 ? count(rowSteps(r)) : "—"),
    },
    {
      key: "duration",
      header: head("Duration", "duration"),
      width: "112px",
      align: "right",
      mono: true,
      cell: (r) => <Duration run={r} />,
    },
    {
      key: "tokens",
      header: head("Tokens", "tokens"),
      width: "72px",
      align: "right",
      mono: true,
      cell: (r) => <Tokens run={r} />,
    },
    {
      key: "cost",
      header: head("Cost", "cost"),
      width: "92px",
      align: "right",
      mono: true,
      cell: (r) => <span title={r.cost_reason}>{rowCost(r)}</span>,
    },
    {
      key: "started",
      header: head("Started", undefined),
      width: "76px",
      align: "right",
      muted: true,
      cell: (r) => <RelativeTime time={rowTimes(r).started} />,
    },
  ]
}

/** Reuse for any list of runs (a session's turns, an agent's activity). */
export const RUN_COLUMNS: Column<RunRow>[] = runColumns()

/** The same row without the agent: a list that is already one agent's. */
export const RUN_COLUMNS_OF_AGENT: Column<RunRow>[] = RUN_COLUMNS.filter(
  (c) => c.key !== "agent" && c.key !== "model"
)
