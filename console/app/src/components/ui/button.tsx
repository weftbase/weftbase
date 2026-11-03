import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "@/lib/utils"

// Weftbase "Keycap · lit" buttons: gradient face, 1px top highlight, 2px ledge.
// Sizes from the brand canvas: sm 36px (console), default 44px, lg 52px (hero, CTA).
const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 font-medium whitespace-nowrap transition-[translate,box-shadow,background-color] duration-75 outline-none select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
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
        sm: "h-9 rounded-input px-3.5 text-sm",
        default: "h-11 rounded-lg px-5 text-[15px]",
        lg: "h-13 rounded-alert px-6.5 text-base [&_svg:not([class*='size-'])]:size-[17px]",
        icon: "size-11 rounded-lg [&_svg:not([class*='size-'])]:size-[18px]",
        "icon-sm": "size-9 rounded-input",
        "icon-lg":
          "size-13 rounded-alert [&_svg:not([class*='size-'])]:size-[18px]",
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
