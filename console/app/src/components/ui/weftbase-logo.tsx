import * as React from "react"

import { cn } from "@/lib/utils"

const files = import.meta.glob<string>("../../assets/brand/*.svg", {
  eager: true,
  query: "?url",
  import: "default",
})
const src = (name: string) => files[`../../assets/brand/${name}.svg`]

// The SVGs from the design system's Logos group live in src/assets/brand/
// and are bundled as hashed assets, so they resolve under any mount
// prefix (the Console reads its base from <base href>).
// The planes invert on dark (front plane lightest), so each variant ships twice
// and the theme picks one. Below 24px use the flat marks.
// ground="dark" always uses the -dark file: for the graphite hero tile and the
// always-dark CTA band, where the canvas shows the light-front mark in both themes.
type Variant = "lockup" | "mark" | "mark-flat" | "wordmark" | "app-icon"

function WeftbaseLogo({
  variant = "lockup",
  ground = "auto",
  className,
  ...props
}: {
  variant?: Variant
  ground?: "auto" | "dark"
} & Omit<React.ComponentProps<"img">, "src">) {
  if (ground === "dark") {
    return (
      <span data-slot="weftbase-logo" className={cn("inline-flex", className)}>
        <img
          src={src(`${variant}-dark`)}
          alt="Weftbase"
          className="h-full w-auto"
          {...props}
        />
      </span>
    )
  }
  return (
    <span data-slot="weftbase-logo" className={cn("inline-flex", className)}>
      <img
        src={src(`${variant}-light`)}
        alt="Weftbase"
        className="h-full w-auto dark:hidden"
        {...props}
      />
      <img
        src={src(`${variant}-dark`)}
        alt="Weftbase"
        className="hidden h-full w-auto dark:block"
        {...props}
      />
    </span>
  )
}

// The mark beside the live wordmark, as in the console sidebar (mark 33px, 20px text).
function WeftbaseBrand({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      data-slot="weftbase-brand"
      className={cn("inline-flex items-center gap-2.5", className)}
      {...props}
    >
      <WeftbaseLogo variant="mark" className="h-[33px]" alt="" />
      <span className="text-xl leading-none font-semibold tracking-[-0.045em] text-foreground">
        weftbase
      </span>
    </span>
  )
}

export { WeftbaseLogo, WeftbaseBrand }
