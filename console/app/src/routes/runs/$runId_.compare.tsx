import { useQueries, useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { ArrowLeftRightIcon, GitCompareArrowsIcon } from "lucide-react"
import * as React from "react"

import { CompareDialog } from "@/components/console/compare/dialog"
import { CompareTimeline } from "@/components/console/compare/timeline"
import {
  CompareTotals,
  RunLink,
  SideTag,
} from "@/components/console/compare/totals"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { Button } from "@/components/ui/button"
import { chainQuery, chainStep } from "@/lib/api/chain"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import {
  buildCompare,
  itemsOf,
  pairChains,
  stepFacts,
  summaryOf,
} from "@/lib/compare"
import type { StepFacts } from "@/lib/compare"
import { shortId } from "@/lib/format"
import { useSetSearch, validateCompare } from "@/lib/search"
import type { CompareSearch } from "@/lib/search"

// Compare two runs (GET /v1/diff): the run in the path is A, `?with=` is
// B. One line of totals per run, then the two timelines aligned by
// step, with what differs marked and the identical steps folded away.
// Two-way only. A run that parked and was resumed is one run here, as on
// the run page: the two chains are compared run by run.
export const Route = createFileRoute("/runs/$runId_/compare")({
  validateSearch: validateCompare,
  head: ({ params }) => ({
    meta: [{ title: `Compare ${params.runId.slice(-8)} · Runs · weftbase` }],
  }),
  component: ComparePage,
})

function ComparePage() {
  const { runId } = Route.useParams()
  const search = Route.useSearch()
  const [choosing, setChoosing] = React.useState(false)
  const other = search.with

  if (!other)
    return (
      <Page header={<PageHeader title="Compare" />}>
        <EmptyState
          icon={GitCompareArrowsIcon}
          title="Choose a run to compare with"
          action={
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setChoosing(true)}
            >
              Compare with…
            </Button>
          }
        >
          Run {shortId(runId)} is shown beside another run, step by step.
        </EmptyState>
        <CompareDialog
          runId={runId}
          open={choosing}
          onOpenChange={setChoosing}
        />
      </Page>
    )
  return <Compare key={`${runId} ${other}`} a={runId} b={other} />
}

function Compare({ a, b }: { a: string; b: string }) {
  const search = Route.useSearch()
  const set = useSetSearch<CompareSearch>(Route.fullPath)
  const chainA = useQuery({ ...chainQuery(a), retry: false })
  const chainB = useQuery({ ...chainQuery(b), retry: false })
  const A = chainA.data
  const B = chainB.data

  // One /v1/diff per pair of runs: one pair unless a run parked.
  const pairs = React.useMemo(
    () =>
      A && B
        ? pairChains(
            A.runs.map((r) => r.id),
            B.runs.map((r) => r.id)
          )
        : [],
    [A, B]
  )
  const diffs = useQueries({ queries: pairs.map((p) => q.diff(p.a, p.b)) })
  const diffError = diffs.find((d) => d.error)?.error
  const ready = pairs.length > 0 && diffs.every((d) => d.data)
  const docs = diffs.map((d) => d.data)
  const key = diffs.map((d) => d.dataUpdatedAt).join()

  const model = React.useMemo(() => {
    if (!A || !B || !ready) return undefined
    const facts = new Map<string, Map<number, StepFacts>>()
    for (const chain of [A, B])
      for (const r of chain.runs) facts.set(r.id, stepFacts(chain.spans, r.id))
    return buildCompare(
      pairs.flatMap((p, i) => {
        const doc = docs[i]
        return doc ? [{ doc, only: p.only }] : []
      }),
      (id) => facts.get(id),
      // Steps are numbered through each chain, as on the run's page.
      (id) => chainStep(A, id, 0) ?? chainStep(B, id, 0)
    )
    // docs is a new array each render: `key` says when its content changed.
  }, [A, B, ready, pairs, key])

  const header = (
    <PageHeader
      title="Compare"
      meta={
        <>
          <Side side="a" id={a} chain={A} />
          <span className="flex-none text-subtle-foreground">with</span>
          <Side side="b" id={b} chain={B} />
        </>
      }
      actions={
        <Button size="sm" variant="secondary" asChild>
          <Link
            to="/runs/$runId/compare"
            params={{ runId: b }}
            search={{ with: a, all: search.all }}
            replace
            title="Put the other run on the left"
          >
            <ArrowLeftRightIcon />
            Swap
          </Link>
        </Button>
      }
    />
  )

  // A run that cannot be read is said by name: which of the two, and why.
  if (chainA.error || chainB.error)
    return (
      <Page header={header}>
        {chainA.error && (
          <ErrorState error={chainA.error} what={`Run ${shortId(a)}`} />
        )}
        {chainB.error && (
          <ErrorState error={chainB.error} what={`Run ${shortId(b)}`} />
        )}
      </Page>
    )
  if (!A || !B)
    return (
      <Page header={header}>
        <div className="overflow-hidden rounded-alert border bg-card">
          <LoadingRows rows={3} />
        </div>
      </Page>
    )

  const identical = model ? model.same + model.kept : 0
  return (
    <Page header={header} className="flex flex-col gap-3">
      <CompareTotals a={A} b={B} />
      {A.run.agent !== B.run.agent && (
        <p className="text-xs text-muted-foreground">
          These are runs of two agents ({A.run.agent} and {B.run.agent}): their
          steps are lined up by position.
        </p>
      )}
      {diffError ? (
        <ErrorState error={diffError} what="The comparison" />
      ) : !model ? (
        <div className="overflow-hidden rounded-alert border bg-card">
          <LoadingRows rows={6} />
        </div>
      ) : model.rows.length === 0 ? (
        <div className="rounded-alert border bg-card">
          <EmptyState title="No steps to compare">
            Neither run recorded a model call.
          </EmptyState>
        </div>
      ) : (
        <section className="flex min-w-0 flex-col gap-2">
          <div className="flex min-h-6 items-center justify-between gap-3">
            <p className="min-w-0 text-sm">
              {summaryOf(model)}
              {model.aligned && (
                <span className="text-muted-foreground"> {model.aligned}</span>
              )}
            </p>
            {/* Folded, each stretch of identical steps has its own
                "Show"; this folds them again. */}
            {identical > 0 && search.all && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => set({ all: undefined })}
              >
                Hide identical steps
              </Button>
            )}
          </div>
          {model.holes.map((h, i) => (
            <p key={i} className="text-xs text-muted-foreground">
              {h.reason}
              {h.fix ? `: ${h.fix}` : ""}
            </p>
          ))}
          <CompareTimeline
            items={itemsOf(model, search.all === true)}
            runs={{ a, b }}
            onShowAll={() => set({ all: true })}
          />
        </section>
      )}
    </Page>
  )
}

/** One run in the header: its side and its name as a link (its status
 * is in the totals under it). */
function Side({
  side,
  id,
  chain,
}: {
  side: "a" | "b"
  id: string
  chain: ChainView | undefined
}) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <SideTag side={side} />
      {chain ? (
        <RunLink chain={chain} />
      ) : (
        <span className="font-mono text-xs">{shortId(id)}</span>
      )}
    </span>
  )
}
