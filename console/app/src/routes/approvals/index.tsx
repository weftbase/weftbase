import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { CircleCheckIcon } from "lucide-react"

import { DataTable } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { RUN_COLUMNS } from "@/components/console/run-columns"
import { q } from "@/lib/api/queries"
import { count } from "@/lib/format"
import { validateApprovals } from "@/lib/search"

// Approvals. The server has no approvals resource: an approval is a run
// waiting on a person (GET /v1/runs?status=parked), decided with
// POST /v1/runs/{id}/resume. A stub on the new foundation: the waiting
// runs. The inbox with Approve and Deny is built on top of this.
export const Route = createFileRoute("/approvals/")({
  validateSearch: validateApprovals,
  head: () => ({ meta: [{ title: "Approvals · weftbase" }] }),
  component: ApprovalsPage,
})

function ApprovalsPage() {
  const waiting = useQuery(q.runsPage({ status: "parked", limit: 100 }))
  return (
    <Page
      flush
      header={
        <PageHeader
          title="Approvals"
          meta={
            waiting.data && <span>{count(waiting.data.total)} waiting</span>
          }
        />
      }
    >
      {waiting.error ? (
        <ErrorState error={waiting.error} what="Approvals" />
      ) : !waiting.data ? (
        <LoadingRows />
      ) : waiting.data.runs.length === 0 ? (
        <EmptyState icon={CircleCheckIcon} title="Nothing is waiting on you">
          A run appears here when one of its tool calls needs a person&apos;s
          decision.
        </EmptyState>
      ) : (
        <DataTable
          label="Runs waiting on a person"
          columns={RUN_COLUMNS}
          rows={waiting.data.runs}
          rowKey={(r) => r.id}
          rowLink={(r) => ({ to: "/runs/$runId", params: { runId: r.id } })}
        />
      )}
    </Page>
  )
}
