import { Badge } from "@/components/ui/badge"
import type { Label } from "@/lib/api/types"

// Deployment labels are names, not states: graphite outline tags in
// mono, never a status colour.
export const LABELS: Label[] = ["dev", "staging", "prod"]

export function LabelTag({
  label,
  version,
}: {
  label: Label
  version?: string
}) {
  return (
    <Badge variant="outline" size="tag" className="gap-1 font-mono">
      {label}
      {version && <span className="text-subtle-foreground">{version}</span>}
    </Badge>
  )
}
