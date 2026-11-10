import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  InfoIcon,
  OctagonXIcon,
} from "lucide-react"
import * as React from "react"

import { CopyId } from "@/components/console/copy-id"
import { ErrorState } from "@/components/console/empty-state"
import { LinkTabs, Page, PageHeader } from "@/components/console/page-header"
import { useNow } from "@/components/console/relative-time"
import { RunActions } from "@/components/console/run/actions"
import { ApprovalCallout } from "@/components/console/run/approval"
import { DetailsTab } from "@/components/console/run/details-tab"
import { LogsTab } from "@/components/console/run/logs-tab"
import { SpanDetail } from "@/components/console/run/span-detail"
import { TranscriptTab } from "@/components/console/run/transcript-tab"
import { useRun } from "@/components/console/run/use-run"
import type { RunData } from "@/components/console/run/use-run"
import { SplitPane } from "@/components/console/split-pane"
import { StatStrip } from "@/components/console/stat-strip"
import type { Stat } from "@/components/console/stat-strip"
import { StatusBadge } from "@/components/console/status"
import {
  TraceWaterfall,
  failingNode,
  landingSelection,
  useTraceView,
  waitingNode,
} from "@/components/console/trace-waterfall"
import type { TraceNode, TraceView } from "@/components/console/trace-waterfall"
import { Button } from "@/components/ui/button"
import { approvalsOf, chainStep } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import { STATUS_HINT } from "@/lib/api/status"
import type { Hole } from "@/lib/api/types"
import {
  between,
  cost,
  count,
  dateTime,
  duration,
  tokens,
  usd,
} from "@/lib/format"
import { useSetSearch, validateRun } from "@/lib/search"
import type { RunSearch } from "@/lib/search"
import { cn } from "@/lib/utils"

// The run page (CONSOLE-UX, "Run (detail)" and "Debugging a run"). Four
// groups and nothing outside them:
//   what happened   the header line, the one stat strip, at most one
//                   banner and at most one muted notice;
//   Timeline        the trace waterfall, and the detail of the row chosen;
//   Transcript      the conversation, ending with the final output;
//   Logs, Details   the app's log lines; how the run was configured.
// A run that parked and was resumed is one run here (lib/api/chain.ts).
export const Route = createFileRoute("/runs/$runId")({
  validateSearch: validateRun,
  head: ({ params }) => ({
    meta: [{ title: `${params.runId.slice(-8)} · Runs · weftbase` }],
  }),
  component: RunPage,
})

function RunPage() {
  const { runId } = Route.useParams()
  // One page instance per run: selection, playhead and folds start over.
  return <Run key={runId} runId={runId} />
}

function Run({ runId }: { runId: string }) {
  const search = Route.useSearch()
  const set = useSetSearch<RunSearch>(Route.fullPath)
  const data = useRun(runId)
  const view = useTraceView(data.trace)
  const { chain, status } = data
  const tab = search.tab ?? "timeline"

  // The landing rule: a failed run opens on the failing step, a waiting
  // run on the waiting row, anything else with nothing selected. Once.
  const landed = React.useRef(false)
  React.useEffect(() => {
    if (landed.current || !chain || view.model.nodes.length === 0) return
    landed.current = true
    if (search.span !== undefined || search.tab !== undefined) return
    const id = landingSelection(view.model, chain.status)
    if (id) set({ span: id })
  }, [chain, view.model, search.span, search.tab, set])

  const select = (id: string | undefined) => set({ span: id })
  const showStep = (id: string) => set({ tab: undefined, span: id })
  const node = search.span ? view.model.byId.get(search.span) : undefined

  if (data.error)
    return (
      <Page
        header={
          <PageHeader title="Run" id={<CopyId id={runId} label="run id" />} />
        }
      >
        <ErrorState error={data.error} what="The run" />
      </Page>
    )
  if (!chain) return <Page header={<PageHeader title="Run" />}>{null}</Page>

  const rootSpan = view.model.roots.at(0)?.span
  const pageUrl =
    typeof rootSpan?.attrs["weft.page_url"] === "string"
      ? rootSpan.attrs["weft.page_url"]
      : undefined
  const outputType =
    typeof rootSpan?.attrs["weft.output.type"] === "string"
      ? rootSpan.attrs["weft.output.type"]
      : undefined

  const playheadStep = view.playback.atEnd
    ? undefined
    : playheadStepOf(view, chain)

  return (
    <Page
      header={<Header data={data} view={view} pageUrl={pageUrl} />}
      className="flex flex-col gap-3 overflow-hidden"
    >
      <StatStrip stats={stripOf(data, view)} />
      <WhatHappened data={data} view={view} onJump={(id) => showStep(id)} />
      <LinkTabs
        className="-mt-1 border-b px-0"
        tabs={(
          [
            ["timeline", "Timeline"],
            ["transcript", "Transcript"],
            ["logs", "Logs"],
            ["details", "Details"],
          ] as const
        ).map(([key, label]) => ({
          label,
          active: tab === key,
          link: {
            to: "/runs/$runId",
            params: { runId },
            search: (prev: RunSearch) => ({
              ...prev,
              tab: key === "timeline" ? undefined : key,
            }),
            replace: true,
          } as never,
        }))}
      />
      <div
        className="min-h-0 flex-1"
        onKeyDown={(e) => {
          if (e.key !== "Escape" || !search.span || tab !== "timeline") return
          const t = e.target as HTMLElement
          if (t.closest("input, textarea, [role=dialog], [role=menu]")) return
          select(undefined)
        }}
      >
        {tab === "timeline" ? (
          <SplitPane
            id="run"
            gutter
            defaultWidth={480}
            min={360}
            max={900}
            minBeside={320}
            detail={
              node && (
                <SpanDetail
                  key={node.id}
                  node={node}
                  model={view.model}
                  chain={chain}
                  onSelect={select}
                  onClose={() => select(undefined)}
                />
              )
            }
          >
            <TraceWaterfall
              id="run"
              view={view}
              selected={search.span}
              onSelect={select}
              stream={data.stream}
              autoFocus
              empty={
                data.queued
                  ? "Queued: waiting for a worker to pick the run up. The timeline appears with its first step."
                  : status === "running"
                    ? "Waiting for the first step…"
                    : "No spans were recorded for this run."
              }
            />
          </SplitPane>
        ) : !chain ? (
          <p className="text-xs text-muted-foreground">
            The run is queued; nothing is recorded yet.
          </p>
        ) : tab === "transcript" ? (
          <div className="h-full overflow-y-auto">
            <TranscriptTab
              chain={chain}
              step={search.step}
              playheadStep={playheadStep}
              outputType={outputType}
              onStep={(step) => set({ step })}
              onCall={(callId, name) => {
                const hit = [...view.model.nodes]
                  .reverse()
                  .find(
                    (n) => n.callId === callId && (n.tool ?? n.label) === name
                  )
                if (hit) showStep(hit.id)
              }}
            />
          </div>
        ) : tab === "logs" ? (
          <LogsTab
            chain={chain}
            t0={view.model.t0}
            known={(id) => view.model.byId.has(id)}
            onStep={showStep}
          />
        ) : (
          <div className="h-full overflow-y-auto">
            <DetailsTab
              chain={chain}
              version={data.version}
              t0={view.model.t0}
            />
          </div>
        )}
      </div>
    </Page>
  )
}

/** The step the playhead is in: the newest model call of the chain's
 * own runs that had started by then, counted through the chain. */
function playheadStepOf(view: TraceView, chain: ChainView) {
  const at = view.scale.toReal(view.playback.at)
  let step: number | undefined
  for (const n of view.model.nodes) {
    if (n.base !== "model" || n.start > at) continue
    const s = chainStep(chain, n.span?.run_id ?? n.runId, n.step)
    if (s !== undefined) step = Math.max(step ?? 0, s)
  }
  return step
}

// ── Header ──────────────────────────────────────────────────────────

function Header({
  data,
  view,
  pageUrl,
}: {
  data: RunData
  view: TraceView
  pageUrl: string | undefined
}) {
  const { chain, status, version } = data
  const run = chain?.last
  const agent = run?.agent ?? "Run"
  const sessionId = run?.session_id || undefined
  const session = useQuery({
    ...q.session(sessionId ?? ""),
    enabled: !!sessionId,
    staleTime: 30_000,
  })
  const turns = session.data?.runs ?? []
  // A park and its resume are one turn: step over the chain's own runs.
  const mine = turns
    .map((t, i) => (chain?.runs.some((r) => r.id === t.id) ? i : -1))
    .filter((i) => i >= 0)
  const prev = mine.length > 0 && mine[0] > 0 ? turns[mine[0] - 1] : undefined
  const next = mine.length > 0 ? turns.at(mine[mine.length - 1] + 1) : undefined
  const cause = [
    run?.error_type,
    run?.stopped_by && run.stopped_by !== "final_answer"
      ? `stopped by ${run.stopped_by}`
      : "",
    run?.cancel_cause ? `cancelled by ${run.cancel_cause}` : "",
  ]
    .filter(Boolean)
    .join(" · ")

  const title = sessionId ? (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link
        to="/sessions"
        className="font-normal text-muted-foreground hover:text-foreground"
      >
        Sessions
      </Link>
      <span className="font-normal text-subtle-foreground">›</span>
      <Link
        to="/sessions/$sessionId"
        params={{ sessionId }}
        search={{ turn: run?.turn }}
        className="max-w-56 truncate font-normal text-muted-foreground hover:text-foreground"
      >
        {session.data?.title ||
          session.data?.first_prompt ||
          session.data?.public_id ||
          "Session"}
      </Link>
      <span className="font-normal text-subtle-foreground">›</span>
      <span className="flex-none">Turn {run?.turn}</span>
      <span className="ml-1 flex flex-none items-center">
        {(
          [
            [prev, ChevronLeftIcon, "Previous turn"],
            [next, ChevronRightIcon, "Next turn"],
          ] as const
        ).map(([t, Icon, label]) =>
          t ? (
            <Button key={label} asChild size="icon-sm" variant="ghost">
              <Link
                to="/runs/$runId"
                params={{ runId: t.id }}
                aria-label={label}
                title={label}
              >
                <Icon />
              </Link>
            </Button>
          ) : (
            <Button
              key={label}
              size="icon-sm"
              variant="ghost"
              disabled
              aria-label={label}
            >
              <Icon />
            </Button>
          )
        )}
      </span>
    </span>
  ) : (
    agent
  )

  return (
    <PageHeader
      title={title}
      status={
        status && (
          <span title={cause ? `${STATUS_HINT[status]} (${cause})` : undefined}>
            <StatusBadge state={status} />
          </span>
        )
      }
      id={<CopyId id={data.id} label="run id" />}
      meta={
        <>
          {sessionId && (
            <span className="font-medium text-foreground">{agent}</span>
          )}
          {version.id && (
            <span
              className="inline-flex h-5 items-center gap-1 rounded-sm border px-1.5 font-mono text-xs"
              title={[
                version.resolvedVia &&
                  `resolved through ${version.resolvedVia}`,
                version.placement && `placement ${version.placement}`,
                version.id,
              ]
                .filter(Boolean)
                .join(" · ")}
            >
              {version.number !== undefined ? `v${version.number}` : "version"}
              {version.labels.length > 0 && (
                <span className="font-sans text-subtle-foreground">
                  {version.labels.join(", ")}
                </span>
              )}
            </span>
          )}
        </>
      }
      actions={
        run && (
          <RunActions
            runId={run.id}
            running={status === "running"}
            hasChildren={view.model.nodes.some((n) => n.handed)}
            pageUrl={pageUrl}
          />
        )
      }
    />
  )
}

// ── The stat strip ──────────────────────────────────────────────────

const COUNTERS = [
  ["attempts_retried", "retried attempt", "retried attempts"],
  ["fallbacks", "fallback", "fallbacks"],
  ["denied", "denial", "denials"],
  ["truncated", "truncated output", "truncated outputs"],
  ["repaired", "repaired call", "repaired calls"],
  ["log_error", "log error", "log errors"],
  ["log_warn", "log warning", "log warnings"],
] as const

function StripClock({
  chain,
  children,
}: {
  chain: ChainView
  children: (elapsed: number | undefined) => React.ReactNode
}) {
  const now = useNow()
  return (
    <>
      {children(
        chain.totals.durationMs ?? between(chain.totals.started, null, now)
      )}
    </>
  )
}

function stripOf(data: RunData, view: TraceView): Stat[] {
  const { chain } = data
  const s = view.stats
  const playing = !view.playback.atEnd
  // Nothing has happened yet in a run that waits for a worker.
  if (!chain || data.queued)
    return [
      "Duration",
      "Cost",
      "Tokens",
      "Model calls",
      "Tool calls",
      "Issues",
    ].map((label) => ({ label, value: "—" }))
  const t = chain.totals
  const last = chain.last
  const total = t.usage.input_tokens + t.usage.output_tokens
  const own = chain.runs.reduce(
    (n, r) =>
      n +
      (r.own_usage ? r.own_usage.input_tokens + r.own_usage.output_tokens : 0),
    0
  )
  const delegated = chain.runs.reduce(
    (n, r) =>
      n +
      (r.delegated_usage
        ? r.delegated_usage.input_tokens + r.delegated_usage.output_tokens
        : 0),
    0
  )
  const unpriced = [
    ...new Set(
      chain.runs.flatMap((r) => [
        ...(r.unpriced_models ?? []),
        ...(r.delegated_unpriced_models ?? []),
      ])
    ),
  ]
  const delegatedCost = chain.runs.every((r) => r.delegated_cost_known)
    ? chain.runs.reduce((n, r) => n + (r.delegated_cost_usd ?? 0), 0)
    : null
  const hasChildren = chain.runs.some((r) => r.children.length > 0)

  const counters = COUNTERS.map(([key, one, many]) => {
    const n = chain.runs.reduce((sum, r) => sum + (r[key] ?? 0), 0)
    return n > 0 ? `${n} ${n === 1 ? one : many}` : ""
  }).filter(Boolean)
  const counted = COUNTERS.reduce(
    (sum, [key]) => sum + chain.runs.reduce((n, r) => n + (r[key] ?? 0), 0),
    0
  )
  const failedCalls = s.failed
  const runFailed = chain.status === "failed" && failedCalls === 0 ? 1 : 0
  const issues = playing ? failedCalls : failedCalls + runFailed + counted
  const issueList = [
    failedCalls > 0
      ? `${failedCalls} failed ${failedCalls === 1 ? "call" : "calls"}`
      : "",
    runFailed ? `the run failed (${last.error_type ?? "error"})` : "",
    ...counters,
  ].filter(Boolean)

  const share = total > 0 ? (s.tokensIn + s.tokensOut) / total : 0
  // A run that is going: its row carries the usage and cost so far when
  // the server knows them. Until it does, the tokens are those of the
  // model calls that have ended (their spans), and with none of either
  // the cells read "—", never a made-up zero.
  const going = chain.status === "running"
  const ended = s.tokensIn + s.tokensOut
  const fromSpans = going && total === 0 && ended > 0
  const noUsage = going && total === 0 && ended === 0
  const [tokensIn, tokensOut] =
    playing || fromSpans
      ? [s.tokensIn, s.tokensOut]
      : [t.usage.input_tokens, t.usage.output_tokens]

  return [
    {
      label: "Duration",
      value:
        playing || view.live ? (
          duration(s.elapsed)
        ) : (
          <StripClock chain={chain}>{(ms) => duration(ms)}</StripClock>
        ),
      hint:
        t.waitingMs > 0 && !playing
          ? `${duration(t.waitingMs)} waiting`
          : chain.status === "waiting" && !playing
            ? "waiting on a person"
            : undefined,
      title: `Started ${dateTime(t.started)}${t.finished && chain.status !== "running" && chain.status !== "waiting" ? `, ended ${dateTime(t.finished)}` : ""}`,
    },
    {
      label: "Cost",
      value:
        playing && t.cost_known && t.cost_usd !== null
          ? usd(t.cost_usd * share)
          : going && (total === 0 || (!t.cost_known && t.cost_usd === null))
            ? "—"
            : cost(t),
      hint: playing
        ? "so far, by tokens"
        : going
          ? undefined
          : unpriced.length > 0
            ? "children unpriced"
            : undefined,
      title: [
        `This run's own model calls: ${cost(t)}${last.cost_reason ? ` (${last.cost_reason})` : ""}.`,
        hasChildren
          ? `Child runs: ${delegatedCost === null ? "unknown" : usd(delegatedCost)}${last.delegated_cost_reason ? ` (${last.delegated_cost_reason})` : ""}.`
          : "",
        unpriced.length > 0 ? `No price for: ${unpriced.join(", ")}.` : "",
      ]
        .filter(Boolean)
        .join("\n"),
    },
    {
      label: "Tokens",
      value: noUsage ? "—" : tokens(tokensIn + tokensOut),
      hint: noUsage
        ? undefined
        : `${tokens(tokensIn)} in · ${tokens(tokensOut)} out`,
      title: noUsage
        ? "No model call has ended yet"
        : fromSpans
          ? "Of the model calls that have ended so far"
          : hasChildren && !going
            ? `Own: ${tokens(own)} · child runs: ${tokens(delegated)}`
            : "Input and output tokens of every model call",
    },
    {
      label: "Model calls",
      value: count(s.modelCalls),
      title: "Model calls of this run and the runs it started",
    },
    {
      label: "Tool calls",
      value: count(s.toolCalls),
      title:
        "Tool calls of this run and the runs it started, handoffs included",
    },
    {
      label: "Issues",
      value: count(issues),
      tone:
        failedCalls + runFailed > 0
          ? "danger"
          : issues > 0
            ? "warning"
            : "default",
      title:
        issueList.length > 0
          ? issueList.join("\n")
          : "No errors, retries, denials or log warnings",
    },
  ]
}

// ── What happened: at most one banner, at most one notice ───────────

function sentence(chain: ChainView, node: TraceNode | undefined): string {
  const r = chain.last
  // A step of the chain's own runs is counted through the chain.
  const step =
    node &&
    (chainStep(chain, node.span?.run_id ?? node.runId, node.step) ?? node.step)
  const at = step !== undefined ? ` at step ${step}` : ""
  switch (chain.status) {
    case "failed":
      if (r.error_type === "max_steps")
        return `Stopped at its limit of ${r.steps} steps without a final answer${node ? `; the last call, ${node.tool ?? node.label}, failed` : ""}.`
      if (node && node.base === "model")
        return `Failed${at}: the model call${node.model ? ` to ${node.model}` : ""} returned an error.`
      if (node && node.base !== "agent")
        return `Failed${at}: ${node.tool ?? node.label} returned an error.`
      return `Failed${r.error_type ? ` (${r.error_type})` : ""}.`
    case "cancelled": {
      const cause = r.cancel_cause ?? chain.run.chain?.cancel_cause
      return `Cancelled${cause ? ` by ${cause === "user" ? "a person" : cause}` : ""}${node ? ` while ${node.tool ?? node.label} was running` : ""}.`
    }
    case "interrupted":
      return `Stopped reporting ${dateTime(r.last_seen)} without an end: the app may have crashed or lost its connection.`
    default:
      return ""
  }
}

function WhatHappened({
  data,
  view,
  onJump,
}: {
  data: RunData
  view: TraceView
  onJump: (id: string) => void
}) {
  const { chain } = data
  if (!chain) return null
  const status = chain.status
  const failing = failingNode(view.model)
  const waiting = waitingNode(view.model)
  const node =
    status === "failed" || status === "cancelled" ? failing : undefined
  const text = sentence(chain, node)
  const derived = chain.runs.some((r) => r.derived)
  // A derived run's own hole says what the sentence below says.
  const holes: Hole[] = [
    ...chain.holes,
    ...chain.runs.flatMap((r) => r.holes),
  ].filter((h) => !(derived && h.hole === "derived"))
  // One sentence each; the first is shown, the rest (and what the
  // server says to do about it) are in the tooltip.
  const notes = [
    chain.last.ledger_only && !data.queued
      ? "Only the run ledger's row is stored for this run: its trace was deleted, or never arrived."
      : "",
    derived
      ? "This run was built from stock OpenTelemetry spans, which carry no messages, requests or tool arguments, so those cannot be shown."
      : "",
    ...holes.map(
      (h) => `Some of this run is missing: ${h.reason.replace(/\.$/, "")}.`
    ),
  ].filter(Boolean)
  const fixes = [
    ...new Set(
      [...chain.holes, ...chain.runs.flatMap((r) => r.holes)]
        .map((h) => h.fix)
        .filter((f): f is string => !!f)
    ),
  ]

  return (
    <>
      {status === "waiting" && (
        <ApprovalCallout
          approvals={approvalsOf(chain)}
          onJump={waiting ? () => onJump(waiting.id) : undefined}
        />
      )}
      {text && (
        <div
          role="status"
          className={cn(
            "flex min-h-9 flex-none items-center gap-2 rounded-alert border px-3 py-1.5 text-sm",
            status === "failed"
              ? "border-danger/30 bg-danger-muted"
              : "bg-neutral-muted"
          )}
        >
          <OctagonXIcon
            className={cn(
              "size-3.5 flex-none",
              status === "failed"
                ? "text-danger-foreground"
                : "text-muted-foreground"
            )}
          />
          <p className="min-w-0 flex-none font-medium">{text}</p>
          <span
            className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground"
            title={chain.last.err}
          >
            {status === "failed" && chain.last.error_type !== "max_steps"
              ? chain.last.err
              : ""}
          </span>
          {node && (
            <Button size="sm" variant="ghost" onClick={() => onJump(node.id)}>
              Jump to step
            </Button>
          )}
        </div>
      )}
      {notes.length > 0 && (
        <p
          className="flex flex-none items-start gap-1.5 text-xs text-muted-foreground"
          title={[
            ...notes.slice(1),
            ...fixes.map((f) => `To record it: ${f.replace(/\.$/, "")}.`),
          ].join("\n\n")}
        >
          <InfoIcon className="mt-px size-3.5 flex-none text-subtle-foreground" />
          <span className="line-clamp-2">
            {notes[0]}
            {notes.length > 1 &&
              ` (And ${notes.length - 1} more: hover to read.)`}
          </span>
        </p>
      )}
    </>
  )
}
