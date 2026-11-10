import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

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
import { Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { sessionTitle } from "@/components/console/session/model"
import { OutcomeStrip } from "@/components/console/session/outcome-strip"
import { LivePill, StatusDot } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { q } from "@/lib/api/queries"
import { STATUS_LABEL, sessionStatus } from "@/lib/api/status"
import type { SessionRow } from "@/lib/api/types"
import { cost, count, tokens } from "@/lib/format"
import { SESSION_STATES, useSetSearch, validateSessions } from "@/lib/search"
import type { SessionsSearch } from "@/lib/search"

// Sessions: the dense list of conversations, newest activity first,
// live. One filter bar (search, status, agent), all of it in the URL.
// The search also finds a session by its public id or its own id.
export const Route = createFileRoute("/sessions/")({
  validateSearch: validateSessions,
  head: () => ({ meta: [{ title: "Sessions · weftbase" }] }),
  component: SessionsPage,
})

const STATE_LABEL: Record<(typeof SESSION_STATES)[number], string> = {
  running: "Running",
  waiting: "Waiting on a person",
  failed: "Has a failed turn",
  succeeded: "Succeeded",
  interrupted: "Interrupted",
}

const COLUMNS: Column<SessionRow>[] = [
  {
    key: "title",
    header: "Session",
    width: "minmax(220px, 1fr)",
    cell: (s) => (
      <span
        className={
          s.title || s.first_prompt ? "font-medium" : "text-muted-foreground"
        }
      >
        {sessionTitle(s)}
      </span>
    ),
  },
  {
    key: "agent",
    hideBelow: 560,
    header: "Agent",
    width: "minmax(96px, 160px)",
    muted: true,
    cell: (s) => s.agent || "—",
  },
  {
    key: "status",
    header: "Status",
    width: "200px",
    cell: (s) => {
      const state = sessionStatus(s)
      return (
        <span className="flex items-center gap-1.5">
          <StatusDot state={state} />
          <span className="w-[72px] flex-none truncate">
            {STATUS_LABEL[state]}
          </span>
          <OutcomeStrip session={s} />
        </span>
      )
    },
  },
  {
    key: "turns",
    hideBelow: 700,
    header: "Turns",
    width: "56px",
    align: "right",
    mono: true,
    cell: (s) => count(s.turns),
  },
  {
    key: "last",
    header: "Last activity",
    width: "96px",
    align: "right",
    muted: true,
    cell: (s) => <RelativeTime time={s.last_seen} />,
  },
  {
    key: "tokens",
    hideBelow: 860,
    header: "Tokens",
    width: "72px",
    align: "right",
    mono: true,
    cell: (s) => tokens(s.usage.input_tokens + s.usage.output_tokens),
  },
  {
    key: "cost",
    header: "Cost",
    width: "80px",
    align: "right",
    mono: true,
    cell: (s) => <span title={s.cost_reason}>{cost(s)}</span>,
  },
]

function SessionsPage() {
  const search = Route.useSearch()
  const set = useSetSearch<SessionsSearch>(Route.fullPath)
  const live = useLiveState()
  const sessions = useInfiniteQuery(
    q.sessionsList({ q: search.q, state: search.state, agent: search.agent })
  )
  // Agent names: every agent that ran in the last 30 days.
  const agents = useQuery(q.stats("agent", "30d"))
  const names = (agents.data?.groups.map((g) => g.key) ?? []).filter(Boolean)
  if (search.agent && !names.includes(search.agent)) names.push(search.agent)

  const filtered = Boolean(search.q || search.state || search.agent)
  const rows = sessions.data?.pages.flatMap((p) => p.sessions) ?? []
  const total = sessions.data?.pages[0]?.total

  return (
    <Page
      flush
      header={<PageHeader title="Sessions" meta={<LivePill state={live} />} />}
      bar={
        <FilterBar
          right={
            total !== undefined && (
              <span className="font-mono tabular-nums">
                {count(total)} {total === 1 ? "session" : "sessions"}
              </span>
            )
          }
        >
          <SearchInput
            value={search.q}
            onChange={(v) => set({ q: v })}
            placeholder="Search sessions or paste an id"
          />
          <FilterSelect
            label="Status"
            value={search.state}
            options={SESSION_STATES.map((s) => ({
              value: s,
              label: STATE_LABEL[s],
            }))}
            onChange={(state) => set({ state })}
          />
          <FilterSelect
            label="Agent"
            value={search.agent}
            options={names.sort().map((n) => ({ value: n, label: n }))}
            onChange={(agent) => set({ agent })}
          />
        </FilterBar>
      }
    >
      {sessions.error ? (
        <ErrorState
          error={sessions.error}
          what="Sessions"
          onRetry={() => void sessions.refetch()}
        />
      ) : !sessions.data ? (
        <LoadingRows rows={12} />
      ) : rows.length === 0 ? (
        filtered ? (
          <EmptyState
            title="No sessions match these filters"
            action={
              <Button
                size="sm"
                variant="secondary"
                onClick={() =>
                  set({ q: undefined, state: undefined, agent: undefined })
                }
              >
                Clear filters
              </Button>
            }
          />
        ) : (
          <EmptyState title="No sessions yet">
            A session appears here when runs share a session id: each run is one
            turn of the conversation.
          </EmptyState>
        )
      ) : (
        <DataTable
          label="Sessions"
          columns={COLUMNS}
          rows={rows}
          rowKey={(s) => s.id}
          rowLink={(s) => ({
            to: "/sessions/$sessionId",
            params: { sessionId: s.id },
          })}
          footer={
            sessions.hasNextPage ? (
              <Button
                size="sm"
                variant="ghost"
                disabled={sessions.isFetchingNextPage}
                onClick={() => void sessions.fetchNextPage()}
              >
                {sessions.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            ) : (
              <span>
                {count(rows.length)}{" "}
                {rows.length === 1 ? "session" : "sessions"}, all shown
              </span>
            )
          }
        />
      )}
    </Page>
  )
}
