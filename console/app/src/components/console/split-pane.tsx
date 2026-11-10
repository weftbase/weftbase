import * as React from "react"

import { usePref } from "@/lib/prefs"
import { cn } from "@/lib/utils"

// SplitPane: a main area and an optional detail beside it, with a
// draggable divider. Without `detail` the main area takes the full
// width. The detail's width is remembered per `id`. The divider is a
// focusable separator: ←/→ resize by 16px, Home/End go to the limits.
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
  className?: string
}) {
  const [stored, setStored] = usePref(`wb.split.${id}`, String(defaultWidth))
  const clamp = (n: number) => Math.min(max, Math.max(min, Math.round(n)))
  const width = clamp(Number(stored) || defaultWidth)
  const root = React.useRef<HTMLDivElement>(null)
  const [dragging, setDragging] = React.useState(false)

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
    <div ref={root} className={cn("flex h-full min-h-0 min-w-0", className)}>
      <div className="min-h-0 min-w-0 flex-1 overflow-auto">{children}</div>
      {open && (
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
            style={{ width, maxWidth: gutter ? "56%" : "70%" }}
          >
            {detail}
          </aside>
        </>
      )}
    </div>
  )
}
