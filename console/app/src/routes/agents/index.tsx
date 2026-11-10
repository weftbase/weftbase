import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { ToolsTable } from "@/components/console/agent/tools-table"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { LinkTabs, Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { Segmented } from "@/components/console/segmented"
import { q } from "@/lib/api/queries"
import type { AgentSummary } from "@/lib/api/types"
import { inventoryLine, labelPointers, versionNumbers } from "@/lib/agents"
import { count, percent, shortId } from "@/lib/format"
import { RANGE_LABEL, WINDOWS, validateAgents } from "@/lib/search"
import type { Window } from "@/lib/search"

// Agents, the inventory: what is deployed and what each agent can do.
// The header counts it; the list is one row per agent; the Tools tab is
// every tool once. Runs and failure rate are over the window.
export const Route = createFileRoute("/agents/")({
  validateSearch: validateAgents,
  head: () => ({ meta: [{ title: "Agents · weftbase" }] }),
  component: AgentsPage,
})

function columns(
  numbers: ReadonlyMap<string, number>,
  range: Window
): Column<AgentSummary>[] {
  return [
    {
      key: "name",
      header: "Agent",
      width: "minmax(140px, 1fr)",
      cell: (a) => (
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate font-medium">{a.name}</span>
          {!a.registered && (
            <span
              className="flex-none text-xs text-subtle-foreground"
              title="Seen in runs only (a subagent, or traces from an app): no build has registered it with this server, so it has no versions, labels or manifest here."
            >
              not registered
            </span>
          )}
        </span>
      ),
    },
    {
      key: "model",
      header: "Model",
      width: "minmax(110px, 170px)",
      muted: true,
      cell: (a) => a.summary.model.name || "—",
    },
    {
      key: "placement",
      header: "Placement",
      width: "88px",
      muted: true,
      cell: (a) => a.summary.placement || "—",
    },
    {
      key: "labels",
      header: "Labels",
      width: "minmax(150px, 220px)",
      cell: (a) => {
        const ps = labelPointers(a.labels, numbers)
        if (ps.length === 0)
          return a.summary.number ? (
            <span className="font-mono text-xs text-muted-foreground">
              newest v{a.summary.number}
            </span>
          ) : (
            <span className="text-subtle-foreground">—</span>
          )
        return (
          <span className="flex min-w-0 items-center gap-3">
            {ps.map((p) => (
              <span
                key={p.label}
                className="flex-none text-xs"
                title={`${p.label} points to ${p.number ? `version ${p.number}` : p.version}`}
              >
                {p.label}
                <span className="text-subtle-foreground"> → </span>
                <span className="font-mono">
                  {p.number ? `v${p.number}` : shortId(p.version, 4)}
                </span>
              </span>
            ))}
          </span>
        )
      },
    },
    {
      key: "tools",
      header: "Tools",
      width: "56px",
      align: "right",
      mono: true,
      cell: (a) =>
        a.registered ? (
          <span
            title={
              a.summary.needs_approval > 0
                ? `${count(a.summary.needs_approval)} need approval`
                : undefined
            }
          >
            {count(a.summary.tools)}
          </span>
        ) : (
          "—"
        ),
    },
    {
      key: "subagents",
      header: "Subagents",
      width: "84px",
      align: "right",
      mono: true,
      cell: (a) => (a.registered ? count(a.summary.subagents) : "—"),
    },
    {
      key: "runs",
      header: `Runs ${range}`,
      width: "76px",
      align: "right",
      mono: true,
      cell: (a) => count(a.summary.stats.runs),
    },
    {
      key: "failure",
      header: "Failure rate",
      width: "96px",
      align: "right",
      mono: true,
      cell: (a) => {
        const s = a.summary.stats
        const finished = s.succeeded + s.failed
        if (finished === 0) return "—"
        return (
          <span
            title={`${count(s.failed)} of ${count(finished)} finished runs failed`}
            className={s.failed > 0 ? "text-danger-foreground" : undefined}
          >
            {percent(s.failure_rate)}
          </span>
        )
      },
    },
    {
      key: "last",
      header: "Last run",
      width: "84px",
      align: "right",
      muted: true,
      cell: (a) => <RelativeTime time={a.summary.stats.last_run_at} />,
    },
  ]
}

function AgentsPage() {
  const search = Route.useSearch()
  const range: Window = search.range ?? "24h"
  const onTools = search.tab === "tools"
  const agents = useQuery(q.agentsSummary(range))
  const tools = useQuery(q.tools(range))
  const numbers = versionNumbers(agents.data?.agents ?? [], tools.data)

  return (
    <Page
      flush
      header={
        <PageHeader
          title="Agents"
          meta={
            agents.data && (
              <span className="truncate">
                {inventoryLine(agents.data.totals)}
              </span>
            )
          }
          actions={
            <Segmented
              label="Window"
              options={WINDOWS.map((w) => ({
                label: w,
                title: RANGE_LABEL[w],
                active: w === range,
                link: {
                  to: "/agents",
                  search: { ...search, range: w === "24h" ? undefined : w },
                },
              }))}
            />
          }
        />
      }
      bar={
        <LinkTabs
          tabs={[
            {
              label: "Agents",
              link: { to: "/agents", search: { ...search, tab: undefined } },
              active: !onTools,
              count: agents.data?.agents.length,
            },
            {
              label: "Tools",
              link: { to: "/agents", search: { ...search, tab: "tools" } },
              active: onTools,
              count: tools.data?.tools.length,
            },
          ]}
        />
      }
    >
      {onTools ? (
        tools.error ? (
          <ErrorState error={tools.error} what="The tool inventory" />
        ) : !tools.data ? (
          <LoadingRows />
        ) : tools.data.tools.length === 0 ? (
          <EmptyState title="No tools">
            A tool appears here once a registered agent version has it.
          </EmptyState>
        ) : (
          <ToolsTable tools={tools.data.tools} range={range} />
        )
      ) : agents.error ? (
        <ErrorState error={agents.error} what="The agent registry" />
      ) : !agents.data ? (
        <LoadingRows />
      ) : agents.data.agents.length === 0 ? (
        <EmptyState title="No agents yet">
          An agent appears here once a build that serves it registers with this
          server, or once a run of it arrives.
        </EmptyState>
      ) : (
        <DataTable
          label="Agents"
          columns={columns(numbers, range)}
          rows={agents.data.agents}
          rowKey={(a) => a.name}
          rowLink={(a) => ({ to: "/agents/$name", params: { name: a.name } })}
        />
      )}
    </Page>
  )
}
