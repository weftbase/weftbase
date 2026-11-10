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
import { runColumns } from "@/components/console/run-columns"
import { RunsFilterMenu } from "@/components/console/runs-filter"
import { LivePill } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { q } from "@/lib/api/queries"
import { FILTERABLE, STATUS_LABEL } from "@/lib/api/status"
import type { TraceRow } from "@/lib/api/types"
import { count, duration, shortId } from "@/lib/format"
import { runsCount, runsRows, toRunsQuery, unpricedLine } from "@/lib/runs"
import {
  RANGES,
  RANGE_LABEL,
  sinceOf,
  useSetSearch,
  validateRuns,
} from "@/lib/search"
import type { RunsSearch } from "@/lib/search"

// Runs: the dense list of runs, newest first, live. A park and its
// resume are one row. One filter bar (search, status, agent, time range,
// then "+ Filter"), all of it in the URL; the headers of the columns the
// server can order by sort. Traces that belong to no weft run are a
// second tab, shown only when the server has any.
export const Route = createFileRoute("/runs/")({
  validateSearch: validateRuns,
  head: () => ({ meta: [{ title: "Runs · weftbase" }] }),
  component: RunsPage,
})

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
  return useInfiniteQuery(q.runs(toRunsQuery(search, since)))
}

/** Every filter the URL can hold, cleared. */
const CLEARED: Record<Exclude<keyof RunsSearch, "tab">, undefined> = {
  q: undefined,
  status: undefined,
  agent: undefined,
  range: undefined,
  tool: undefined,
  model: undefined,
  error: undefined,
  min_cost: undefined,
  min_tokens: undefined,
  tag: undefined,
  session: undefined,
  sub: undefined,
  sort: undefined,
}

const keys = (groups: { key: string }[] | undefined, also?: string) => {
  const out = (groups?.map((g) => g.key) ?? []).filter(Boolean)
  if (also && !out.includes(also)) out.push(also)
  return out.sort()
}

function RunsFilters() {
  const search = Route.useSearch()
  const set = useSetSearch<RunsSearch>(Route.fullPath)
  const runs = useRunsQuery()
  // What the selects offer: what ran in the last 30 days.
  const agents = useQuery(q.options("agent"))
  const models = useQuery(q.options("model"))
  const tools = useQuery(q.options("tool"))
  const errors = useQuery(q.options("error_type"))
  const total = runsCount(runs.data?.pages, runs.hasNextPage)

  return (
    <FilterBar
      right={
        total && (
          <span className="font-mono tabular-nums" title={total.title}>
            {total.text}
            {total.more && (
              <span className="font-sans text-muted-foreground">
                {" "}
                · {total.more}
              </span>
            )}
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
        options={keys(agents.data?.groups, search.agent).map((n) => ({
          value: n,
          label: n,
        }))}
        onChange={(agent) => set({ agent })}
      />
      <FilterSelect
        label="Time range"
        any="Any time"
        value={search.range}
        options={RANGES.map((r) => ({ value: r, label: RANGE_LABEL[r] }))}
        onChange={(range) => set({ range })}
      />
      <RunsFilterMenu
        search={search}
        set={set}
        tools={keys(tools.data?.groups)}
        models={keys(models.data?.groups)}
        errors={keys(errors.data?.groups).filter((k) => k !== "cancelled")}
      />
    </FilterBar>
  )
}

function RunsTable() {
  const search = Route.useSearch()
  const set = useSetSearch<RunsSearch>(Route.fullPath)
  const runs = useRunsQuery()
  const filtered = Object.keys(CLEARED).some(
    (k) => search[k as keyof RunsSearch] !== undefined
  )
  const columns = React.useMemo(
    () => runColumns({ sort: search.sort, onSort: (sort) => set({ sort }) }),
    // set is a new function each render; the sort is what matters.
    [search.sort]
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
  const rows = runsRows(runs.data.pages)
  if (rows.length === 0 && !runs.hasNextPage)
    return filtered ? (
      <EmptyState
        title="No runs match these filters"
        action={
          <Button size="sm" variant="secondary" onClick={() => set(CLEARED)}>
            Clear filters
          </Button>
        }
      />
    ) : (
      <EmptyState title="No runs yet">
        Runs appear here as soon as an agent reports to this server.
      </EmptyState>
    )

  const unpriced = unpricedLine(rows)
  return (
    <>
      <DataTable
        label="Runs"
        columns={columns}
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
          ) : undefined
        }
      />
      {unpriced && (
        <p className="px-4 pb-3 text-center text-xs text-muted-foreground">
          {unpriced}
        </p>
      )}
    </>
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
        // The tab counts them; only a list cut short says so.
        traces.data.traces.length < traces.data.total ? (
          <span>
            {count(traces.data.traces.length)} of {count(traces.data.total)}{" "}
            traces
          </span>
        ) : undefined
      }
    />
  )
}
