import { useQuery, useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"
import { MessageSquareIcon } from "lucide-react"
import * as React from "react"

import { EmptyState } from "@/components/console/empty-state"
import { FilterSelect, SearchFilter } from "@/components/console/filters"
import { LinkTab, LinkTabs } from "@/components/console/link-tabs"
import { PageHeader } from "@/components/console/page-header"
import { SessionStateBadge } from "@/components/console/status"
import { Card } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { q } from "@/lib/api/queries"
import { ago, cost, dateTime, tokens } from "@/lib/format"
import { useEnv, validateSessions } from "@/lib/search"
import type { SessionsSearch } from "@/lib/search"

export const Route = createFileRoute("/sessions/")({
  validateSearch: validateSessions,
  loaderDeps: ({ search }) => ({
    env: search.env ?? "live",
    f: { state: search.state, agent: search.agent, q: search.q },
  }),
  loader: ({ context: { queryClient }, deps: { env, f } }) =>
    queryClient.ensureQueryData(q.sessions(env, f)),
  head: () => ({ meta: [{ title: "Sessions · weftbase" }] }),
  component: Sessions,
})

function Sessions() {
  const search = Route.useSearch()
  const env = useEnv()
  const navigate = useNavigate({ from: Route.fullPath })
  const { data } = useSuspenseQuery(
    q.sessions(env, { state: search.state, agent: search.agent, q: search.q })
  )
  const agents = useQuery(q.agents(env))
  const set = React.useCallback(
    (patch: Partial<SessionsSearch>) =>
      void navigate({ search: (prev) => ({ ...prev, ...patch }) }),
    [navigate]
  )
  const setQ = React.useCallback(
    (v: string | undefined) => set({ q: v }),
    [set]
  )
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-4">
      <PageHeader
        title="Sessions"
        description="Conversations that span many runs, with their turns and approvals."
      >
        <LinkTabs label="Session state">
          <LinkTab to="/sessions" search={(p) => ({ ...p, state: undefined })}>
            All
          </LinkTab>
          <LinkTab
            to="/sessions"
            search={(p) => ({ ...p, state: "active" as const })}
          >
            Active
          </LinkTab>
          <LinkTab
            to="/sessions"
            search={(p) => ({ ...p, state: "waiting" as const })}
          >
            Waiting
          </LinkTab>
          <LinkTab
            to="/sessions"
            search={(p) => ({ ...p, state: "idle" as const })}
          >
            Idle
          </LinkTab>
        </LinkTabs>
      </PageHeader>
      <div
        className="flex flex-wrap items-center gap-2.5"
        role="group"
        aria-label="Filters"
      >
        <SearchFilter
          value={search.q}
          onChange={setQ}
          placeholder="Filter by id or agent"
        />
        <FilterSelect
          label="Agent"
          value={search.agent}
          options={(agents.data ?? []).map((a) => a.name)}
          onChange={(v) => set({ agent: v })}
        />
      </div>
      {data.items.length === 0 ? (
        <EmptyState icon={MessageSquareIcon} title="No sessions match">
          Sessions appear when an agent runs with a{" "}
          <span className="font-mono">session_id</span>.
        </EmptyState>
      ) : (
        <Card className="overflow-x-auto py-0">
          <Table className="min-w-[760px]">
            <TableHeader>
              <TableRow>
                <TableHead>Session</TableHead>
                <TableHead>Agent</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="text-right">Turns</TableHead>
                <TableHead className="text-right">Tokens</TableHead>
                <TableHead className="text-right">Cost</TableHead>
                <TableHead className="text-right">Last activity</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.items.map((s) => (
                <TableRow key={s.id} className="relative">
                  <TableCell className="font-mono text-[13px] text-muted-foreground">
                    <Link
                      to="/sessions/$sessionId"
                      params={{ sessionId: s.id }}
                      className="after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-ring"
                    >
                      {s.id}
                    </Link>
                  </TableCell>
                  <TableCell className="font-medium">{s.agent}</TableCell>
                  <TableCell>
                    <SessionStateBadge state={s.state} />
                  </TableCell>
                  <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                    {s.turns}
                  </TableCell>
                  <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                    {tokens(s.usage.input_tokens + s.usage.output_tokens)}
                  </TableCell>
                  <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                    {cost(s.cost)}
                  </TableCell>
                  <TableCell
                    className="text-right text-[13px] text-subtle-foreground"
                    title={dateTime(s.last_activity_at)}
                  >
                    {ago(s.last_activity_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  )
}
