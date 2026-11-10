import { Link } from "@tanstack/react-router"
import type { LinkProps } from "@tanstack/react-router"

import { cn } from "@/lib/utils"

// Segmented: a small switch between a few views of the same thing (a
// time window, what a table groups by). Each option is a link, so the
// choice is a URL.
//
//   <Segmented label="Window" options={[{ label: "24h", link: {…}, active: true }]} />
export function Segmented({
  label,
  options,
  className,
}: {
  /** The accessible name of the group. */
  label: string
  options: { label: string; link: LinkProps; active: boolean; title?: string }[]
  className?: string
}) {
  return (
    <nav
      aria-label={label}
      className={cn(
        "inline-flex h-7 flex-none items-center gap-0.5 rounded-input border bg-muted p-0.5",
        className
      )}
    >
      {options.map((o) => (
        <Link
          key={o.label}
          {...o.link}
          replace
          title={o.title}
          aria-current={o.active ? "true" : undefined}
          className="inline-flex h-full items-center rounded-[4px] px-2 text-xs font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring aria-[current=true]:bg-background aria-[current=true]:text-foreground aria-[current=true]:shadow-tab-active"
        >
          {o.label}
        </Link>
      ))}
    </nav>
  )
}
