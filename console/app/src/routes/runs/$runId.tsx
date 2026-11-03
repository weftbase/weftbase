import { useQuery, useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ArrowLeftIcon, OctagonXIcon } from "lucide-react"

import { ApprovalCallout } from "@/components/console/approval-actions"
import { CodeBlock } from "@/components/console/code-block"
import { LinkTab, LinkTabs } from "@/components/console/link-tabs"
import { PageHeader } from "@/components/console/page-header"
import { RunStatusBadge } from "@/components/console/status"
import { StepTimeline, stepTitle } from "@/components/console/step-timeline"
import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import { q } from "@/lib/api/queries"
import type { Run, Step } from "@/lib/api/types"
import { cost, dateTime, duration, json, tokens } from "@/lib/format"
import { ensure } from "@/lib/load"
import { useEnv, validateRun } from "@/lib/search"

export const Route = createFileRoute("/runs/$runId")({
  validateSearch: validateRun,
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env }, params }) =>
    ensure(queryClient, q.run(env, params.runId)),
  head: ({ params }) => ({
    meta: [{ title: `${params.runId} · Runs · weftbase` }],
  }),
  component: RunPage,
})

function RunPage() {
  const { runId } = Route.useParams()
  const search = Route.useSearch()
  const env = useEnv()
  const { data: run } = useSuspenseQuery(q.run(env, runId))
  const view = search.view ?? "steps"
  const total =
    run.duration_ms ??
    run.steps.reduce(
      (a, s) => Math.max(a, s.offset_ms + (s.duration_ms ?? 0)),
      0
    )

  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-6">
      <Link
        to="/runs"
        className="inline-flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" />
        Runs
      </Link>
      <PageHeader
        eyebrow={<span className="normal-case">{run.id}</span>}
        title={
          <span className="flex flex-wrap items-center gap-3">
            {run.agent}
            <RunStatusBadge status={run.status} />
          </span>
        }
        description={run.input}
      >
        <LinkTabs label="Run view">
          <LinkTab
            to="/runs/$runId"
            params={{ runId }}
            search={(p) => ({ ...p, view: undefined })}
          >
            Steps
          </LinkTab>
          <LinkTab
            to="/runs/$runId"
            params={{ runId }}
            search={(p) => ({ ...p, view: "transcript" as const })}
          >
            Transcript
          </LinkTab>
          <LinkTab
            to="/runs/$runId"
            params={{ runId }}
            search={(p) => ({ ...p, view: "raw" as const })}
          >
            Raw
          </LinkTab>
        </LinkTabs>
      </PageHeader>

      <Facts run={run} />

      {run.error && (
        <div
          role="alert"
          className="flex gap-3 rounded-alert bg-danger-muted px-4 py-3.5 text-sm"
        >
          <OctagonXIcon className="mt-px size-[18px] flex-none text-danger-foreground" />
          <div>
            <span className="font-semibold">This run failed.</span>{" "}
            <span className="font-mono text-[13px]">{run.error}</span>
          </div>
        </div>
      )}

      {view === "steps" && (
        <StepsView run={run} selected={search.step} total={total} />
      )}
      {view === "transcript" && <Transcript run={run} />}
      {view === "raw" && (
        <CodeBlock title={`${run.id}.json`} code={json(run)} />
      )}
    </div>
  )
}

function Facts({ run }: { run: Run }) {
  const facts: { label: string; value: React.ReactNode }[] = [
    {
      label: "Started",
      value: <span title={run.started_at}>{dateTime(run.started_at)}</span>,
    },
    {
      label: "Time",
      value: <span className="font-mono">{duration(run.duration_ms)}</span>,
    },
    {
      label: "Tokens",
      value: (
        <span
          className="font-mono"
          title={`${tokens(run.usage.input_tokens)} in, ${tokens(run.usage.output_tokens)} out`}
        >
          {tokens(run.usage.input_tokens + run.usage.output_tokens)}
        </span>
      ),
    },
    {
      label: "Cost",
      value: (
        <span className="font-mono">
          {cost(run.cost)}
          {run.delegated_cost !== null && (
            <span className="text-subtle-foreground">
              {" "}
              + {cost(run.delegated_cost)} delegated
            </span>
          )}
        </span>
      ),
    },
    {
      label: "Model",
      value: <span className="font-mono text-[13px]">{run.model}</span>,
    },
    { label: "Version", value: <Kbd>{run.agent_version}</Kbd> },
    { label: "Tenant", value: run.tenant },
    {
      label: "Session",
      value: run.session_id ? (
        <Link
          to="/sessions/$sessionId"
          params={{ sessionId: run.session_id }}
          className="font-mono text-[13px] underline-offset-4 hover:underline"
        >
          {run.session_id}
        </Link>
      ) : (
        <span className="text-subtle-foreground">none</span>
      ),
    },
    {
      label: "Trace",
      value: (
        <Link
          to="/traces/$traceId"
          params={{ traceId: run.trace_id }}
          className="font-mono text-[13px] underline-offset-4 hover:underline"
        >
          {run.trace_id.slice(0, 12)}
        </Link>
      ),
    },
  ]
  return (
    <Card className="py-0">
      <dl className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-x-5 gap-y-4 p-5">
        {facts.map((f) => (
          <div key={f.label} className="min-w-0">
            <dt className="eyebrow">{f.label}</dt>
            <dd className="mt-1 truncate text-sm">{f.value}</dd>
          </div>
        ))}
      </dl>
    </Card>
  )
}

function StepsView({
  run,
  selected,
  total,
}: {
  run: Run
  selected: number | undefined
  total: number
}) {
  const env = useEnv()
  const current = run.steps.find((s) => s.index === selected) ?? run.steps[0]
  const approvalId = run.steps.find(
    (s) => s.kind === "approval" && s.status === "waiting"
  )?.approval_id
  const approval = useQuery({
    ...q.approval(env, approvalId ?? ""),
    enabled: !!approvalId,
  })
  return (
    <div className="flex flex-wrap gap-5">
      <Card className="min-w-[300px] flex-[0_1_400px] self-start p-5">
        <div className="flex items-baseline justify-between">
          <h2 className="text-base">Steps</h2>
          <span className="font-mono text-xs text-subtle-foreground">
            {run.steps.length} steps · {duration(total)}
          </span>
        </div>
        <StepTimeline
          steps={run.steps}
          runId={run.id}
          selected={current?.index}
          total={total}
        />
        {approval.data && (
          <ApprovalCallout approval={approval.data} env={env} />
        )}
      </Card>
      {current && <StepDetail step={current} />}
    </div>
  )
}

function StepDetail({ step }: { step: Step }) {
  return (
    <section
      aria-label="Step detail"
      className="flex min-w-0 flex-[1_1_480px] flex-col gap-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <h2 className="text-lg">
          <span className="mr-2 font-mono text-sm text-subtle-foreground">
            #{step.index}
          </span>
          {stepTitle(step)}
        </h2>
        <span className="font-mono text-[13px] text-muted-foreground">
          {step.usage
            ? `${tokens(step.usage.input_tokens)} in · ${tokens(step.usage.output_tokens)} out · `
            : ""}
          {step.kind === "model" ? `${cost(step.cost)} · ` : ""}
          {duration(step.duration_ms)}
        </span>
      </div>
      {step.child_run_id && (
        <p className="text-sm text-muted-foreground">
          Child run{" "}
          <span className="font-mono text-[13px] text-foreground">
            {step.child_run_id}
          </span>
        </p>
      )}
      <CodeBlock title="request" code={json(step.request)} />
      <CodeBlock
        title="response"
        code={
          step.response === null ? "// no response yet" : json(step.response)
        }
      />
    </section>
  )
}

function Transcript({ run }: { run: Run }) {
  return (
    <Card className="gap-0 py-0">
      <ol>
        {run.transcript.map((m, i) => (
          <li
            key={i}
            className="grid grid-cols-[96px_1fr] gap-4 border-t px-5 py-4 first:border-t-0 max-[600px]:grid-cols-1 max-[600px]:gap-1"
          >
            <span className="pt-0.5 eyebrow">{m.role}</span>
            <p
              className={
                m.role === "tool"
                  ? "font-mono text-[13px] break-all text-muted-foreground"
                  : "text-sm leading-relaxed"
              }
            >
              {m.text}
            </p>
          </li>
        ))}
      </ol>
    </Card>
  )
}
