import * as React from "react"

import { usePref } from "@/lib/prefs"
import { cn } from "@/lib/utils"

// SplitPane: a main area and an optional detail beside it, with a
// draggable divider. Without `detail` the main area takes the full
// width. The detail's width is remembered per `id`. The divider is a
// focusable separator: ←/→ resize by 16px, Home/End go to the limits.
// When the pane is too narrow to leave the main area `minMain` pixels
// beside the detail, the detail becomes a sheet over the main area (it
// keeps its own close button and Esc), so nothing is squeezed.
//
//   <SplitPane id="run" detail={span && <SpanDetail span={span} />}>
//     <Waterfall … />
//   </SplitPane>
export function SplitPane({
  id,
  children,
  detail,
  defaultWidth = 440,
  min = 320,
  max = 800,
  gutter = false,
  minMain = 520,
  className,
}: {
  /** Names the remembered width. */
  id: string
  children: React.ReactNode
  /** The detail; null or undefined closes it. */
  detail?: React.ReactNode
  defaultWidth?: number
  min?: number
  max?: number
  /** The divider is a 12px gap, not a hairline: for two bordered
   * containers side by side (the run page's waterfall and detail). */
  gutter?: boolean
  /** The least width the main area keeps beside the detail; under it
   * the detail is laid over the main area. */
  minMain?: number
  className?: string
}) {
  const [stored, setStored] = usePref(`wb.split.${id}`, String(defaultWidth))
  const clamp = (n: number) => Math.min(max, Math.max(min, Math.round(n)))
  const width = clamp(Number(stored) || defaultWidth)
  const root = React.useRef<HTMLDivElement>(null)
  const [dragging, setDragging] = React.useState(false)
  // The pane's own width (not the window's: the sidebar may be open).
  const [space, setSpace] = React.useState<number>()
  React.useLayoutEffect(() => {
    const el = root.current
    if (!el) return
    setSpace(el.clientWidth)
    if (typeof ResizeObserver === "undefined") return
    const ro = new ResizeObserver(() => setSpace(el.clientWidth))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const sheet = space !== undefined && space - min < minMain

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    setDragging(true)
  }
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging || !root.current) return
    const right = root.current.getBoundingClientRect().right
    setStored(String(clamp(right - e.clientX)))
  }
  const onKeyDown = (e: React.KeyboardEvent) => {
    const step =
      e.key === "ArrowLeft" ? 16 : e.key === "ArrowRight" ? -16 : undefined
    if (step !== undefined) setStored(String(clamp(width + step)))
    else if (e.key === "Home") setStored(String(max))
    else if (e.key === "End") setStored(String(min))
    else return
    e.preventDefault()
  }

  const open = detail !== undefined && detail !== null && detail !== false
  return (
    <div
      ref={root}
      className={cn("relative flex h-full min-h-0 min-w-0", className)}
    >
      <div className="min-h-0 min-w-0 flex-1 overflow-auto">{children}</div>
      {open && sheet && (
        <aside
          data-sheet
          className="absolute inset-y-0 right-0 z-20 min-h-0 overflow-auto border-l bg-background shadow-card"
          style={{ width: Math.min(width, Math.max(280, space - 40)) }}
        >
          {detail}
        </aside>
      )}
      {open && !sheet && (
        <>
          <div
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize detail"
            aria-valuenow={width}
            aria-valuemin={min}
            aria-valuemax={max}
            tabIndex={0}
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={() => setDragging(false)}
            onPointerCancel={() => setDragging(false)}
            onKeyDown={onKeyDown}
            className={cn(
              "relative flex-none cursor-col-resize outline-none",
              gutter
                ? "w-3 after:absolute after:inset-y-2 after:left-1/2 after:w-0.5 after:-translate-x-1/2 after:rounded-full hover:after:bg-ring focus-visible:after:bg-ring"
                : "w-px bg-border after:absolute after:inset-y-0 after:-left-1 after:w-2 hover:bg-ring focus-visible:bg-ring",
              dragging && (gutter ? "after:bg-ring" : "bg-ring")
            )}
          />
          <aside
            className="min-h-0 flex-none overflow-auto"
            style={{
              width: Math.min(
                width,
                Math.max(min, (space ?? Infinity) - minMain)
              ),
            }}
          >
            {detail}
          </aside>
        </>
      )}
    </div>
  )
}
