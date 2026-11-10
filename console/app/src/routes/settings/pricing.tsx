import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import { ErrorState, LoadingRows } from "@/components/console/empty-state"
import { Section } from "@/components/console/page-header"
import { q } from "@/lib/api/queries"
import type { PricingTable } from "@/lib/api/types"

// Settings, Pricing: the table costs are computed from (read-only; set
// with the server's --pricing flag). USD per 1M tokens.
export const Route = createFileRoute("/settings/pricing")({
  component: Pricing,
})

type Price = PricingTable["models"][number]
const rate = (n: number | undefined) => (n === undefined ? "—" : `$${n}`)

const COLUMNS: Column<Price>[] = [
  {
    key: "provider",
    header: "Provider",
    width: "minmax(100px, 160px)",
    muted: true,
    cell: (p) => p.provider ?? "any",
  },
  {
    key: "model",
    header: "Model",
    width: "minmax(160px, 1fr)",
    mono: true,
    cell: (p) => p.model_glob,
  },
  {
    key: "in",
    header: "Input",
    width: "80px",
    align: "right",
    mono: true,
    cell: (p) => rate(p.input),
  },
  {
    key: "out",
    header: "Output",
    width: "80px",
    align: "right",
    mono: true,
    cell: (p) => rate(p.output),
  },
  {
    key: "cached",
    header: "Cached in",
    width: "88px",
    align: "right",
    mono: true,
    cell: (p) => rate(p.cached_input),
  },
  {
    key: "write",
    header: "Cache write",
    width: "96px",
    align: "right",
    mono: true,
    cell: (p) => rate(p.cache_write),
  },
]

function Pricing() {
  const pricing = useQuery(q.pricing())
  if (pricing.error) return <ErrorState error={pricing.error} what="Pricing" />
  if (!pricing.data) return <LoadingRows />
  const p = pricing.data
  return (
    <Section
      title="Prices, USD per 1M tokens"
      action={
        <span>
          {p.source === "default" ? "Shipped table" : (p.path ?? p.source)},
          dated {p.dated}
        </span>
      }
    >
      <DataTable
        label="Prices"
        bordered
        stickyHeader={false}
        columns={COLUMNS}
        rows={p.models}
        rowKey={(m) => `${m.provider ?? "*"}/${m.model_glob}`}
      />
    </Section>
  )
}
