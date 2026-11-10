import { useQueries, useQuery } from "@tanstack/react-query"

import { ErrorState } from "@/components/console/empty-state"
import { JsonView } from "@/components/console/json-view"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { chainSteps, locateStep } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import type { Message, Transcript } from "@/lib/api/types"

import { Muted } from "./fold"
import { Messages, parseJson } from "./messages"

// The Transcript tab: the conversation as a person reads it, ending with
// the run's final output. "As of step N" shows what the model had seen
// at that step; during playback it follows the playhead.
//
// A run that parked and was resumed is one conversation and one count of
// steps: the steps before a park are the earlier run's, and "as of step
// N" is read from the run of the chain that took that step.

const END = "end"

function finalText(m: Message | undefined): string | undefined {
  const text = m?.content
    .map((p) => (p.type === "text" && typeof p.text === "string" ? p.text : ""))
    .join("")
  return text || undefined
}

/** submittedOf: the output a run submitted: the arguments of the last
 * call of the conversation, and where that call's message begins when
 * the message is nothing but the call. */
function submittedOf(
  all: readonly Message[]
): { value: unknown; from: number } | undefined {
  for (let i = all.length - 1; i >= 0; i--) {
    const m = all[i]
    if (m.role !== "assistant") continue
    const calls = m.content.filter((p) => p.type === "tool_call")
    const call = calls.at(-1)
    if (!call || call.type !== "tool_call") return undefined
    const only = m.content.every(
      (p) => p.type === "tool_call" && p.id === call.id
    )
    return { value: call.args, from: only ? i : all.length }
  }
  return undefined
}

/** wholeOf joins the transcripts of a chain's runs, oldest first, into
 * one conversation. A resume's input repeats what came before it, so
 * only what it produced is added; unless nothing came before (the
 * earlier run's messages were not recorded), when its input is all
 * there is. */
export function wholeOf(docs: readonly (Transcript | undefined)[]): Message[] {
  const out: Message[] = []
  for (const doc of docs)
    for (const b of doc?.batches ?? [])
      if (!b.input || out.length === 0) out.push(...(b.messages ?? []))
  return out
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
  /** The step in the address (`?step=`), counted through the chain. */
  step: number | undefined
  /** The step the playhead is in, while playback is under way (counted
   * through the chain). */
  playheadStep: number | undefined
  /** The name of the run's typed output, when it has one. */
  outputType?: string
  onStep: (step: number | undefined) => void
  /** A tool call was chosen: show its step in the timeline. */
  onCall: (callId: string, name: string) => void
}) {
  const run = chain.last
  const total = chainSteps(chain)
  const wanted = step ?? playheadStep
  // A step the chain has not taken (an old link) is the whole run.
  const asOf = wanted !== undefined && wanted < total ? wanted : undefined
  const follows = step === undefined && asOf !== undefined
  const at = locateStep(chain, asOf ?? 0)
  const seen = useQuery({
    ...q.transcript(at.run.id, at.step),
    enabled: asOf !== undefined,
  })
  const whole = useQueries({
    queries: chain.runs.map((r) => ({
      ...q.transcript(r.id),
      enabled: asOf === undefined,
    })),
  })
  const steps = Array.from({ length: total }, (_, i) => i)

  const error =
    asOf !== undefined ? seen.error : whole.find((w) => w.error)?.error
  if (error) return <ErrorState error={error} what="The transcript" />
  // The newest run's document says why there is nothing, when there is
  // nothing.
  const doc =
    asOf !== undefined
      ? seen.data
      : whole.every((w) => w.data)
        ? whole.at(-1)?.data
        : undefined
  const all =
    asOf !== undefined
      ? (seen.data?.messages ?? [])
      : wholeOf(whole.map((w) => w.data))
  const finished = run.finished !== null && chain.status !== "waiting"
  const last = all.at(-1)
  // A typed output is submitted as the arguments of the run's last call
  // (stopped_by: output_submitted); that call and its acknowledgement
  // are the final output, not two more lines of the conversation.
  const submitted =
    asOf === undefined && finished && run.stopped_by === "output_submitted"
      ? submittedOf(all)
      : undefined
  const final =
    submitted !== undefined
      ? undefined
      : asOf === undefined && finished && last?.role === "assistant"
        ? finalText(last)
        : undefined
  const body = submitted
    ? all.slice(0, submitted.from)
    : final !== undefined
      ? all.slice(0, -1)
      : all
  const typed = submitted
    ? submitted.value
    : final !== undefined
      ? parseJson(final)
      : undefined
  const hasFinal = submitted !== undefined || final !== undefined

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
          {hasFinal && (
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
              (submitted || outputType || typeof typed === "object") ? (
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
