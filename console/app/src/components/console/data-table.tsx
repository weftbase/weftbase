import { Link } from "@tanstack/react-router"
import type { LinkProps } from "@tanstack/react-router"
import * as React from "react"

import { cn } from "@/lib/utils"

export interface Column<T> {
  key: string
  header: React.ReactNode
  /** A CSS grid track: "96px", "minmax(0,1fr)", "minmax(120px,2fr)". */
  width: string
  align?: "left" | "right"
  /** Numbers, ids, durations, cost: mono with tabular figures. */
  mono?: boolean
  /** Secondary text colour for the whole column. */
  muted?: boolean
  /** Drop the column when the table is narrower than this many pixels
   * (its own width, not the window's: a detail may be open beside it).
   * Give it to the columns a narrow list can do without. */
  hideBelow?: number
  cell: (row: T) => React.ReactNode
}

// The dense table every list uses. Rows are 32px, the header 28px, cells
// have 8px of padding. A row is a real link (middle-click and copy-link
// work); ↑/↓ or j/k move between rows, Home/End jump, Enter opens.
// It has no card around it: on a list page the table is the page (wrap it
// in <Page flush>); inside a section it sits in one bordered container.
//
//   <DataTable label="Runs" columns={columns} rows={runs} rowKey={(r) => r.id}
//     rowLink={(r) => ({ to: "/runs/$runId", params: { runId: r.id } })} />
export function DataTable<T>({
  label,
  columns: allColumns,
  rows,
  rowKey,
  rowLink,
  selectedKey,
  bordered = false,
  stickyHeader = true,
  footer,
  className,
}: {
  /** The accessible name: "Runs". */
  label: string
  columns: Column<T>[]
  rows: readonly T[]
  rowKey: (row: T) => string
  rowLink?: (row: T) => LinkProps
  /** The row shown as selected (its detail is open beside the table). */
  selectedKey?: string
  /** Draw the container border (a table inside a padded page). */
  bordered?: boolean
  stickyHeader?: boolean
  /** Under the last row: "Load more", a count. */
  footer?: React.ReactNode
  className?: string
}) {
  const body = React.useRef<HTMLDivElement>(null)
  // The room the table has: its scroll container's width.
  const table = React.useRef<HTMLDivElement>(null)
  const [room, setRoom] = React.useState<number>()
  React.useLayoutEffect(() => {
    const el = table.current?.parentElement
    if (!el || typeof ResizeObserver === "undefined") return
    setRoom(el.clientWidth)
    const ro = new ResizeObserver(() => setRoom(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const columns =
    room === undefined
      ? allColumns
      : allColumns.filter((c) => !c.hideBelow || room >= c.hideBelow)
  const template = columns.map((c) => c.width).join(" ")

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return
    const all = Array.from(
      body.current?.querySelectorAll<HTMLElement>("[data-row]") ?? []
    )
    if (all.length === 0) return
    const i = all.indexOf(document.activeElement as HTMLElement)
    let next: number
    switch (e.key) {
      case "ArrowDown":
      case "j":
        next = Math.min(all.length - 1, i + 1)
        break
      case "ArrowUp":
      case "k":
        next = Math.max(0, i - 1)
        break
      case "Home":
        next = 0
        break
      case "End":
        next = all.length - 1
        break
      default:
        return
    }
    e.preventDefault()
    all[next].focus()
  }

  const cellClass = (c: Column<T>) =>
    cn(
      "flex min-w-0 items-center px-2",
      c.align === "right" && "justify-end text-right",
      c.mono && "font-mono text-xs tabular-nums",
      c.muted && "text-muted-foreground"
    )

  const rowClass =
    "grid h-8 items-stretch border-b border-table-border outline-none hover:bg-muted/60 focus-visible:bg-muted focus-visible:shadow-[inset_2px_0_0_var(--ring)] aria-selected:bg-table-row-selected"

  return (
    <div
      ref={table}
      role="table"
      aria-label={label}
      aria-rowcount={rows.length}
      className={cn(
        "min-w-fit text-sm",
        bordered && "overflow-hidden rounded-alert border bg-card",
        className
      )}
    >
      <div
        role="row"
        className={cn(
          "grid h-7 items-stretch border-b border-table-border bg-background",
          bordered && "bg-card",
          stickyHeader && "sticky top-0 z-10"
        )}
        style={{ gridTemplateColumns: template }}
      >
        {columns.map((c) => (
          <div
            key={c.key}
            role="columnheader"
            className={cn(
              "flex min-w-0 items-center truncate px-2 table-head",
              c.align === "right" && "justify-end"
            )}
          >
            {c.header}
          </div>
        ))}
      </div>
      <div
        ref={body}
        role="rowgroup"
        onKeyDown={onKeyDown}
        className={cn(bordered && !footer && "[&>*:last-child]:border-b-0")}
      >
        {rows.map((row) => {
          const key = rowKey(row)
          const cells = columns.map((c) => (
            <div key={c.key} role="cell" className={cellClass(c)}>
              <span className="min-w-0 truncate">{c.cell(row)}</span>
            </div>
          ))
          const selected = key === selectedKey
          return rowLink ? (
            <Link
              key={key}
              {...rowLink(row)}
              role="row"
              data-row
              aria-selected={selected || undefined}
              className={rowClass}
              style={{ gridTemplateColumns: template }}
            >
              {cells}
            </Link>
          ) : (
            <div
              key={key}
              role="row"
              data-row
              tabIndex={0}
              aria-selected={selected || undefined}
              className={rowClass}
              style={{ gridTemplateColumns: template }}
            >
              {cells}
            </div>
          )
        })}
      </div>
      {footer && (
        <div className="flex h-8 items-center justify-center gap-2 px-2 text-xs text-muted-foreground">
          {footer}
        </div>
      )}
    </div>
  )
}
