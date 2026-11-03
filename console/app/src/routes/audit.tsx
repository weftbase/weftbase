import { createFileRoute } from "@tanstack/react-router"
import { ScrollTextIcon } from "lucide-react"

import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { Badge } from "@/components/ui/badge"

export const Route = createFileRoute("/audit")({
  head: () => ({ meta: [{ title: "Audit · weftbase" }] }),
  component: () => (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-4">
      <PageHeader
        title="Audit"
        description="A tamper-evident record of who did what, per tenant."
      >
        <Badge variant="outline" size="tag" className="font-mono">
          planned
        </Badge>
      </PageHeader>
      <EmptyState icon={ScrollTextIcon} title="The audit log is planned">
        It arrives with tenants, keys and members: every decision, key and
        deletion, with a button to verify the chain.
      </EmptyState>
    </div>
  ),
})
