import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { CopyId } from "@/components/console/copy-id"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
  Rebuilding,
} from "@/components/console/empty-state"
import { Page, PageHeader, Section } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { Badge } from "@/components/ui/badge"
import { q } from "@/lib/api/queries"
import type { AgentVersion } from "@/lib/api/types"
import { validateAgent } from "@/lib/search"

// One agent. A stub on the new foundation: its versions, newest first.
// Moving a label and the manifest view are built on top of this.
export const Route = createFileRoute("/agents/$name")({
  validateSearch: validateAgent,
  head: ({ params }) => ({
    meta: [{ title: `${params.name} · Agents · weftbase` }],
  }),
  component: AgentPage,
})

const COLUMNS: Column<AgentVersion>[] = [
  {
    key: "number",
    header: "Version",
    width: "80px",
    mono: true,
    cell: (v) => `v${v.number}`,
  },
  {
    key: "labels",
    header: "Labels",
    width: "minmax(120px, 1fr)",
    cell: (v) => (
      <span className="flex gap-1">
        {(v.labels ?? []).map((l) => (
          <Badge key={l} variant="secondary" size="sm">
            {l}
          </Badge>
        ))}
      </span>
    ),
  },
  {
    key: "id",
    header: "Id",
    width: "140px",
    cell: (v) => <CopyId id={v.id} label="version id" />,
  },
  {
    key: "created",
    header: "Created",
    width: "100px",
    align: "right",
    muted: true,
    cell: (v) => <RelativeTime time={v.created_at} />,
  },
]

function AgentPage() {
  const { name } = Route.useParams()
  const versions = useQuery(q.agentVersions(name))
  return (
    <Page header={<PageHeader title={name} />} className="flex flex-col gap-4">
      <Rebuilding what="The agent page" />
      <Section title="Versions">
        {versions.error ? (
          <div className="rounded-alert border">
            <ErrorState error={versions.error} what="This agent" />
          </div>
        ) : !versions.data ? (
          <div className="rounded-alert border">
            <LoadingRows rows={3} />
          </div>
        ) : versions.data.length === 0 ? (
          <div className="rounded-alert border">
            <EmptyState title="No versions" />
          </div>
        ) : (
          <DataTable
            label="Versions"
            bordered
            stickyHeader={false}
            columns={COLUMNS}
            rows={versions.data}
            rowKey={(v) => v.id}
          />
        )}
      </Section>
    </Page>
  )
}
