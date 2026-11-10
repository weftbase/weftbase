import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

// Weftbase "Keycap · lit" buttons: gradient face, 1px top highlight, 2px ledge.
// Compact sizes (CONVENTIONS.md): default 28px, sm 24px, lg 32px; icon 28, icon-sm 24.
const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-1.5 font-medium whitespace-nowrap transition-[translate,box-shadow,background-color] duration-75 outline-none select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5",
  {
    variants: {
      variant: {
        default: "keycap",
        secondary: "keycap-secondary",
        outline: "keycap-secondary",
        destructive: "keycap-destructive",
        ghost:
          "border border-transparent hover:bg-accent hover:text-accent-foreground",
        link: "text-foreground underline-offset-4 hover:underline",
      },
      size: {
        sm: "h-6 rounded-input px-2 text-xs",
        default: "h-7 rounded-input px-2.5 text-sm",
        lg: "h-8 rounded-input px-3 text-sm",
        icon: "size-7 rounded-input",
        "icon-sm": "size-6 rounded-input",
        "icon-lg": "size-8 rounded-input",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
  }) {
  const Comp = asChild ? Slot.Root : "button"

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
