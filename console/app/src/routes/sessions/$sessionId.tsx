import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { CopyId } from "@/components/console/copy-id"
import { DataTable } from "@/components/console/data-table"
import { ErrorState, Rebuilding } from "@/components/console/empty-state"
import { Page, PageHeader, Section } from "@/components/console/page-header"
import { RUN_COLUMNS } from "@/components/console/run-columns"
import { StatusBadge } from "@/components/console/status"
import { q } from "@/lib/api/queries"
import { count } from "@/lib/format"
import { sessionStatus } from "@/lib/api/status"
import { validateSession } from "@/lib/search"

// A session. A stub on the new foundation: the header and the session's
// turns (each one a run). The conversation view is built on top of this.
export const Route = createFileRoute("/sessions/$sessionId")({
  validateSearch: validateSession,
  head: () => ({ meta: [{ title: "Session · weftbase" }] }),
  component: SessionPage,
})

function SessionPage() {
  const { sessionId } = Route.useParams()
  const session = useQuery(q.session(sessionId))
  const s = session.data
  return (
    <Page
      header={
        <PageHeader
          title={s ? (s.title ?? s.first_prompt ?? "Session") : "Session"}
          status={s && <StatusBadge state={sessionStatus(s)} />}
          id={<CopyId id={sessionId} label="session id" />}
          meta={
            s && (
              <span>
                {s.agent} · {count(s.turns)} turns
              </span>
            )
          }
        />
      }
      className="flex flex-col gap-4"
    >
      {session.error ? (
        <ErrorState error={session.error} what="The session" />
      ) : s ? (
        <>
          <Rebuilding what="The conversation view" />
          <Section title="Turns">
            <DataTable
              label="Turns"
              bordered
              stickyHeader={false}
              columns={RUN_COLUMNS}
              rows={s.runs}
              rowKey={(r) => r.id}
              rowLink={(r) => ({ to: "/runs/$runId", params: { runId: r.id } })}
            />
          </Section>
        </>
      ) : null}
    </Page>
  )
}
