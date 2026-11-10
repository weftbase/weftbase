import { Link } from "@tanstack/react-router"
import type { LinkProps } from "@tanstack/react-router"
import * as React from "react"

import { cn } from "@/lib/utils"

// The page header is one 40px line: title, then status, id and facts,
// with actions on the right. No eyebrow, no description stack.
//
//   <PageHeader title="support-triage" status={<StatusBadge state="failed" />}
//     id={<CopyId id={run.id} />} actions={<Button size="sm">Cancel</Button>} />
export function PageHeader({
  title,
  status,
  id,
  meta,
  actions,
  className,
}: {
  title: React.ReactNode
  /** A StatusBadge, right after the title. */
  status?: React.ReactNode
  /** A CopyId. */
  id?: React.ReactNode
  /** Small muted facts or chips after the id (version, count, "Live"). */
  meta?: React.ReactNode
  /** Buttons on the right (size default or sm). */
  actions?: React.ReactNode
  className?: string
}) {
  return (
    <header
      className={cn("flex h-10 flex-none items-center gap-2 px-4", className)}
    >
      <h1 className="min-w-0 truncate text-sm font-semibold">{title}</h1>
      {status}
      {id}
      {meta && (
        <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
          {meta}
        </div>
      )}
      {actions && (
        <div className="ml-auto flex flex-none items-center gap-2">
          {actions}
        </div>
      )}
    </header>
  )
}

/** Page is a screen's frame: it fills the main area, the header and bars
 * stay put and `children` scroll. Use `flush` for a full-page table (the
 * table is the page, no padding around it). */
export function Page({
  header,
  bar,
  children,
  flush = false,
  className,
}: {
  header: React.ReactNode
  /** A FilterBar or LinkTabs row under the header. */
  bar?: React.ReactNode
  children: React.ReactNode
  flush?: boolean
  className?: string
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}
      {bar}
      <div
        className={cn(
          "min-h-0 flex-1 overflow-auto border-t",
          !flush && "p-4",
          className
        )}
      >
        {children}
      </div>
    </div>
  )
}

/** Section is a titled block on a padded page: an 11px uppercase label
 * with an optional action, then one bordered container. */
export function Section({
  title,
  action,
  children,
  className,
}: {
  title: React.ReactNode
  action?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <section className={cn("flex min-w-0 flex-col gap-2", className)}>
      <div className="flex h-5 items-center justify-between gap-2">
        <h2 className="table-head">{title}</h2>
        {action && (
          <div className="text-xs text-muted-foreground">{action}</div>
        )}
      </div>
      {children}
    </section>
  )
}

/** LinkTabs: tabs whose selection is a URL (a search param or a route).
 * An underline row, 32px, for the views of one page. */
export function LinkTabs({
  tabs,
  className,
}: {
  tabs: {
    label: React.ReactNode
    link: LinkProps
    active: boolean
    count?: number
  }[]
  className?: string
}) {
  return (
    <nav
      aria-label="Views"
      className={cn("flex h-8 flex-none items-stretch gap-4 px-4", className)}
    >
      {tabs.map((t, i) => (
        // `active` is the page's answer and the only one. The router has
        // its own idea of an active link (it matches the search loosely,
        // so the tab without a param matches every tab's URL, and it then
        // sets aria-current itself, over ours): make its match exact, and
        // style from `data-tab-active`, which only this component sets.
        <Link
          key={i}
          {...t.link}
          activeOptions={{ exact: true, includeSearch: true }}
          aria-current={t.active ? "page" : undefined}
          data-tab-active={t.active || undefined}
          className="-mb-px inline-flex items-center gap-1.5 border-b-2 border-transparent text-sm font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:text-foreground focus-visible:underline data-[tab-active]:border-foreground data-[tab-active]:text-foreground"
        >
          {t.label}
          {t.count !== undefined && (
            <span className="font-mono text-xs text-subtle-foreground">
              {t.count}
            </span>
          )}
        </Link>
      ))}
    </nav>
  )
}
