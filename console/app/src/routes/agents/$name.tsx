import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"

import { Capabilities } from "@/components/console/agent/capabilities"
import { labelVersions } from "@/components/console/agent/inventory"
import { LabelChip, Versions } from "@/components/console/agent/versions"
import { DataTable } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader, Section } from "@/components/console/page-header"
import {
  RUN_COLUMNS,
  RUN_COLUMNS_OF_AGENT,
} from "@/components/console/run-columns"
import { isNotFound } from "@/lib/api/errors"
import { q } from "@/lib/api/queries"
import type { AgentDetail, RunRow } from "@/lib/api/types"
import { count, percent } from "@/lib/format"
import { validateAgent } from "@/lib/search"

// One agent, in the order of the questions asked about it: what is it
// (the header line), what can it do (Capabilities of one version), what
// versions exist and where the labels point (Versions), and what it has
// been doing (Activity).
export const Route = createFileRoute("/agents/$name")({
  validateSearch: validateAgent,
  head: ({ params }) => ({
    meta: [{ title: `${params.name} · Agents · weftbase` }],
  }),
  component: AgentPage,
})

const runLink = (r: RunRow) =>
  ({ to: "/runs/$runId", params: { runId: r.id } }) as const

function AgentPage() {
  const { name } = Route.useParams()
  const search = Route.useSearch()
  const agent = useQuery(q.agent(name))
  // An agent seen in runs only (a subagent) has no registry entry.
  const unregistered = isNotFound(agent.error)
  const a = agent.data

  if (agent.error && !unregistered)
    return (
      <Page header={<PageHeader title={name} />}>
        <ErrorState error={agent.error} what="This agent" />
      </Page>
    )

  const version =
    a?.version_list.find((v) => v.number === search.v) ?? a?.newest ?? undefined

  return (
    <Page
      header={
        <PageHeader
          title={
            <span className="flex items-center gap-1.5">
              <Link
                to="/agents"
                className="font-normal text-muted-foreground hover:text-foreground"
              >
                Agents
              </Link>
              <span className="font-normal text-subtle-foreground">/</span>
              {name}
            </span>
          }
          meta={a ? <HeaderFacts agent={a} /> : undefined}
        />
      }
      className="flex flex-col gap-4"
    >
      {unregistered ? (
        <p className="rounded-alert border bg-card px-3 py-2 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">
            {name} is not registered with this server.
          </span>{" "}
          It was seen in runs only (as a subagent of another agent, or in traces
          an app sent), so it has no versions, labels or manifest here. Its runs
          are below.
        </p>
      ) : !a ? (
        <div className="rounded-alert border">
          <LoadingRows rows={6} />
        </div>
      ) : (
        <>
          <Section
            title="Capabilities"
            action={
              version && (
                <span>
                  of{" "}
                  <span className="font-mono text-foreground">
                    v{version.number}
                  </span>
                  {version.id === a.newest?.id ? ", the newest" : ""}
                  {(version.labels ?? []).length > 0 &&
                    ` (${(version.labels ?? []).join(", ")})`}
                </span>
              )
            }
          >
            {version ? (
              <Capabilities version={version} />
            ) : (
              <div className="rounded-alert border bg-card">
                <EmptyState title="No versions yet">
                  A version appears when a build that serves this agent
                  registers.
                </EmptyState>
              </div>
            )}
          </Section>
          {a.version_list.length > 0 && (
            <Section title="Versions">
              <Versions agent={a} shown={version?.id} />
            </Section>
          )}
        </>
      )}
      <Activity name={name} all={unregistered} />
    </Page>
  )
}

function HeaderFacts({ agent }: { agent: AgentDetail }) {
  const s = agent.stats
  const finished = s.succeeded + s.failed
  return (
    <>
      {agent.model.name && <span className="truncate">{agent.model.name}</span>}
      {agent.placement && <span>{agent.placement}</span>}
      {labelVersions(agent).map((p) => (
        <span key={p.label} className="flex flex-none items-center gap-1">
          <LabelChip label={p.label} />
          {p.number && <span className="font-mono">v{p.number}</span>}
        </span>
      ))}
      <span className="truncate max-[1100px]:hidden">
        {count(s.runs)} {s.runs === 1 ? "run" : "runs"} in 24h
        {finished > 0 && ` · ${percent(s.failure_rate)} failed`}
      </span>
    </>
  )
}

function Activity({ name, all }: { name: string; all: boolean }) {
  // A subagent's runs are child runs: ask for every run, not top-level.
  const runs = useQuery(
    q.runsPage({ agent: name, limit: 10, parent: all ? "*" : undefined })
  )
  return (
    <Section
      title="Activity"
      action={
        <Link
          to="/runs"
          search={{ agent: name, sub: all ? true : undefined }}
          className="hover:text-foreground hover:underline"
        >
          All runs of {name}
        </Link>
      }
    >
      {runs.error ? (
        <div className="rounded-alert border">
          <ErrorState error={runs.error} what="The runs" />
        </div>
      ) : !runs.data ? (
        <div className="rounded-alert border">
          <LoadingRows rows={4} />
        </div>
      ) : runs.data.runs.length === 0 ? (
        <div className="rounded-alert border bg-card">
          <EmptyState title="No runs yet">
            Runs of {name} appear here as they start.
          </EmptyState>
        </div>
      ) : (
        <DataTable
          label={`Recent runs of ${name}`}
          bordered
          stickyHeader={false}
          columns={all ? RUN_COLUMNS : RUN_COLUMNS_OF_AGENT}
          rows={runs.data.runs.slice(0, 10)}
          rowKey={(r) => r.id}
          rowLink={runLink}
        />
      )}
    </Section>
  )
}
