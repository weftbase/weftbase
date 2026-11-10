import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { TriangleAlertIcon } from "lucide-react"
import * as React from "react"

import { useLiveState } from "@/components/console/app-shell"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import {
  FilterBar,
  FilterSelect,
  SearchInput,
} from "@/components/console/filter-bar"
import { LinkTabs, Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { RUN_COLUMNS } from "@/components/console/run-columns"
import { LivePill } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { q } from "@/lib/api/queries"
import type { RunsQuery, TraceRow } from "@/lib/api/types"
import { count, duration, shortId } from "@/lib/format"
import { FILTERABLE, STATUS_LABEL, statusFilter } from "@/lib/api/status"
import {
  RANGES,
  RANGE_LABEL,
  sinceOf,
  useSetSearch,
  validateRuns,
} from "@/lib/search"
import type { RunsSearch } from "@/lib/search"

// Runs: the dense list of top-level runs, newest first, live. One filter
// bar (search, status, agent, time range), all of it in the URL. Traces
// that belong to no weft run are a second tab, shown only when the
// server has any.
export const Route = createFileRoute("/runs/")({
  validateSearch: validateRuns,
  head: () => ({ meta: [{ title: "Runs · weftbase" }] }),
  component: RunsPage,
})

/** toQuery maps the URL's filters onto GET /v1/runs parameters. */
export function toQuery(s: RunsSearch, since?: string): RunsQuery {
  return {
    q: s.q,
    status: s.status ? statusFilter(s.status) : undefined,
    agent: s.agent,
    since,
    limit: 50,
  }
}

function RunsPage() {
  const search = Route.useSearch()
  const live = useLiveState()
  // Raw traces: is there anything for the second tab?
  const raw = useQuery(q.traces({ notRun: true, limit: 50 }))
  const hasRaw = (raw.data?.total ?? 0) > 0
  const onTraces = search.tab === "traces"

  return (
    <Page
      flush
      header={<PageHeader title="Runs" meta={<LivePill state={live} />} />}
      bar={
        <>
          {(hasRaw || onTraces) && (
            <LinkTabs
              tabs={[
                {
                  label: "Runs",
                  link: { to: "/runs", search: { ...search, tab: undefined } },
                  active: !onTraces,
                },
                {
                  label: "Traces",
                  link: { to: "/runs", search: { tab: "traces" } },
                  active: onTraces,
                  count: raw.data?.total,
                },
              ]}
            />
          )}
          {!onTraces && <RunsFilters />}
        </>
      }
    >
      {onTraces ? <RawTraces /> : <RunsTable />}
    </Page>
  )
}

function useRunsQuery() {
  const search = Route.useSearch()
  const since = React.useMemo(
    () => (search.range ? sinceOf(search.range) : undefined),
    [search.range]
  )
  return useInfiniteQuery(q.runs(toQuery(search, since)))
}

function RunsFilters() {
  const search = Route.useSearch()
  const set = useSetSearch<RunsSearch>(Route.fullPath)
  const runs = useRunsQuery()
  // Agent names: every agent that ran in the last 30 days.
  const agents = useQuery(q.stats("agent", "30d"))
  const names = (agents.data?.groups.map((g) => g.key) ?? []).filter(Boolean)
  if (search.agent && !names.includes(search.agent)) names.push(search.agent)
  const total = runs.data?.pages[0]?.total

  return (
    <FilterBar
      right={
        total !== undefined && (
          <span className="font-mono tabular-nums">
            {count(total)} {total === 1 ? "run" : "runs"}
          </span>
        )
      }
    >
      <SearchInput
        value={search.q}
        onChange={(v) => set({ q: v })}
        placeholder="Search runs"
      />
      <FilterSelect
        label="Status"
        value={search.status}
        options={FILTERABLE.map((s) => ({ value: s, label: STATUS_LABEL[s] }))}
        onChange={(status) => set({ status })}
      />
      <FilterSelect
        label="Agent"
        value={search.agent}
        options={names.sort().map((n) => ({ value: n, label: n }))}
        onChange={(agent) => set({ agent })}
      />
      <FilterSelect
        label="Time range"
        any="Any time"
        value={search.range}
        options={RANGES.map((r) => ({ value: r, label: RANGE_LABEL[r] }))}
        onChange={(range) => set({ range })}
      />
    </FilterBar>
  )
}

function RunsTable() {
  const search = Route.useSearch()
  const set = useSetSearch<RunsSearch>(Route.fullPath)
  const runs = useRunsQuery()
  const filtered = Boolean(
    search.q || search.status || search.agent || search.range
  )

  if (runs.error)
    return (
      <ErrorState
        error={runs.error}
        what="Runs"
        onRetry={() => void runs.refetch()}
      />
    )
  if (!runs.data) return <LoadingRows rows={12} />
  const rows = runs.data.pages.flatMap((p) => p.runs)
  if (rows.length === 0)
    return filtered ? (
      <EmptyState
        title="No runs match these filters"
        action={
          <Button
            size="sm"
            variant="secondary"
            onClick={() =>
              set({
                q: undefined,
                status: undefined,
                agent: undefined,
                range: undefined,
              })
            }
          >
            Clear filters
          </Button>
        }
      />
    ) : (
      <EmptyState title="No runs yet">
        Runs appear here as soon as an agent reports to this server.
      </EmptyState>
    )

  return (
    <DataTable
      label="Runs"
      columns={RUN_COLUMNS}
      rows={rows}
      rowKey={(r) => r.id}
      rowLink={(r) => ({ to: "/runs/$runId", params: { runId: r.id } })}
      footer={
        runs.hasNextPage ? (
          <Button
            size="sm"
            variant="ghost"
            disabled={runs.isFetchingNextPage}
            onClick={() => void runs.fetchNextPage()}
          >
            {runs.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        ) : (
          <span>
            {count(rows.length)} {rows.length === 1 ? "run" : "runs"}, all shown
          </span>
        )
      }
    />
  )
}

const TRACE_COLUMNS: Column<TraceRow>[] = [
  {
    key: "error",
    header: <span className="sr-only">Error</span>,
    width: "28px",
    cell: (t) =>
      t.error ? (
        <TriangleAlertIcon
          aria-label="Has an error"
          className="size-3.5 text-danger-foreground"
        />
      ) : null,
  },
  {
    key: "root",
    header: "Root span",
    width: "minmax(200px, 1fr)",
    cell: (t) =>
      t.root ? (
        <span className="font-medium">{t.root.name}</span>
      ) : (
        <span className="text-muted-foreground" title={t.reason}>
          root span missing
        </span>
      ),
  },
  {
    key: "service",
    header: "Service",
    width: "minmax(100px, 200px)",
    muted: true,
    cell: (t) => t.service || "—",
  },
  {
    key: "id",
    header: "Trace",
    width: "100px",
    mono: true,
    muted: true,
    cell: (t) => shortId(t.trace_id),
  },
  {
    key: "spans",
    header: "Spans",
    width: "64px",
    align: "right",
    mono: true,
    cell: (t) => count(t.span_count),
  },
  {
    key: "duration",
    header: "Duration",
    width: "80px",
    align: "right",
    mono: true,
    cell: (t) => duration(t.duration_ms),
  },
  {
    key: "started",
    header: "Started",
    width: "80px",
    align: "right",
    muted: true,
    cell: (t) => <RelativeTime time={t.start} />,
  },
]

/** Traces that belong to no weft run (plain OTel traces sent here). */
function RawTraces() {
  const traces = useQuery(q.traces({ notRun: true, limit: 50 }))
  if (traces.error) return <ErrorState error={traces.error} what="Traces" />
  if (!traces.data) return <LoadingRows rows={8} />
  if (traces.data.traces.length === 0)
    return (
      <EmptyState title="No traces outside runs">
        Every trace this server has belongs to a run. Open a run to see its
        trace.
      </EmptyState>
    )
  return (
    <DataTable
      label="Traces"
      columns={TRACE_COLUMNS}
      rows={traces.data.traces}
      rowKey={(t) => t.trace_id}
      rowLink={(t) => ({
        to: "/traces/$traceId",
        params: { traceId: t.trace_id },
      })}
      footer={
        <span>
          {count(traces.data.traces.length)} of {count(traces.data.total)}{" "}
          traces
        </span>
      }
    />
  )
}
