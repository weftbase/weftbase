import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ArrowRightIcon, ShieldCheckIcon } from "lucide-react"

import { PageHeader } from "@/components/console/page-header"
import { RunsTable } from "@/components/console/runs-table"
import { StatCard } from "@/components/console/stat-card"
import { RunStatusBadge } from "@/components/console/status"
import { Card } from "@/components/ui/card"
import { q } from "@/lib/api/queries"
import type { RunStatus } from "@/lib/api/types"
import { cost, tokens } from "@/lib/format"
import { useEnv } from "@/lib/search"

export const Route = createFileRoute("/")({
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env } }) =>
    Promise.all([
      queryClient.ensureQueryData(q.stats(env)),
      queryClient.ensureQueryData(q.runs(env, { limit: 6 })),
      queryClient.ensureQueryData(q.approvals(env, { status: "pending" })),
    ]),
  head: () => ({ meta: [{ title: "Overview · weftbase" }] }),
  component: Overview,
})

const ORDER: RunStatus[] = [
  "succeeded",
  "running",
  "waiting",
  "failed",
  "queued",
  "cancelled",
]

function Overview() {
  const env = useEnv()
  const { data: stats } = useSuspenseQuery(q.stats(env))
  const { data: recent } = useSuspenseQuery(q.runs(env, { limit: 6 }))
  const { data: pending } = useSuspenseQuery(
    q.approvals(env, { status: "pending" })
  )
  const total = ORDER.reduce((a, s) => a + stats.runs_by_status[s], 0)
  const failed = stats.runs_by_status.failed

  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-11">
      <section className="flex flex-col gap-4">
        <PageHeader
          title="Overview"
          description="What your agents did in the last 24 hours."
        />
        <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-4">
          <StatCard
            label="Runs"
            value={tokens(total)}
            meta={`${stats.runs_by_status.running} running now`}
          />
          <StatCard
            label="Failed"
            value={tokens(failed)}
            meta={
              total
                ? `${((failed / total) * 100).toFixed(1)}% of runs`
                : "No runs yet"
            }
          />
          <StatCard
            label="Cost today"
            value={cost(stats.cost_today)}
            meta={`${tokens(stats.tokens_today)} tokens`}
          />
          <StatCard
            label="Waiting for approval"
            value={tokens(stats.pending_approvals)}
            meta={
              stats.pending_approvals
                ? "A person needs to act"
                : "Nothing waiting"
            }
          />
        </div>
      </section>

      <div className="flex flex-wrap gap-5">
        <section className="flex min-w-0 flex-[1_1_640px] flex-col gap-4">
          <div className="flex items-end justify-between gap-4">
            <h2 className="text-lg">Recent runs</h2>
            <Link
              to="/runs"
              className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
            >
              All runs
              <ArrowRightIcon className="size-4" />
            </Link>
          </div>
          <Card className="overflow-x-auto py-0">
            <RunsTable runs={recent.items} />
          </Card>
        </section>

        <div className="flex min-w-[300px] flex-[0_1_360px] flex-col gap-5">
          {pending.items.length > 0 && (
            <div className="flex gap-3 rounded-alert bg-warning-muted px-4 py-3.5 text-sm">
              <ShieldCheckIcon className="mt-px size-[18px] flex-none text-warning-foreground" />
              <div>
                <span className="font-semibold">
                  {pending.items.length}{" "}
                  {pending.items.length === 1 ? "action is" : "actions are"}{" "}
                  waiting for a person.
                </span>{" "}
                <Link
                  to="/approvals"
                  className="font-medium text-warning-foreground underline underline-offset-4"
                >
                  Review now
                </Link>
              </div>
            </div>
          )}
          <Card className="gap-4 px-5 py-5">
            <h2 className="text-base">Runs by status</h2>
            <ul className="flex flex-col gap-2.5">
              {ORDER.filter((s) => stats.runs_by_status[s] > 0).map((s) => (
                <li key={s} className="flex items-center justify-between gap-3">
                  <Link
                    to="/runs"
                    search={{ status: s }}
                    className="rounded-full"
                  >
                    <RunStatusBadge status={s} short />
                  </Link>
                  <span className="font-mono text-[13px] text-muted-foreground">
                    {stats.runs_by_status[s]}
                  </span>
                </li>
              ))}
            </ul>
          </Card>
          <Card className="gap-4 px-5 py-5">
            <h2 className="text-base">Failures by error class</h2>
            {stats.failures_by_error_class.length === 0 ? (
              <p className="text-[13px] text-muted-foreground">
                No failures in the last 24 hours.
              </p>
            ) : (
              <ul className="flex flex-col gap-2.5">
                {stats.failures_by_error_class.map((f) => (
                  <li
                    key={f.error_class}
                    className="flex items-center justify-between gap-3"
                  >
                    <Link
                      to="/runs"
                      search={{ status: "failed", error: f.error_class }}
                      className="truncate font-mono text-[13px] hover:underline"
                    >
                      {f.error_class}
                    </Link>
                    <span className="font-mono text-[13px] text-muted-foreground">
                      {f.count}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}
