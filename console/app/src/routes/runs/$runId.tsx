import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { DownloadIcon } from "lucide-react"

import { CopyId } from "@/components/console/copy-id"
import { ErrorState, Rebuilding } from "@/components/console/empty-state"
import { JsonView } from "@/components/console/json-view"
import { Page, PageHeader } from "@/components/console/page-header"
import { useNow } from "@/components/console/relative-time"
import { StatStrip } from "@/components/console/stat-strip"
import { LivePill, StatusBadge } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { api } from "@/lib/api/http"
import { useRunLive } from "@/lib/api/live"
import { q } from "@/lib/api/queries"
import { between, cost, count, duration, tokens } from "@/lib/format"
import { displayStatus } from "@/lib/api/status"
import { validateRun } from "@/lib/search"

// The run page. A stub on the new foundation: the header line, the stat
// strip and the run's row, live. The timeline (trace waterfall, replay),
// transcript, logs and raw tabs are built on top of this.
export const Route = createFileRoute("/runs/$runId")({
  validateSearch: validateRun,
  head: ({ params }) => ({
    meta: [{ title: `${params.runId.slice(-8)} · Runs · weftbase` }],
  }),
  component: RunPage,
})

function RunPage() {
  const { runId } = Route.useParams()
  const run = useQuery(q.run(runId))
  const running = run.data?.status === "running"
  const live = useRunLive(runId, { enabled: running })
  const now = useNow()

  if (run.error)
    return (
      <Page
        header={
          <PageHeader title="Run" id={<CopyId id={runId} label="run id" />} />
        }
      >
        <ErrorState error={run.error} what="The run" />
      </Page>
    )
  const r = run.data
  if (!r) return <Page header={<PageHeader title="Run" />}>{null}</Page>

  return (
    <Page
      header={
        <PageHeader
          title={r.agent || "Run"}
          status={<StatusBadge state={displayStatus(r)} />}
          id={<CopyId id={r.id} label="run id" />}
          meta={
            <>
              {running && <LivePill state={live} />}
              {r.session_id && (
                <Link
                  to="/sessions/$sessionId"
                  params={{ sessionId: r.session_id }}
                  className="hover:text-foreground hover:underline"
                >
                  Session, turn {r.turn}
                </Link>
              )}
            </>
          }
          actions={
            <Button asChild size="sm" variant="secondary">
              <a href={api.exportUrl(r.id)} download>
                <DownloadIcon />
                Export
              </a>
            </Button>
          }
        />
      }
      className="flex flex-col gap-4"
    >
      <StatStrip
        stats={[
          {
            label: "Duration",
            value:
              r.finished || running
                ? duration(between(r.started, r.finished, now))
                : "—",
          },
          { label: "Cost", value: cost(r), title: r.cost_reason },
          {
            label: "Tokens",
            value: tokens(r.usage.input_tokens + r.usage.output_tokens),
            hint: `${tokens(r.usage.input_tokens)} in · ${tokens(r.usage.output_tokens)} out`,
          },
          { label: "Steps", value: count(r.steps) },
          { label: "Subagent runs", value: count(r.children.length) },
          {
            label: "Tool errors",
            value: count(r.tool_errors ?? 0),
            tone: r.tool_errors ? "danger" : "default",
          },
        ]}
      />
      {r.err && (
        <p className="rounded-alert border border-danger/30 bg-danger-muted px-3 py-2 font-mono text-xs text-danger-foreground">
          {r.err}
        </p>
      )}
      <Rebuilding what="The run's timeline" />
      <JsonView value={r} depth={1} />
    </Page>
  )
}
