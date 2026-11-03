import { Link } from "@tanstack/react-router"

import { RunStatusBadge } from "@/components/console/status"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { RunSummary } from "@/lib/api/types"
import { ago, dateTime, duration, tokens } from "@/lib/format"

// The runs table from the Console artboard: Run, Agent, Status, Time,
// Tokens, Started. IDs and numbers in 13px mono. With `select`, a row
// click selects the run (?run=) instead of opening it.
export function RunsTable({
  runs,
  selected,
  select = false,
}: {
  runs: RunSummary[]
  selected?: string
  select?: boolean
}) {
  return (
    <Table className="min-w-[820px] table-fixed">
      <colgroup>
        <col className="w-[150px]" />
        <col />
        <col className="w-[200px]" />
        <col className="w-[92px]" />
        <col className="w-[92px]" />
        <col className="w-[112px]" />
      </colgroup>
      <TableHeader>
        <TableRow>
          <TableHead>Run</TableHead>
          <TableHead>Agent</TableHead>
          <TableHead>Status</TableHead>
          <TableHead className="text-right">Time</TableHead>
          <TableHead className="text-right">Tokens</TableHead>
          <TableHead className="text-right">Started</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {runs.map((r) => (
          <TableRow
            key={r.id}
            aria-selected={selected === r.id ? true : undefined}
            className="relative"
          >
            <TableCell className="font-mono text-[13px] text-muted-foreground">
              {select ? (
                <Link
                  from="/runs/"
                  to="/runs"
                  search={(prev) => ({ ...prev, run: r.id })}
                  className="after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:-outline-offset-2 focus-visible:after:outline-ring"
                  aria-label={`Show run ${r.id}`}
                >
                  {r.id}
                </Link>
              ) : (
                <Link
                  to="/runs/$runId"
                  params={{ runId: r.id }}
                  className="after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:-outline-offset-2 focus-visible:after:outline-ring"
                >
                  {r.id}
                </Link>
              )}
            </TableCell>
            <TableCell className="truncate font-medium">{r.agent}</TableCell>
            <TableCell>
              <RunStatusBadge status={r.status} />
            </TableCell>
            <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
              {duration(r.duration_ms)}
            </TableCell>
            <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
              {tokens(r.usage.input_tokens + r.usage.output_tokens)}
            </TableCell>
            <TableCell
              className="text-right text-[13px] text-subtle-foreground"
              title={dateTime(r.started_at)}
            >
              {ago(r.started_at)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}
