import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { CircleCheckIcon } from "lucide-react"
import * as React from "react"

import { useLiveState } from "@/components/console/app-shell"
import { ApprovalOutcome } from "@/components/console/approval/outcome"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader, Section } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { ErrorKind, RUN_COLUMNS } from "@/components/console/run-columns"
import { RunsChart } from "@/components/console/runs-chart"
import { Segmented } from "@/components/console/segmented"
import { Sparkline } from "@/components/console/sparkline"
import { StatStrip } from "@/components/console/stat-strip"
import { LivePill, Status } from "@/components/console/status"
import { q } from "@/lib/api/queries"
import type { Approval, RunRow } from "@/lib/api/types"
import { argsSummary } from "@/lib/approval-view"
import { count, duration, percent, tokens } from "@/lib/format"
import { rowCause, rowStatus, rowTimes } from "@/lib/runs"
import {
  BREAKDOWNS,
  RANGE_LABEL,
  WINDOWS,
  sinceOf,
  validateOverview,
} from "@/lib/search"
import type { Breakdown, RunsSearch, Window } from "@/lib/search"
import {
  BREAKDOWN_LABEL,
  BREAKDOWN_NOTE,
  breakdownRows,
  figuresOf,
} from "@/lib/stats"
import type { BreakdownRow } from "@/lib/stats"

// Overview: is anything wrong right now? Over one window (1h, 24h, 7d):
// four figures, runs over time by outcome, what needs attention, one
// breakdown table, and the latest runs. Nothing else.
export const Route = createFileRoute("/")({
  validateSearch: validateOverview,
  head: () => ({ meta: [{ title: "Overview · weftbase" }] }),
  component: Overview,
})

const runLink = (r: RunRow) =>
  ({ to: "/runs/$runId", params: { runId: r.id } }) as const

const BUCKET: Record<Window, string> = {
  "1h": "2.5 minutes",
  "24h": "hour",
  "7d": "7 hours",
}

function Overview() {
  const search = Route.useSearch()
  const range: Window = search.range ?? "24h"
  const by: Breakdown = search.by ?? "agent"
  const live = useLiveState()

  const total = useQuery(q.statsTotal(range))
  const fig = total.data ? figuresOf(total.data.total) : undefined

  return (
    <Page
      header={
        <PageHeader
          title="Overview"
          meta={<LivePill state={live} />}
          actions={
            <Segmented
              label="Window"
              options={WINDOWS.map((w) => ({
                label: w,
                title: RANGE_LABEL[w],
                active: w === range,
                link: {
                  to: "/",
                  search: { ...search, range: w === "24h" ? undefined : w },
                },
              }))}
            />
          }
        />
      }
      className="flex flex-col gap-4"
    >
      {total.error ? (
        <div className="rounded-alert border">
          <ErrorState error={total.error} what="The figures" />
        </div>
      ) : (
        <StatStrip
          stats={[
            { label: "Runs", value: fig?.runs.value ?? "—", ...fig?.runs },
            {
              label: "Failure rate",
              value: fig?.failureRate.value ?? "—",
              ...fig?.failureRate,
            },
            { label: "Cost", value: fig?.cost.value ?? "—", ...fig?.cost },
            {
              label: "Tokens",
              value: fig?.tokens.value ?? "—",
              ...fig?.tokens,
            },
          ]}
        />
      )}

      <div className="grid grid-cols-2 gap-4 max-[1240px]:grid-cols-1">
        <Section title="Runs over time">
          <div className="rounded-alert border bg-card p-3">
            {total.data ? (
              <RunsChart
                series={total.data.series}
                bucketMs={total.data.bucket_ms}
              />
            ) : (
              <div className="h-[172px]" />
            )}
          </div>
        </Section>
        <Attention range={range} failedInWindow={total.data?.total.failed} />
      </div>

      <Section
        title="Breakdown"
        action={
          <Segmented
            label="Break down by"
            options={BREAKDOWNS.map((b) => ({
              label: BREAKDOWN_LABEL[b],
              active: b === by,
              link: {
                to: "/",
                search: { ...search, by: b === "agent" ? undefined : b },
              },
            }))}
          />
        }
        className="[&>div:first-child]:h-7"
      >
        <BreakdownTable by={by} range={range} />
      </Section>

      <Latest />
    </Page>
  )
}

// ── Needs attention ─────────────────────────────────────────────────

type Item =
  | { kind: "approval"; at: string; approval: Approval }
  | { kind: "run"; at: string; run: RunRow }
  | { kind: "expired"; at: string; run: RunRow }

/** The rows the list shows; the rest is a count and a link. */
const ATTENTION_ROWS = 6

/** What a person should look at: calls waiting on a decision (oldest
 * first: they block a run), then the newest runs that failed in the
 * window. A short list with the real totals: every waiting call is in
 * the inbox, every failed run in the Runs list, and the header and the
 * last line link there. */
function Attention({
  range,
  failedInWindow,
}: {
  range: Window
  /** Runs that failed in the window (the stats' count); undefined
   * until it is known. */
  failedInWindow: number | undefined
}) {
  const since = React.useMemo(() => sinceOf(range), [range])
  const pending = useQuery(
    q.approvals({ status: "pending", limit: ATTENTION_ROWS })
  )
  // The window's failed runs, as the figures above count them (an
  // older server answers the newest of any time: the filter below
  // keeps the window's).
  const failed = useQuery(q.failedSince(since, ATTENTION_ROWS))
  const expired = useQuery(q.expiredSince(since, ATTENTION_ROWS))
  const error = pending.error ?? failed.error
  const waiting: Item[] = (pending.data?.approvals ?? []).map((a) => ({
    kind: "approval",
    at: a.requested_at,
    approval: a,
  }))
  // Failed runs, then runs that skipped a call because its approval
  // expired (they end succeeded, so no failure count shows them).
  const failures: Item[] = [
    ...(failed.data?.runs ?? [])
      .filter((r) => (rowTimes(r).started ?? "") >= since)
      .map((r): Item => ({
        kind: "run",
        at: rowTimes(r).started ?? "",
        run: r,
      })),
    ...(expired.data?.runs ?? []).map((r): Item => ({
      kind: "expired",
      at: rowTimes(r).started ?? "",
      run: r,
    })),
  ]
  // Each kind gets half of the rows when both have that many; what one
  // leaves free the other takes.
  const half = ATTENTION_ROWS / 2
  const w = Math.min(
    waiting.length,
    Math.max(half, ATTENTION_ROWS - failures.length)
  )
  const shown = [
    ...waiting.slice(0, w),
    ...failures.slice(0, ATTENTION_ROWS - w),
  ]
  // The totals are the server's: every pending approval, and the
  // window's failed runs as the figures above count them.
  const waitingTotal = Math.max(pending.data?.pending ?? 0, waiting.length)
  const failedTotal = Math.max(failedInWindow ?? 0, failures.length)
  const moreWaiting = waitingTotal - Math.min(w, waiting.length)
  const moreFailed = failedTotal - (shown.length - Math.min(w, waiting.length))
  const inbox = (
    n: number,
    word: string // "waiting" | "more waiting"
  ) => (
    <Link to="/approvals" className="hover:text-foreground hover:underline">
      {count(n)} {word}
    </Link>
  )
  const failedRuns = (n: number, word: string) => (
    <Link
      to="/runs"
      search={{ status: "failed", range }}
      className="hover:text-foreground hover:underline"
    >
      {count(n)} {word}
    </Link>
  )

  return (
    <Section
      title="Needs attention"
      action={
        (waitingTotal > 0 || failedTotal > 0) && (
          <span className="flex gap-3">
            {waitingTotal > 0 && inbox(waitingTotal, "waiting")}
            {failedTotal > 0 && failedRuns(failedTotal, "failed")}
          </span>
        )
      }
    >
      {error ? (
        <div className="rounded-alert border">
          <ErrorState error={error} what="This list" />
        </div>
      ) : !pending.data || !failed.data ? (
        <div className="rounded-alert border">
          <LoadingRows rows={5} />
        </div>
      ) : shown.length === 0 && failedTotal === 0 ? (
        <div className="rounded-alert border bg-card">
          <EmptyState icon={CircleCheckIcon} title="Nothing needs attention">
            No run failed in this window and none is waiting on a person.
          </EmptyState>
        </div>
      ) : (
        <ul className="overflow-hidden rounded-alert border bg-card text-sm">
          {shown.map((it) => (
            <li
              key={it.kind === "approval" ? it.approval.id : it.run.id}
              className="border-b border-table-border last:border-b-0"
            >
              <Link
                {...(it.kind === "approval"
                  ? { to: "/approvals", search: { id: it.approval.id } }
                  : runLink(it.run))}
                className="grid h-8 grid-cols-[100px_minmax(72px,120px)_minmax(0,1fr)_64px] items-center outline-none hover:bg-muted/60 focus-visible:bg-muted"
              >
                <span className="px-2">
                  {it.kind === "approval" ? (
                    <Status
                      state="waiting"
                      cause={`${it.approval.tool} needs a decision`}
                    />
                  ) : it.kind === "expired" ? (
                    <Status
                      state={rowStatus(it.run)}
                      cause="an approval expired: the call was skipped"
                    />
                  ) : (
                    <Status state="failed" cause={rowCause(it.run)} />
                  )}
                </span>
                <span className="truncate px-2 font-medium">
                  {it.kind === "approval" ? it.approval.agent : it.run.agent}
                </span>
                {it.kind === "approval" ? (
                  <span className="truncate px-2 text-muted-foreground">
                    <span className="font-mono text-xs text-foreground">
                      {it.approval.tool}
                    </span>{" "}
                    {argsSummary(it.approval.args)}
                  </span>
                ) : (
                  <span className="flex min-w-0 items-center gap-1.5 px-2 text-muted-foreground">
                    <span className="min-w-0 truncate">
                      {it.run.prompt_preview || "—"}
                    </span>
                    {it.kind === "expired" ? (
                      <ApprovalOutcome outcome="expired" />
                    ) : (
                      <ErrorKind run={it.run} />
                    )}
                  </span>
                )}
                <span className="px-2 text-right text-xs text-muted-foreground">
                  <RelativeTime time={it.at} />
                </span>
              </Link>
            </li>
          ))}
          {(moreWaiting > 0 || moreFailed > 0) && (
            <li className="flex h-7 items-center justify-center gap-1.5 text-xs text-muted-foreground">
              {moreWaiting > 0 && inbox(moreWaiting, "more waiting")}
              {moreWaiting > 0 && moreFailed > 0 && <span aria-hidden>·</span>}
              {moreFailed > 0 && failedRuns(moreFailed, "more failed")}
            </li>
          )}
        </ul>
      )}
    </Section>
  )
}

// ── Breakdown ───────────────────────────────────────────────────────

function BreakdownTable({ by, range }: { by: Breakdown; range: Window }) {
  const stats = useQuery(
    by === "agent"
      ? q.stats("agent", range)
      : by === "error"
        ? q.stats("error_type", range)
        : q.statsAll(by, range)
  )
  const rows = React.useMemo(
    () => (stats.data ? breakdownRows(by, stats.data) : undefined),
    [by, stats.data]
  )
  const columns = React.useMemo(() => breakdownColumns(by, range), [by, range])

  if (stats.error)
    return (
      <div className="rounded-alert border">
        <ErrorState error={stats.error} what="The breakdown" />
      </div>
    )
  if (!rows)
    return (
      <div className="rounded-alert border">
        <LoadingRows rows={3} />
      </div>
    )
  if (rows.length === 0)
    return (
      <div className="rounded-alert border bg-card">
        <EmptyState
          title={
            by === "error"
              ? "No run failed in this window"
              : "No runs in this window"
          }
        />
      </div>
    )
  return (
    <DataTable
      label={`Breakdown by ${BREAKDOWN_LABEL[by].toLowerCase()}`}
      bordered
      stickyHeader={false}
      columns={columns}
      rows={rows}
      rowKey={(r) => r.key}
      footer={BREAKDOWN_NOTE[by] && <span>{BREAKDOWN_NOTE[by]}</span>}
    />
  )
}

function Num({
  to,
  children,
  title,
  tone,
}: {
  /** The Runs list filters the number links to. */
  to: Partial<RunsSearch>
  children: React.ReactNode
  title?: string
  tone?: "danger"
}) {
  return (
    <Link
      to="/runs"
      search={to}
      title={title}
      className={
        "rounded-sm outline-none hover:underline focus-visible:outline-2 focus-visible:outline-ring" +
        (tone === "danger" ? " text-danger-foreground" : "")
      }
    >
      {children}
    </Link>
  )
}

function breakdownColumns(
  by: Breakdown,
  range: Window
): Column<BreakdownRow>[] {
  const link = (r: BreakdownRow, extra: Partial<RunsSearch> = {}) => ({
    ...r.link,
    range,
    ...extra,
  })
  const all: Column<BreakdownRow>[] = [
    {
      key: "key",
      header: BREAKDOWN_LABEL[by],
      width: "minmax(140px, 1fr)",
      cell: (r) => (
        <Num to={link(r)}>
          <span
            className={
              by === "agent" ? "font-medium" : "font-mono text-xs font-medium"
            }
          >
            {r.key}
          </span>
        </Num>
      ),
    },
    {
      key: "runs",
      header: "Runs",
      width: "64px",
      align: "right",
      mono: true,
      cell: (r) => <Num to={link(r)}>{count(r.runs)}</Num>,
    },
    {
      key: "calls",
      header: "Calls",
      width: "64px",
      align: "right",
      mono: true,
      cell: (r) => (r.calls === undefined ? "—" : count(r.calls)),
    },
    {
      key: "failure",
      header: "Failure rate",
      width: "96px",
      align: "right",
      mono: true,
      cell: (r) =>
        r.failureRate === null ? (
          "—"
        ) : (
          <Num
            to={{ ...r.failedLink, range }}
            title={r.failureNote}
            tone={r.failureRate > 0 ? "danger" : undefined}
          >
            {percent(r.failureRate)}
          </Num>
        ),
    },
    {
      key: "p50",
      header: "p50",
      width: "72px",
      align: "right",
      mono: true,
      cell: (r) =>
        r.p50 === undefined ? (
          "—"
        ) : (
          <Num to={link(r, { sort: "duration" })} title="Median duration">
            {duration(r.p50)}
          </Num>
        ),
    },
    {
      key: "p95",
      header: "p95",
      width: "72px",
      align: "right",
      mono: true,
      cell: (r) =>
        r.p95 === undefined ? (
          "—"
        ) : (
          <Num
            to={link(r, { sort: "duration" })}
            title="95th percentile duration"
          >
            {duration(r.p95)}
          </Num>
        ),
    },
    {
      key: "tokens",
      header: "Tokens",
      width: "72px",
      align: "right",
      mono: true,
      cell: (r) =>
        r.tokens === undefined ? (
          "—"
        ) : (
          <Num to={link(r, { sort: "tokens" })}>{tokens(r.tokens)}</Num>
        ),
    },
    {
      key: "cost",
      header: "Cost",
      width: "84px",
      align: "right",
      mono: true,
      cell: (r) =>
        r.cost === "—" ? (
          "—"
        ) : (
          <Num to={link(r, { sort: "cost" })} title={r.costTitle}>
            {r.cost}
          </Num>
        ),
    },
    {
      key: "spark",
      header: RANGE_LABEL[range],
      width: "110px",
      align: "right",
      cell: (r) => (
        <Sparkline
          points={r.spark}
          label={`${by === "tool" || by === "model" ? "Calls" : "Runs"} per ${BUCKET[range]}, ${RANGE_LABEL[range].toLowerCase()}`}
        />
      ),
    },
  ]
  // A tool call has no tokens and no cost: its row shows calls instead.
  const hidden =
    by === "tool" ? ["tokens", "cost"] : by === "model" ? [] : ["calls"]
  return all.filter((c) => !hidden.includes(c.key))
}

// ── Latest runs ─────────────────────────────────────────────────────

function Latest() {
  const latest = useQuery(q.runsPage({ limit: 8 }))
  return (
    <Section
      title="Latest runs"
      action={
        <Link to="/runs" className="hover:text-foreground hover:underline">
          All runs
        </Link>
      }
    >
      {latest.error ? (
        <div className="rounded-alert border">
          <ErrorState error={latest.error} what="The latest runs" />
        </div>
      ) : !latest.data ? (
        <div className="rounded-alert border">
          <LoadingRows />
        </div>
      ) : latest.data.runs.length === 0 ? (
        <div className="rounded-alert border bg-card">
          <EmptyState title="No runs yet">
            Runs appear here as soon as an agent reports to this server.
          </EmptyState>
        </div>
      ) : (
        <DataTable
          label="Latest runs"
          bordered
          stickyHeader={false}
          columns={RUN_COLUMNS}
          rows={latest.data.runs.slice(0, 8)}
          rowKey={(r) => r.id}
          rowLink={runLink}
        />
      )}
    </Section>
  )
}
