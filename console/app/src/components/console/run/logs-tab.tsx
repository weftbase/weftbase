import { useInfiniteQuery } from "@tanstack/react-query"
import * as React from "react"

import { EmptyState, ErrorState } from "@/components/console/empty-state"
import { FilterSelect, SearchInput } from "@/components/console/filter-bar"
import { Button } from "@/components/ui/button"
import { logFrontier, treeLogsQuery, treeRunIds } from "@/lib/api/chain"
import type { ChainView, LogLine } from "@/lib/api/chain"
import { count, offset } from "@/lib/format"
import { cn } from "@/lib/utils"

// The Logs tab: the app's log lines for this run and every run it
// started, at any depth, in one time order. One filter row (level,
// text); a line links to the step it was written in.
//
// The server pages a run's logs, one run at a time. They are read in
// rounds (lib/api/chain.ts, "The logs of a tree"): the first rounds by
// themselves, up to AUTO_ROUNDS or AUTO_LINES, the rest one "Load more"
// at a time. Only lines up to the point every run has been read to are
// shown, so a later round adds lines at the end, never in the middle.

const LEVELS = ["debug", "info", "warn", "error"] as const
type Level = (typeof LEVELS)[number]
const FLOOR: Record<Level, number> = { debug: 0, info: 9, warn: 13, error: 17 }
const AUTO_ROUNDS = 4
const AUTO_LINES = 2000
// While the run is going: read again when it has moved, this far apart.
const LIVE_EVERY_MS = 3000

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
  const ids = React.useMemo(() => treeRunIds(chain), [chain])
  // The level is the server's to filter: fewer pages to read.
  const logs = useInfiniteQuery({
    ...treeLogsQuery(
      chain.run.id,
      ids,
      level && level !== "debug" ? level : undefined
    ),
    // A new child run or another level: the lines on screen stay until
    // the new read answers.
    placeholderData: (prev) => prev,
  })
  const {
    data,
    hasNextPage,
    isFetching,
    isFetchingNextPage,
    fetchNextPage,
    refetch,
  } = logs
  const rounds = data?.pages
  const last = rounds?.at(-1)

  const { rows, held } = React.useMemo(() => {
    // A running run's indexes may shift between rounds: the newest read
    // of a line wins.
    const byKey = new Map<string, LogLine>()
    for (const r of rounds ?? [])
      for (const l of r.lines) byKey.set(`${l.run}:${l.index}`, l)
    const all = [...byKey.values()].sort((a, b) =>
      a.time < b.time ? -1 : a.time > b.time ? 1 : a.index - b.index
    )
    const frontier = logFrontier(last?.more)
    const shown = all.filter((l) => Date.parse(l.time) <= frontier)
    return { rows: shown, held: all.length - shown.length }
  }, [rounds, last])

  // The first rounds read themselves; after that a person asks.
  const loaded = rows.length + held
  const unread = Object.values(last?.more ?? {}).some((c) => c.at === null)
  const auto =
    hasNextPage &&
    !logs.isError &&
    (unread ||
      rows.length === 0 ||
      ((rounds?.length ?? 0) < AUTO_ROUNDS && loaded < AUTO_LINES))
  React.useEffect(() => {
    if (auto && !isFetching) void fetchNextPage()
  }, [auto, isFetching, fetchNextPage, rounds?.length])

  // A run that is going writes more lines: read again when it has moved
  // (the chain is refetched by the runs stream), never more often than
  // LIVE_EVERY_MS and never on top of a read that is under way.
  const open = chain.status === "running" || chain.status === "queued"
  const seen = React.useRef({ chain, at: 0 })
  React.useEffect(() => {
    if (seen.current.chain === chain) return
    seen.current.chain = chain
    if (!open && !last?.partial) return
    const wait = Math.max(0, seen.current.at + LIVE_EVERY_MS - Date.now())
    const timer = setTimeout(() => {
      seen.current.at = Date.now()
      void refetch({ cancelRefetch: false })
    }, wait)
    return () => clearTimeout(timer)
  }, [chain, open, last?.partial, refetch])

  const needle = text.trim().toLowerCase()
  const shown = rows.filter(
    (l) =>
      l.severity_number >= FLOOR[level ?? "debug"] &&
      (!needle ||
        l.body.toLowerCase().includes(needle) ||
        attrsLine(l.attrs).toLowerCase().includes(needle))
  )
  const why = rounds?.find((r) => r.note)?.note
  const partial = rounds?.find((r) => r.partial)?.partial
  const failed = rounds?.find((r) => r.failed)?.failed
  const filtered = shown.length !== rows.length
  const more = hasNextPage ? "+" : ""
  // A park and its resumes are one run to the reader: only the runs it
  // started are counted beside the lines.
  const children = ids.length - chain.runs.length

  if (logs.error && !data)
    return <ErrorState error={logs.error} what="The logs" />
  if (failed !== undefined && loaded === 0)
    return <ErrorState error={failed} what="The logs" />
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
          {filtered
            ? `${count(shown.length)} of ${count(rows.length)}${more} lines`
            : `${count(rows.length)}${more} ${rows.length === 1 && !more ? "line" : "lines"}`}
          {children > 0 &&
            ` · ${count(children)} child ${children === 1 ? "run" : "runs"}`}
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {!data || (rows.length === 0 && (auto || isFetchingNextPage)) ? (
          <p className="p-3 text-xs text-muted-foreground">Loading the logs…</p>
        ) : rows.length === 0 ? (
          <EmptyState title="No log lines for this run">
            {level && level !== "debug"
              ? `No line at ${level} or above.`
              : (why?.reason ??
                "The app wrote no log lines while this run was going, or its logs are not bridged to weft.")}
            {(!level || level === "debug") && why?.fix && ` ${why.fix}.`}
          </EmptyState>
        ) : shown.length === 0 ? (
          <EmptyState title="No matching lines">
            {hasNextPage
              ? "Nothing matches in the lines read so far."
              : "Nothing matches the filter."}
          </EmptyState>
        ) : (
          <ul className="font-mono text-xs leading-[18px]">
            {shown.map((l) => {
              const link = l.span_id && known(l.span_id)
              return (
                <li
                  key={`${l.run}:${l.index}`}
                  className="group/line flex items-baseline gap-3 border-b px-3 py-1 last:border-b-0 hover:bg-muted/60"
                >
                  <span
                    className="w-16 flex-none text-right text-subtle-foreground tabular-nums"
                    title={l.time}
                  >
                    {offset(Date.parse(l.time) - t0)}
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
                      className="flex-none rounded-sm font-sans text-muted-foreground opacity-0 outline-none group-hover/line:opacity-100 hover:text-foreground hover:underline focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-ring"
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
      {(hasNextPage || partial || failed !== undefined) && rows.length > 0 && (
        <div className="flex min-h-9 flex-none items-center gap-3 border-t px-3 py-1 text-xs text-muted-foreground">
          <p className="min-w-0 flex-1 truncate" title={partial}>
            {hasNextPage
              ? `Read up to ${offset(Date.parse(rows.at(-1)!.time) - t0)}; there are later lines.`
              : failed !== undefined
                ? "The logs of one of the runs could not be read."
                : partial}
          </p>
          {hasNextPage && (
            <Button
              size="sm"
              variant="secondary"
              disabled={isFetching}
              onClick={() => void fetchNextPage()}
            >
              {isFetchingNextPage ? "Loading…" : "Load more"}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
