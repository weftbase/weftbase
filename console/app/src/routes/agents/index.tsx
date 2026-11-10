import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { Badge } from "@/components/ui/badge"
import { q } from "@/lib/api/queries"
import type { Agent } from "@/lib/api/types"
import { count } from "@/lib/format"

// Agents. A stub on the new foundation: the registry's agents with their
// labels. Runs and failures per agent, and the detail, are built on top.
export const Route = createFileRoute("/agents/")({
  head: () => ({ meta: [{ title: "Agents · weftbase" }] }),
  component: AgentsPage,
})

const COLUMNS: Column<Agent>[] = [
  {
    key: "name",
    header: "Agent",
    width: "minmax(160px, 1fr)",
    cell: (a) => <span className="font-medium">{a.name}</span>,
  },
  {
    key: "labels",
    header: "Labels",
    width: "minmax(160px, 2fr)",
    cell: (a) => (
      <span className="flex gap-1">
        {Object.keys(a.labels).length === 0
          ? "—"
          : Object.keys(a.labels)
              .sort()
              .map((l) => (
                <Badge key={l} variant="secondary" size="sm">
                  {l}
                </Badge>
              ))}
      </span>
    ),
  },
  {
    key: "versions",
    header: "Versions",
    width: "80px",
    align: "right",
    mono: true,
    cell: (a) => count(a.versions),
  },
  {
    key: "created",
    header: "Created",
    width: "100px",
    align: "right",
    muted: true,
    cell: (a) => <RelativeTime time={a.created_at} />,
  },
]

function AgentsPage() {
  const agents = useQuery(q.agents())
  return (
    <Page flush header={<PageHeader title="Agents" />}>
      {agents.error ? (
        <ErrorState error={agents.error} what="The agent registry" />
      ) : !agents.data ? (
        <LoadingRows />
      ) : agents.data.length === 0 ? (
        <EmptyState title="No agents registered">
          An agent appears here once a build that serves it registers with this
          server. Runs sent only as traces do not register an agent.
        </EmptyState>
      ) : (
        <DataTable
          label="Agents"
          columns={COLUMNS}
          rows={agents.data}
          rowKey={(a) => a.id}
          rowLink={(a) => ({ to: "/agents/$name", params: { name: a.name } })}
        />
      )}
    </Page>
  )
}
