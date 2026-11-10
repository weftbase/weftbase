import { Link, useNavigate } from "@tanstack/react-router"
import { ArrowUpRightIcon, BotIcon, ChevronRightIcon } from "lucide-react"
import * as React from "react"

import { ErrorState, LoadingRows } from "@/components/console/empty-state"
import { useNow } from "@/components/console/relative-time"
import { useRun } from "@/components/console/run/use-run"
import { StatusDot } from "@/components/console/status"
import { TraceWaterfall } from "@/components/console/trace-waterfall"
import { STATUS_LABEL } from "@/lib/api/status"
import type { ConversationTurn } from "@/lib/api/types"
import { bytes, dateTime } from "@/lib/format"
import { cn } from "@/lib/utils"

import { MarkerLine, OpenWait } from "./marker"
import { summaryParts, summaryTitle, turnNumber } from "./model"
import type { Item, TurnNote } from "./model"

type TurnItem = Extract<Item, { type: "turn" }>

const timeOfDay = (iso: string) =>
  new Date(iso).toLocaleTimeString("en-GB", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  })

/** TurnBlock: one turn of the conversation. The user's message, the one
 * collapsed line of what the agent did (it opens the turn's waterfall
 * in place), what happened on the way (waits, approvals, compactions…),
 * then the agent's reply, or in words why there is none. */
export function TurnBlock({
  item,
  agent,
  open,
  onToggle,
}: {
  item: TurnItem
  agent: string
  /** Its waterfall is open. */
  open: boolean
  onToggle: () => void
}) {
  const { turn, markers, note } = item
  const n = turnNumber(turn, item.ordinal)
  // A rolled-back turn is off the conversation's path: folded to a line.
  const [unfolded, setUnfolded] = React.useState(false)
  if (item.offPath && !unfolded && !open)
    return (
      <article
        id={`turn-${n}`}
        aria-label={`Turn ${n}, rolled back`}
        className="grid grid-cols-[28px_minmax(0,1fr)] gap-x-2 opacity-60"
      >
        <Gutter>{n}</Gutter>
        <button
          type="button"
          aria-expanded={false}
          onClick={() => setUnfolded(true)}
          className="flex h-7 min-w-0 items-center gap-1.5 rounded-input border border-dashed px-2 text-left text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRightIcon aria-hidden className="size-3.5 flex-none" />
          <span className="flex-none">
            Rolled back
            {turn.ended_by_turn ? ` by turn ${turn.ended_by_turn}` : ""}
          </span>
          <span className="min-w-0 truncate text-subtle-foreground">
            {turn.input}
          </span>
        </button>
      </article>
    )

  return (
    <article
      id={`turn-${n}`}
      aria-label={`Turn ${n}`}
      className={cn(
        "grid grid-cols-[28px_minmax(0,1fr)] gap-x-2 gap-y-1.5",
        item.offPath && "opacity-60"
      )}
    >
      <Gutter title={`Turn ${n} · ${dateTime(turn.started)}`}>{n}</Gutter>
      <div className="min-w-0 rounded-alert bg-muted px-3 py-2">
        {turn.input ? (
          <LongText text={turn.input} />
        ) : (
          <span className="text-sm text-muted-foreground">
            {turn.input_known
              ? "(empty message)"
              : "The message was not recorded (content capture is off for this run)."}
          </span>
        )}
      </div>

      <Gutter line />
      <div className="flex min-w-0 flex-col gap-0.5">
        <SummaryLine turn={turn} n={n} open={open} onToggle={onToggle} />
        {open && <TurnTrace turn={turn} />}
        {markers.map((m) =>
          m.pendingRun ? (
            <OpenWait
              key={m.key}
              marker={m}
              approvals={
                (turn.waits ?? []).find((w) => w.run === m.pendingRun)
                  ?.approvals ?? []
              }
            />
          ) : (
            <MarkerLine key={m.key} marker={m} />
          )
        )}
      </div>

      {(turn.reply !== null || note) && (
        <>
          <div className="pt-[3px]">
            <BotIcon
              aria-label={agent}
              className="ml-auto size-3.5 text-subtle-foreground"
            />
          </div>
          <div className="min-w-0 pb-1">
            {turn.reply !== null && <LongText text={turn.reply} />}
            {note && <Note note={note} />}
          </div>
        </>
      )}
    </article>
  )
}

function Gutter({
  children,
  line = false,
  title,
}: {
  children?: React.ReactNode
  /** A hairline down the gutter: the agent's work between the messages. */
  line?: boolean
  title?: string
}) {
  return (
    <div
      title={title}
      className={cn(
        "pt-2 text-right font-mono text-xs text-subtle-foreground tabular-nums",
        line && "relative pt-0"
      )}
    >
      {line ? (
        <span
          aria-hidden
          className="absolute inset-y-0 right-[6px] border-l border-border"
        />
      ) : (
        children
      )}
    </div>
  )
}

function SummaryLine({
  turn,
  n,
  open,
  onToggle,
}: {
  turn: ConversationTurn
  n: number
  open: boolean
  onToggle: () => void
}) {
  const now = useNow()
  const status = turn.display_status
  return (
    <div className="flex h-7 min-w-0 items-center gap-1">
      <button
        type="button"
        data-turn-line
        aria-expanded={open}
        aria-label={`Turn ${n}: ${open ? "hide" : "show"} what the agent did`}
        title={summaryTitle(turn)}
        onClick={onToggle}
        className={cn(
          "-ml-1.5 flex h-7 min-w-0 flex-1 items-center gap-1.5 rounded-input px-1.5 text-left outline-none hover:bg-muted focus-visible:bg-muted focus-visible:shadow-[inset_2px_0_0_var(--ring)]",
          open && "bg-muted"
        )}
      >
        <ChevronRightIcon
          aria-hidden
          className={cn(
            "size-3.5 flex-none text-subtle-foreground transition-transform",
            open && "rotate-90"
          )}
        />
        {status !== "succeeded" && (
          <span className="flex flex-none items-center gap-1.5 text-xs font-medium">
            <StatusDot state={status} />
            {STATUS_LABEL[status]}
          </span>
        )}
        <span className="min-w-0 truncate font-mono text-xs text-muted-foreground tabular-nums">
          {summaryParts(turn, Math.max(now, Date.now())).join(" · ")}
        </span>
      </button>
      <time
        dateTime={turn.started}
        title={dateTime(turn.started)}
        className="flex-none font-mono text-xs text-subtle-foreground tabular-nums max-[720px]:hidden"
      >
        {timeOfDay(turn.started)}
      </time>
      <Link
        to="/runs/$runId"
        params={{ runId: turn.run_id }}
        className="flex h-6 flex-none items-center gap-0.5 rounded-input px-1.5 text-xs text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        Open run
        <ArrowUpRightIcon aria-hidden className="size-3" />
      </Link>
    </div>
  )
}

/** TurnTrace: the turn's waterfall, in place. A row opens the run page
 * at that step. */
function TurnTrace({ turn }: { turn: ConversationTurn }) {
  const data = useRun(turn.run_id)
  const navigate = useNavigate()
  if (data.error)
    return (
      <div className="rounded-alert border">
        <ErrorState error={data.error} what="This turn's timeline" />
      </div>
    )
  if (!data.chain)
    return (
      <div className="overflow-hidden rounded-alert border">
        <LoadingRows rows={3} />
      </div>
    )
  return (
    <TraceWaterfall
      compact
      id="turn"
      trace={data.trace}
      stream={data.stream}
      maxHeight={360}
      className="my-1"
      empty={
        <p className="p-3 text-xs text-muted-foreground">
          No step of this turn has been reported yet.
        </p>
      }
      onSelect={(span) => {
        if (span)
          void navigate({
            to: "/runs/$runId",
            params: { runId: turn.run_id },
            search: { span },
          })
      }}
    />
  )
}

function Note({ note }: { note: TurnNote }) {
  return (
    <div className="flex flex-col gap-1 text-sm">
      <p
        className={cn(
          "text-muted-foreground",
          note.tone === "danger" && "text-danger-foreground"
        )}
      >
        {note.text}
      </p>
      {note.detail && (
        <p className="font-mono text-xs break-words text-muted-foreground">
          {note.detail}
        </p>
      )}
    </div>
  )
}

const CLAMP_CHARS = 1600
const CLAMP_LINES = 24

/** LongText: a message as written. A very long one starts clamped. */
function LongText({ text }: { text: string }) {
  const long =
    text.length > CLAMP_CHARS || text.split("\n").length > CLAMP_LINES
  const [all, setAll] = React.useState(false)
  return (
    <div className="min-w-0">
      <p
        className={cn(
          "text-sm leading-5 break-words whitespace-pre-wrap",
          long && !all && "max-h-60 overflow-hidden"
        )}
      >
        {text}
      </p>
      {long && (
        <button
          type="button"
          onClick={() => setAll(!all)}
          className="mt-1 rounded-sm text-xs text-muted-foreground underline-offset-2 outline-none hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
        >
          {all ? "Show less" : `Show all (${bytes(text.length)})`}
        </button>
      )}
    </div>
  )
}
