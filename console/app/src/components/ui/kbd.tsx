import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

// Keycap tag: shortcuts (⌘K) and versions (v0.20.3).
// size="sm" is the ⌘K hint inside the console search field.
const kbdVariants = cva(
  "pointer-events-none inline-flex w-fit items-center justify-center border border-kbd-border bg-kbd font-mono text-muted-foreground shadow-kbd select-none",
  {
    variants: {
      size: {
        default: "rounded-sm px-2 py-[3px] text-xs",
        sm: "rounded-kbd-sm px-1.5 py-0.5 text-[11px]",
      },
    },
    defaultVariants: { size: "default" },
  }
)

function Kbd({
  className,
  size = "default",
  ...props
}: React.ComponentProps<"kbd"> & VariantProps<typeof kbdVariants>) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(kbdVariants({ size }), className)}
      {...props}
    />
  )
}

function KbdGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <kbd
      data-slot="kbd-group"
      className={cn("inline-flex items-center gap-1", className)}
      {...props}
    />
  )
}

export { Kbd, KbdGroup, kbdVariants }
