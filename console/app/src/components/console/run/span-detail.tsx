import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import {
  BotIcon,
  CircleIcon,
  FoldVerticalIcon,
  HourglassIcon,
  SparklesIcon,
  WrenchIcon,
  XIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"
import * as React from "react"

import { DetailBoundary } from "@/components/console/detail-boundary"
import { JsonView } from "@/components/console/json-view"
import type {
  TraceModel,
  TraceNode,
} from "@/components/console/trace-waterfall"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { actorText, useMe } from "@/lib/actor"
import type { ActorLike } from "@/lib/actor"
import { approvalsOf, chainStep, chainSteps } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import { stepRequest, stepRequestBadge } from "@/lib/api/steps"
import type {
  Catalog,
  CostFields,
  Message,
  StepDoc,
  ToolEntry,
  Transcript,
} from "@/lib/api/types"
import {
  bytes,
  cost,
  count,
  dateTime,
  duration,
  offset,
  tokens,
} from "@/lib/format"
import { cn } from "@/lib/utils"

import { Facts, Fold, Inline, Muted, Text } from "./fold"
import { Messages, Value, newFrom } from "./messages"

// The detail of one row of the waterfall. One scroll, no tabs, always
// the same order: what the row is (the head and its six facts), then
// Input → Output → Error → Timing & usage → Raw. A section that has
// nothing to say is absent; Raw starts folded.

const str = (v: unknown) => (typeof v === "string" && v ? v : undefined)
const num = (v: unknown) => (typeof v === "number" ? v : undefined)

const ICON: Record<TraceNode["base"], LucideIcon> = {
  agent: BotIcon,
  model: SparklesIcon,
  tool: WrenchIcon,
  handoff: WrenchIcon,
  other: CircleIcon,
  marker: FoldVerticalIcon,
}

const RUN_STATE: Record<
  ChainView["status"],
  ["failed" | "waiting" | "running" | "succeeded" | "neutral", string]
> = {
  queued: ["neutral", "Queued"],
  running: ["running", "Running"],
  waiting: ["waiting", "Waiting"],
  succeeded: ["succeeded", "OK"],
  failed: ["failed", "Failed"],
  cancelled: ["neutral", "Cancelled"],
  interrupted: ["neutral", "Interrupted"],
}

function StateBadge({
  node,
  run,
}: {
  node: TraceNode
  /** The row is the run itself: its state is the run's. */
  run?: ChainView["status"]
}) {
  if (run) return <Badge variant={RUN_STATE[run][0]}>{RUN_STATE[run][1]}</Badge>
  if (node.failed) return <Badge variant="failed">Failed</Badge>
  if (node.kind === "waiting")
    return node.wait?.to === null ? (
      <Badge variant="waiting">Waiting</Badge>
    ) : node.end > node.start ? (
      <Badge variant="neutral">Decided</Badge>
    ) : (
      <Badge variant="neutral">Did not run</Badge>
    )
  if (node.running) return <Badge variant="running">Running</Badge>
  if (node.kind === "marker") return null
  return <Badge variant="succeeded">OK</Badge>
}

type Cell = [label: string, value: React.ReactNode, title?: string]

// Twelve tracks, so a row of 2, 3 or 4 cells fills the width.
const SPAN: Record<number, string> = {
  1: "col-span-12",
  2: "col-span-6",
  3: "col-span-4",
  4: "col-span-3",
}

/** The facts of a row, in one row up to four and in two rows above
 * that (5: three over two, 7: four over three), every row full. */
function Grid({ cells }: { cells: Cell[] }) {
  if (cells.length === 0) return null
  const top = cells.length <= 4 ? cells.length : Math.ceil(cells.length / 2)
  return (
    <dl className="grid flex-none grid-cols-12 border-b">
      {cells.map(([label, value, title], i) => {
        const second = i >= top
        const inRow = second ? cells.length - top : top
        return (
          <div
            key={label}
            title={title}
            className={cn(
              "flex min-w-0 flex-col px-3 py-1.5",
              SPAN[inRow],
              i !== 0 && i !== top && "border-l",
              second && "border-t"
            )}
          >
            <dt className="truncate text-xs text-muted-foreground">{label}</dt>
            <dd className="truncate font-mono text-sm tabular-nums">{value}</dd>
          </div>
        )
      })}
    </dl>
  )
}

function isCatalog(v: unknown): v is Catalog {
  return !!v && typeof v === "object" && Array.isArray((v as Catalog).tools)
}

function toolEntry(
  step: StepDoc | undefined,
  name: string
): ToolEntry | undefined {
  const t = stepRequest(step)?.tools
  return isCatalog(t) ? t.tools.find((x) => x.name === name) : undefined
}

function Loading({ what }: { what: string }) {
  return <Muted>Loading {what}…</Muted>
}

function Unavailable({ error, what }: { error: unknown; what: string }) {
  return (
    <Muted>
      {what} could not be read:{" "}
      {error instanceof Error ? error.message : String(error)}
    </Muted>
  )
}

type FactRow = readonly [string, React.ReactNode] | false | null | undefined

/** "Timing & usage": absent when it has no fact and nothing else to say. */
function Timing({
  rows,
  children,
}: {
  rows: FactRow[]
  children?: React.ReactNode
}) {
  const any = rows.some(
    (r) => !!r && r[1] !== undefined && r[1] !== null && r[1] !== ""
  )
  const more = React.Children.toArray(children).length > 0
  if (!any && !more) return null
  return (
    <Fold id="detail.timing" title="Timing & usage">
      <Facts rows={rows} />
      {children}
    </Fold>
  )
}

function Raw({ node, extra }: { node: TraceNode; extra?: React.ReactNode }) {
  if (node.spans.length === 0 && !extra) return null
  return (
    <Fold id="detail.raw" title="Raw" open={false}>
      {node.spans.map((s) => (
        <React.Fragment key={s.span_id}>
          {node.spans.length > 1 && (
            <span className="font-mono text-xs text-muted-foreground">
              {s.name}
            </span>
          )}
          <JsonView
            value={{
              span_id: s.span_id,
              parent_span_id: s.parent_span_id,
              name: s.name,
              kind: s.kind,
              start: s.start,
              end: s.end,
              status: s.status,
              status_message: s.status_message || undefined,
              attrs: s.attrs,
              events: s.events.length > 0 ? s.events : undefined,
            }}
            depth={2}
          />
        </React.Fragment>
      ))}
      {extra}
    </Fold>
  )
}

// ── Model call ──────────────────────────────────────────────────────

function replyOf(t: Transcript | undefined, step: number): Message | undefined {
  for (const b of t?.batches ?? [])
    if (b.step === step && !b.input)
      for (const m of b.messages ?? []) if (m.role === "assistant") return m
  return undefined
}

function ChangedChip({
  runId,
  step,
  what,
  now,
}: {
  runId: string
  step: number
  what: "system" | "tools"
  now: StepDoc
}) {
  const [open, setOpen] = React.useState(false)
  const before = useQuery({ ...q.step(runId, step - 1), enabled: open })
  const pick = (d: StepDoc | undefined): unknown => {
    if (what === "system") {
      const p = stepRequest(d)?.prompt
      return p && "text" in p ? p.text : undefined
    }
    const t = stepRequest(d)?.tools
    return isCatalog(t) ? t.tools.map((x) => x.name) : undefined
  }
  return (
    <div className="flex flex-col gap-1.5">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex h-5 w-fit items-center rounded-sm bg-warning-muted px-1.5 text-xs font-medium text-warning-foreground outline-none focus-visible:outline-2 focus-visible:outline-ring"
      >
        {what === "system" ? "System prompt" : "Tool catalog"} changed since the
        previous call
      </button>
      {open && (
        <div className="grid grid-cols-2 gap-2">
          {(
            [
              ["Before", pick(before.data)],
              ["Now", pick(now)],
            ] as const
          ).map(([label, v]) => (
            <div key={label} className="flex min-w-0 flex-col gap-1">
              <span className="table-head">{label}</span>
              {v === undefined ? (
                <Muted>{before.isLoading ? "Loading…" : "Not recorded"}</Muted>
              ) : (
                <Value value={v} />
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function ModelCall({
  node,
  onPickCall,
}: {
  node: TraceNode
  onPickCall: (runId: string, step: number, callId: string) => void
}) {
  const runId = node.runId ?? ""
  const step = node.step ?? 0
  const has = node.runId !== undefined && node.step !== undefined
  const stepQ = useQuery({ ...q.step(runId, step), enabled: has })
  const seenQ = useQuery({ ...q.transcript(runId, step), enabled: has })
  const fullQ = useQuery({ ...q.transcript(runId), enabled: has })
  const reqsQ = useQuery({ ...q.requests(runId), enabled: has && step > 0 })
  const s = stepQ.data
  const messages = seenQ.data?.messages ?? []
  const reply = replyOf(fullQ.data, step)
  // A step without a request record carries a badge in its place.
  const req = stepRequest(s)
  const missing = stepRequestBadge(s)
  const prompt = req?.prompt
  const system = prompt && "text" in prompt ? prompt.text : undefined
  const catalog = isCatalog(req?.tools) ? req.tools : undefined

  const hashes = (n: number) =>
    reqsQ.data?.requests.find((r) => r.step === n && r.attempt === 1)
  const prev = hashes(step - 1)
  const cur = req
  const systemChanged =
    !!prev &&
    !!cur &&
    !!prev.system_hash &&
    prev.system_hash !== cur.system_hash
  const toolsChanged =
    !!prev &&
    !!cur &&
    !!prev.catalog_hash &&
    prev.catalog_hash !== cur.catalog_hash

  const attempts = s?.attempts ?? []
  // One attempt that failed is the Error section; the list is for a call
  // that was tried more than once.
  const troubled = attempts.length > 1
  const errors = attempts.filter((a) => a.error)
  const u = s?.usage
  const params = req?.body.params
  const raw = req

  return (
    <>
      <Fold
        id="detail.input"
        title="Input"
        hint={
          seenQ.data
            ? `${count(messages.length)} ${messages.length === 1 ? "message" : "messages"}`
            : undefined
        }
      >
        {s && systemChanged && (
          <ChangedChip runId={runId} step={step} what="system" now={s} />
        )}
        {s && toolsChanged && (
          <ChangedChip runId={runId} step={step} what="tools" now={s} />
        )}
        {missing && (
          <Muted>
            The request was not recorded
            {missing.reason ? `: ${missing.reason}` : ""}.
          </Muted>
        )}
        {system !== undefined && (
          <Inline label="System prompt">
            <Text>{system || "(empty)"}</Text>
          </Inline>
        )}
        {seenQ.isLoading ? (
          <Loading what="the messages" />
        ) : seenQ.error ? (
          <Unavailable error={seenQ.error} what="The messages" />
        ) : messages.length === 0 ? (
          <Muted>
            {seenQ.data?.reason ?? "No messages were recorded for this call."}
          </Muted>
        ) : (
          <Messages
            messages={messages}
            fold={newFrom(messages)}
            onCall={(id) => onPickCall(runId, step - 1, id)}
          />
        )}
        {catalog && (
          <Inline label={`Tools offered · ${catalog.tools.length}`}>
            <ul className="flex flex-col gap-1">
              {catalog.tools.map((t) => (
                <li key={t.name}>
                  <Inline
                    label={
                      <>
                        <span className="font-mono font-medium text-foreground">
                          {t.name}
                        </span>{" "}
                        {t.description}
                      </>
                    }
                  >
                    <JsonView value={t.schema} depth={3} />
                  </Inline>
                </li>
              ))}
            </ul>
          </Inline>
        )}
      </Fold>

      <Fold id="detail.output" title="Output">
        {fullQ.isLoading ? (
          <Loading what="the reply" />
        ) : reply ? (
          <Messages
            messages={[reply]}
            onCall={(id) => onPickCall(runId, step, id)}
          />
        ) : (
          <Muted>
            {node.running
              ? "The model has not answered yet."
              : node.failed
                ? "The call failed before a reply."
                : (fullQ.data?.reason ?? "No reply was recorded.")}
          </Muted>
        )}
      </Fold>

      {(node.failed || errors.length > 0) && (
        <Fold
          id="detail.error"
          title="Error"
          hint={node.failed ? undefined : "of an attempt; the call went on"}
        >
          <Text mono tone="danger">
            {errors.at(-1)?.error ||
              node.span?.status_message ||
              "The call ended with an error."}
          </Text>
          <Facts
            rows={[
              [
                "Kind",
                str(node.span?.attrs["error.type"]) ??
                  errors.at(-1)?.error_type,
              ],
              ["HTTP status", errors.at(-1)?.http_status],
            ]}
          />
        </Fold>
      )}

      <Timing
        rows={[
          // The head says how long the call took: the latency is said
          // only when it is another figure (attempts, a wait between).
          [
            "Latency",
            s?.latency_ms !== undefined &&
            duration(s.latency_ms) !== duration(node.end - node.start)
              ? duration(s.latency_ms)
              : undefined,
          ],
          [
            "First token",
            s?.ttft_ms !== undefined &&
            duration(s.ttft_ms) !== duration(node.end - node.start)
              ? duration(s.ttft_ms)
              : undefined,
          ],
          ["Finish reason", s?.reason],
          // The row names its model; here only a call another model
          // answered.
          [
            "Model",
            s?.model.answered && s.model.answered !== s.model.requested
              ? `${s.model.requested} → answered by ${s.model.answered}`
              : undefined,
          ],
          [
            "Cached input",
            u?.cached_input_tokens ? tokens(u.cached_input_tokens) : undefined,
          ],
          [
            "Cache write",
            u?.cache_write_tokens ? tokens(u.cache_write_tokens) : undefined,
          ],
          [
            "Reasoning tokens",
            u?.reasoning_tokens ? tokens(u.reasoning_tokens) : undefined,
          ],
          // A known cost's caveat is the Cost cell's tooltip.
          ["Cost note", s && !s.cost_known ? s.cost_reason : undefined],
          [
            "Settings",
            params && Object.keys(params).length > 0 ? (
              <span className="font-mono">{JSON.stringify(params)}</span>
            ) : undefined,
          ],
        ]}
      >
        {stepQ.error && <Unavailable error={stepQ.error} what="The step" />}
        {troubled && (
          <div className="flex flex-col gap-1">
            <span className="table-head">Attempts</span>
            <ol className="flex flex-col divide-y rounded-input border">
              {attempts.map((a) => (
                <li
                  key={a.attempt}
                  className="flex items-center gap-2 px-2 py-1 font-mono text-xs"
                >
                  <span className="text-subtle-foreground">#{a.attempt}</span>
                  <span>{a.model}</span>
                  <span
                    className={cn(
                      a.outcome === "ok"
                        ? "text-success-foreground"
                        : "text-danger-foreground"
                    )}
                  >
                    {a.outcome ?? "—"}
                    {a.error_type && ` (${a.error_type})`}
                  </span>
                  {a.wait_ms !== undefined && (
                    <span className="text-muted-foreground">
                      waited {duration(a.wait_ms)}
                    </span>
                  )}
                  {a.model !== s?.model.requested && (
                    <span
                      className="ml-auto rounded-sm bg-warning-muted px-1 font-sans text-[11px] leading-4 font-medium text-warning-foreground"
                      title={`Not the model the call asked for (${s?.model.requested})`}
                    >
                      Fallback
                    </span>
                  )}
                </li>
              ))}
            </ol>
          </div>
        )}
      </Timing>

      <Raw
        node={node}
        extra={
          raw && (
            <>
              <Inline label="Request as recorded">
                <JsonView value={raw.body} depth={2} />
              </Inline>
              {raw.raw !== null && raw.raw !== undefined ? (
                <Inline label="HTTP request and response">
                  <JsonView value={raw.raw} depth={1} />
                </Inline>
              ) : (
                raw.raw_reason && (
                  <Muted>
                    HTTP bodies were not captured: {raw.raw_reason}.
                  </Muted>
                )
              )}
            </>
          )
        }
      />
    </>
  )
}

// ── Tool call, handoff, waiting ─────────────────────────────────────

function useToolCall(node: TraceNode, runId: string | undefined) {
  const has = runId !== undefined && node.step !== undefined
  const stepQ = useQuery({
    ...q.step(runId ?? "", node.step ?? 0),
    enabled: has,
  })
  const ids = new Set(node.spans.map((s) => s.span_id))
  const calls = stepQ.data?.tool_calls ?? []
  const call =
    calls.find((c) => c.span && ids.has(c.span.id)) ??
    calls.find((c) => c.call_id === node.callId)
  return { stepQ, call }
}

function approvalFacts(
  node: TraceNode,
  chain: ChainView,
  entry: ToolEntry | undefined,
  me: ActorLike | undefined
): (readonly [string, React.ReactNode] | undefined)[] {
  const a = node.span?.attrs ?? {}
  const decided = approvalsOf(chain).find(
    (x) => x.call_id === node.callId && x.tool === (node.tool ?? node.label)
  )
  const approved = a["weft.tool.approved"] === true
  const denied = a["weft.tool.denied"] === true
  if (!approved && !denied && !entry?.approval && !decided) return []
  const who =
    actorText(decided?.actor, me) ??
    actorText(
      str(a["weft.tool.approved_by"]) ??
        str(a["weft.tool.denied_by"]) ??
        str(a["weft.approval.actor"]),
      me
    )
  const outcome = approved
    ? "approved"
    : denied
      ? "denied"
      : (decided?.status ?? "required")
  return [
    ["Approval", who ? `${outcome} by ${who}` : outcome],
    ["Reason given", decided?.reason],
  ]
}

function ToolCall({ node, chain }: { node: TraceNode; chain: ChainView }) {
  const { stepQ, call } = useToolCall(node, node.runId)
  const me = useMe()
  const a = node.span?.attrs ?? {}
  const entry = toolEntry(stepQ.data, node.tool ?? node.label)
  const isError = call?.result?.is_error === true || node.failed
  const timeout =
    num(a["weft.tool.timeout_ms"]) ?? (entry?.timeout_ms || undefined)
  return (
    <>
      <Fold id="detail.input" title="Input">
        {stepQ.isLoading ? (
          <Loading what="the arguments" />
        ) : stepQ.error ? (
          <Unavailable error={stepQ.error} what="The step" />
        ) : call ? (
          <Value value={call.args} depth={3} />
        ) : (
          <Muted>The arguments were not recorded.</Muted>
        )}
      </Fold>
      {!isError && (
        <Fold
          id="detail.output"
          title="Output"
          hint={
            call?.result
              ? `${bytes(call.result.bytes)}${call.result.truncated ? " · truncated" : ""}`
              : undefined
          }
        >
          {call?.result ? (
            <Value value={call.result.content} depth={3} />
          ) : (
            <Muted>
              {node.running
                ? "The tool is still running."
                : stepQ.isLoading
                  ? "Loading the result…"
                  : (node.note ?? "No result was recorded.")}
            </Muted>
          )}
        </Fold>
      )}
      {isError && (
        <Fold id="detail.error" title="Error" hint={str(a["error.type"])}>
          <Text mono tone="danger">
            {call?.result?.content ||
              node.span?.status_message ||
              `The tool ended with an error${a["error.type"] ? `: ${String(a["error.type"])}` : "."}`}
          </Text>
        </Fold>
      )}
      <Timing
        rows={[
          [
            "Queue wait",
            num(a["weft.tool.queue_ms"]) !== undefined
              ? duration(num(a["weft.tool.queue_ms"]))
              : undefined,
          ],
          ["Timeout", timeout ? duration(timeout) : undefined],
          // The Output section's title carries the size of a result.
          [
            "Result size",
            isError && num(a["weft.tool.result_bytes"]) !== undefined
              ? bytes(num(a["weft.tool.result_bytes"]))
              : undefined,
          ],
          [
            "Result cap",
            entry?.max_result_bytes ? bytes(entry.max_result_bytes) : undefined,
          ],
          ["Comes from", entry?.source ?? str(a["weft.tool.origin"])],
          ...approvalFacts(node, chain, entry, me),
          ["Note", node.glyph?.title],
        ]}
      />
      <Raw node={node} />
    </>
  )
}

function Handoff({ node, chain }: { node: TraceNode; chain: ChainView }) {
  // A child in flight has no handoff span yet: its row names its parent,
  // and the call's step is read once the call is over.
  const parentRun = node.handoff?.run_id
  const { stepQ, call } = useToolCall(node, parentRun)
  const childId = call?.child_run_id ?? node.span?.run_id ?? node.runId
  const child = chain.runs
    .flatMap((r) => r.children)
    .find((c) => c.id === childId)
  const args = call?.args as Record<string, unknown> | undefined
  const prompt = str(args?.prompt)
  return (
    <>
      <Fold id="detail.input" title="Input">
        {stepQ.isLoading ? (
          <Loading what="the handoff" />
        ) : prompt ? (
          <Text>{prompt}</Text>
        ) : call ? (
          <Value value={call.args} />
        ) : (
          <Muted>
            {child?.prompt_preview ?? "The input was not recorded."}
          </Muted>
        )}
      </Fold>
      <Fold id="detail.output" title="Output">
        {call?.result && !call.result.is_error ? (
          <Value value={call.result.content} />
        ) : (
          <Muted>
            {node.running
              ? "The agent is still running."
              : node.failed || call?.result?.is_error
                ? "None: it failed (below)."
                : "No answer was recorded."}
          </Muted>
        )}
      </Fold>
      {(node.failed || call?.result?.is_error) && (
        <Fold id="detail.error" title="Error">
          <Text mono tone="danger">
            {child?.err ||
              call?.result?.content ||
              node.span?.status_message ||
              "The agent failed."}
          </Text>
        </Fold>
      )}
      <Timing
        rows={[
          [
            "Cost note",
            child && !child.cost_known ? child.cost_reason : undefined,
          ],
          ["Ended by", child?.stopped_by],
        ]}
      />
      <Raw node={node} />
    </>
  )
}

function Waiting({ node, chain }: { node: TraceNode; chain: ChainView }) {
  const { stepQ, call } = useToolCall(node, node.runId)
  const me = useMe()
  const mine = approvalsOf(chain).filter(
    (a) =>
      a.run_id === node.runId && (!node.callId || a.call_id === node.callId)
  )
  const open = mine.some((a) => a.status === "pending")
  const decided = mine.find((a) => a.status !== "pending")
  const wait = chain.waits.find((w) => w.run === node.runId)
  return (
    <>
      <Fold id="detail.input" title="Input">
        {open ? (
          <Muted>
            The call and its exact arguments are in the callout above the
            timeline; decide there.
          </Muted>
        ) : stepQ.isLoading ? (
          <Loading what="the arguments" />
        ) : (decided ?? call) ? (
          <Value value={decided?.args ?? call?.args} depth={3} />
        ) : (
          <Muted>The arguments were not recorded.</Muted>
        )}
      </Fold>
      {!open && (
        <Fold id="detail.output" title="Output">
          <Facts
            rows={[
              ["Decision", decided?.status ?? "none: the run ended first"],
              ["Decided by", actorText(decided?.actor, me)],
              [
                "Decided",
                decided?.decided_at ? dateTime(decided.decided_at) : undefined,
              ],
              ["Reason given", decided?.reason],
              [
                "Answer given",
                decided?.content ? (
                  <span className="font-mono">{decided.content}</span>
                ) : undefined,
              ],
              [
                "Continued in",
                wait?.resumedBy ? (
                  <span className="font-mono">{wait.resumedBy}</span>
                ) : undefined,
              ],
            ]}
          />
        </Fold>
      )}
      <Raw node={node} />
    </>
  )
}

// ── The run itself (the root row), and anything else ────────────────

function RootAgent({ chain }: { node: TraceNode; chain: ChainView }) {
  const first = chain.runs[0]
  const last = chain.last
  const firstQ = useQuery(q.transcript(first.id))
  const lastQ = useQuery(q.transcript(last.id))
  const input =
    firstQ.data?.batches
      .filter((b) => b.input)
      .flatMap((b) => b.messages ?? []) ?? []
  let output: Message | undefined
  for (const b of lastQ.data?.batches ?? [])
    if (!b.input)
      for (const m of b.messages ?? []) if (m.role === "assistant") output = m
  const finished = last.finished !== null && chain.status !== "waiting"
  return (
    <>
      <Fold id="detail.input" title="Input">
        {firstQ.isLoading ? (
          <Loading what="the input" />
        ) : input.length > 0 ? (
          <Messages messages={input} fold={Math.max(0, input.length - 3)} />
        ) : (
          <Muted>
            {firstQ.data?.reason ??
              first.prompt_preview ??
              "The input was not recorded."}
          </Muted>
        )}
      </Fold>
      <Fold id="detail.output" title="Output">
        {finished && output ? (
          <Messages messages={[output]} />
        ) : (
          <Muted>
            {finished
              ? "No final answer was recorded."
              : "The run has not finished."}
          </Muted>
        )}
      </Fold>
      {last.err && chain.status !== "cancelled" && (
        <Fold id="detail.error" title="Error" hint={last.error_type}>
          <Text mono tone="danger">
            {last.err}
          </Text>
        </Fold>
      )}
    </>
  )
}

function Other({ node }: { node: TraceNode }) {
  const s = node.span
  return (
    <>
      {node.failed && (
        <Fold id="detail.error" title="Error">
          <Text mono tone="danger">
            {s?.status_message || "The span ended with an error."}
          </Text>
        </Fold>
      )}
      <Timing
        rows={[
          ["Service", s?.service],
          ["Span kind", s?.kind],
          ["Note", node.note],
        ]}
      >
        {node.spans.length === 0 && node.running && (
          <Muted>
            This call is in flight; its details arrive when it ends.
          </Muted>
        )}
      </Timing>
      <Raw node={node} />
    </>
  )
}

type SpanDetailProps = {
  node: TraceNode
  model: TraceModel
  /** Absent on a raw trace: every row shows as a plain span. */
  chain?: ChainView
  onSelect: (id: string) => void
  onClose: () => void
}

/** SpanDetail: the detail of one row. A row whose data the detail cannot
 * draw says so in the detail; it never takes the page down with it. */
export function SpanDetail(props: SpanDetailProps) {
  return (
    <DetailBoundary key={props.node.id} onClose={props.onClose}>
      <SpanDetailBody {...props} />
    </DetailBoundary>
  )
}

function SpanDetailBody({
  node,
  model,
  chain,
  onSelect,
  onClose,
}: SpanDetailProps) {
  const Icon = node.kind === "waiting" ? HourglassIcon : ICON[node.base]
  const isRoot = !!chain && node === model.roots.at(0) && node.base === "agent"
  const isHandoff = !!chain && node.handed
  // A call in flight has no span to read yet (a child run in flight has
  // its run row, and shows as the handoff it is).
  const live =
    node.spans.length === 0 && node.kind !== "waiting" && !node.handed
  const d = Math.max(0, node.end - node.start)
  // The run row of a child agent: its own totals.
  const childRow =
    chain && node.base === "agent" && !isRoot
      ? chain.runs
          .flatMap((r) => r.children)
          .find((c) => c.id === (node.span?.run_id ?? node.runId))
      : undefined
  // A model call's cost is on its step (the same read its sections use).
  const isModel = !!chain && node.base === "model" && node.spans.length > 0
  const stepQ = useQuery({
    ...q.step(node.runId ?? "", node.step ?? 0),
    enabled: isModel && node.runId !== undefined && node.step !== undefined,
  })
  const stepCost =
    isModel && stepQ.data?.cost_known !== undefined
      ? {
          cost_usd: stepQ.data.cost_usd ?? null,
          cost_known: stepQ.data.cost_known,
          cost_reason: stepQ.data.cost_reason,
        }
      : undefined
  const pickCall = (runId: string, step: number, callId: string) => {
    const hit =
      model.nodes.find(
        (n) =>
          n.runId === runId &&
          n.callId === callId &&
          n.step === step &&
          n.base !== "model"
      ) ??
      model.nodes.find(
        (n) => n.callId === callId && n.step === step && n.base !== "model"
      )
    if (hit) onSelect(hit.id)
  }
  // The facts this kind of row has, and no others: a tool call has no
  // tokens, a wait no cost, the run itself no offset from its start.
  const started: Cell = [
    "Started",
    offset(node.start - model.t0),
    dateTime(new Date(node.start).toISOString()),
  ]
  // A step of the chain's own runs is numbered through the chain.
  const stepNo = chain
    ? (chainStep(chain, node.span?.run_id ?? node.runId, node.step) ??
      node.step)
    : node.step
  const step: Cell | undefined =
    stepNo !== undefined ? ["Step", `#${stepNo}`] : undefined
  const costCell = (
    c:
      | (Pick<CostFields, "cost_usd" | "cost_known" | "cost_partial"> & {
          cost_reason?: string
        })
      | undefined
  ): Cell | undefined => (c ? ["Cost", cost(c), c.cost_reason] : undefined)
  const tokenCells = (i: number | undefined, o: number | undefined): Cell[] =>
    i === undefined && o === undefined
      ? []
      : [
          ["Tokens in", tokens(i ?? 0)],
          ["Tokens out", tokens(o ?? 0)],
        ]
  const took: Cell =
    node.running && node.kind !== "waiting"
      ? ["Running for", duration(d)]
      : ["Duration", duration(d)]
  const attempts = stepQ.data?.attempts?.length ?? 0
  const unused =
    node.failed && !node.tokensIn && !node.tokensOut && !stepQ.data?.cost_usd
  // A time of day, the date on hover.
  const clock = (label: string, iso: string): Cell => [
    label,
    new Date(iso).toLocaleTimeString("en-GB"),
    dateTime(iso),
  ]
  const over =
    !!chain &&
    chain.status !== "running" &&
    chain.status !== "queued" &&
    chain.status !== "waiting"
  const some = (list: (Cell | undefined)[]) =>
    list.filter((c): c is Cell => c !== undefined)
  const cells: Cell[] =
    node.kind === "marker"
      ? []
      : node.kind === "waiting"
        ? some([
            [
              node.wait?.to === null ? "Waiting for" : "Waited",
              node.end > node.start ? duration(d) : "did not run",
            ],
            started,
            step,
          ])
        : isRoot
          ? // The run itself: its duration, cost and tokens are the page's
            // stat strip; here is what the strip does not say.
            some([
              clock("Started", chain.totals.started),
              over && chain.totals.finished
                ? clock("Finished", chain.totals.finished)
                : undefined,
              ["Steps", count(chainSteps(chain))],
              over && chain.last.stopped_by
                ? ["Ended by", chain.last.stopped_by]
                : undefined,
            ])
          : node.base === "agent"
            ? // A child run's totals arrive when it ends.
              node.running
              ? some([took, started, step])
              : some([
                  took,
                  started,
                  childRow
                    ? ["Steps", count(childRow.steps)]
                    : node.handed
                      ? step
                      : undefined,
                  costCell(childRow ?? node.cost),
                  ...tokenCells(
                    childRow?.usage.input_tokens ?? node.tokensIn,
                    childRow?.usage.output_tokens ?? node.tokensOut
                  ),
                ])
            : isModel
              ? some([
                  took,
                  started,
                  step,
                  attempts > 1 ? ["Attempts", count(attempts)] : undefined,
                  // A call that failed before it used a token has neither
                  // tokens nor a cost to show.
                  ...(unused
                    ? []
                    : [
                        stepCost
                          ? costCell(stepCost)
                          : ([
                              "Cost",
                              stepQ.isLoading ? "…" : "unknown",
                            ] as Cell),
                        ...tokenCells(node.tokensIn ?? 0, node.tokensOut ?? 0),
                      ]),
                ])
              : some([took, started, step])

  const what = isHandoff
    ? "Handoff"
    : node.kind === "waiting"
      ? "Approval"
      : node.base === "agent"
        ? "Agent"
        : node.base === "model"
          ? "Model call"
          : node.base === "tool" || node.base === "handoff"
            ? "Tool call"
            : node.base === "marker"
              ? "Marker"
              : "Span"

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-alert border bg-card">
      <div className="flex h-9 flex-none items-center gap-2 border-b pr-1.5 pl-3">
        <span
          className={cn(
            "flex size-5 flex-none items-center justify-center rounded-[5px] border bg-background",
            node.failed
              ? "text-danger-foreground"
              : node.lane.index === 0
                ? "text-muted-foreground"
                : node.lane.text
          )}
        >
          <Icon className="size-3" />
        </span>
        <h2 className="min-w-0 truncate text-sm font-semibold">{node.label}</h2>
        {what !== node.label && (
          <span className="flex-none text-xs text-subtle-foreground">
            {what}
          </span>
        )}
        <StateBadge node={node} run={isRoot ? chain.status : undefined} />
        <div className="ml-auto flex flex-none items-center gap-1">
          {isHandoff && (node.span?.run_id ?? node.runId) && (
            <Button asChild size="sm" variant="secondary">
              <Link
                to="/runs/$runId"
                params={{ runId: node.span?.run_id ?? node.runId ?? "" }}
              >
                Open run
              </Link>
            </Button>
          )}
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label="Close detail"
            title="Close (Esc)"
            onClick={onClose}
          >
            <XIcon />
          </Button>
        </div>
      </div>
      <Grid cells={cells} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {node.kind === "marker" ? (
          <div className="flex flex-col gap-2 p-3">
            <p className="text-sm">{node.label}</p>
            {node.note && <Muted>{node.note}</Muted>}
          </div>
        ) : !chain || live ? (
          <Other node={node} />
        ) : node.kind === "waiting" ? (
          <Waiting node={node} chain={chain} />
        ) : isRoot ? (
          <RootAgent node={node} chain={chain} />
        ) : isHandoff ? (
          <Handoff node={node} chain={chain} />
        ) : node.base === "model" ? (
          <ModelCall node={node} onPickCall={pickCall} />
        ) : node.base === "tool" || node.base === "handoff" ? (
          <ToolCall node={node} chain={chain} />
        ) : (
          <Other node={node} />
        )}
      </div>
    </div>
  )
}
