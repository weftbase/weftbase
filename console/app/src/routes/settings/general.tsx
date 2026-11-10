import { useQuery } from "@tanstack/react-query"
import { createFileRoute } from "@tanstack/react-router"
import { CheckIcon, CopyIcon } from "lucide-react"
import * as React from "react"

import { useCopy } from "@/components/console/copy-id"
import { ErrorState, LoadingRows } from "@/components/console/empty-state"
import { Section } from "@/components/console/page-header"
import { RelativeTime } from "@/components/console/relative-time"
import { q } from "@/lib/api/queries"
import type { Meta } from "@/lib/api/types"
import { bytes, count } from "@/lib/format"

// Settings, General: what this server is and how healthy its intake is
// (GET /v1/meta), and how to point an app at it. Read-only.
export const Route = createFileRoute("/settings/general")({
  component: General,
})

/** Facts: label and value rows in one bordered container. */
export function Facts({
  rows,
}: {
  rows: [string, React.ReactNode, ("text" | "danger")?][]
}) {
  return (
    <dl className="overflow-hidden rounded-alert border bg-card">
      {rows.map(([label, value, kind]) => (
        <div
          key={label}
          className="flex min-h-8 items-center gap-4 border-b px-3 py-1.5 last:border-b-0"
        >
          <dt className="w-36 flex-none text-xs text-muted-foreground">
            {label}
          </dt>
          <dd
            className={
              kind === "text"
                ? "min-w-0 text-xs"
                : kind === "danger"
                  ? "min-w-0 font-mono text-xs break-all text-danger-foreground"
                  : "min-w-0 font-mono text-xs break-all"
            }
          >
            {value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

/** What each optional capability is, for the ones the server names. */
const CAPABILITY: Record<string, string> = {
  ingest: "accepts traces and logs over OTLP",
  clickhouse: "the trace store is ClickHouse",
  search: "runs can be searched",
  stats: "aggregates (the Overview)",
  traces: "plain OpenTelemetry traces are listed",
  pricing: "costs are computed from the pricing table",
}

const CONTENT_POLICY: Record<string, string> = {
  as_received:
    "Stored as received. The server never strips content; what an app sends (prompts, replies, tool arguments and results) is what is kept. To keep content out, turn it off in the app's exporter.",
}

function serverUrl(): string {
  if (typeof document === "undefined") return ""
  return document.baseURI.replace(/\/+$/, "")
}

function Snippet({ text }: { text: string }) {
  const [copied, copy] = useCopy()
  return (
    <div className="group/snippet relative overflow-hidden rounded-alert border bg-surface">
      <pre className="overflow-x-auto p-3 font-mono text-xs leading-[18px]">
        {text}
      </pre>
      <button
        type="button"
        aria-label="Copy"
        title={copied ? "Copied" : "Copy"}
        onClick={() => copy(text)}
        className="absolute top-1.5 right-1.5 flex size-6 items-center justify-center rounded-sm border bg-background text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        {copied ? (
          <CheckIcon className="size-3 text-success-foreground" />
        ) : (
          <CopyIcon className="size-3" />
        )}
      </button>
    </div>
  )
}

function ingestRows(
  m: Meta
): [string, React.ReactNode, ("text" | "danger")?][] {
  const i = m.ingest
  if (!i) return []
  const dropped = i.dropped.reduce((a, d) => a + d.records, 0)
  return [
    ["Batches accepted", count(i.accepted)],
    [
      "Batches refused",
      i.refused === 0 ? (
        "0"
      ) : (
        <>
          {count(i.refused)}
          {i.last_error && (
            <span className="text-danger-foreground">
              {" "}
              · last: {i.last_error}
              {i.last_at && (
                <>
                  {" "}
                  (<RelativeTime time={i.last_at} />)
                </>
              )}
            </span>
          )}
        </>
      ),
    ],
    [
      "Records dropped",
      dropped === 0 ? (
        "0"
      ) : (
        <ul className="flex flex-col gap-0.5">
          {i.dropped.map((d) => (
            <li key={d.service}>
              {count(d.records)} lost by{" "}
              <span className="font-medium">{d.service}</span> before they
              reached this server
              {d.last_heard && (
                <>
                  {" "}
                  (last heard <RelativeTime time={d.last_heard} />)
                </>
              )}
            </li>
          ))}
        </ul>
      ),
      dropped === 0 ? undefined : "danger",
    ],
  ]
}

function General() {
  const meta = useQuery(q.meta())
  if (meta.error) return <ErrorState error={meta.error} what="The server" />
  const m = meta.data
  if (!m) return <LoadingRows rows={5} />
  const url = serverUrl()
  const policy = CONTENT_POLICY[m.content.ingest] ?? m.content.ingest
  return (
    <>
      <Section title="Server">
        <Facts
          rows={[
            ["Weftbase", m.server_version],
            ["Weft", m.weft_version],
            [
              "Project",
              `${m.principal.scope.project} / ${m.principal.scope.env}`,
            ],
            [
              "Capabilities on",
              m.capabilities.length === 0 ? (
                "none"
              ) : (
                <ul className="flex flex-col gap-0.5">
                  {m.capabilities.map((c) => (
                    <li key={c}>
                      {c}
                      {CAPABILITY[c] && (
                        <span className="font-sans text-muted-foreground">
                          {" "}
                          · {CAPABILITY[c]}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              ),
            ],
          ]}
        />
      </Section>
      <Section title="Data">
        <Facts
          rows={[
            [
              "Database",
              `${m.db.kind}${m.db.size !== undefined ? ` · ${bytes(m.db.size)}` : ""}`,
            ],
            ["Path", m.db.path ?? "—"],
            [
              "Retention",
              m.retention
                ? `runs older than ${m.retention} are removed`
                : "none set: everything is kept",
              "text",
            ],
            [
              "Interrupted after",
              `A run that stops reporting for ${count(m.interrupted_after_ms / 1000)} s reads as interrupted.`,
              "text",
            ],
          ]}
        />
      </Section>
      {m.ingest && (
        <Section title="Ingest health, since the server started">
          <Facts rows={ingestRows(m)} />
        </Section>
      )}
      <Section title="Content policy">
        <Facts
          rows={[
            ["On arrival", policy, "text"],
            ...(m.content.latest
              ? ([
                  [
                    "Latest run",
                    `${m.content.latest.mark}: ${m.content.latest.note}${m.content.latest.fix ? ` ${m.content.latest.fix}` : ""}`,
                    "text",
                  ],
                ] as [string, React.ReactNode, "text"][])
              : []),
            ...(m.content.error
              ? ([["Read error", m.content.error, "danger"]] as [
                  string,
                  React.ReactNode,
                  "danger",
                ][])
              : []),
          ]}
        />
      </Section>
      <Section title="Connect an app">
        <p className="text-xs text-muted-foreground">
          Give the app this server&apos;s address and an API key (
          <code className="font-mono">weftbase dev</code> keeps one in{" "}
          <code className="font-mono">&lt;data&gt;/bootstrap.key</code>), then
          install the exporter once at start. Its runs appear under Runs.
        </p>
        <Snippet
          text={`export WEFTBASE_URL=${url}\nexport WEFTBASE_TOKEN=<an API key>`}
        />
        <Snippet
          text={`import "github.com/weftbase/weftbase/client/exporter"\n\nfunc main() {\n\tdefer exporter.Install()() // reads WEFTBASE_URL and WEFTBASE_TOKEN\n\t// … run your agents\n}`}
        />
      </Section>
    </>
  )
}
