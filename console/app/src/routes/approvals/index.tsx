import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import { CircleCheckIcon, XIcon } from "lucide-react"
import * as React from "react"

import { DecideForm } from "@/components/console/approval/decide"
import { CopyId } from "@/components/console/copy-id"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { FilterBar, FilterSelect } from "@/components/console/filter-bar"
import { JsonView } from "@/components/console/json-view"
import { Page, PageHeader } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { SplitPane } from "@/components/console/split-pane"
import { Button } from "@/components/ui/button"
import { actorText, useMe } from "@/lib/actor"
import type { ActorLike } from "@/lib/actor"
import { q } from "@/lib/api/queries"
import type { Approval } from "@/lib/api/types"
import {
  approvalState,
  approvalVersion,
  argsSummary,
} from "@/lib/approval-view"
import type { ApprovalTone } from "@/lib/approval-view"
import { count, dateTime } from "@/lib/format"
import {
  APPROVAL_STATUSES,
  useSetSearch,
  validateApprovals,
} from "@/lib/search"
import type { ApprovalsSearch } from "@/lib/search"
import { cn } from "@/lib/utils"

// Approvals: the inbox of tool calls that wait on, or were decided by, a
// person. Pending first (oldest first: they block a run), decided below.
// Selecting one shows the request, the exact arguments and the decision.
export const Route = createFileRoute("/approvals/")({
  validateSearch: validateApprovals,
  head: () => ({ meta: [{ title: "Approvals · weftbase" }] }),
  component: ApprovalsPage,
})

const DOT: Record<ApprovalTone, string> = {
  waiting: "bg-warning",
  good: "bg-success",
  bad: "bg-danger",
  neutral: "bg-neutral",
}

function State({ approval }: { approval: Approval }) {
  const s = approvalState(approval)
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span
        aria-hidden
        className={cn("size-2 flex-none rounded-full", DOT[s.tone])}
      />
      <span className="truncate">{s.label}</span>
    </span>
  )
}

const STATUS_LABEL: Record<(typeof APPROVAL_STATUSES)[number], string> = {
  pending: "Pending",
  decided: "Decided",
  approved: "Approved",
  denied: "Denied",
  resolved: "Answered",
  expired: "Expired",
  cancelled: "Cancelled",
}

const columns = (me: ActorLike | undefined): Column<Approval>[] => [
  {
    key: "status",
    header: "Status",
    width: "100px",
    cell: (a) => <State approval={a} />,
  },
  {
    key: "agent",
    header: "Agent",
    width: "minmax(88px, 140px)",
    cell: (a) => <span className="font-medium">{a.agent}</span>,
  },
  {
    key: "tool",
    header: "Wants to call",
    width: "minmax(110px, 160px)",
    mono: true,
    cell: (a) => a.tool,
  },
  {
    key: "args",
    header: "With",
    width: "minmax(160px, 1fr)",
    muted: true,
    cell: (a) => argsSummary(a.args) || "no arguments",
  },
  {
    key: "who",
    hideBelow: 700,
    header: "Decided by",
    width: "minmax(96px, 150px)",
    muted: true,
    cell: (a) =>
      a.status === "pending" ? (
        "—"
      ) : (
        <span title={a.actor?.id || undefined}>
          {actorText(a.actor, me) ?? "—"}
        </span>
      ),
  },
  {
    key: "when",
    hideBelow: 480,
    header: "When",
    width: "84px",
    align: "right",
    muted: true,
    cell: (a) => <RelativeTime time={a.decided_at ?? a.requested_at} />,
  },
]

function ApprovalsPage() {
  const search = Route.useSearch()
  const set = useSetSearch<ApprovalsSearch>(Route.fullPath)
  const me = useMe()
  const cols = React.useMemo(() => columns(me), [me])
  // Pages on demand, in the server's order (pending first, oldest first;
  // then decided, newest first). The outbox stream refetches the loaded
  // pages; an approval that moved between two of them meanwhile is shown
  // once.
  const list = useInfiniteQuery(
    q.approvalsList({
      status: search.status,
      agent: search.agent,
      tool: search.tool,
      run: search.run,
    })
  )
  const head = list.data?.pages.at(0)
  const agents = useQuery(q.agentsSummary())
  const tools = useQuery(q.tools())
  const agentNames = (agents.data?.agents ?? []).map((a) => a.name)
  const toolNames = (tools.data?.tools ?? [])
    .filter(
      (t) =>
        t.needs_approval ||
        t.agents.some((a) => a.versions.some((v) => v.needs_approval))
    )
    .map((t) => t.name)
  for (const [xs, v] of [
    [agentNames, search.agent],
    [toolNames, search.tool],
  ] as const)
    if (v && !xs.includes(v)) xs.push(v)
  const filtered = Boolean(
    search.status || search.agent || search.tool || search.run
  )
  const rows = React.useMemo(() => {
    const seen = new Set<string>()
    return (list.data?.pages ?? [])
      .flatMap((p) => p.approvals)
      .filter((a) => !seen.has(a.id) && seen.add(a.id))
  }, [list.data])

  // Esc closes the detail.
  const close = React.useEffectEvent(() => set({ id: undefined }))
  React.useEffect(() => {
    if (!search.id) return
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      if (e.key !== "Escape" || el?.closest("[role=dialog],[role=menu]")) return
      if (el && ["INPUT", "TEXTAREA"].includes(el.tagName)) return
      close()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [search.id])

  return (
    <Page
      flush
      header={
        <PageHeader
          title="Approvals"
          meta={
            head && (
              <span>
                {head.pending === 0
                  ? "nothing waiting"
                  : `${count(head.pending)} waiting`}
              </span>
            )
          }
        />
      }
      bar={
        <FilterBar
          right={
            head && (
              <span className="font-mono tabular-nums">
                {count(head.total)}{" "}
                {head.total === 1 ? "approval" : "approvals"}
              </span>
            )
          }
        >
          <FilterSelect
            label="Status"
            value={search.status}
            options={APPROVAL_STATUSES.map((s) => ({
              value: s,
              label: STATUS_LABEL[s],
            }))}
            onChange={(status) => set({ status })}
          />
          <FilterSelect
            label="Agent"
            value={search.agent}
            options={agentNames.sort().map((n) => ({ value: n, label: n }))}
            onChange={(agent) => set({ agent })}
          />
          <FilterSelect
            label="Tool"
            value={search.tool}
            options={toolNames.sort().map((n) => ({ value: n, label: n }))}
            onChange={(tool) => set({ tool })}
          />
          {search.run && (
            <span className="inline-flex h-6 flex-none items-center gap-1 rounded-sm bg-muted pr-0.5 pl-2 text-xs">
              one run&apos;s approvals
              <button
                type="button"
                aria-label="Show every run's approvals"
                onClick={() => set({ run: undefined })}
                className="flex size-5 items-center justify-center rounded-sm text-subtle-foreground hover:bg-background hover:text-foreground"
              >
                <XIcon className="size-3" />
              </button>
            </span>
          )}
        </FilterBar>
      }
    >
      <SplitPane
        id="approvals"
        defaultWidth={460}
        detail={
          search.id ? (
            <Detail
              key={search.id}
              id={search.id}
              listed={rows.find((a) => a.id === search.id)}
              onClose={() => set({ id: undefined })}
            />
          ) : null
        }
      >
        {list.error ? (
          <ErrorState error={list.error} what="Approvals" />
        ) : !head ? (
          <LoadingRows />
        ) : rows.length === 0 ? (
          filtered ? (
            <EmptyState
              title="No approvals match these filters"
              action={
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() =>
                    set({
                      status: undefined,
                      agent: undefined,
                      tool: undefined,
                      run: undefined,
                    })
                  }
                >
                  Clear filters
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={CircleCheckIcon}
              title="Nothing is waiting on you"
            >
              A tool call appears here when an agent needs a person&apos;s
              decision before running it.
            </EmptyState>
          )
        ) : (
          <DataTable
            label="Approvals"
            columns={cols}
            rows={rows}
            rowKey={(a) => a.id}
            selectedKey={search.id}
            rowLink={(a) => ({
              to: "/approvals",
              search: { ...search, id: a.id },
              replace: true,
            })}
            footer={
              list.hasNextPage ? (
                <>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={list.isFetchingNextPage}
                    onClick={() => void list.fetchNextPage()}
                  >
                    {list.isFetchingNextPage ? "Loading…" : "Load more"}
                  </Button>
                  <span>
                    {count(rows.length)} of {count(head.total)}
                  </span>
                </>
              ) : undefined
            }
          />
        )}
      </SplitPane>
    </Page>
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
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  )
}

/** The version the run ran, as the run page names it: "v2" and the
 * labels on it. It opens that version on the agent's page. */
function Version({ approval: a }: { approval: Approval }) {
  const v = approvalVersion(a.agent_version)
  if (!v) return null
  return (
    <Link
      to="/agents/$name"
      params={{ name: a.agent }}
      search={{ v: v.number }}
      title={`Version ${v.number} of ${a.agent} · ${v.id}`}
      className="inline-flex h-5 items-center gap-1 rounded-sm border px-1.5 font-mono text-xs hover:bg-muted"
    >
      v{v.number}
      {v.labels.length > 0 && (
        <span className="font-sans text-subtle-foreground">
          {v.labels.join(", ")}
        </span>
      )}
    </Link>
  )
}

function Detail({
  id,
  listed,
  onClose,
}: {
  id: string
  /** The approval as the list holds it, when it does. */
  listed: Approval | undefined
  onClose: () => void
}) {
  const me = useMe()
  const one = useQuery({ ...q.approval(id), enabled: !listed })
  const a = listed ?? one.data
  if (!a)
    return one.error ? (
      <ErrorState error={one.error} what="This approval" />
    ) : (
      <LoadingRows rows={4} />
    )
  const pending = a.status === "pending"
  const who = actorText(a.actor, me)
  return (
    <section aria-label="Approval" className="flex flex-col text-sm">
      <header className="flex h-10 flex-none items-center gap-2 border-b px-3">
        <h2 className="min-w-0 truncate font-mono text-xs font-semibold">
          {a.tool}
        </h2>
        <State approval={a} />
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label="Close"
          title="Close (Esc)"
          onClick={onClose}
          className="ml-auto"
        >
          <XIcon />
        </Button>
      </header>
      <div className="flex flex-col gap-3 p-3">
        <p>
          <Link
            to="/agents/$name"
            params={{ name: a.agent }}
            className="font-medium underline-offset-4 hover:underline"
          >
            {a.agent}
          </Link>{" "}
          {pending ? "wants to call" : "asked to call"}{" "}
          <span className="font-mono text-xs font-medium">{a.tool}</span> with
          exactly these arguments.
        </p>
        <JsonView value={a.args} depth={4} />
        {pending ? (
          <DecideForm approval={a} showArgs={false} />
        ) : (
          <p
            className={cn(
              "rounded-input border px-2 py-1.5 text-sm",
              a.status === "denied" && "border-danger/30 bg-danger-muted",
              a.status === "approved" && "border-success/30 bg-success-muted"
            )}
          >
            <span className="font-medium">{approvalState(a).label}</span>
            {who && who !== "system" && <> by {who}</>}
            {a.decided_at && (
              <>
                {" "}
                <RelativeTime time={a.decided_at} sentence />
              </>
            )}
            .
          </p>
        )}
        <dl className="grid grid-cols-[96px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
          {/* An expiry's reason only says that it expired. */}
          {a.reason && a.status !== "expired" && (
            <Row label="Reason">{a.reason}</Row>
          )}
          {a.content && (
            <Row label="Answer given">
              <span className="font-mono whitespace-pre-wrap">{a.content}</span>
            </Row>
          )}
          <Row label="Run">
            <Link
              to="/runs/$runId"
              params={{ runId: a.run_id }}
              className="font-medium underline-offset-4 hover:underline"
            >
              Open the run
            </Link>
            <CopyId id={a.run_id} label="run id" className="ml-1" />
          </Row>
          {a.resumed_by && (
            <Row label="Continued as">
              <CopyId id={a.resumed_by} label="run id" className="-ml-1" />
            </Row>
          )}
          {approvalVersion(a.agent_version) && (
            <Row label="Version">
              <Version approval={a} />
            </Row>
          )}
          <Row label="Asked">
            <span title={dateTime(a.requested_at)}>
              <RelativeTime time={a.requested_at} />
            </span>
          </Row>
          <Row label="Call">
            <span className="font-mono">{a.call_id}</span>
          </Row>
        </dl>
      </div>
    </section>
  )
}
