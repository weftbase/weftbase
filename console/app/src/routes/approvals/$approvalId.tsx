import { useSuspenseQuery } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"

import { ApprovalActions } from "@/components/console/approval-actions"
import { CodeBlock } from "@/components/console/code-block"
import { ApprovalStatusBadge } from "@/components/console/status"
import { Card } from "@/components/ui/card"
import { q } from "@/lib/api/queries"
import { dateTime, json } from "@/lib/format"
import { ensure } from "@/lib/load"
import { useEnv } from "@/lib/search"

export const Route = createFileRoute("/approvals/$approvalId")({
  loaderDeps: ({ search }) => ({ env: search.env ?? "live" }),
  loader: ({ context: { queryClient }, deps: { env }, params }) =>
    ensure(queryClient, q.approval(env, params.approvalId)),
  head: ({ params }) => ({
    meta: [{ title: `${params.approvalId} · Approvals · weftbase` }],
  }),
  component: ApprovalDetail,
})

function ApprovalDetail() {
  const { approvalId } = Route.useParams()
  const env = useEnv()
  const { data: a } = useSuspenseQuery(q.approval(env, approvalId))
  return (
    <Card className="gap-5 p-5" aria-label={`Approval ${a.id}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="font-mono text-xs text-subtle-foreground">{a.id}</div>
          <h2 className="mt-1 font-mono text-lg">{a.tool}</h2>
        </div>
        <ApprovalStatusBadge status={a.status} />
      </div>
      <p className="text-sm leading-relaxed">{a.summary}</p>
      <dl className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-4 text-sm">
        <div>
          <dt className="eyebrow">Agent</dt>
          <dd className="mt-1">
            <Link
              to="/agents/$name"
              params={{ name: a.agent }}
              className="hover:underline"
            >
              {a.agent}
            </Link>
          </dd>
        </div>
        <div>
          <dt className="eyebrow">Run</dt>
          <dd className="mt-1">
            <Link
              to="/runs/$runId"
              params={{ runId: a.run_id }}
              className="font-mono text-[13px] hover:underline"
            >
              {a.run_id}
            </Link>
          </dd>
        </div>
        <div>
          <dt className="eyebrow">Tenant</dt>
          <dd className="mt-1">{a.tenant}</dd>
        </div>
        <div>
          <dt className="eyebrow">Requested</dt>
          <dd className="mt-1">{dateTime(a.requested_at)}</dd>
        </div>
      </dl>
      <div className="flex flex-col gap-2">
        <h3 className="text-sm">Arguments, exactly as the model wrote them</h3>
        <CodeBlock title={`${a.tool}.args.json`} code={json(a.args)} />
        <p className="font-mono text-xs break-all text-subtle-foreground">
          sha256 {a.args_sha256}
        </p>
      </div>
      {a.status === "pending" ? (
        <div className="flex flex-col gap-3 rounded-alert bg-warning-muted p-3.5">
          <div className="text-sm">
            <span className="font-semibold">{a.tool}</span> needs a person to
            approve it. Your decision applies to these arguments only.
          </div>
          <ApprovalActions approval={a} env={env} />
        </div>
      ) : (
        <div className="rounded-alert bg-muted p-3.5 text-sm">
          {a.status === "approved"
            ? "Approved"
            : a.status === "denied"
              ? "Denied"
              : "Expired"}
          {a.decided_by && <> by {a.decided_by}</>}
          {a.decided_at && <> on {dateTime(a.decided_at)}</>}.
          {a.reason && (
            <span className="mt-1 block text-muted-foreground">
              Reason: {a.reason}
            </span>
          )}
        </div>
      )}
    </Card>
  )
}
