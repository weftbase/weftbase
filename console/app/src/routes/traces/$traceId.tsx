import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"

import { CopyId } from "@/components/console/copy-id"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import { ErrorState, Rebuilding } from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { q } from "@/lib/api/queries"
import type { Span } from "@/lib/api/types"
import { between, count, duration } from "@/lib/format"
import { validateTrace } from "@/lib/search"
import { SPAN_KINDS, spanKind, spanLabel } from "@/lib/span-kind"

// A raw trace (one that belongs to no weft run), reached from the Traces
// tab of Runs. A stub on the new foundation: its spans in start order.
// The trace waterfall replaces the table.
export const Route = createFileRoute("/traces/$traceId")({
  validateSearch: validateTrace,
  head: () => ({ meta: [{ title: "Trace · weftbase" }] }),
  component: TracePage,
})

const COLUMNS: Column<Span>[] = [
  {
    key: "kind",
    header: <span className="sr-only">Kind</span>,
    width: "28px",
    cell: (s) => {
      const k = SPAN_KINDS[spanKind(s)]
      return (
        <k.icon
          aria-label={k.label}
          className="size-3.5 text-muted-foreground"
        />
      )
    },
  },
  {
    key: "name",
    header: "Span",
    width: "minmax(200px, 1fr)",
    cell: (s) => <span className="font-medium">{spanLabel(s)}</span>,
  },
  {
    key: "service",
    header: "Service",
    width: "minmax(100px, 200px)",
    muted: true,
    cell: (s) => s.service || "—",
  },
  {
    key: "status",
    header: "Status",
    width: "80px",
    muted: true,
    cell: (s) => s.status,
  },
  {
    key: "duration",
    header: "Duration",
    width: "88px",
    align: "right",
    mono: true,
    cell: (s) => duration(between(s.start, s.end)),
  },
]

function TracePage() {
  const { traceId } = Route.useParams()
  const trace = useQuery(q.trace(traceId))
  const spans = [...(trace.data?.spans ?? [])].sort((a, b) =>
    a.start < b.start ? -1 : 1
  )
  return (
    <Page
      header={
        <PageHeader
          title="Trace"
          id={<CopyId id={traceId} label="trace id" />}
          meta={trace.data && <span>{count(spans.length)} spans</span>}
        />
      }
      className="flex flex-col gap-4"
    >
      {trace.error ? (
        <ErrorState error={trace.error} what="The trace" />
      ) : trace.data ? (
        <>
          {trace.data.run_ids.length > 0 && (
            <p className="text-xs text-muted-foreground">
              This trace belongs to{" "}
              {trace.data.run_ids.map((id, i) => (
                <span key={id}>
                  {i > 0 && ", "}
                  <Link
                    to="/runs/$runId"
                    params={{ runId: id }}
                    className="font-mono text-foreground hover:underline"
                  >
                    {id}
                  </Link>
                </span>
              ))}
              .
            </p>
          )}
          <Rebuilding what="The trace view" />
          <DataTable
            label="Spans"
            bordered
            stickyHeader={false}
            columns={COLUMNS}
            rows={spans}
            rowKey={(s) => s.span_id}
          />
        </>
      ) : null}
    </Page>
  )
}
