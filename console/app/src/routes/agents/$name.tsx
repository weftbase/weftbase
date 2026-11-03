import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ArrowLeftIcon, ShieldCheckIcon } from "lucide-react"

import { CodeBlock } from "@/components/console/code-block"
import { LABELS, LabelTag } from "@/components/console/labels"
import { LinkTab, LinkTabs } from "@/components/console/link-tabs"
import { PageHeader } from "@/components/console/page-header"
import { StatCard } from "@/components/console/stat-card"
import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { q } from "@/lib/api/queries"
import type { Agent } from "@/lib/api/types"
import { ago, cost, dateTime, duration, json, tokens } from "@/lib/format"
import { ensure } from "@/lib/load"
import { useEnv, validateAgent } from "@/lib/search"

export const Route = createFileRoute("/agents/$name")({
  validateSearch: validateAgent,
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env }, params }) =>
    ensure(queryClient, q.agent(env, params.name)),
  head: ({ params }) => ({
    meta: [{ title: `${params.name} · Agents · weftbase` }],
  }),
  component: AgentPage,
})

function AgentPage() {
  const { name } = Route.useParams()
  const { tab } = Route.useSearch()
  const env = useEnv()
  const { data: a } = useSuspenseQuery(q.agent(env, name))
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-6">
      <Link
        to="/agents"
        className="inline-flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" />
        Agents
      </Link>
      <PageHeader eyebrow="Agent" title={a.name} description={a.description}>
        <LinkTabs label="Agent view">
          <LinkTab
            to="/agents/$name"
            params={{ name }}
            search={(p) => ({ ...p, tab: undefined })}
          >
            Overview
          </LinkTab>
          <LinkTab
            to="/agents/$name"
            params={{ name }}
            search={(p) => ({ ...p, tab: "versions" as const })}
          >
            Versions
          </LinkTab>
        </LinkTabs>
      </PageHeader>
      {(tab ?? "overview") === "overview" ? (
        <Overview agent={a} />
      ) : (
        <Versions agent={a} />
      )}
    </div>
  )
}

function Overview({ agent: a }: { agent: Agent }) {
  return (
    <>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-4">
        <StatCard label="Runs, 24 h" value={a.stats.runs_24h} />
        <StatCard label="Failed, 24 h" value={a.stats.failed_24h} />
        <StatCard label="Median time" value={duration(a.stats.p50_ms)} />
        <StatCard label="Cost, 24 h" value={cost(a.stats.cost_24h)} />
      </div>
      <div className="flex flex-wrap gap-5">
        <Card className="min-w-[300px] flex-[1_1_420px] gap-5 p-5">
          <h2 className="text-base">Manifest</h2>
          <dl className="grid gap-4 text-sm">
            <Row label="Model">
              <span className="font-mono text-[13px]">{a.model}</span>
            </Row>
            <Row label="Tools">
              <span className="flex flex-wrap gap-1.5">
                {a.tools.map((t) => (
                  <Kbd key={t}>{t}</Kbd>
                ))}
              </span>
            </Row>
            <Row label="Needs approval">
              {a.approval_tools.length ? (
                <span className="flex flex-wrap gap-1.5">
                  {a.approval_tools.map((t) => (
                    <Kbd key={t} className="gap-1">
                      <ShieldCheckIcon
                        className="size-3 text-warning-foreground"
                        aria-hidden
                      />
                      {t}
                    </Kbd>
                  ))}
                </span>
              ) : (
                <span className="text-subtle-foreground">
                  No tool pauses for a person
                </span>
              )}
            </Row>
            <Row label="Middleware">
              <span className="font-mono text-[13px]">
                {a.middleware.join(" › ")}
              </span>
            </Row>
            <Row label="Budget">
              {a.budget ? (
                <span className="font-mono text-[13px]">
                  {tokens(a.budget.max_tokens)} tokens,{" "}
                  {cost(a.budget.max_cost)} per run
                </span>
              ) : (
                <span className="text-subtle-foreground">None</span>
              )}
            </Row>
            <Row label="Labels">
              <span className="flex flex-wrap gap-1.5">
                {LABELS.filter((l) => a.labels[l]).map((l) => (
                  <LabelTag key={l} label={l} version={a.labels[l]} />
                ))}
              </span>
            </Row>
          </dl>
        </Card>
        <section className="flex min-w-[300px] flex-[1_1_420px] flex-col gap-4">
          <CodeBlock
            title="agent.json"
            code={json({
              name: a.name,
              model: a.model,
              tools: a.tools,
              approval: a.approval_tools,
              middleware: a.middleware,
              budget: a.budget,
            })}
          />
          <Link
            to="/runs"
            search={{ agent: a.name }}
            className="self-start text-sm font-medium underline underline-offset-4"
          >
            See runs of {a.name}
          </Link>
        </section>
      </div>
    </>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="grid grid-cols-[132px_1fr] items-start gap-3 max-[520px]:grid-cols-1 max-[520px]:gap-1">
      <dt className="pt-0.5 text-[13px] text-subtle-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  )
}

function Versions({ agent: a }: { agent: Agent }) {
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        Labels point at a version. Promoting a version to a label is planned;
        this view is read-only for now.
      </p>
      <Card className="overflow-x-auto py-0">
        <Table className="min-w-[720px]">
          <TableHeader>
            <TableRow>
              <TableHead>Version</TableHead>
              <TableHead>Labels</TableHead>
              <TableHead>Manifest</TableHead>
              <TableHead>Note</TableHead>
              <TableHead className="text-right">Created</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {a.versions.map((v) => (
              <TableRow key={v.version}>
                <TableCell>
                  <Kbd>{v.version}</Kbd>
                </TableCell>
                <TableCell>
                  <span className="flex gap-1.5">
                    {v.labels.length ? (
                      v.labels.map((l) => <LabelTag key={l} label={l} />)
                    ) : (
                      <span className="text-[13px] text-subtle-foreground">
                        none
                      </span>
                    )}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-[13px] text-muted-foreground">
                  {v.manifest_hash}
                </TableCell>
                <TableCell className="text-[13px]">{v.note}</TableCell>
                <TableCell
                  className="text-right text-[13px] text-subtle-foreground"
                  title={dateTime(v.created_at)}
                >
                  {ago(v.created_at)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
}
