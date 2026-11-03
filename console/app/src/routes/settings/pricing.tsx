import { useSuspenseQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

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

export const Route = createFileRoute("/settings/pricing")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(q.pricing()),
  component: Pricing,
})

const usd = (n: number) => `$${n.toFixed(2)}`

function Pricing() {
  const { data } = useSuspenseQuery(q.pricing())
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h2 className="text-lg">Pricing</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          The prices used to cost each model call, per million tokens. A model
          with no price shows its cost as unknown.
        </p>
      </div>
      <Card className="overflow-x-auto py-0">
        <Table className="min-w-[560px]">
          <TableHeader>
            <TableRow>
              <TableHead>Model</TableHead>
              <TableHead className="text-right">Input</TableHead>
              <TableHead className="text-right">Output</TableHead>
              <TableHead className="text-right">Source</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((p) => (
              <TableRow key={`${p.provider}/${p.model}`}>
                <TableCell className="font-mono text-[13px]">
                  <span className="text-subtle-foreground">{p.provider}/</span>
                  {p.model}
                </TableCell>
                <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                  {usd(p.input_per_mtok)}
                </TableCell>
                <TableCell className="text-right font-mono text-[13px] text-muted-foreground">
                  {usd(p.output_per_mtok)}
                </TableCell>
                <TableCell className="text-right">
                  <Badge variant="outline" size="tag" className="font-mono">
                    {p.source}
                  </Badge>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </section>
  )
}
