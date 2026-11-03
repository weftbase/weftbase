import * as React from "react"
import { Card } from "@/components/ui/card"

// Stat card (Card README): label small muted, value in the stat style,
// meta in subtle-foreground, 6px apart.
export function StatCard({
  label,
  value,
  meta,
}: {
  label: string
  value: React.ReactNode
  meta?: React.ReactNode
}) {
  return (
    <Card className="gap-1.5 px-[18px]">
      <div className="text-[13px] text-muted-foreground">{label}</div>
      <div className="stat">{value}</div>
      {meta && <div className="text-xs text-subtle-foreground">{meta}</div>}
    </Card>
  )
}
