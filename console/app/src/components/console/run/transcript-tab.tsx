import { useQuery } from "@tanstack/react-query"

import { ErrorState } from "@/components/console/empty-state"
import { JsonView } from "@/components/console/json-view"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import type { Message } from "@/lib/api/types"

import { Muted } from "./fold"
import { Messages, parseJson } from "./messages"

// The Transcript tab: the conversation as a person reads it, ending with
// the run's final output. "As of step N" shows what the model had seen
// at that step; during playback it follows the playhead.

const END = "end"

function finalText(m: Message | undefined): string | undefined {
  const text = m?.content
    .map((p) => (p.type === "text" && typeof p.text === "string" ? p.text : ""))
    .join("")
  return text || undefined
}

export function TranscriptTab({
  chain,
  step,
  playheadStep,
  outputType,
  onStep,
  onCall,
}: {
  chain: ChainView
  /** The step in the address (`?step=`). */
  step: number | undefined
  /** The step the playhead is in, while playback is under way. */
  playheadStep: number | undefined
  /** The name of the run's typed output, when it has one. */
  outputType?: string
  onStep: (step: number | undefined) => void
  /** A tool call was chosen: show its step in the timeline. */
  onCall: (callId: string, name: string) => void
}) {
  const run = chain.last
  const asOf = step ?? playheadStep
  const follows = step === undefined && playheadStep !== undefined
  const t = useQuery(q.transcript(run.id, asOf))
  const steps = Array.from({ length: run.steps }, (_, i) => i)

  if (t.error) return <ErrorState error={t.error} what="The transcript" />
  const doc = t.data
  const all =
    asOf !== undefined
      ? (doc?.messages ?? [])
      : (doc?.batches.flatMap((b) => b.messages ?? []) ?? [])
  const finished = run.finished !== null && chain.status !== "waiting"
  const last = all.at(-1)
  const final =
    asOf === undefined && finished && last?.role === "assistant"
      ? finalText(last)
      : undefined
  const body = final !== undefined ? all.slice(0, -1) : all
  const typed = final !== undefined ? parseJson(final) : undefined

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 pb-6">
      <div className="flex h-7 items-center gap-2 text-xs text-muted-foreground">
        <span>Show</span>
        <Select
          value={asOf === undefined ? END : String(asOf)}
          onValueChange={(v) => onStep(v === END ? undefined : Number(v))}
        >
          <SelectTrigger size="sm" aria-label="Show the transcript as of">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={END}>the whole run</SelectItem>
            {steps.map((n) => (
              <SelectItem key={n} value={String(n)}>
                as of step {n}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {follows && <span>following the playhead</span>}
        {asOf !== undefined && (
          <span>what the model had seen when step {asOf} began</span>
        )}
        {chain.runs.length > 1 && asOf === undefined && (
          <span className="ml-auto">
            One conversation across {chain.runs.length - 1}{" "}
            {chain.runs.length === 2 ? "wait" : "waits"} on a person
          </span>
        )}
      </div>
      {!doc ? (
        <Muted>Loading the transcript…</Muted>
      ) : all.length === 0 ? (
        <Muted>
          {doc.reason ??
            "No messages were recorded for this run (content may be off for this app)."}
          {doc.fix && ` ${doc.fix}.`}
        </Muted>
      ) : (
        <>
          <Messages messages={body} onCall={onCall} />
          {final !== undefined && (
            <div className="flex flex-col gap-1 rounded-alert border bg-card p-3">
              <span className="table-head">
                Final output
                {outputType && (
                  <span className="ml-1.5 font-mono normal-case">
                    {outputType}
                  </span>
                )}
              </span>
              {typed !== undefined &&
              (outputType || typeof typed === "object") ? (
                <JsonView value={typed} depth={3} />
              ) : (
                <p className="text-sm break-words whitespace-pre-wrap">
                  {final}
                </p>
              )}
            </div>
          )}
          {asOf === undefined && !finished && (
            <Muted>
              {chain.status === "waiting"
                ? "The run is waiting on a person; it has no final output yet."
                : "The run is still going."}
            </Muted>
          )}
        </>
      )}
    </div>
  )
}
