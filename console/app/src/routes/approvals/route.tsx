import { useSuspenseQuery } from "@tanstack/react-query"
import {
  Link,
  Outlet,
  createFileRoute,
  useParams,
} from "@tanstack/react-router"
import { ShieldCheckIcon } from "lucide-react"

import { EmptyState } from "@/components/console/empty-state"
import { LinkTab, LinkTabs } from "@/components/console/link-tabs"
import { PageHeader } from "@/components/console/page-header"
import { ApprovalStatusBadge } from "@/components/console/status"
import { Card } from "@/components/ui/card"
import { q } from "@/lib/api/queries"
import { ago, dateTime } from "@/lib/format"
import { useEnv, validateApprovals } from "@/lib/search"
import { cn } from "@/lib/utils"

// The approvals inbox: the list on the left, the selected approval (a
// child route, so the selection is the URL) on the right.
export const Route = createFileRoute("/approvals")({
  validateSearch: validateApprovals,
  loaderDeps: ({ search }) => ({
    env: search.env ?? "live",
    f: {
      status: search.status ?? "pending",
      agent: search.agent,
      tool: search.tool,
    },
  }),
  loader: ({ context: { queryClient }, deps: { env, f } }) =>
    queryClient.ensureQueryData(q.approvals(env, f)),
  head: () => ({ meta: [{ title: "Approvals · weftbase" }] }),
  component: Inbox,
})

function Inbox() {
  const search = Route.useSearch()
  const env = useEnv()
  const status = search.status ?? "pending"
  const { data } = useSuspenseQuery(
    q.approvals(env, { status, agent: search.agent, tool: search.tool })
  )
  const params: { approvalId?: string } = useParams({ strict: false })
  return (
    <div className="mx-auto flex max-w-[1280px] flex-col gap-4">
      <PageHeader
        title="Approvals"
        description="Actions your agents paused for a person to approve, pending first."
      >
        <LinkTabs label="Approval status">
          <LinkTab
            to="/approvals"
            search={(p) => ({ ...p, status: undefined })}
          >
            Pending
          </LinkTab>
          <LinkTab
            to="/approvals"
            search={(p) => ({ ...p, status: "decided" as const })}
          >
            Decided
          </LinkTab>
        </LinkTabs>
      </PageHeader>
      <div className="flex flex-wrap gap-5">
        <section
          aria-label="Inbox"
          className="flex min-w-[300px] flex-[0_1_420px] flex-col gap-3 self-start"
        >
          {data.items.length === 0 ? (
            <EmptyState
              icon={ShieldCheckIcon}
              title={
                status === "pending"
                  ? "Nothing waiting for you"
                  : "No decisions yet"
              }
            >
              {status === "pending"
                ? "When an agent calls a tool that needs a person, it shows up here."
                : "Approved and denied actions are listed here."}
            </EmptyState>
          ) : (
            <Card className="gap-0 py-0">
              <ul>
                {data.items.map((a) => (
                  <li key={a.id} className="border-t first:border-t-0">
                    <Link
                      from="/approvals"
                      to="/approvals/$approvalId"
                      params={{ approvalId: a.id }}
                      search={(p) => p}
                      className={cn(
                        "flex flex-col gap-1.5 px-5 py-4 first:rounded-t-card last:rounded-b-card hover:bg-muted/50",
                        params.approvalId === a.id &&
                          "bg-table-row-selected hover:bg-table-row-selected"
                      )}
                    >
                      <span className="flex items-center justify-between gap-3">
                        <span className="font-mono text-sm font-medium">
                          {a.tool}
                        </span>
                        <ApprovalStatusBadge status={a.status} />
                      </span>
                      <span className="line-clamp-2 text-[13px] text-muted-foreground">
                        {a.summary}
                      </span>
                      <span className="flex gap-2 text-xs text-subtle-foreground">
                        <span>{a.agent}</span>
                        <span aria-hidden>·</span>
                        <span className="font-mono">{a.run_id}</span>
                        <span aria-hidden>·</span>
                        <span title={dateTime(a.requested_at)}>
                          {ago(a.requested_at)}
                        </span>
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </section>
        <div className="min-w-0 flex-[1_1_520px]">
          <Outlet />
        </div>
      </div>
    </div>
  )
}
