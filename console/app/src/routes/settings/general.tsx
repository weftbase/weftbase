import { useSuspenseQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"

import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import { q } from "@/lib/api/queries"
import { ago, dateTime } from "@/lib/format"

export const Route = createFileRoute("/settings/general")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(q.meta()),
  component: General,
})

const POLICY = {
  full: "Prompts, outputs and tool arguments are stored in full.",
  redacted: "Content is stored with secrets and PII redacted.",
  metadata: "Only metadata is stored: no prompts or outputs.",
}

function General() {
  const { data: m } = useSuspenseQuery(q.meta())
  const rows: [string, React.ReactNode][] = [
    ["Workspace", m.workspace],
    ["Version", <Kbd key="v">{m.version}</Kbd>],
    ["Hosting", m.hosting],
    [
      "Up since",
      <span key="u" title={dateTime(m.started_at)}>
        {ago(m.started_at).replace(" ago", "")}
      </span>,
    ],
    [
      "Data directory",
      <span key="d" className="font-mono text-[13px]">
        {m.data_dir}
      </span>,
    ],
    ["Database", m.database],
    ["Retention", `${m.retention_days} days, then runs and traces are deleted`],
    ["Content policy", POLICY[m.content_policy]],
  ]
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h2 className="text-lg">General</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Server facts. Change them in the server config and restart.
        </p>
      </div>
      <Card className="gap-0 py-0">
        <dl>
          {rows.map(([k, v]) => (
            <div
              key={k}
              className="grid grid-cols-[180px_1fr] items-center gap-3 border-t px-5 py-3.5 first:border-t-0 max-[600px]:grid-cols-1 max-[600px]:gap-1"
            >
              <dt className="text-[13px] text-subtle-foreground">{k}</dt>
              <dd className="text-sm">{v}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </section>
  )
}
