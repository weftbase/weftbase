import { useQueries } from "@tanstack/react-query"
import * as React from "react"

import { EmptyState, ErrorState } from "@/components/console/empty-state"
import { FilterSelect, SearchInput } from "@/components/console/filter-bar"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import type { LogRow } from "@/lib/api/types"
import { duration } from "@/lib/format"
import { cn } from "@/lib/utils"

// The Logs tab: the app's log lines for this run and the runs it started,
// in time order. One filter row (level, text); a line links to the step
// it was written in.

const LEVELS = ["debug", "info", "warn", "error"] as const
type Level = (typeof LEVELS)[number]
const FLOOR: Record<Level, number> = { debug: 0, info: 9, warn: 13, error: 17 }
const MAX_RUNS = 24

const tone = (n: number) =>
  n >= 17
    ? "text-danger-foreground"
    : n >= 13
      ? "text-warning-foreground"
      : n >= 9
        ? "text-foreground"
        : "text-subtle-foreground"

const attrsLine = (a: Record<string, unknown>) =>
  Object.entries(a)
    .filter(([k]) => k !== "run")
    .map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`)
    .join(" ")

export function LogsTab({
  chain,
  t0,
  known,
  onStep,
}: {
  chain: ChainView
  /** The start of the trace, for the offsets. */
  t0: number
  /** Whether a span id is a row of the timeline. */
  known: (spanId: string) => boolean
  /** Show the step a line was written in. */
  onStep: (spanId: string) => void
}) {
  const [level, setLevel] = React.useState<Level | undefined>(undefined)
  const [text, setText] = React.useState("")
  const ids = React.useMemo(
    () =>
      [
        ...chain.runs.map((r) => r.id),
        ...chain.runs.flatMap((r) => r.children.map((c) => c.id)),
      ].slice(0, MAX_RUNS),
    [chain.runs]
  )
  const results = useQueries({ queries: ids.map((id) => q.logs(id)) })
  const loading = results.some((r) => r.isLoading)
  const firstError = results.find((r) => r.error)?.error
  const pages = results.map((r) => r.data).filter((d) => d !== undefined)
  const rows = React.useMemo(() => {
    const all: (LogRow & { run: string })[] = []
    results.forEach((r, i) => {
      for (const l of r.data?.logs ?? []) all.push({ ...l, run: ids[i] })
    })
    return all.sort((a, b) => (a.time < b.time ? -1 : a.time > b.time ? 1 : 0))
    // The pages' identities change only when a query refetches.
  }, [results.map((r) => r.dataUpdatedAt).join(","), ids])
  const needle = text.trim().toLowerCase()
  const shown = rows.filter(
    (l) =>
      l.severity_number >= FLOOR[level ?? "debug"] &&
      (!needle ||
        l.body.toLowerCase().includes(needle) ||
        attrsLine(l.attrs).toLowerCase().includes(needle))
  )
  const why = pages.find((p) => p.reason)
  const partial = pages.find((p) => p.partial)

  if (firstError && rows.length === 0)
    return <ErrorState error={firstError} what="The logs" />
  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-alert border bg-card">
      <div className="flex h-9 flex-none items-center gap-2 border-b px-2">
        <SearchInput
          value={text}
          onChange={(v) => setText(v ?? "")}
          placeholder="Filter log lines"
        />
        <FilterSelect
          label="Level"
          value={level}
          onChange={setLevel}
          options={LEVELS.map((l) => ({ value: l, label: `${l} and above` }))}
        />
        <span className="ml-auto font-mono text-xs text-muted-foreground">
          {shown.length === rows.length
            ? `${rows.length} lines`
            : `${shown.length} of ${rows.length} lines`}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {loading && rows.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">Loading the logs…</p>
        ) : rows.length === 0 ? (
          <EmptyState title="No log lines for this run">
            {why?.reason ??
              "The app wrote no log lines while this run was going, or its logs are not bridged to weft."}
            {why?.fix && ` ${why.fix}.`}
          </EmptyState>
        ) : shown.length === 0 ? (
          <EmptyState title="No matching lines">
            Nothing matches the filter.
          </EmptyState>
        ) : (
          <ul className="font-mono text-xs leading-[18px]">
            {shown.map((l) => {
              const link = l.span_id && known(l.span_id)
              return (
                <li
                  key={`${l.run}:${l.index}`}
                  className="flex items-baseline gap-3 border-b px-3 py-1 last:border-b-0 hover:bg-muted/60"
                >
                  <span
                    className="w-16 flex-none text-right text-subtle-foreground tabular-nums"
                    title={l.time}
                  >
                    +{duration(Math.max(0, Date.parse(l.time) - t0))}
                  </span>
                  <span
                    className={cn(
                      "w-10 flex-none uppercase",
                      tone(l.severity_number)
                    )}
                  >
                    {l.severity.toLowerCase().slice(0, 5)}
                  </span>
                  <span className="min-w-0 flex-1 break-words">
                    <span
                      className={cn("font-medium", tone(l.severity_number))}
                    >
                      {l.body}
                    </span>{" "}
                    <span className="text-muted-foreground">
                      {attrsLine(l.attrs)}
                    </span>
                  </span>
                  {link && (
                    <button
                      type="button"
                      onClick={() => onStep(l.span_id!)}
                      className="flex-none rounded-sm font-sans text-muted-foreground outline-none hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                    >
                      Go to step
                    </button>
                  )}
                </li>
              )
            })}
          </ul>
        )}
      </div>
      {(partial || ids.length === MAX_RUNS) && (
        <p className="flex-none border-t px-3 py-1.5 text-xs text-muted-foreground">
          {partial?.partial_reason ??
            `Showing the logs of the first ${MAX_RUNS} runs of this tree.`}
        </p>
      )}
    </div>
  )
}
