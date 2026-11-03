import { useQuery, useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router"
import { AlignLeftIcon, ArrowUpRightIcon } from "lucide-react"
import * as React from "react"

import { ApprovalCallout } from "@/components/console/approval-actions"
import { EmptyState } from "@/components/console/empty-state"
import {
  ClearFilters,
  FilterSelect,
  SearchFilter,
} from "@/components/console/filters"
import { LinkTab, LinkTabs } from "@/components/console/link-tabs"
import { PageHeader } from "@/components/console/page-header"
import { RunsTable } from "@/components/console/runs-table"
import { RunStatusBadge } from "@/components/console/status"
import { StepTimeline } from "@/components/console/step-timeline"
import { Card } from "@/components/ui/card"
import { q } from "@/lib/api/queries"
import type { RunFilters } from "@/lib/api/types"
import { cost, duration, tokens } from "@/lib/format"
import { useEnv, validateRuns } from "@/lib/search"
import type { RunsSearch } from "@/lib/search"

export const Route = createFileRoute("/runs/")({
  validateSearch: validateRuns,
  loaderDeps: ({ search }) => ({
    env: search.env ?? "live",
    f: toFilters(search),
  }),
  loader: ({ context: { queryClient }, deps: { env, f } }) =>
    queryClient.ensureQueryData(q.runs(env, f)),
  head: () => ({ meta: [{ title: "Runs · weftbase" }] }),
  component: Runs,
})

/** toFilters maps the URL to the API's filter names. */
export function toFilters(s: RunsSearch): RunFilters {
  return {
    status: s.status,
    agent: s.agent,
    model: s.model,
    tool: s.tool,
    error_class: s.error,
    tenant: s.tenant,
    q: s.q,
  }
}

const uniq = (xs: (string | null)[]) =>
  [...new Set(xs.filter((x): x is string => !!x))].sort()

function Runs() {
  const search = Route.useSearch()
  const env = useEnv()
  const navigate = useNavigate({ from: Route.fullPath })
  const { data } = useSuspenseQuery(q.runs(env, toFilters(search)))
  const all = useQuery(q.runs(env, {}))
  const set = React.useCallback(
    (patch: Partial<RunsSearch>) =>
      void navigate({ search: (prev) => ({ ...prev, ...patch }) }),
    [navigate]
  )
  const setQ = React.useCallback(
    (v: string | undefined) => set({ q: v }),
    [set]
  )
  const opts = React.useMemo(() => {
    const items = all.data?.items ?? []
    return {
      agent: uniq(items.map((r) => r.agent)),
      model: uniq(items.map((r) => r.model)),
      tool: uniq(items.flatMap((r) => r.tools)),
      tenant: uniq(items.map((r) => r.tenant)),
      error: uniq(items.map((r) => r.error_class)),
    }
  }, [all.data])
  const filtered = !!(
    search.agent ||
    search.model ||
    search.tool ||
    search.tenant ||
    search.error ||
    search.q
  )
  const selected = search.run ?? data.items[0]?.id

  return (
    <div className="flex flex-wrap gap-5">
      <section className="flex min-w-0 flex-[1_1_640px] flex-col gap-4">
        <PageHeader
          title="Runs"
          description="Every agent run on this server, newest first."
        >
          <LinkTabs label="Run status">
            <LinkTab to="/runs" search={(p) => ({ ...p, status: undefined })}>
              All
            </LinkTab>
            <LinkTab
              to="/runs"
              search={(p) => ({ ...p, status: "running" as const })}
            >
              Running
            </LinkTab>
            <LinkTab
              to="/runs"
              search={(p) => ({ ...p, status: "waiting" as const })}
            >
              Waiting
            </LinkTab>
            <LinkTab
              to="/runs"
              search={(p) => ({ ...p, status: "failed" as const })}
            >
              Failed
            </LinkTab>
          </LinkTabs>
        </PageHeader>
        <div
          className="flex flex-wrap items-center gap-2.5"
          role="group"
          aria-label="Filters"
        >
          <SearchFilter
            value={search.q}
            onChange={setQ}
            placeholder="Filter by id, agent or model"
          />
          <FilterSelect
            label="Agent"
            value={search.agent}
            options={opts.agent}
            onChange={(v) => set({ agent: v })}
          />
          <FilterSelect
            label="Model"
            value={search.model}
            options={opts.model}
            onChange={(v) => set({ model: v })}
          />
          <FilterSelect
            label="Tool"
            value={search.tool}
            options={opts.tool}
            onChange={(v) => set({ tool: v })}
          />
          <FilterSelect
            label="Tenant"
            value={search.tenant}
            options={opts.tenant}
            onChange={(v) => set({ tenant: v })}
          />
          <FilterSelect
            label="Error class"
            value={search.error}
            options={opts.error}
            onChange={(v) => set({ error: v })}
          />
          <ClearFilters
            shown={filtered}
            onClear={() =>
              set({
                agent: undefined,
                model: undefined,
                tool: undefined,
                tenant: undefined,
                error: undefined,
                q: undefined,
              })
            }
          />
        </div>
        {data.items.length === 0 ? (
          <EmptyState icon={AlignLeftIcon} title="No runs match these filters">
            Clear a filter, or pick another status tab.
          </EmptyState>
        ) : (
          <Card className="overflow-x-auto py-0">
            <RunsTable runs={data.items} selected={selected} select />
          </Card>
        )}
        <p className="text-xs text-subtle-foreground">
          {data.total} {data.total === 1 ? "run" : "runs"}
        </p>
      </section>
      {selected && <RunCard id={selected} />}
    </div>
  )
}

/** RunCard is the artboard's right-hand detail card: the compact run. */
function RunCard({ id }: { id: string }) {
  const env = useEnv()
  const run = useQuery(q.run(env, id))
  const approvalId = run.data?.steps.find(
    (s) => s.kind === "approval" && s.status === "waiting"
  )?.approval_id
  const approval = useQuery({
    ...q.approval(env, approvalId ?? ""),
    enabled: !!approvalId,
  })
  if (!run.data)
    return (
      <Card aria-busy className="min-h-64 min-w-[300px] flex-[0_1_360px]" />
    )
  const r = run.data
  return (
    <aside
      aria-label="Run detail"
      className="min-w-[300px] flex-[0_1_360px] self-start"
    >
      <Card className="gap-[18px] p-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="font-mono text-xs text-subtle-foreground">
              {r.id}
            </div>
            <div className="mt-1 truncate text-lg font-semibold">{r.agent}</div>
          </div>
          <RunStatusBadge status={r.status} short />
        </div>
        <dl className="grid grid-cols-3 gap-3 text-[13px]">
          <div>
            <dt className="text-subtle-foreground">Time</dt>
            <dd className="font-mono">{duration(r.duration_ms)}</dd>
          </div>
          <div>
            <dt className="text-subtle-foreground">Tokens</dt>
            <dd className="font-mono">
              {tokens(r.usage.input_tokens + r.usage.output_tokens)}
            </dd>
          </div>
          <div>
            <dt className="text-subtle-foreground">Cost</dt>
            <dd className="font-mono">{cost(r.cost)}</dd>
          </div>
        </dl>
        <StepTimeline steps={r.steps} />
        {approval.data && (
          <ApprovalCallout approval={approval.data} env={env} />
        )}
        <Link
          to="/runs/$runId"
          params={{ runId: r.id }}
          className="inline-flex items-center gap-1.5 self-start text-sm font-medium hover:underline"
        >
          Open run
          <ArrowUpRightIcon className="size-4" />
        </Link>
      </Card>
    </aside>
  )
}
