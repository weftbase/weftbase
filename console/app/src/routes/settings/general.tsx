import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import * as React from "react"

import { ErrorState } from "@/components/console/empty-state"
import { Section } from "@/components/console/page-header"
import { q } from "@/lib/api/queries"
import { bytes, count } from "@/lib/format"

// Settings, General: what this server is (GET /v1/meta). Read-only.
export const Route = createFileRoute("/settings/general")({
  component: General,
})

/** Facts: label and value rows in one bordered container. */
export function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="overflow-hidden rounded-alert border bg-card">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="flex min-h-8 items-center gap-4 border-b px-3 py-1.5 last:border-b-0"
        >
          <dt className="w-40 flex-none text-xs text-muted-foreground">
            {label}
          </dt>
          <dd className="min-w-0 font-mono text-xs break-all">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function General() {
  const meta = useQuery(q.meta())
  if (meta.error) return <ErrorState error={meta.error} what="The server" />
  const m = meta.data
  if (!m) return null
  return (
    <>
      <Section title="Server">
        <Facts
          rows={[
            ["Weftbase version", m.server_version],
            ["Weft version", m.weft_version],
            ["Capabilities", m.capabilities.join(", ") || "none"],
            ["Project", m.principal.scope.project],
            ["Environment", m.principal.scope.env],
          ]}
        />
      </Section>
      <Section title="Data">
        <Facts
          rows={[
            ["Trace store", m.db.kind],
            ["Path", m.db.path ?? "—"],
            ["Size", bytes(m.db.size)],
            [
              "Retention",
              m.retention ?? "Everything is kept (no retention set)",
            ],
            [
              "Interrupted after",
              `${count(m.interrupted_after_ms / 1000)} s without a heartbeat`,
            ],
          ]}
        />
      </Section>
      {m.ingest && (
        <Section title="Ingest">
          <Facts
            rows={[
              ["Batches accepted", count(m.ingest.accepted)],
              ["Batches refused", count(m.ingest.refused)],
              ["Last error", m.ingest.last_error ?? "none"],
            ]}
          />
        </Section>
      )}
    </>
  )
}
