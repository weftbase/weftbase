import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

// Status pills. Every pill carries its word, never colour alone.
// Run states: succeeded, running, waiting, failed. Marketing labels: success,
// neutral, warning (maturity tags) and size="sm" for "Coming soon".
const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center gap-1 rounded-sm font-medium whitespace-nowrap [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground",
        secondary: "bg-muted text-foreground",
        outline: "border border-border text-foreground",
        destructive: "bg-danger-muted text-danger-foreground",
        succeeded: "bg-success-muted text-success-foreground",
        running: "bg-neutral-muted text-neutral-foreground",
        waiting: "bg-warning-muted text-warning-foreground",
        failed: "bg-danger-muted text-danger-foreground",
        success: "bg-success-muted text-success-foreground",
        neutral: "bg-neutral-muted text-neutral-foreground",
        warning: "bg-warning-muted text-warning-foreground",
      },
      size: {
        default: "h-5 px-1.5 text-xs",
        tag: "h-5 px-1.5 text-xs",
        sm: "h-[18px] px-1.5 text-[11px]",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

const dotColor: Record<string, string> = {
  succeeded: "bg-success",
  running: "bg-neutral",
  waiting: "bg-warning",
  failed: "bg-danger",
  success: "bg-success",
  neutral: "bg-neutral",
  warning: "bg-warning",
}

function Badge({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  children,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot.Root : "span"
  const dot = variant ? dotColor[variant] : undefined

  return (
    <Comp
      data-slot="badge"
      data-variant={variant}
      className={cn(badgeVariants({ variant, size }), className)}
      {...props}
    >
      {dot && (
        <span
          aria-hidden
          className={cn(
            "rounded-full",
            size === "sm" ? "size-[5px]" : "size-1.5",
            dot
          )}
        />
      )}
      {children}
    </Comp>
  )
}

export { Badge, badgeVariants }
