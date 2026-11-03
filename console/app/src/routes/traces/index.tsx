import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"
import * as React from "react"

import { SearchFilter } from "@/components/console/filters"
import { PageHeader } from "@/components/console/page-header"
import { Badge } from "@/components/ui/badge"
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
import { ago, dateTime, duration } from "@/lib/format"
import { useEnv, validateTraces } from "@/lib/search"

export const Route = createFileRoute("/traces/")({
  validateSearch: validateTraces,
  loaderDeps: ({ search }) => ({ env: search.env ?? "live", q: search.q }),
  loader: ({ context: { queryClient }, deps }) =>
    queryClient.ensureQueryData(q.traces(deps.env, deps.q)),
  head: () => ({ meta: [{ title: "Traces · weftbase" }] }),
  component: Traces,
})

function Traces() {
  const search = Route.useSearch()
  const env = useEnv()
  const navigate = useNavigate({ from: Route.fullPath })
  const { data } = useSuspenseQuery(q.traces(env, search.q))
  const setQ = React.useCallback(
    (v: string | undefined) =>
      void navigate({ search: (p) => ({ ...p, q: v }) }),
    [navigate]
  )
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-4">
      <PageHeader
        title="Traces"
        description="Every trace sent to this server: weft runs and stock OpenTelemetry GenAI spans."
      >
        <SearchFilter
          value={search.q}
          onChange={setQ}
          placeholder="Filter by id, span or service"
        />
      </PageHeader>
      <Card className="overflow-x-auto py-0">
        <Table className="min-w-[760px]">
          <TableHeader>
            <TableRow>
              <TableHead>Trace</TableHead>
              <TableHead>Root span</TableHead>
              <TableHead>Source</TableHead>
              <TableHead className="text-right">Spans</TableHead>
              <TableHead className="text-right">Time</TableHead>
              <TableHead className="text-right">Started</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.items.map((t) => (
              <TableRow key={t.id} className="relative">
                <TableCell className="font-mono text-[13px] text-muted-foreground">
                  <Link
                    to="/traces/$traceId"
                    params={{ traceId: t.id }}
                    className="after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-ring"
                  >
                    {t.id.slice(0, 16)}
                  </Link>
                </TableCell>
                <TableCell className="max-w-[320px] truncate font-medium">
                  {t.root}
                  {t.error && (
                    <Badge variant="failed" size="sm" className="ml-2">
                      Error
                    </Badge>
                  )}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" size="tag" className="font-mono">
                    {t.source === "weft" ? "weft" : "GenAI"}
                  </Badge>
                </TableCell>
                <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                  {t.span_count}
                </TableCell>
                <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                  {duration(t.duration_ms)}
                </TableCell>
                <TableCell
                  className="text-right text-[13px] text-subtle-foreground"
                  title={dateTime(t.started_at)}
                >
                  {ago(t.started_at)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
}
