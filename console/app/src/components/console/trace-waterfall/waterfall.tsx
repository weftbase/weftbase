import {
  ChevronRightIcon,
  ChevronsDownUpIcon,
  ChevronsUpDownIcon,
  FoldVerticalIcon,
  ListTreeIcon,
  PauseIcon,
  PlayIcon,
  RotateCcwIcon,
  RotateCwIcon,
  ScissorsIcon,
  SearchIcon,
  SkipBackIcon,
  SkipForwardIcon,
  TriangleAlertIcon,
  WandSparklesIcon,
  XIcon,
  ZoomOutIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"
import * as React from "react"

import { LivePill } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  cost as formatCost,
  count,
  duration,
  offset,
  tokens,
} from "@/lib/format"
import { usePref } from "@/lib/prefs"
import { SPAN_KINDS, SPAN_KIND_ORDER } from "@/lib/span-kind"
import { cn } from "@/lib/utils"

import { SPEEDS } from "./clock"
import type { Speed } from "./clock"
import {
  allExpandable,
  ancestorIds,
  defaultCollapsed,
  gist,
  summarySegments,
  visibleRows,
} from "./model"
import type { LegendKind, TraceInput, TraceNode, TraceRow } from "./model"
import { pan, ticks, zoomAt } from "./scale"
import { useTraceView } from "./use-trace-view"
import type { TraceView } from "./use-trace-view"

// The trace waterfall (CONSOLE-UX, "Trace waterfall"). One bordered
// container: a toolbar (search, fold), the "Steps" header over the name
// column and the ruler over the chart, the rows, and a footer with the
// transport and the legend. See CONVENTIONS.md, "Waterfall", for the
// props and how to embed it.

const ROW = 26
const INDENT = 14
const PAD_L = 8
const OVERSCAN = 6
// The name column: sized to its names until a person drags the divider.
const NAME_MIN = 160
const NAME_MAX = 640
const NAME_AUTO: Record<"full" | "compact", [number, number]> = {
  full: [200, 440],
  compact: [180, 340],
}
// The chart keeps at least this much when the divider is dragged.
const CHART_MIN = 200

export interface TraceWaterfallProps {
  /** The spans and what is known beside them. The component keeps its
   * own playhead. Use this, or `view`. */
  trace?: TraceInput
  /** A view made by useTraceView, when the page needs the playhead too
   * (a stat strip that follows it). */
  view?: TraceView
  /** The selected row: a span id (or a row id the model made). */
  selected?: string
  /** A row was chosen (click, Enter) or the selection closed (Esc). */
  onSelect?: (id: string | undefined) => void
  /** No toolbar, strip, footer or legend: for a session turn. The
   * container grows with its rows up to `maxHeight`. */
  compact?: boolean
  /** Draw the stat strip inside the container (a raw trace). The run
   * page draws its own above the tabs and leaves this off. */
  strip?: boolean
  /** Compact mode: the tallest the rows area gets, in pixels. */
  maxHeight?: number
  /** Shown instead of rows when there are no spans. */
  empty?: React.ReactNode
  /** The live stream's state, for the pill; shown while the view is live. */
  stream?: "connecting" | "live" | "reconnecting"
  /** Take the keyboard on mount (the run page's timeline). */
  autoFocus?: boolean
  /** Names the remembered name-column width. */
  id?: string
  className?: string
}

export function TraceWaterfall(props: TraceWaterfallProps) {
  if (props.view) return <Waterfall {...props} view={props.view} />
  return <OwnView {...props} />
}

const NO_TRACE: TraceInput = { spans: [] }

function OwnView(props: TraceWaterfallProps) {
  const view = useTraceView(props.trace ?? NO_TRACE)
  return <Waterfall {...props} view={view} />
}

const typing = (t: EventTarget | null) =>
  t instanceof HTMLElement &&
  (t.tagName === "INPUT" ||
    t.tagName === "TEXTAREA" ||
    t.tagName === "SELECT" ||
    t.isContentEditable)

function Waterfall({
  view,
  selected,
  onSelect,
  compact = false,
  strip = false,
  maxHeight = 360,
  empty,
  stream,
  autoFocus = false,
  id = "trace",
  className,
}: TraceWaterfallProps & { view: TraceView }) {
  const { model, scale, playback, live } = view
  // "": nobody has dragged the divider; the names decide.
  const [storedName, setStoredName] = usePref(`wb.waterfall.${id}.name`, "")
  const [query, setQuery] = React.useState("")
  const [kinds, setKinds] = React.useState<ReadonlySet<LegendKind>>(new Set())
  const [collapsed, setCollapsed] = React.useState<ReadonlySet<string>>(
    new Set()
  )
  const [cursor, setCursor] = React.useState<string | undefined>(selected)
  const [zoom, setZoom] = React.useState<[number, number] | null>(null)
  const [band, setBand] = React.useState<[number, number] | null>(null)
  const [scrollTop, setScrollTop] = React.useState(0)
  const [size, setSize] = React.useState({ w: 0, h: 0, bar: 0 })
  const [tip, setTip] = React.useState<{
    node: TraceNode
    x: number
    y: number
  } | null>(null)

  const scroller = React.useRef<HTMLDivElement>(null)
  const search = React.useRef<HTMLInputElement>(null)
  const root = React.useRef<HTMLDivElement>(null)

  // Fold rows deeper than two levels once, when the tree first has rows.
  const seeded = React.useRef<string | undefined>(undefined)
  const rootId = model.roots.at(0)?.id
  React.useEffect(() => {
    if (rootId === undefined || seeded.current !== undefined) return
    seeded.current = rootId
    const folded = defaultCollapsed(model)
    if (selected) for (const a of ancestorIds(model, selected)) folded.delete(a)
    setCollapsed(folded)
  }, [rootId, model, selected])

  const rows = React.useMemo(
    () => visibleRows(model, { collapsed, query, kinds }),
    [model, collapsed, query, kinds]
  )
  const filtering = query.trim() !== "" || kinds.size > 0
  const selectedNode = selected ? model.byId.get(selected) : undefined
  const selectedId = selectedNode?.id

  // Geometry.
  React.useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const measure = () =>
      setSize({
        w: el.clientWidth,
        h: el.clientHeight,
        bar: el.offsetWidth - el.clientWidth,
      })
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // The name column: as wide as the person made it, else as wide as its
  // names need, within bounds. In a narrow container the names give way
  // first.
  const [autoMin, autoMax] = NAME_AUTO[compact ? "compact" : "full"]
  const fonts = useFontsReady()
  const autoName = React.useMemo(
    () => Math.min(autoMax, Math.max(autoMin, nameWidthOf(model.nodes))),
    // Measured again when the page's fonts have loaded.
    [model.nodes, autoMin, autoMax, fonts]
  )
  const chosenName = Number(storedName) || undefined
  // The widest the divider goes here: the chart keeps its minimum.
  const nameLimit = Math.max(
    NAME_MIN,
    Math.min(NAME_MAX, (size.w || Infinity) - CHART_MIN)
  )
  const nameW =
    chosenName !== undefined
      ? Math.min(nameLimit, Math.max(NAME_MIN, chosenName))
      : Math.min(autoName, Math.max(NAME_MIN, size.w * 0.45 || autoName))
  const chartW = Math.max(0, size.w - nameW)
  // The plot leaves room for the label after the last bar: the widest
  // plot at which every bar's label still ends inside the chart.
  const plotW = React.useMemo(() => {
    const room = chartW - PAD_L - 8
    let w = room
    for (const n of model.nodes) {
      if (n.kind === "marker") continue
      const frac = scale.length > 0 ? scale.toDisplay(n.end) / scale.length : 1
      if (frac <= 0) continue
      const label = labelWidth(n, model.slowestId === n.id)
      w = Math.min(w, (room - 6 - label) / frac)
    }
    return Math.max(1, Math.max(w, room * 0.35))
  }, [chartW, model, scale])
  const v0 = zoom ? zoom[0] : 0
  const v1 = zoom
    ? Math.min(zoom[1], Math.max(scale.length, zoom[0] + 1))
    : scale.length
  const span = Math.max(1, v1 - v0)
  const xOf = React.useCallback(
    (d: number) => PAD_L + ((d - v0) / span) * plotW,
    [v0, span, plotW]
  )
  const xAt = React.useCallback(
    (t: number) => xOf(scale.toDisplay(t)),
    [xOf, scale]
  )
  const dOfClientX = (clientX: number) => {
    const el = scroller.current
    if (!el) return 0
    const x = clientX - el.getBoundingClientRect().left - nameW - PAD_L
    return Math.max(0, Math.min(scale.length, v0 + (x / plotW) * span))
  }

  // Selection from outside (a link, the landing rule): unfold and show it.
  const lastShown = React.useRef<string | undefined>(undefined)
  React.useEffect(() => {
    if (!selectedId || lastShown.current === selectedId) return
    lastShown.current = selectedId
    setCursor(selectedId)
    const up = ancestorIds(model, selectedId)
    if (up.some((a) => collapsed.has(a)))
      setCollapsed((c) => {
        const next = new Set(c)
        for (const a of up) next.delete(a)
        return next
      })
  }, [selectedId, model, collapsed])
  const reveal = React.useCallback(
    (nodeId: string) => {
      const el = scroller.current
      const i = rows.findIndex((r) => r.node.id === nodeId)
      if (!el || i < 0) return
      const top = i * ROW
      if (top < el.scrollTop + ROW) el.scrollTop = Math.max(0, top - ROW * 2)
      else if (top + ROW > el.scrollTop + el.clientHeight)
        el.scrollTop = top + ROW * 2 - el.clientHeight
    },
    [rows]
  )
  const revealed = React.useRef<string | undefined>(undefined)
  React.useEffect(() => {
    if (!selectedId || revealed.current === selectedId) return
    if (!rows.some((r) => r.node.id === selectedId)) return
    revealed.current = selectedId
    reveal(selectedId)
  }, [selectedId, rows, reveal])

  React.useEffect(() => {
    if (autoFocus) scroller.current?.focus({ preventScroll: true })
  }, [autoFocus])

  // "/" and ⌘F focus the search (full mode only: one search per page).
  React.useEffect(() => {
    if (compact) return
    const onKey = (e: KeyboardEvent) => {
      const find = (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f"
      const slash =
        e.key === "/" && !e.metaKey && !e.ctrlKey && !typing(e.target)
      if (!find && !slash) return
      if (!root.current?.offsetParent) return // not on screen (another tab)
      e.preventDefault()
      search.current?.focus()
      search.current?.select()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [compact])

  // Zoom: ctrl+wheel and pinch (which browsers report as ctrl+wheel);
  // a sideways wheel pans a zoomed chart.
  const zoomRef = React.useRef({
    v0,
    v1,
    length: scale.length,
    dOf: dOfClientX,
  })
  zoomRef.current = { v0, v1, length: scale.length, dOf: dOfClientX }
  React.useEffect(() => {
    const el = root.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      const z = zoomRef.current
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault()
        const next = zoomAt(
          z.v0,
          z.v1,
          z.dOf(e.clientX),
          Math.exp(e.deltaY * 0.0025),
          z.length
        )
        setZoom(next[1] - next[0] >= z.length - 0.5 ? null : next)
      } else if (
        z.v1 - z.v0 < z.length - 0.5 &&
        (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY))
      ) {
        e.preventDefault()
        const delta = ((e.shiftKey ? e.deltaY : e.deltaX) / 600) * (z.v1 - z.v0)
        setZoom(pan(z.v0, z.v1, delta, z.length))
      }
    }
    el.addEventListener("wheel", onWheel, { passive: false })
    return () => el.removeEventListener("wheel", onWheel)
  }, [])

  // The ruler: click seeks, drag selects a range to zoom, double-click resets.
  const drag = React.useRef<{ from: number; moved: boolean } | null>(null)
  const onRulerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.currentTarget.setPointerCapture(e.pointerId)
    drag.current = { from: dOfClientX(e.clientX), moved: false }
  }
  const onRulerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const g = drag.current
    if (!g) return
    const to = dOfClientX(e.clientX)
    if (Math.abs(xOf(to) - xOf(g.from)) > 4) g.moved = true
    if (g.moved) setBand([Math.min(g.from, to), Math.max(g.from, to)])
  }
  const onRulerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    const g = drag.current
    drag.current = null
    setBand(null)
    if (!g) return
    const to = dOfClientX(e.clientX)
    if (g.moved) {
      const a = Math.min(g.from, to)
      const b = Math.max(g.from, to)
      if (b - a > 1) setZoom([a, b])
    } else if (playback.enabled) playback.seek(to)
  }

  const toggle = (nodeId: string) =>
    setCollapsed((c) => {
      const next = new Set(c)
      if (!next.delete(nodeId)) next.add(nodeId)
      return next
    })

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (typing(e.target) || e.metaKey || e.ctrlKey || e.altKey) return
    const i = rows.findIndex((r) => r.node.id === cursor)
    const go = (j: number) => {
      const row = rows.at(Math.max(0, Math.min(rows.length - 1, j)))
      if (!row) return
      setCursor(row.node.id)
      reveal(row.node.id)
    }
    const row = i >= 0 ? rows[i] : undefined
    switch (e.key) {
      case "ArrowDown":
        go(i + 1)
        break
      case "ArrowUp":
        go(i < 0 ? 0 : i - 1)
        break
      case "Home":
        go(0)
        break
      case "End":
        go(rows.length - 1)
        break
      case "ArrowRight":
        if (!row) return
        if (row.expandable && !row.expanded) toggle(row.node.id)
        else if (row.expandable) go(i + 1)
        break
      case "ArrowLeft":
        if (!row) return
        if (row.expandable && row.expanded && !filtering) toggle(row.node.id)
        else if (row.node.parent) {
          setCursor(row.node.parent.id)
          reveal(row.node.parent.id)
        }
        break
      case "Enter":
        if (!row) return
        onSelect?.(row.node.id)
        break
      case "Escape":
        if (selectedId) onSelect?.(undefined)
        else return
        break
      case " ":
        if (compact || !playback.enabled) return
        playback.toggle()
        break
      default:
        return
    }
    e.preventDefault()
  }

  // Windowing: only the rows near the viewport are in the DOM.
  const viewH = compact ? Math.min(rows.length * ROW, maxHeight) : size.h || 400
  const first = Math.max(0, Math.floor(scrollTop / ROW) - OVERSCAN)
  const last = Math.min(
    rows.length,
    Math.ceil((scrollTop + viewH) / ROW) + OVERSCAN
  )
  const stuck = stickyRows(rows, scrollTop)

  const tickList = React.useMemo(
    () => ticks(scale, v0, v1, plotW),
    [scale, v0, v1, plotW]
  )
  const playing = !playback.atEnd
  const headX = xOf(playback.at)
  // A tick's label is drawn right of its line: leave it out where it
  // would run under a compressed wait or past the chart's edge.
  const labelled = (t: { d: number; label: string }) => {
    const from = xOf(t.d)
    const to = from + 6 + t.label.length * 6.8
    if (to > chartW) return false
    return !scale.gaps.some((g) => xOf(g.d0) < to && xOf(g.d1) > from)
  }
  const nowT = playing ? scale.toReal(playback.at) : Infinity

  const rowProps = {
    nameW,
    chartW,
    xAt,
    model,
    scaleEnd: scale.t1,
    cursor,
    selectedId,
    nowT,
    filtering,
    compact,
    onToggle: toggle,
    onPick: (n: TraceNode) => {
      setTip(null)
      setCursor(n.id)
      onSelect?.(n.id)
    },
    onTip: setTip,
  }

  const gapBands = scale.gaps
    .map((g) => ({ left: xOf(g.d0), right: xOf(g.d1), gap: g }))
    .filter((g) => g.right > 0 && g.left < chartW)

  return (
    <div
      ref={root}
      className={cn(
        "flex min-h-0 min-w-0 flex-col overflow-hidden rounded-alert border bg-card",
        !compact && "h-full",
        className
      )}
    >
      {!compact && (
        <div className="flex h-9 flex-none items-center gap-2 border-b pr-2 pl-3">
          <span className="text-sm font-medium">Trace</span>
          {live ? (
            <LivePill state={stream ?? "live"} />
          ) : (
            playing && (
              <span className="truncate text-xs text-muted-foreground">
                Playback of the recorded run
              </span>
            )
          )}
          <div className="ml-auto flex items-center gap-1">
            {zoom && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setZoom(null)}
                title="Reset zoom (double-click the ruler)"
              >
                <ZoomOutIcon />
                Reset zoom
              </Button>
            )}
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="Expand all"
              title="Expand all"
              disabled={filtering}
              onClick={() => setCollapsed(new Set())}
            >
              <ChevronsUpDownIcon />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="Collapse all"
              title="Collapse all"
              disabled={filtering}
              onClick={() => {
                const all = allExpandable(model)
                for (const r of model.roots) all.delete(r.id)
                setCollapsed(all)
              }}
            >
              <ChevronsDownUpIcon />
            </Button>
            <label className="relative flex h-7 w-56 min-w-0 flex-shrink items-center">
              <SearchIcon className="pointer-events-none absolute left-2 size-3.5 text-subtle-foreground" />
              <input
                ref={search}
                type="search"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value)
                  if (scroller.current) scroller.current.scrollTop = 0
                }}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.stopPropagation()
                    if (query) setQuery("")
                    else scroller.current?.focus()
                  } else if (e.key === "Enter" || e.key === "ArrowDown") {
                    e.preventDefault()
                    const hit = rows.find((r) => !r.expandable) ?? rows.at(0)
                    if (hit) setCursor(hit.node.id)
                    scroller.current?.focus()
                  }
                }}
                placeholder="Search steps"
                aria-label="Search steps"
                className="h-7 w-full rounded-input border border-input bg-input-background pr-9 pl-7 text-sm outline-none placeholder:text-subtle-foreground focus-visible:border-ring [&::-webkit-search-cancel-button]:hidden"
              />
              <Kbd size="sm" className="absolute right-1.5">
                ⌘F
              </Kbd>
            </label>
          </div>
        </div>
      )}

      {strip && !compact && <Strip view={view} />}

      <div
        className={cn("relative flex min-h-0 flex-col", !compact && "flex-1")}
      >
        {/* "Steps" over the names, the ruler over the chart. */}
        <div
          className="flex h-7 flex-none border-b text-xs text-muted-foreground"
          style={{ paddingRight: size.bar }}
        >
          <div
            className="relative flex flex-none items-center gap-1.5 border-r pl-3"
            style={{ width: nameW }}
          >
            <ListTreeIcon className="size-3.5 text-subtle-foreground" />
            <span className="font-medium">Steps</span>
            <span className="font-mono text-subtle-foreground">
              {filtering
                ? `${count(rows.length)} of ${count(model.nodes.length)}`
                : count(model.nodes.length)}
            </span>
          </div>
          <div
            role="slider"
            aria-label="Time"
            aria-valuemin={0}
            aria-valuemax={Math.round(scale.length)}
            aria-valuenow={Math.round(playback.at)}
            aria-valuetext={duration(scale.toReal(playback.at) - scale.t0)}
            tabIndex={-1}
            className="relative min-w-0 flex-1 cursor-crosshair touch-none overflow-hidden select-none"
            onPointerDown={onRulerDown}
            onPointerMove={onRulerMove}
            onPointerUp={onRulerUp}
            onPointerCancel={() => {
              drag.current = null
              setBand(null)
            }}
            onDoubleClick={() => {
              setZoom(null)
              // The two clicks of the double-click moved the playhead.
              if (!playback.playing) playback.seek(scale.length)
            }}
            title="Click to move the playhead · drag to zoom · double-click to reset"
          >
            {tickList.map((t) => (
              <span
                key={t.d}
                className="absolute top-0 flex h-full items-center border-l pl-1 font-mono text-[11px] whitespace-nowrap text-subtle-foreground"
                style={{ left: xOf(t.d) }}
              >
                {labelled(t) ? t.label : ""}
              </span>
            ))}
            {gapBands.map(({ left, right, gap }) => (
              <span
                key={gap.from}
                className="absolute inset-y-1.5 overflow-hidden rounded-sm bg-card px-1 text-center font-mono text-[11px] leading-4 whitespace-nowrap text-subtle-foreground"
                style={{ left: left + 1, width: Math.max(0, right - left - 2) }}
                title={`Waited ${duration(gap.to - gap.from)} on a person (drawn short)`}
              >
                {right - left > 64
                  ? `waited ${duration(gap.to - gap.from)}`
                  : ""}
              </span>
            ))}
            {band && (
              <span
                className="absolute inset-y-0 bg-foreground/10"
                style={{
                  left: xOf(band[0]),
                  width: Math.max(0, xOf(band[1]) - xOf(band[0])),
                }}
              />
            )}
            {playing && headX >= 0 && headX <= chartW && (
              <span
                className="absolute bottom-0 size-2 -translate-x-1/2 translate-y-1/2 rounded-full bg-foreground"
                style={{ left: headX }}
              />
            )}
          </div>
        </div>

        <div
          className={cn("relative min-h-0", !compact && "flex-1")}
          style={compact ? { height: Math.max(viewH, ROW * 2) } : undefined}
        >
          {/* Behind the rows: gridlines and the hatched gaps. */}
          <div
            aria-hidden
            className="pointer-events-none absolute inset-y-0 overflow-hidden"
            style={{ left: nameW, right: size.bar }}
          >
            {tickList.map((t) => (
              <span
                key={t.d}
                className="absolute inset-y-0 border-l border-border/60"
                style={{ left: xOf(t.d) }}
              />
            ))}
            {gapBands.map(({ left, right, gap }) => (
              <span
                key={gap.from}
                className="absolute inset-y-0 border-y-0 span-hatch opacity-25"
                style={{ left, width: Math.max(0, right - left) }}
              />
            ))}
          </div>

          <div
            ref={scroller}
            role="tree"
            aria-label="Trace steps"
            tabIndex={0}
            onKeyDown={onKeyDown}
            onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
            onMouseLeave={() => setTip(null)}
            onDoubleClick={(e) => {
              if ((e.target as HTMLElement).closest("[data-chart]"))
                setZoom(null)
            }}
            className="absolute inset-0 overflow-x-hidden overflow-y-auto outline-none"
          >
            {model.nodes.length === 0 ? (
              <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">
                {empty ?? "No spans were recorded."}
              </div>
            ) : rows.length === 0 ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 px-4 text-center">
                <p className="text-sm font-medium">No matching steps</p>
                <p className="text-xs text-muted-foreground">
                  Nothing in this trace matches
                  {query.trim() ? ` “${query.trim()}”` : " the filter"}.
                </p>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => {
                    setQuery("")
                    setKinds(new Set())
                  }}
                >
                  Clear
                </Button>
              </div>
            ) : (
              <div className="relative" style={{ height: rows.length * ROW }}>
                {rows.slice(first, last).map((r, k) => (
                  <Row
                    key={r.node.id}
                    row={r}
                    top={(first + k) * ROW}
                    prev={rows.at(first + k - 1)?.node}
                    {...rowProps}
                  />
                ))}
              </div>
            )}
          </div>

          {/* Agent rows stay while their children scroll under them. */}
          {stuck.length > 0 && (
            <div
              className="absolute top-0 left-0 z-20 border-b bg-card"
              style={{ right: size.bar, height: stuck.length * ROW }}
            >
              {stuck.map((r, k) => (
                <Row key={r.node.id} row={r} top={k * ROW} {...rowProps} />
              ))}
            </div>
          )}

          {/* Over the rows: the playhead. */}
          {playing && headX >= 0 && headX <= chartW && (
            <div
              aria-hidden
              className="pointer-events-none absolute inset-y-0 z-30 w-px bg-foreground/70"
              style={{ left: nameW + headX }}
            />
          )}
        </div>
        <NameResizer
          width={nameW}
          min={NAME_MIN}
          max={nameLimit}
          onWidth={(w) =>
            setStoredName(w === null ? null : String(Math.round(w)))
          }
        />
      </div>

      {!compact && (
        <Footer
          view={view}
          kinds={kinds}
          onKinds={(k) => {
            setKinds(k)
            if (scroller.current) scroller.current.scrollTop = 0
          }}
        />
      )}

      {tip && <Tip tip={tip} model={view.model} />}
    </div>
  )
}

/** useFontsReady turns true when the page's fonts have loaded: text
 * measured before that was measured in the fallback font. */
function useFontsReady(): boolean {
  const [ready, setReady] = React.useState(false)
  React.useEffect(() => {
    let on = true
    const fonts = typeof document === "undefined" ? undefined : document.fonts
    if (!fonts) return
    void fonts.ready.then(() => on && setReady(true))
    return () => {
      on = false
    }
  }, [])
  return ready
}

/** stickyRows: the agent rows that stay at the top: the agent ancestors
 * of the first row under the stack whose own row has scrolled away. */
function stickyRows(rows: TraceRow[], scrollTop: number): TraceRow[] {
  if (scrollTop <= 0 || rows.length === 0) return []
  const index = new Map<TraceNode, number>()
  rows.forEach((r, i) => index.set(r.node, i))
  let stack: TraceRow[] = []
  for (let pass = 0; pass < 4; pass++) {
    const at = Math.floor((scrollTop + stack.length * ROW) / ROW)
    const under = rows.at(Math.min(rows.length - 1, at))
    if (!under) break
    const chain: TraceRow[] = []
    for (let n = under.node.parent; n; n = n.parent) {
      const i = index.get(n)
      if (i === undefined || n.base !== "agent") continue
      chain.unshift(rows[i])
    }
    const next = chain.filter(
      (r, k) => index.get(r.node)! * ROW < scrollTop + k * ROW
    )
    if (next.length === stack.length) {
      stack = next
      break
    }
    stack = next
  }
  return stack.slice(0, 3)
}

/** The divider between the names and the chart, over the header and
 * the rows: drag, ←/→ (16px), Home/End (the limits); a double-click or
 * Delete gives the width back to the names. */
function NameResizer({
  width,
  min,
  max,
  onWidth,
}: {
  width: number
  min: number
  max: number
  /** null: forget the chosen width. */
  onWidth: (w: number | null) => void
}) {
  const from = React.useRef<{ x: number; w: number } | null>(null)
  const [dragging, setDragging] = React.useState(false)
  const clamp = (w: number) => Math.max(min, Math.min(max, w))
  const stop = () => {
    from.current = null
    setDragging(false)
  }
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the name column"
      aria-valuenow={Math.round(width)}
      aria-valuemin={min}
      aria-valuemax={Math.round(max)}
      tabIndex={0}
      title="Drag to resize · double-click to fit the names"
      className={cn(
        "absolute inset-y-0 z-30 w-2 -translate-x-1/2 cursor-col-resize touch-none outline-none after:absolute after:inset-y-0 after:left-1/2 after:w-0.5 after:-translate-x-1/2 hover:after:bg-ring focus-visible:after:bg-ring",
        dragging && "after:bg-ring"
      )}
      style={{ left: width }}
      onPointerDown={(e) => {
        if (e.button !== 0) return
        e.preventDefault()
        e.currentTarget.setPointerCapture(e.pointerId)
        e.currentTarget.focus()
        from.current = { x: e.clientX, w: width }
        setDragging(true)
      }}
      onPointerMove={(e) => {
        const f = from.current
        if (f) onWidth(clamp(f.w + e.clientX - f.x))
      }}
      onPointerUp={stop}
      onPointerCancel={stop}
      onDoubleClick={() => onWidth(null)}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft") onWidth(clamp(width - 16))
        else if (e.key === "ArrowRight") onWidth(clamp(width + 16))
        else if (e.key === "Home") onWidth(min)
        else if (e.key === "End") onWidth(max)
        else if (e.key === "Delete" || e.key === "Backspace") onWidth(null)
        else return
        e.preventDefault()
        e.stopPropagation()
      }}
    />
  )
}

const GLYPH: Record<NonNullable<TraceNode["glyph"]>["what"], LucideIcon> = {
  truncated: ScissorsIcon,
  max_tokens: FoldVerticalIcon,
  repaired: WandSparklesIcon,
}

function iconOf(n: TraceNode): LucideIcon {
  if (n.kind === "marker") return FoldVerticalIcon
  if (n.kind === "waiting") return SPAN_KINDS.waiting.icon
  return SPAN_KINDS[n.base === "marker" ? "other" : n.base].icon
}

/** labelWidth estimates the pixels the label after a bar takes (mono
 * 12px: 7.3px a character), with the "Slowest" badge. */
function labelWidth(n: TraceNode, slowest: boolean): number {
  return barLabel(n, Infinity).length * 7.3 + (slowest ? 58 : 0)
}

// Text is measured on a canvas in the page's own font; where there is
// no canvas (tests) a per-character estimate stands in.
let ruler: CanvasRenderingContext2D | null | undefined
function textWidth(text: string, px: number, weight: number): number {
  if (ruler === undefined && typeof CanvasRenderingContext2D === "undefined")
    ruler = null
  if (ruler === undefined) {
    try {
      ruler = document.createElement("canvas").getContext("2d")
    } catch {
      ruler = null
    }
  }
  if (!ruler) return text.length * px * 0.56
  ruler.font = `${weight} ${px}px ${getComputedStyle(document.body).fontFamily}`
  return ruler.measureText(text).width
}

/** nameWidthOf: the name column a tree needs: indent, chevron, icon, the
 * name and its muted word, the marks after them. */
function nameWidthOf(nodes: readonly TraceNode[]): number {
  let w = 0
  for (const n of nodes)
    w = Math.max(
      w,
      6 +
        n.depth * INDENT +
        16 +
        6 +
        20 +
        6 +
        textWidth(n.label, 13, n.base === "agent" ? 600 : 500) +
        (n.failed ? 18 : 0) +
        (n.secondary ? 6 + textWidth(n.secondary, 12, 400) : 0) +
        (n.glyph ? 18 : 0) +
        12
    )
  return Math.ceil(w)
}

/** barLabel: the one muted label after a bar. */
function barLabel(n: TraceNode, now: number): string {
  const end = Math.min(n.end, now)
  const d = duration(Math.max(0, end - n.start))
  if (n.kind === "waiting") {
    if (n.wait?.to === null && now === Infinity) return `waiting ${d}`
    return n.end > n.start ? `waited ${d}` : "did not run"
  }
  if (n.cost && n.end <= now && (n.cost.cost_known || n.cost.cost_partial))
    return `${d} · ${formatCost(n.cost)}`
  return d
}

interface RowProps {
  row: TraceRow
  top: number
  /** The row above, for the handoff connector. */
  prev?: TraceNode
  nameW: number
  chartW: number
  xAt: (t: number) => number
  model: TraceView["model"]
  scaleEnd: number
  cursor: string | undefined
  selectedId: string | undefined
  /** The playhead in real time; Infinity at rest. */
  nowT: number
  filtering: boolean
  compact: boolean
  onToggle: (id: string) => void
  onPick: (n: TraceNode) => void
  onTip: (t: { node: TraceNode; x: number; y: number } | null) => void
}

const Row = React.memo(function WaterfallRow({
  row,
  top,
  prev,
  nameW,
  chartW,
  xAt,
  model,
  cursor,
  selectedId,
  nowT,
  filtering,
  onToggle,
  onPick,
  onTip,
}: RowProps) {
  const n = row.node
  const Icon = iconOf(n)
  const isSelected = n.id === selectedId
  const isCursor = n.id === cursor
  const later = n.start > nowT
  const agent = n.base === "agent"
  const failed = n.failed
  const Glyph = n.glyph ? GLYPH[n.glyph.what] : undefined

  return (
    <div
      role="treeitem"
      aria-level={n.depth + 1}
      aria-selected={isSelected}
      aria-expanded={row.expandable ? row.expanded : undefined}
      data-row={n.id}
      data-kind={n.kind}
      className={cn(
        "group/row absolute right-0 left-0 flex cursor-default",
        later && "opacity-35"
      )}
      style={{ top, height: ROW }}
      onClick={() => onPick(n)}
      onMouseMove={(e) => onTip({ node: n, x: e.clientX, y: e.clientY })}
    >
      <div
        className={cn(
          "relative z-10 flex flex-none items-center gap-1.5 border-r bg-card pr-2 group-hover/row:bg-muted",
          isSelected && "bg-muted",
          isCursor && "shadow-[inset_2px_0_0_var(--ring)]"
        )}
        style={{ width: nameW, paddingLeft: 6 + n.depth * INDENT }}
        title={[n.label, n.model ?? n.secondary].filter(Boolean).join(" · ")}
      >
        {Array.from({ length: n.depth }, (_, i) => (
          <span
            key={i}
            aria-hidden
            className="absolute inset-y-0 w-px bg-border"
            style={{ left: 6 + i * INDENT + 8 }}
          />
        ))}
        {row.expandable ? (
          <button
            type="button"
            tabIndex={-1}
            aria-label={row.expanded ? "Collapse" : "Expand"}
            disabled={filtering}
            onClick={(e) => {
              e.stopPropagation()
              onToggle(n.id)
            }}
            className="relative z-10 flex size-4 flex-none items-center justify-center rounded-sm bg-inherit text-subtle-foreground hover:text-foreground"
          >
            <ChevronRightIcon
              className={cn(
                "size-3.5 transition-transform",
                row.expanded && "rotate-90"
              )}
            />
          </button>
        ) : (
          <span className="w-4 flex-none" />
        )}
        <span
          className={cn(
            "flex size-5 flex-none items-center justify-center rounded-[5px] border bg-background",
            failed
              ? "text-danger-foreground"
              : n.lane.index === 0 || n.kind === "waiting"
                ? "text-muted-foreground"
                : n.lane.text
          )}
        >
          <Icon className="size-3" />
        </span>
        <span
          className={cn(
            "min-w-0 flex-initial truncate text-sm",
            agent ? "font-semibold" : "font-medium",
            n.kind === "marker" && "font-normal text-muted-foreground",
            failed && "text-danger-foreground"
          )}
        >
          {n.label}
        </span>
        {failed && (
          <TriangleAlertIcon
            aria-label="Failed"
            className="size-3 flex-none text-danger-foreground"
          />
        )}
        {n.secondary && (
          <span className="min-w-0 flex-[1_10_auto] truncate text-xs text-subtle-foreground">
            {n.secondary}
          </span>
        )}
        {Glyph && (
          <Glyph
            aria-label={n.glyph!.title}
            className="ml-auto size-3 flex-none text-warning-foreground"
          />
        )}
      </div>
      <div
        data-chart
        className={cn(
          "relative min-w-0 flex-1 group-hover/row:bg-muted/60",
          isSelected && "bg-muted/60"
        )}
      >
        <Bar
          n={n}
          prev={prev}
          xAt={xAt}
          nowT={nowT}
          model={model}
          chartW={chartW}
        />
      </div>
    </div>
  )
})

function Bar({
  n,
  prev,
  xAt,
  nowT,
  model,
  chartW,
}: {
  n: TraceNode
  prev?: TraceNode
  xAt: (t: number) => number
  nowT: number
  model: TraceView["model"]
  chartW: number
}) {
  const x0 = xAt(n.start)
  if (n.start > nowT) return null
  const end = Math.min(n.end, nowT)
  const cut = n.end > nowT
  const x1 = Math.max(xAt(end), x0 + (n.kind === "marker" ? 0 : 3))
  const w = x1 - x0
  const label = barLabel(n, nowT)
  const slowest = model.slowestId === n.id && !cut
  const lw = label.length * 7.3 + (slowest ? 58 : 0)
  const pulse = n.running && n.kind !== "waiting" && "motion-safe:live-pulse"
  // The label goes after the bar; before it when it would run out of
  // the chart there (a narrow or zoomed chart); and when neither side
  // has the room, over the bar's end on a chip.
  const place =
    x1 + 6 + lw <= chartW ? "after" : x0 - 6 - lw >= 0 ? "before" : "inside"

  let bar: React.ReactNode
  if (n.kind === "marker") {
    bar = (
      <span
        className="absolute top-1/2 size-1.5 -translate-x-1/2 -translate-y-1/2 rotate-45 bg-neutral"
        style={{ left: x0 }}
      />
    )
  } else if (n.base === "agent") {
    bar = (
      <span
        className="absolute top-1/2 h-3.5 -translate-y-1/2"
        style={{ left: x0, width: w }}
      >
        <span
          className={cn(
            "absolute inset-x-0 top-1/2 h-px -translate-y-1/2",
            n.lane.solid,
            "opacity-40"
          )}
        />
        {summarySegments(n).map((s, i) => {
          if (s.start > nowT) return null
          const a = xAt(s.start) - x0
          const b = Math.max(xAt(Math.min(s.end, nowT)) - x0, a + 2)
          return (
            <span
              key={i}
              className={cn(
                "absolute top-1/2 h-2 -translate-y-1/2 rounded-full",
                s.style === "solid" && s.lane.solid,
                s.style === "dim" && s.lane.dim,
                s.style === "failed" && "bg-span-failed",
                s.style === "hatch" && "span-hatch"
              )}
              style={{ left: a, width: b - a }}
            />
          )
        })}
      </span>
    )
  } else {
    const style =
      n.kind === "failed"
        ? "failed"
        : n.kind === "waiting"
          ? "hatch"
          : n.base === "model"
            ? "solid"
            : n.base === "other"
              ? "thin"
              : "dim"
    bar = (
      <span
        className={cn(
          "absolute top-1/2 flex -translate-y-1/2 items-center justify-center overflow-hidden rounded-full",
          style === "solid" && cn("h-3.5", n.lane.solid),
          style === "dim" && cn("h-2.5", n.lane.dim),
          style === "hatch" && "h-3.5 span-hatch",
          style === "failed" && "h-3.5 bg-span-failed",
          style === "thin" && "h-1 bg-lane-0",
          pulse,
          n.running && "transition-[width] duration-200 ease-linear"
        )}
        style={{ left: x0, width: w }}
      >
        {style === "failed" && w >= 12 && (
          <XIcon
            strokeWidth={3}
            className="size-2.5 text-span-failed-foreground"
          />
        )}
        {n.retry && style !== "failed" && w >= 14 && (
          <RotateCwIcon
            strokeWidth={2.5}
            className="size-2.5 text-background"
          />
        )}
      </span>
    )
  }

  // The handoff connector: an elbow in the child's hue from the row
  // above down to where the child starts.
  const connector = n.handed && prev && (
    <span
      aria-hidden
      className={cn(
        "absolute w-1.5 rounded-bl-[4px] border-b border-l",
        n.lane.border
      )}
      style={{ left: x0 - 7, top: -ROW / 2 + 8, height: ROW - 8 }}
    />
  )

  return (
    <>
      {connector}
      {bar}
      <span
        className={cn(
          "absolute top-1/2 flex -translate-y-1/2 items-center gap-1.5 font-mono text-xs whitespace-nowrap text-muted-foreground tabular-nums",
          place === "inside" && "rounded-sm bg-card/90 px-1"
        )}
        style={
          place === "after"
            ? { left: x1 + 6 }
            : place === "before"
              ? { right: chartW - x0 + 6 }
              : { right: Math.max(4, chartW - x1 + 4) }
        }
      >
        {label}
        {slowest && (
          <span className="rounded-sm bg-span-slowest-muted px-1 font-sans text-[11px] leading-4 font-medium text-span-slowest">
            Slowest
          </span>
        )}
      </span>
    </>
  )
}

function Tip({
  tip,
  model,
}: {
  tip: { node: TraceNode; x: number; y: number }
  model: TraceView["model"]
}) {
  const n = tip.node
  const w = typeof window === "undefined" ? 1200 : window.innerWidth
  const h = typeof window === "undefined" ? 800 : window.innerHeight
  const left = Math.min(tip.x + 14, w - 300)
  const below = tip.y + 18 + 70 < h
  return (
    <div
      role="tooltip"
      className="pointer-events-none fixed z-50 flex max-w-72 flex-col gap-0.5 rounded-sm bg-foreground px-2 py-1 text-xs text-background"
      style={{
        left,
        top: below ? tip.y + 18 : undefined,
        bottom: below ? undefined : h - tip.y + 10,
      }}
    >
      <span className="truncate font-medium">{gist(n)}</span>
      {n.kind !== "marker" && (
        <span className="font-mono opacity-80">
          {offset(n.start - model.t0)} ·{" "}
          {n.running && n.kind !== "waiting"
            ? "running"
            : duration(Math.max(0, n.end - n.start))}
          {n.tokensIn !== undefined &&
            n.base === "model" &&
            ` · ${tokens(n.tokensIn)} in, ${tokens(n.tokensOut ?? 0)} out`}
        </span>
      )}
      {n.note && <span className="opacity-80">{n.note}</span>}
      {n.glyph && <span className="opacity-80">{n.glyph.title}</span>}
    </div>
  )
}

function Strip({ view }: { view: TraceView }) {
  const s = view.stats
  const services = new Set(
    view.model.nodes.map((n) => n.span?.service).filter(Boolean)
  )
  const cells: [string, string, boolean?][] = [
    ["Duration", duration(s.elapsed)],
    ["Spans", count(view.model.nodes.length)],
    ["Services", count(services.size)],
    ["Model calls", count(s.modelCalls)],
    ["Tool calls", count(s.toolCalls)],
    ["Errors", count(s.failed), s.failed > 0],
  ]
  return (
    <dl className="grid flex-none grid-cols-6 border-b">
      {cells.map(([label, value, danger], i) => (
        <div
          key={label}
          className={cn(
            "flex min-w-0 flex-col gap-0.5 px-3 py-2",
            i > 0 && "border-l"
          )}
        >
          <dt className="truncate text-xs text-muted-foreground">{label}</dt>
          <dd
            className={cn("truncate stat", danger && "text-danger-foreground")}
          >
            {value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function Swatch({ kind }: { kind: LegendKind }) {
  switch (kind) {
    case "model":
      return <span className="h-2.5 w-4 rounded-full bg-lane-1" />
    case "tool":
      return <span className="h-2 w-4 rounded-full bg-lane-1-dim" />
    case "waiting":
      return <span className="h-2.5 w-4 rounded-full span-hatch" />
    case "handoff":
      return (
        <span className="mb-1 h-2 w-2.5 rounded-bl-[4px] border-b border-l border-lane-2" />
      )
    case "failed":
      return (
        <span className="flex h-2.5 w-4 items-center justify-center rounded-full bg-span-failed">
          <XIcon
            strokeWidth={3}
            className="size-2 text-span-failed-foreground"
          />
        </span>
      )
  }
}

function Footer({
  view,
  kinds,
  onKinds,
}: {
  view: TraceView
  kinds: ReadonlySet<LegendKind>
  onKinds: (k: ReadonlySet<LegendKind>) => void
}) {
  const { playback: p, scale, live } = view
  const at = scale.toReal(p.at) - scale.t0
  const total = scale.t1 - scale.t0
  return (
    <div className="@container flex h-9 flex-none items-center gap-1 overflow-hidden border-t pr-2 pl-1.5">
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label={p.playing ? "Pause" : "Play"}
        title={
          live
            ? "Playback is available when the run has ended"
            : p.playing
              ? "Pause (Space)"
              : "Play the recorded run (Space)"
        }
        disabled={!p.enabled}
        onClick={p.toggle}
      >
        {p.playing ? <PauseIcon /> : <PlayIcon />}
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="Restart"
        title="Restart"
        disabled={!p.enabled}
        onClick={p.restart}
      >
        <RotateCcwIcon />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="Previous step"
        title="Previous step"
        disabled={!p.enabled}
        onClick={() => p.step(-1)}
      >
        <SkipBackIcon />
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="Next step"
        title="Next step"
        disabled={!p.enabled || p.atEnd}
        onClick={() => p.step(1)}
      >
        <SkipForwardIcon />
      </Button>
      <Select
        value={String(p.speed)}
        onValueChange={(v) => p.setSpeed(Number(v) as Speed)}
        disabled={!p.enabled}
      >
        <SelectTrigger
          size="sm"
          aria-label="Playback speed"
          className="w-16 font-mono text-xs"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {SPEEDS.map((s) => (
            <SelectItem key={s} value={String(s)} className="font-mono text-xs">
              {s}×
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <span className="ml-1 font-mono text-xs whitespace-nowrap text-muted-foreground tabular-nums @max-md:hidden">
        {p.atEnd ? duration(total) : `${duration(at)} / ${duration(total)}`}
      </span>

      <div className="ml-auto flex items-center gap-0.5">
        {SPAN_KIND_ORDER.map((k) => {
          const kind = k as LegendKind
          const on = kinds.has(kind)
          return (
            <button
              key={k}
              type="button"
              aria-pressed={on}
              title={
                on ? "Show every kind" : `Show only: ${SPAN_KINDS[k].label}`
              }
              onClick={() => onKinds(on ? new Set() : new Set([kind]))}
              className={cn(
                "flex h-6 items-center gap-1.5 rounded-sm px-1.5 text-xs whitespace-nowrap text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
                on && "bg-muted text-foreground",
                kinds.size > 0 && !on && "opacity-50"
              )}
            >
              <Swatch kind={kind} />
              <span className="@max-3xl:sr-only">{SPAN_KINDS[k].label}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
