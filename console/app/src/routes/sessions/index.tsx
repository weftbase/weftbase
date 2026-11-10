import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { FilterBar, SearchInput } from "@/components/console/filter-bar"
import { Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { StatusDot } from "@/components/console/status"
import { q } from "@/lib/api/queries"
import type { SessionRow } from "@/lib/api/types"
import { cost, count } from "@/lib/format"
import { sessionStatus } from "@/lib/api/status"
import { useSetSearch, validateSessions } from "@/lib/search"
import type { SessionsSearch } from "@/lib/search"

// Sessions. A stub on the new foundation: the real list with search.
// The state filter and the conversation view are built on top of this.
export const Route = createFileRoute("/sessions/")({
  validateSearch: validateSessions,
  head: () => ({ meta: [{ title: "Sessions · weftbase" }] }),
  component: SessionsPage,
})

const COLUMNS: Column<SessionRow>[] = [
  {
    key: "state",
    header: <span className="sr-only">State</span>,
    width: "28px",
    cell: (s) => <StatusDot state={sessionStatus(s)} />,
  },
  {
    key: "title",
    header: "Session",
    width: "minmax(200px, 1fr)",
    cell: (s) => (
      <span className="font-medium">
        {s.title ?? s.first_prompt ?? "Untitled"}
      </span>
    ),
  },
  {
    key: "agent",
    header: "Agent",
    width: "minmax(120px, 200px)",
    muted: true,
    cell: (s) => s.agent || "—",
  },
  {
    key: "turns",
    header: "Turns",
    width: "64px",
    align: "right",
    mono: true,
    cell: (s) => count(s.turns),
  },
  {
    key: "cost",
    header: "Cost",
    width: "80px",
    align: "right",
    mono: true,
    cell: (s) => <span title={s.cost_reason}>{cost(s)}</span>,
  },
  {
    key: "last",
    header: "Last activity",
    width: "100px",
    align: "right",
    muted: true,
    cell: (s) => <RelativeTime time={s.last_seen} />,
  },
]

function SessionsPage() {
  const search = Route.useSearch()
  const set = useSetSearch<SessionsSearch>(Route.fullPath)
  const sessions = useQuery(q.sessions({ q: search.q, limit: 100 }))
  return (
    <Page
      flush
      header={<PageHeader title="Sessions" />}
      bar={
        <FilterBar
          right={
            sessions.data && (
              <span className="font-mono tabular-nums">
                {count(sessions.data.total)} sessions
              </span>
            )
          }
        >
          <SearchInput
            value={search.q}
            onChange={(v) => set({ q: v })}
            placeholder="Search sessions"
          />
        </FilterBar>
      }
    >
      {sessions.error ? (
        <ErrorState error={sessions.error} what="Sessions" />
      ) : !sessions.data ? (
        <LoadingRows rows={10} />
      ) : sessions.data.sessions.length === 0 ? (
        <EmptyState
          title={search.q ? "No sessions match" : "No sessions yet"}
        />
      ) : (
        <DataTable
          label="Sessions"
          columns={COLUMNS}
          rows={sessions.data.sessions}
          rowKey={(s) => s.id}
          rowLink={(s) => ({
            to: "/sessions/$sessionId",
            params: { sessionId: s.id },
          })}
        />
      )}
    </Page>
  )
}
