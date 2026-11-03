import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ArrowLeftIcon } from "lucide-react"

import { CodeBlock } from "@/components/console/code-block"
import { PageHeader } from "@/components/console/page-header"
import { Badge } from "@/components/ui/badge"
import { Card } from "@/components/ui/card"
import { q } from "@/lib/api/queries"
import type { Span } from "@/lib/api/types"
import { dateTime, duration, json } from "@/lib/format"
import { ensure } from "@/lib/load"
import { useEnv, validateTrace } from "@/lib/search"
import { cn } from "@/lib/utils"

export const Route = createFileRoute("/traces/$traceId")({
  validateSearch: validateTrace,
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env }, params }) =>
    ensure(queryClient, q.trace(env, params.traceId)),
  head: ({ params }) => ({
    meta: [{ title: `${params.traceId.slice(0, 12)} · Traces · weftbase` }],
  }),
  component: TracePage,
})

/** depthFirst orders spans as a tree, each with its depth. */
function depthFirst(spans: Span[]): { span: Span; depth: number }[] {
  const kids = new Map<string | null, Span[]>()
  for (const s of spans)
    kids.set(s.parent_id, [...(kids.get(s.parent_id) ?? []), s])
  const out: { span: Span; depth: number }[] = []
  const walk = (parent: string | null, depth: number) => {
    for (const s of (kids.get(parent) ?? []).sort(
      (a, b) => a.offset_ms - b.offset_ms
    )) {
      out.push({ span: s, depth })
      walk(s.id, depth + 1)
    }
  }
  walk(null, 0)
  return out
}

function TracePage() {
  const { traceId } = Route.useParams()
  const search = Route.useSearch()
  const env = useEnv()
  const { data: t } = useSuspenseQuery(q.trace(env, traceId))
  const rows = depthFirst(t.spans)
  const total = Math.max(1, ...t.spans.map((s) => s.offset_ms + s.duration_ms))
  const current = t.spans.find((s) => s.id === search.span) ?? t.spans[0]
  return (
    <div className="mx-auto flex max-w-[1200px] flex-col gap-6">
      <Link
        to="/traces"
        className="inline-flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" />
        Traces
      </Link>
      <PageHeader
        eyebrow={<span className="normal-case">{t.id}</span>}
        title={t.root}
        description={`${t.span_count} spans from ${t.service}, ${duration(t.duration_ms)}, started ${dateTime(t.started_at)}.`}
      >
        {t.run_id && (
          <Link
            to="/runs/$runId"
            params={{ runId: t.run_id }}
            className="text-sm font-medium underline underline-offset-4"
          >
            Open run {t.run_id}
          </Link>
        )}
      </PageHeader>
      <div className="flex flex-wrap gap-5">
        <Card className="min-w-0 flex-[1_1_620px] gap-0 overflow-x-auto py-0">
          <div className="grid min-w-[560px] grid-cols-[minmax(220px,40%)_1fr_72px] items-center gap-3 px-5 py-3">
            <span className="table-head">Span</span>
            <span className="table-head">Timeline</span>
            <span className="text-right table-head">Time</span>
          </div>
          <ol className="min-w-[560px]">
            {rows.map(({ span, depth }) => (
              <li key={span.id}>
                <Link
                  to="/traces/$traceId"
                  params={{ traceId }}
                  search={(p) => ({ ...p, span: span.id })}
                  aria-current={current.id === span.id ? "true" : undefined}
                  className={cn(
                    "grid h-11 grid-cols-[minmax(220px,40%)_1fr_72px] items-center gap-3 border-t px-5 hover:bg-muted/50",
                    current.id === span.id && "bg-table-row-selected"
                  )}
                >
                  <span
                    className="flex min-w-0 items-center gap-2 text-sm"
                    style={{ paddingLeft: depth * 16 }}
                  >
                    <span className="truncate">{span.name}</span>
                    {span.error && (
                      <Badge variant="failed" size="sm">
                        Error
                      </Badge>
                    )}
                  </span>
                  <span className="relative h-2 rounded-full bg-muted">
                    <span
                      className={cn(
                        "absolute inset-y-0 rounded-full",
                        span.error
                          ? "bg-danger"
                          : depth === 0
                            ? "bg-chart-2"
                            : "bg-chart-3"
                      )}
                      style={{
                        left: `${(span.offset_ms / total) * 100}%`,
                        width: `${Math.max(1, (span.duration_ms / total) * 100)}%`,
                      }}
                    />
                  </span>
                  <span className="text-right font-mono text-[13px] text-muted-foreground">
                    {duration(span.duration_ms)}
                  </span>
                </Link>
              </li>
            ))}
          </ol>
        </Card>
        <section
          aria-label="Span detail"
          className="flex min-w-[300px] flex-[0_1_420px] flex-col gap-4"
        >
          <h2 className="text-lg">{current.name}</h2>
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt className="eyebrow">Kind</dt>
              <dd className="mt-1">{current.kind}</dd>
            </div>
            <div>
              <dt className="eyebrow">Start</dt>
              <dd className="mt-1 font-mono">+{duration(current.offset_ms)}</dd>
            </div>
          </dl>
          <CodeBlock title="attributes" code={json(current.attributes)} />
        </section>
      </div>
    </div>
  )
}
