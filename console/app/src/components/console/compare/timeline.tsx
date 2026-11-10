import { Link } from "@tanstack/react-router"
import { ChevronRightIcon, HourglassIcon } from "lucide-react"
import * as React from "react"

import { JsonView } from "@/components/console/json-view"
import { Value } from "@/components/console/run/messages"
import { shortModel } from "@/components/console/trace-waterfall/model"
import { Button } from "@/components/ui/button"
import { SideTag } from "./totals"
import type {
  CompareCall,
  CompareItem,
  CompareRow,
  CompareStep,
} from "@/lib/compare"
import { duration, tokens } from "@/lib/format"
import { SPAN_KINDS } from "@/lib/span-kind"
import { cn } from "@/lib/utils"

// The two timelines, aligned by step: one row per step, run A on the
// left and run B on the right, and in the gutter what differs. A part
// that differs sits on the warning ground; nothing else is coloured
// (tokens that differ are said in the gutter only: they nearly always do).

/** gutter | A | B. The totals table uses the same tracks. */
export const COMPARE_GRID =
  "grid grid-cols-[6.5rem_minmax(0,1fr)_minmax(0,1fr)]"

const DIFFERS = "bg-warning-muted"

export function CompareTimeline({
  items,
  runs,
  onShowAll,
}: {
  items: CompareItem[]
  /** The run each side's steps open on: the page of its whole chain. */
  runs: { a: string; b: string }
  onShowAll: () => void
}) {
  return (
    <div
      role="table"
      aria-label="Steps of the two runs"
      className="overflow-hidden rounded-alert border bg-card"
    >
      <div role="row" className={cn(COMPARE_GRID, "h-7 items-center border-b")}>
        <div role="columnheader" className="px-3 table-head">
          Step
        </div>
        {(["a", "b"] as const).map((side) => (
          <div
            key={side}
            role="columnheader"
            aria-label={`Run ${side.toUpperCase()}`}
            className="flex min-w-0 items-center border-l px-3"
          >
            <SideTag side={side} />
          </div>
        ))}
      </div>
      {items.map((item) =>
        item.type === "row" ? (
          <Row key={item.row.key} row={item.row} runs={runs} />
        ) : item.type === "fold" ? (
          <FoldLine key={item.key} rows={item.rows} onShowAll={onShowAll} />
        ) : (
          <div
            key={item.key}
            role="row"
            className="flex h-7 items-center gap-1.5 border-b bg-surface px-3 text-xs text-muted-foreground last:border-b-0"
          >
            <HourglassIcon aria-hidden className="size-3.5 flex-none" />
            <span role="cell">Continued after waiting on a person</span>
          </div>
        )
      )}
    </div>
  )
}

function FoldLine({
  rows,
  onShowAll,
}: {
  rows: CompareRow[]
  onShowAll: () => void
}) {
  const names = rows.map((r) => (r.a ?? r.b)?.name).filter(Boolean)
  const n = rows.length
  const kept = rows.every((r) => r.kind === "kept")
  return (
    <div
      role="row"
      className="flex h-8 items-center gap-2 border-b px-3 text-xs text-muted-foreground last:border-b-0"
    >
      <span role="cell" className="flex-none">
        {kept
          ? `${n} ${n === 1 ? "step" : "steps"} kept, not run again`
          : `${n} identical ${n === 1 ? "step" : "steps"}`}
      </span>
      <span
        role="cell"
        className="min-w-0 flex-1 truncate text-subtle-foreground"
      >
        {names.join(" · ")}
      </span>
      <Button size="sm" variant="ghost" onClick={onShowAll}>
        Show
      </Button>
    </div>
  )
}

const WORD: Record<CompareRow["kind"], string> = {
  same: "same",
  changed: "",
  only_a: "only in A",
  only_b: "only in B",
  kept: "kept",
  unknown: "not compared",
}

function Row({
  row,
  runs,
}: {
  row: CompareRow
  runs: { a: string; b: string }
}) {
  const quiet = row.kind === "same" || row.kind === "kept"
  const marked = row.kind !== "same" && row.kind !== "kept"
  return (
    <div role="row" className={cn(COMPARE_GRID, "border-b last:border-b-0")}>
      <div
        role="rowheader"
        className="flex min-w-0 flex-col gap-0.5 px-3 py-2 text-xs"
      >
        <span className="flex h-5 items-center font-mono text-muted-foreground tabular-nums">
          #{row.no}
        </span>
        {row.what.map((w) => (
          <span key={w} className="truncate text-warning-foreground">
            {w}
          </span>
        ))}
        {WORD[row.kind] && (
          <span
            className={cn(
              "truncate",
              marked ? "text-warning-foreground" : "text-subtle-foreground"
            )}
            title={
              row.unknown.length > 0
                ? `Not recorded on one side: ${row.unknown.join(", ")}`
                : undefined
            }
          >
            {WORD[row.kind]}
          </span>
        )}
        {row.kind === "changed" && row.unknown.length > 0 && (
          <span
            className="truncate text-subtle-foreground"
            title={`Not recorded on one side: ${row.unknown.join(", ")}`}
          >
            partly compared
          </span>
        )}
      </div>
      {(["a", "b"] as const).map((side) => {
        const step = row[side]
        const other = row[side === "a" ? "b" : "a"]
        return (
          <div
            key={side}
            role="cell"
            className={cn("min-w-0 border-l px-3 py-2", quiet && "opacity-70")}
          >
            {step ? (
              <StepCell
                step={step}
                runId={runs[side]}
                rowNo={row.no}
                showModel={!!other && other.model !== step.model}
              />
            ) : (
              <p className="flex h-5 items-center text-xs text-subtle-foreground">
                {absent(row, side)}
              </p>
            )}
          </div>
        )
      })}
    </div>
  )
}

function absent(row: CompareRow, side: "a" | "b"): string {
  const name = side.toUpperCase()
  if (row.kind === "kept") return `${name} kept this step: not run again`
  if (row.keptBy === side) return `${name} kept this step; it was not read`
  return `${name} has no step here`
}

function StepCell({
  step,
  runId,
  rowNo,
  showModel,
}: {
  step: CompareStep
  /** The run page the step opens on. */
  runId: string
  /** The row's number: the step's own is said only when it is another. */
  rowNo: number
  /** The step's model is said where the other side's is another. */
  showModel: boolean
}) {
  const Icon = step.failed ? SPAN_KINDS.failed.icon : SPAN_KINDS.model.icon
  const model = showModel ? shortModel(step.model) : undefined
  const used = step.inputTokens + step.outputTokens
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex h-5 min-w-0 items-center gap-1.5">
        <Icon
          aria-hidden
          className={cn(
            "size-3.5 flex-none",
            step.failed ? "text-danger-foreground" : "text-muted-foreground"
          )}
        />
        <Link
          to="/runs/$runId"
          params={{ runId }}
          search={{ span: step.spanId }}
          title={`Open step #${step.no} on its run's page`}
          className={cn(
            "min-w-0 truncate rounded-sm text-sm font-medium outline-none hover:underline focus-visible:outline-2 focus-visible:outline-ring",
            step.failed && "text-danger-foreground"
          )}
        >
          {step.name}
        </Link>
        {step.no !== rowNo && (
          <span
            className="flex-none font-mono text-xs text-subtle-foreground tabular-nums"
            title="This step's number in its own run"
          >
            #{step.no}
          </span>
        )}
        {model && (
          <span className="flex-none text-xs text-subtle-foreground max-xl:hidden">
            {model}
          </span>
        )}
        {step.failed && (
          <span className="flex-none text-xs text-danger-foreground">
            failed
          </span>
        )}
        {step.parked && (
          <span className="flex-none text-xs text-muted-foreground">
            waited for approval
          </span>
        )}
        <span className="ml-auto flex flex-none items-center gap-2 pl-2 font-mono text-xs text-muted-foreground tabular-nums">
          {step.durationMs !== undefined && (
            <span>{duration(step.durationMs)}</span>
          )}
          {used > 0 && (
            <span
              title={`${tokens(step.inputTokens)} input tokens, ${tokens(step.outputTokens)} output tokens${step.tokensDiffer ? "; not the same as on the other side" : ""}`}
            >
              {tokens(step.inputTokens)} → {tokens(step.outputTokens)}
            </span>
          )}
        </span>
      </div>
      {step.calls.map((c, i) => (
        <CallLine key={i} call={c} />
      ))}
      {step.text ? <Reply text={step.text} differs={step.textDiffers} /> : null}
      {step.holes.map((h, i) => (
        <p
          key={i}
          className="text-xs text-subtle-foreground"
          title={h.fix ? `${h.reason}. ${h.fix}` : h.reason}
        >
          Not recorded: {h.reason}
        </p>
      ))}
    </div>
  )
}

const one = (v: unknown) =>
  (typeof v === "string" ? v : (JSON.stringify(v) ?? "")).replace(/\s+/g, " ")

const MARK: Record<NonNullable<CompareCall["mark"]>, string> = {
  tool: "Another tool than on the other side",
  args: "Other arguments than on the other side",
  only: "The other side made no call here",
}

/** A tool call folded to its name, its arguments and its result on one
 * line each; it opens in place to both in full. */
function CallLine({ call }: { call: CompareCall }) {
  const [open, setOpen] = React.useState(false)
  const Icon = call.handoff ? SPAN_KINDS.handoff.icon : SPAN_KINDS.tool.icon
  const r = call.result
  // Another tool's result is another result: the call's mark says it.
  const resultDiffers =
    !!r?.differs && call.mark !== "tool" && call.mark !== "only"
  return (
    <div className="min-w-0">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full min-w-0 flex-col rounded-sm text-left outline-none hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring"
      >
        <span
          title={call.mark ? MARK[call.mark] : undefined}
          className={cn(
            "flex h-5 w-full min-w-0 items-center gap-1.5 rounded-sm",
            call.mark && DIFFERS
          )}
        >
          <ChevronRightIcon
            aria-hidden
            className={cn(
              "size-3 flex-none text-subtle-foreground transition-transform",
              open && "rotate-90"
            )}
          />
          <Icon
            aria-hidden
            className="size-3.5 flex-none text-muted-foreground"
          />
          <span className="flex-none font-mono text-xs">{call.name}</span>
          <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
            {one(call.args)}
          </span>
        </span>
        {r && !open && (
          <span
            title={
              resultDiffers
                ? "Another result than on the other side"
                : undefined
            }
            className={cn(
              "flex h-5 w-full min-w-0 items-center gap-1.5 rounded-sm pl-[18px]",
              resultDiffers && DIFFERS
            )}
          >
            <span
              aria-hidden
              className="w-3.5 flex-none text-center text-xs text-subtle-foreground"
            >
              →
            </span>
            <span
              className={cn(
                "min-w-0 truncate font-mono text-xs",
                r.isError ? "text-danger-foreground" : "text-muted-foreground"
              )}
            >
              {one(r.content) || "(empty)"}
            </span>
          </span>
        )}
      </button>
      {open && (
        <div className="mt-1 mb-1 flex flex-col gap-1 pl-[18px]">
          <span className="table-head">Arguments</span>
          <JsonView value={call.args} depth={2} />
          <span className="table-head">Result</span>
          {r ? (
            <Value value={r.content} tone={r.isError ? "danger" : undefined} />
          ) : (
            <p className="text-xs text-muted-foreground">
              No result: the call did not finish in this run.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

/** The assistant's text: three lines, the rest on a click. */
function Reply({ text, differs }: { text: string; differs: boolean }) {
  const [open, setOpen] = React.useState(false)
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={() => setOpen((o) => !o)}
      title={differs ? "Another reply than on the other side" : undefined}
      className={cn(
        "min-w-0 rounded-sm px-1.5 py-0.5 text-left text-sm break-words whitespace-pre-wrap outline-none hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring",
        differs && cn(DIFFERS, "hover:bg-warning-muted"),
        !open && "line-clamp-3"
      )}
    >
      {text}
    </button>
  )
}
