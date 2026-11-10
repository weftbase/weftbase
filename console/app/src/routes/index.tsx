import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { CircleCheckIcon } from "lucide-react"
import * as React from "react"

import { useLiveState } from "@/components/console/app-shell"
import { DataTable } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { FilterSelect } from "@/components/console/filter-bar"
import { Page, PageHeader, Section } from "@/components/console/page-header"
import {
  RUN_COLUMNS,
  RUN_COLUMNS_SHORT,
} from "@/components/console/run-columns"
import { RunsChart } from "@/components/console/runs-chart"
import { StatStrip } from "@/components/console/stat-strip"
import { LivePill } from "@/components/console/status"
import { q } from "@/lib/api/queries"
import { displayStatus } from "@/lib/api/status"
import type { RunRow } from "@/lib/api/types"
import { cost, count, percent, tokens } from "@/lib/format"
import {
  RANGES,
  RANGE_LABEL,
  sinceOf,
  useSetSearch,
  validateOverview,
} from "@/lib/search"
import type { OverviewSearch, Range } from "@/lib/search"
import { totalOf } from "@/lib/stats"

// Overview: is anything wrong right now? Four figures over the window,
// runs over time by outcome, what needs attention, and the latest runs.
export const Route = createFileRoute("/")({
  validateSearch: validateOverview,
  head: () => ({ meta: [{ title: "Overview · weftbase" }] }),
  component: Overview,
})

const runLink = (r: RunRow) =>
  ({ to: "/runs/$runId", params: { runId: r.id } }) as const

function Overview() {
  const search = Route.useSearch()
  const set = useSetSearch<OverviewSearch>(Route.fullPath)
  const range: Range = search.range ?? "24h"
  const live = useLiveState()
  const since = React.useMemo(() => sinceOf(range), [range])

  const stats = useQuery(q.stats("agent", range))
  const failed = useQuery(q.runsPage({ status: "failed", since, limit: 6 }))
  const waiting = useQuery(q.runsPage({ status: "parked", limit: 6 }))
  const latest = useQuery(q.runsPage({ limit: 10 }))

  const total = stats.data ? totalOf(stats.data) : undefined
  // The server's "failed" includes cancelled runs; someone chose those.
  const attention = [
    ...(waiting.data?.runs ?? []),
    ...(failed.data?.runs ?? []),
  ]
    .filter((r) => displayStatus(r) !== "cancelled")
    .sort((a, b) => (a.started < b.started ? 1 : -1))
    .slice(0, 8)
  const attentionTotal = (waiting.data?.total ?? 0) + (failed.data?.total ?? 0)

  return (
    <Page
      header={
        <PageHeader
          title="Overview"
          meta={<LivePill state={live} />}
          actions={
            <FilterSelect
              label="Window"
              value={range}
              options={RANGES.map((r) => ({ value: r, label: RANGE_LABEL[r] }))}
              onChange={(r) => set({ range: r === "24h" ? undefined : r })}
              any={false}
            />
          }
        />
      }
      className="flex flex-col gap-4"
    >
      {stats.error ? (
        <div className="rounded-alert border">
          <ErrorState error={stats.error} what="The figures" />
        </div>
      ) : (
        <StatStrip
          stats={[
            { label: "Runs", value: total ? count(total.runs) : "—" },
            {
              label: "Failure rate",
              value: total ? percent(total.failureRate) : "—",
              hint:
                total && total.failed > 0
                  ? `${count(total.failed)} failed`
                  : undefined,
              tone: total && total.failed > 0 ? "danger" : "default",
            },
            {
              label: "Cost",
              value: total ? cost(total.cost) : "—",
              title:
                total && !total.cost.cost_known
                  ? "Some runs used a model with no price in the pricing table."
                  : undefined,
            },
            {
              label: "Tokens",
              value: total
                ? tokens(total.inputTokens + total.outputTokens)
                : "—",
              hint: total
                ? `${tokens(total.inputTokens)} in · ${tokens(total.outputTokens)} out`
                : undefined,
            },
          ]}
        />
      )}

      <div className="grid grid-cols-2 gap-4 max-[1100px]:grid-cols-1">
        <Section title="Runs over time">
          <div className="rounded-alert border bg-card p-3">
            {stats.data && total ? (
              <RunsChart
                series={total.series}
                since={stats.data.since}
                bucketMs={stats.data.bucket_ms}
              />
            ) : (
              <div className="h-[172px]" />
            )}
          </div>
        </Section>

        <Section
          title="Needs attention"
          action={
            attentionTotal > attention.length && (
              <Link
                to="/runs"
                search={{ status: "failed", range }}
                className="hover:text-foreground hover:underline"
              >
                All failed runs
              </Link>
            )
          }
        >
          {failed.error || waiting.error ? (
            <div className="rounded-alert border">
              <ErrorState
                error={failed.error ?? waiting.error}
                what="This list"
              />
            </div>
          ) : !failed.data || !waiting.data ? (
            <div className="rounded-alert border">
              <LoadingRows rows={5} />
            </div>
          ) : attention.length === 0 ? (
            <div className="rounded-alert border bg-card">
              <EmptyState
                icon={CircleCheckIcon}
                title="Nothing needs attention"
              >
                No run failed in this window and none is waiting on a person.
              </EmptyState>
            </div>
          ) : (
            <DataTable
              label="Runs that need attention"
              bordered
              stickyHeader={false}
              columns={RUN_COLUMNS_SHORT}
              rows={attention}
              rowKey={(r) => r.id}
              rowLink={runLink}
            />
          )}
        </Section>
      </div>

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
            rows={latest.data.runs}
            rowKey={(r) => r.id}
            rowLink={runLink}
          />
        )}
      </Section>
    </Page>
  )
}
