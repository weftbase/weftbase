import { createFileRoute } from "@tanstack/react-router"
import { MousePointerClickIcon } from "lucide-react"

import { EmptyState } from "@/components/console/empty-state"

export const Route = createFileRoute("/approvals/")({
  component: () => (
    <EmptyState icon={MousePointerClickIcon} title="Pick an approval">
      Its arguments, the run it belongs to and the decision buttons show here.
    </EmptyState>
  ),
})
