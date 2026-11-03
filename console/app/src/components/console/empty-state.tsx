import type { LucideIcon } from "lucide-react"
import * as React from "react"

import { Card } from "@/components/ui/card"

// EmptyState (design system): a centred card with a graphite icon tile,
// one line saying what is missing and one saying how to add the first.
export function EmptyState({
  icon: Icon,
  title,
  children,
}: {
  icon: LucideIcon
  title: string
  children?: React.ReactNode
}) {
  return (
    <Card className="items-center gap-2.5 px-6 py-10 text-center">
      <span className="flex size-10 items-center justify-center rounded-icon tile">
        <Icon className="size-[18px]" />
      </span>
      <div className="text-sm font-semibold">{title}</div>
      {children && (
        <div className="max-w-sm text-[13px] text-muted-foreground">
          {children}
        </div>
      )}
    </Card>
  )
}
