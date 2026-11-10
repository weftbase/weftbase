import { useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { InfoIcon } from "lucide-react"
import * as React from "react"

import { CopyId } from "@/components/console/copy-id"
import { ErrorState } from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { SpanDetail } from "@/components/console/run/span-detail"
import { SplitPane } from "@/components/console/split-pane"
import {
  TraceWaterfall,
  useTraceView,
} from "@/components/console/trace-waterfall"
import type { TraceInput } from "@/components/console/trace-waterfall"
import { q } from "@/lib/api/queries"
import { useSetSearch, validateTrace } from "@/lib/search"
import type { TraceSearch } from "@/lib/search"

// A raw trace: every span of one trace id, whether or not a weft run is
// in it. The same waterfall as a run's timeline, with neutral rows for
// stock spans; a span's detail is its timing and attributes. Reached
// from the Traces tab of Runs and from a run's Details ("Open whole
// trace").
export const Route = createFileRoute("/traces/$traceId")({
  validateSearch: validateTrace,
  head: () => ({ meta: [{ title: "Trace · weftbase" }] }),
  component: TracePage,
})

const NONE: TraceInput = { spans: [] }

function TracePage() {
  const { traceId } = Route.useParams()
  const search = Route.useSearch()
  const set = useSetSearch<TraceSearch>(Route.fullPath)
  const trace = useQuery(q.trace(traceId))
  const input = React.useMemo<TraceInput>(
    () => (trace.data ? { spans: trace.data.spans } : NONE),
    [trace.data]
  )
  const view = useTraceView(input)
  const node = search.span ? view.model.byId.get(search.span) : undefined
  const select = (id: string | undefined) => set({ span: id })
  const runs = trace.data?.run_ids ?? []

  return (
    <Page
      header={
        <PageHeader
          title="Trace"
          id={<CopyId id={traceId} label="trace id" />}
        />
      }
      className="flex flex-col gap-3 overflow-hidden"
    >
      {trace.error ? (
        <ErrorState error={trace.error} what="The trace" />
      ) : trace.data ? (
        <>
          <p
            className="flex flex-none items-start gap-1.5 text-xs text-muted-foreground"
            title={
              runs.length === 0 && trace.data.not_run?.fix
                ? `To get a run page: ${trace.data.not_run.fix}.`
                : undefined
            }
          >
            <InfoIcon className="mt-px size-3.5 flex-none text-subtle-foreground" />
            {runs.length > 0 ? (
              <span>
                Every span of this trace as recorded; messages and tool
                arguments are on the page of{" "}
                {runs.length === 1 ? "its run" : "its runs"}:{" "}
                {runs.slice(0, 6).map((id, i) => (
                  <React.Fragment key={id}>
                    {i > 0 && ", "}
                    <Link
                      to="/runs/$runId"
                      params={{ runId: id }}
                      className="font-mono text-foreground hover:underline"
                    >
                      {id}
                    </Link>
                  </React.Fragment>
                ))}
                {runs.length > 6 && ` and ${runs.length - 6} more`}.
              </span>
            ) : (
              <span>
                No agent run was recorded in this trace, so there are no
                messages or tool arguments to show: only its spans, as they
                arrived.
              </span>
            )}
          </p>
          <div
            className="min-h-0 flex-1"
            onKeyDown={(e) => {
              if (e.key === "Escape" && search.span) select(undefined)
            }}
          >
            <SplitPane
              id="trace"
              gutter
              defaultWidth={440}
              minBeside={320}
              detail={
                node && (
                  <SpanDetail
                    key={node.id}
                    node={node}
                    model={view.model}
                    onSelect={select}
                    onClose={() => select(undefined)}
                  />
                )
              }
            >
              <TraceWaterfall
                id="trace"
                view={view}
                strip
                selected={search.span}
                onSelect={select}
                autoFocus
              />
            </SplitPane>
          </div>
        </>
      ) : null}
    </Page>
  )
}
