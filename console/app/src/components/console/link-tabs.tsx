import { Link } from "@tanstack/react-router"
import type { LinkProps } from "@tanstack/react-router"
import * as React from "react"

import { tabTriggerClass, tabsListClass } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"

// Segmented tabs whose selection is the URL: each tab is a link, and the
// current one carries aria-current="page" (styled like a selected tab).
export function LinkTabs({
  label,
  children,
  className,
}: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <nav aria-label={label} className={cn(tabsListClass, className)}>
      {children}
    </nav>
  )
}

// The router marks the tab whose full search matches the current URL
// with aria-current="page", so tabs pass their whole next search.
export function LinkTab({
  children,
  ...props
}: LinkProps & { children: React.ReactNode }) {
  return (
    <Link
      {...props}
      className={tabTriggerClass}
      activeOptions={{ exact: true, includeSearch: true }}
    >
      {children}
    </Link>
  )
}
