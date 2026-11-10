// Everything the run page reads, in one hook: the run with its chain
// (a park and its resumes are one run; a queued run is answered from the
// ledger before its first span), the live stream, and the input of the
// trace waterfall.

import { useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"

import type { LiveCall, TraceInput } from "@/components/console/trace-waterfall"
import { chainQuery } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { isNotFound } from "@/lib/api/errors"
import { useRunLive } from "@/lib/api/live"
import { q } from "@/lib/api/queries"
import type { DisplayStatus } from "@/lib/api/status"
import type { StreamState } from "@/lib/api/stream"
import type { CostFields, PosEvent, RunDetail, Span } from "@/lib/api/types"

export interface RunVersion {
  id?: string
  number?: number
  labels: string[]
  resolvedVia?: string
  placement?: string
  /** The run's manifest is not the one its version registered. */
  drift: boolean
}

export interface RunData {
  id: string
  chain: ChainView | undefined
  status: DisplayStatus | undefined
  /** In the ledger, not in the trace store yet: no span has arrived. */
  queued: boolean
  running: boolean
  stream: StreamState
  error: unknown
  trace: TraceInput
  version: RunVersion
}

const num = (v: unknown) => (typeof v === "number" ? v : undefined)
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined)

/** liveCalls reads a running run's events for the calls that have
 * started and have no span yet (a span is reported when its call ends). */
export function liveCalls(
  runId: string,
  model: string | undefined,
  events: readonly PosEvent[],
  spans: readonly Span[]
): LiveCall[] {
  const done = new Set<string>()
  for (const s of spans) {
    if (s.run_id !== undefined && s.run_id !== runId) continue
    const step = num(s.attrs["weft.step.index"])
    const op = s.attrs["gen_ai.operation.name"]
    if (op === "execute_tool")
      done.add(`tool:${step}:${str(s.attrs["gen_ai.tool.call.id"])}`)
    else if (op !== "invoke_agent") done.add(`model:${step}`)
  }
  let step: number | undefined
  let modelOpen: { step: number; start: number } | undefined
  const tools = new Map<string, LiveCall>()
  for (const e of events) {
    const ev = e.event
    const t = Date.parse(e.time)
    switch (ev.type) {
      case "step_start":
        step = num(ev.index)
        if (step !== undefined) modelOpen = { step, start: t }
        break
      case "tool_start": {
        modelOpen = undefined
        const callId = str(ev.call_id) ?? ""
        tools.set(`${step}:${callId}`, {
          runId,
          kind: "tool",
          label: str(ev.name) ?? "tool",
          start: t,
          step,
          callId,
        })
        break
      }
      case "tool_finish":
        tools.delete(`${step}:${str(ev.call_id) ?? ""}`)
        break
      case "step_finish":
      case "run_finish":
        modelOpen = undefined
        if (ev.type === "run_finish") tools.clear()
        break
    }
  }
  const out: LiveCall[] = []
  if (modelOpen && !done.has(`model:${modelOpen.step}`))
    out.push({
      runId,
      kind: "model",
      label: model ?? "model",
      start: modelOpen.start,
      step: modelOpen.step,
    })
  for (const c of tools.values())
    if (!done.has(`tool:${c.step}:${c.callId}`)) out.push(c)
  return out
}

function versionOf(run: RunDetail | undefined): RunVersion {
  const v = run?.agent_version
  return {
    id: v?.id,
    number: v?.number,
    labels: v?.labels ?? [],
    resolvedVia: run?.resolved_via,
    placement: run?.placement,
    drift: !!run?.manifest_drift,
  }
}

/** isQueued: the ledger has the run and no span has arrived yet. (A
 * ledger-only run that is over is one whose trace was deleted.) */
const isQueued = (c: ChainView) =>
  c.last.ledger_only === true &&
  (c.status === "queued" || c.status === "running")

export function useRun(id: string): RunData {
  const qc = useQueryClient()
  const chainQ = useQuery({
    ...chainQuery(id),
    retry: (n, e) => !isNotFound(e) && n < 2,
    // A queued run is in the ledger only: look again until it starts.
    // A waiting run has no stream to follow (the parked run is over and
    // its resume does not exist yet): look for the decision now and then.
    refetchInterval: (query) => {
      const d = query.state.data
      if (d && (isQueued(d) || d.last.settling)) return 1000
      return d?.status === "waiting" ? 5000 : false
    },
  })
  const chain = chainQ.data
  const lastId = chain?.last.id ?? id
  const status = chain?.status
  const queued = !!chain && isQueued(chain)
  const running = status === "running" || status === "queued"

  const stream = useRunLive(lastId, {
    enabled: running && !!chain && !queued,
    onFrame: () => {
      // The chain is cached under the id in the address.
      if (lastId !== id)
        void qc.invalidateQueries({ queryKey: ["run", id, "chain"] })
    },
  })

  // The last spans of a run (its root among them) are written a moment
  // after its row says it ended, when the stream has already closed:
  // read once more, twice.
  const wasRunning = React.useRef(false)
  React.useEffect(() => {
    const ended = wasRunning.current && !running
    wasRunning.current = running
    if (!ended) return
    const again = () =>
      void qc.invalidateQueries({ queryKey: ["run", id, "chain"] })
    const timers = [setTimeout(again, 1200), setTimeout(again, 4000)]
    return () => timers.forEach(clearTimeout)
  }, [running, qc, id])

  const eventsQ = useQuery({
    ...q.events(lastId),
    enabled: !!chain && status === "running" && !queued,
  })

  const live = React.useMemo(
    () =>
      chain && status === "running"
        ? liveCalls(
            lastId,
            chain.last.model.name,
            eventsQ.data?.events ?? [],
            chain.spans
          )
        : [],
    [chain, status, lastId, eventsQ.data]
  )

  const trace = React.useMemo<TraceInput>(() => {
    if (!chain) return { spans: [], rootRunId: id }
    const costs = new Map<string, CostFields>()
    const head = chain.runs[0]
    costs.set(head.id, { ...head, ...chain.totals })
    for (const r of chain.runs) for (const c of r.children) costs.set(c.id, c)
    return {
      spans: chain.spans,
      rootRunId: head.id,
      chain: chain.runs.map((r) => r.id),
      waits: chain.waits.map((w) => ({
        run: w.run,
        from: w.from,
        to: w.to,
        decided: describeWait(w),
      })),
      live,
      compactions: chain.runs.flatMap((r) =>
        r.compactions.map((c) => ({ ...c, run_id: r.id }))
      ),
      costs,
      root: queued
        ? undefined
        : {
            label: head.agent,
            secondary: head.model.name || undefined,
            started: Date.parse(head.started),
          },
      running: status === "running" && !queued,
    }
  }, [chain, id, live, status, queued])

  return {
    id,
    chain,
    status,
    queued,
    running,
    stream,
    error: chainQ.error,
    trace,
    version: versionOf(chain?.last),
  }
}

/** describeWait: "issue_refund approved by operator op_1", for a wait
 * that ended. */
export function describeWait(
  w: ChainView["waits"][number]
): string | undefined {
  if (w.to === null) return undefined
  const calls = w.approvals.map((a) => `${a.tool} ${a.status}`).join(", ")
  const who = w.decidedBy.length > 0 ? ` by ${w.decidedBy.join(", ")}` : ""
  return `${calls || "Decided"}${who}`
}
