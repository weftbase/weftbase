import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import {
  ArrowLeftIcon,
  ArchiveIcon,
  GitForkIcon,
  MessageSquareIcon,
  ShieldCheckIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { PageHeader } from "@/components/console/page-header"
import { StatCard } from "@/components/console/stat-card"
import { RunStatusBadge, SessionStateBadge } from "@/components/console/status"
import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import { q } from "@/lib/api/queries"
import type { SessionEntry } from "@/lib/api/types"
import { ago, cost, dateTime, tokens } from "@/lib/format"
import { ensure } from "@/lib/load"
import { useEnv, validateSession } from "@/lib/search"
import { cn } from "@/lib/utils"

export const Route = createFileRoute("/sessions/$sessionId")({
  validateSearch: validateSession,
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env }, params }) =>
    ensure(queryClient, q.session(env, params.sessionId)),
  head: ({ params }) => ({
    meta: [{ title: `${params.sessionId} · Sessions · weftbase` }],
  }),
  component: SessionPage,
})

const ENTRY_ICON: Record<SessionEntry["kind"], LucideIcon> = {
  turn: MessageSquareIcon,
  approval: ShieldCheckIcon,
  compaction: ArchiveIcon,
  fork: GitForkIcon,
}

function SessionPage() {
  const { sessionId } = Route.useParams()
  const { turn } = Route.useSearch()
  const env = useEnv()
  const { data: s } = useSuspenseQuery(q.session(env, sessionId))
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-6">
      <Link
        to="/sessions"
        className="inline-flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" />
        Sessions
      </Link>
      <PageHeader
        eyebrow={<span className="normal-case">{s.id}</span>}
        title={
          <span className="flex flex-wrap items-center gap-3">
            {s.agent}
            <SessionStateBadge state={s.state} />
          </span>
        }
        description={`Started ${ago(s.started_at)}, tenant ${s.tenant}.`}
      />
      <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-4">
        <StatCard label="Turns" value={s.turns} />
        <StatCard
          label="Tokens"
          value={tokens(s.usage.input_tokens + s.usage.output_tokens)}
        />
        <StatCard label="Cost" value={cost(s.cost)} />
        <StatCard
          label="Last activity"
          value={ago(s.last_activity_at)}
          meta={dateTime(s.last_activity_at)}
        />
      </div>
      <div className="flex flex-wrap gap-5">
        <section className="flex min-w-0 flex-[1_1_560px] flex-col gap-4">
          <h2 className="text-lg">Turns</h2>
          <Card className="gap-0 py-0">
            <ol>
              {s.turn_list.map((t) => (
                <li key={t.index} className="border-t first:border-t-0">
                  <Link
                    to="/sessions/$sessionId"
                    params={{ sessionId }}
                    search={(p) => ({ ...p, turn: t.index })}
                    className={cn(
                      "grid grid-cols-[40px_1fr_auto] items-start gap-3 px-5 py-4 hover:bg-muted/50",
                      turn === t.index && "bg-table-row-selected"
                    )}
                    aria-current={turn === t.index ? "true" : undefined}
                  >
                    <span className="font-mono text-[13px] text-subtle-foreground">
                      #{t.index + 1}
                    </span>
                    <span className="min-w-0">
                      <span className="block text-sm">{t.input}</span>
                      {t.output && (
                        <span className="mt-1 block text-[13px] text-muted-foreground">
                          {t.output}
                        </span>
                      )}
                      <span className="mt-2 flex items-center gap-2 text-xs text-subtle-foreground">
                        <span className="font-mono">{t.run_id}</span>
                        <Kbd size="sm">{t.agent_version}</Kbd>
                      </span>
                    </span>
                    <RunStatusBadge status={t.status} short />
                  </Link>
                  {turn === t.index && (
                    <div className="px-5 pb-4 pl-[72px]">
                      <Link
                        to="/runs/$runId"
                        params={{ runId: t.run_id }}
                        className="text-sm font-medium underline underline-offset-4"
                      >
                        Open run {t.run_id}
                      </Link>
                    </div>
                  )}
                </li>
              ))}
            </ol>
          </Card>
        </section>
        <section className="flex min-w-[300px] flex-[0_1_380px] flex-col gap-4">
          <h2 className="text-lg">Timeline</h2>
          <Card className="p-5">
            <ol className="flex flex-col">
              {s.timeline.map((e, i) => {
                const Icon = ENTRY_ICON[e.kind]
                return (
                  <li
                    key={i}
                    className="relative flex gap-3 pb-[18px] last:pb-0"
                  >
                    {i < s.timeline.length - 1 && (
                      <span
                        aria-hidden
                        className="absolute top-[34px] -bottom-1.5 left-[15px] w-px bg-border"
                      />
                    )}
                    <span
                      className={cn(
                        "flex size-[31px] flex-none items-center justify-center rounded-input border bg-muted text-muted-foreground",
                        e.kind === "approval" && "text-warning-foreground"
                      )}
                    >
                      <Icon className="size-4" />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="flex justify-between gap-2">
                        <span className="text-sm font-medium capitalize">
                          {e.kind}
                        </span>
                        <span className="font-mono text-xs text-subtle-foreground">
                          {ago(e.at)}
                        </span>
                      </span>
                      <span className="mt-0.5 block text-[13px] text-muted-foreground">
                        {e.text}
                      </span>
                    </span>
                  </li>
                )
              })}
            </ol>
          </Card>
        </section>
      </div>
    </div>
  )
}
