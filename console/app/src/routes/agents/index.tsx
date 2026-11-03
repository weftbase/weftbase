import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ShieldCheckIcon } from "lucide-react"

import { LABELS, LabelTag } from "@/components/console/labels"
import { PageHeader } from "@/components/console/page-header"
import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import { q } from "@/lib/api/queries"
import { cost, duration } from "@/lib/format"
import { useEnv } from "@/lib/search"

export const Route = createFileRoute("/agents/")({
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env } }) =>
    queryClient.ensureQueryData(q.agents(env)),
  head: () => ({ meta: [{ title: "Agents · weftbase" }] }),
  component: Agents,
})

function Agents() {
  const env = useEnv()
  const { data: agents } = useSuspenseQuery(q.agents(env))
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-4">
      <PageHeader
        title="Agents"
        description="The agents this server runs, from their manifests, with what they did today."
      />
      <div className="grid grid-cols-[repeat(auto-fill,minmax(340px,1fr))] gap-4 max-[420px]:grid-cols-1">
        {agents.map((a) => (
          <Card
            key={a.name}
            className="relative gap-4 px-[18px] transition-colors hover:border-ring/30"
          >
            <div className="flex flex-col gap-2.5">
              <div className="min-w-0">
                <Link
                  to="/agents/$name"
                  params={{ name: a.name }}
                  className="text-base font-semibold after:absolute after:inset-0 after:rounded-card focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-ring"
                >
                  {a.name}
                </Link>
                <div className="mt-0.5 truncate font-mono text-xs text-subtle-foreground">
                  {a.model}
                </div>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {LABELS.filter((l) => a.labels[l]).map((l) => (
                  <LabelTag key={l} label={l} version={a.labels[l]} />
                ))}
              </div>
            </div>
            <p className="text-[13px] leading-relaxed text-muted-foreground">
              {a.description}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {a.tools.map((t) => (
                <Kbd key={t} className="gap-1">
                  {a.approval_tools.includes(t) && (
                    <ShieldCheckIcon
                      className="size-3 text-warning-foreground"
                      aria-label="needs approval"
                    />
                  )}
                  {t}
                </Kbd>
              ))}
            </div>
            <dl className="grid grid-cols-4 gap-3 border-t pt-4 text-[13px]">
              <div>
                <dt className="text-subtle-foreground">Runs</dt>
                <dd className="font-mono">{a.stats.runs_24h}</dd>
              </div>
              <div>
                <dt className="text-subtle-foreground">Failed</dt>
                <dd className="font-mono">{a.stats.failed_24h}</dd>
              </div>
              <div>
                <dt className="text-subtle-foreground">p50</dt>
                <dd className="font-mono">{duration(a.stats.p50_ms)}</dd>
              </div>
              <div>
                <dt className="text-subtle-foreground">Cost</dt>
                <dd className="font-mono">{cost(a.stats.cost_24h)}</dd>
              </div>
            </dl>
          </Card>
        ))}
      </div>
    </div>
  )
}
