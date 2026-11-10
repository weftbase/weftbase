import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import * as React from "react"

import { CopyId } from "@/components/console/copy-id"
import { JsonView } from "@/components/console/json-view"
import { Section } from "@/components/console/page-header"
import { Badge } from "@/components/ui/badge"
import type { ChainView } from "@/lib/api/chain"
import { q } from "@/lib/api/queries"
import type { PosEvent } from "@/lib/api/types"
import { count, dateTime, duration } from "@/lib/format"

import { Facts, Inline, Muted } from "./fold"
import type { RunVersion } from "./use-run"

// The Details tab: what the run was configured with, in titled sections.
// Agent, Model & settings, Tools available, Metadata, Identifiers (with
// the lineage), Events, and the raw JSON last, folded.

const box = "rounded-alert border bg-card p-3"

type Obj = Record<string, unknown>
const obj = (v: unknown): Obj | undefined =>
  v && typeof v === "object" && !Array.isArray(v) ? (v as Obj) : undefined
const str = (v: unknown) => (typeof v === "string" && v ? v : undefined)

const show = (v: unknown): React.ReactNode =>
  v === null || v === undefined ? undefined : typeof v === "object" ? (
    <span className="font-mono">{JSON.stringify(v)}</span>
  ) : (
    String(v)
  )

const words = (k: string) =>
  k.replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase())

function eventSummary(e: PosEvent): string {
  const ev = e.event
  const bits: string[] = []
  for (const k of ["name", "call_id", "index", "reason", "stopped_by", "error"])
    if (ev[k] !== undefined && ev[k] !== "") bits.push(`${k}=${String(ev[k])}`)
  return bits.join(" ")
}

function Events({ runId, t0 }: { runId: string; t0: number }) {
  // Mounted when its disclosure opens, so the read starts then.
  const ev = useQuery(q.events(runId))
  if (ev.error)
    return <Muted>The events could not be read: {String(ev.error)}</Muted>
  if (!ev.data) return <Muted>Loading the events…</Muted>
  if (ev.data.events.length === 0)
    return <Muted>{ev.data.reason ?? "No events were recorded."}</Muted>
  return (
    <ol className="max-h-96 overflow-auto rounded-input border font-mono text-xs leading-[18px]">
      {ev.data.events.map((e) => (
        <li
          key={e.pos}
          className="flex gap-3 border-b px-2 py-0.5 last:border-b-0"
        >
          <span className="w-8 flex-none text-right text-subtle-foreground">
            {e.pos}
          </span>
          <span
            className="w-16 flex-none text-right text-subtle-foreground tabular-nums"
            title={e.time}
          >
            +{duration(Math.max(0, Date.parse(e.time) - t0))}
          </span>
          <span className="w-28 flex-none font-medium">{e.event.type}</span>
          <span className="min-w-0 flex-1 truncate text-muted-foreground">
            {eventSummary(e)}
          </span>
        </li>
      ))}
    </ol>
  )
}

export function DetailsTab({
  chain,
  version,
  t0,
}: {
  chain: ChainView
  version: RunVersion
  t0: number
}) {
  const run = chain.last
  const first = chain.runs[0]
  const manifestQ = useQuery({
    ...q.manifest(run.manifest_hash),
    enabled: !!run.manifest_hash && run.manifest_available !== false,
  })
  const agents = obj(manifestQ.data?.manifest)?.agents
  const agent = (Array.isArray(agents) ? agents : [])
    .map(obj)
    .find((a) => a?.name === run.agent)
  const policy = obj(agent?.policy)
  const params = obj(agent?.params)
  const tools = (Array.isArray(agent?.tools) ? agent.tools : [])
    .map(obj)
    .filter((t) => t !== undefined)
  const subagents = Array.isArray(agent?.subagents) ? agent.subagents : []
  const meta = Object.entries(run.meta).filter(
    ([k]) => !k.startsWith("weftbase.")
  )
  const tenant = run.meta["weftbase.tenant_id"]

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-4 pb-6">
      <Section title="Agent">
        <div className={box}>
          <Facts
            rows={[
              [
                "Name",
                <Link
                  key="a"
                  to="/agents/$name"
                  params={{ name: run.agent }}
                  className="font-medium hover:underline"
                >
                  {run.agent}
                </Link>,
              ],
              [
                "Version",
                version.id ? (
                  <span className="flex flex-wrap items-center gap-1.5">
                    {version.number !== undefined && (
                      <span className="font-mono">v{version.number}</span>
                    )}
                    {version.labels.map((l) => (
                      <Badge key={l} variant="secondary">
                        {l}
                      </Badge>
                    ))}
                    {version.drift && (
                      <Badge
                        variant="warning"
                        title="The manifest this run reported is not the one its version registered."
                      >
                        manifest differs from the registered one
                      </Badge>
                    )}
                    <CopyId id={version.id} label="version id" />
                  </span>
                ) : (
                  "not admitted by this server (the app ran it itself)"
                ),
              ],
              ["Resolved through", version.resolvedVia],
              ["Placement", version.placement],
              ["weft", run.weft_version],
              ["Service", run.service],
            ]}
          />
        </div>
      </Section>

      <Section title="Model & settings">
        <div className={box}>
          <Facts
            rows={[
              ["Model", `${run.model.provider} / ${run.model.name}`],
              ...Object.entries(policy ?? {}).map(
                ([k, v]) => [words(k), show(v)] as const
              ),
              ...Object.entries(params ?? {}).map(
                ([k, v]) => [words(k), show(v)] as const
              ),
            ]}
          />
          {!agent && (
            <Muted>
              {manifestQ.isLoading
                ? "Loading the manifest…"
                : "This run's manifest is not stored, so its limits and settings cannot be shown."}
            </Muted>
          )}
        </div>
      </Section>

      {agent && (
        <Section
          title="Tools available"
          action={`${tools.length + subagents.length}`}
        >
          <div className={`${box} flex flex-col gap-1`}>
            {str(agent.instructions) && (
              <Inline label="Instructions (system prompt)">
                <p className="rounded-input border bg-surface px-2 py-1.5 text-sm whitespace-pre-wrap">
                  {str(agent.instructions)}
                </p>
              </Inline>
            )}
            {tools.map((t) => (
              <Inline
                key={String(t.name)}
                label={
                  <>
                    <span className="font-mono font-medium text-foreground">
                      {String(t.name)}
                    </span>{" "}
                    {str(t.description)}
                    {t.approval === true || t.needs_approval === true
                      ? " · needs approval"
                      : ""}
                  </>
                }
              >
                <JsonView value={t.input_schema ?? t.schema ?? t} depth={3} />
              </Inline>
            ))}
            {subagents.map((s, i) => {
              const name = str(obj(s)?.name) ?? str(s) ?? `subagent ${i}`
              return (
                <div
                  key={name}
                  className="flex h-6 items-center gap-1 pl-4 text-xs"
                >
                  <span className="font-mono font-medium">{name}</span>
                  <span className="text-muted-foreground">subagent</span>
                </div>
              )
            })}
            {tools.length + subagents.length === 0 && (
              <Muted>The manifest lists no tools.</Muted>
            )}
          </div>
        </Section>
      )}

      {meta.length > 0 && (
        <Section title="Metadata">
          <div className={box}>
            <Facts
              rows={meta.map(
                ([k, v]) =>
                  [
                    k,
                    <span key={k} className="font-mono">
                      {v}
                    </span>,
                  ] as const
              )}
            />
          </div>
        </Section>
      )}

      <Section title="Identifiers">
        <div className={box}>
          <Facts
            rows={[
              ...chain.runs.map(
                (r, i) =>
                  [
                    chain.runs.length === 1
                      ? "Run"
                      : i === 0
                        ? "Run (parked)"
                        : i === 1
                          ? "Resumed as"
                          : `Resumed as (${i})`,
                    <CopyId key={r.id} id={r.id} full label="run id" />,
                  ] as const
              ),
              [
                "Session",
                run.session_id ? (
                  <span className="flex items-center gap-2">
                    <CopyId id={run.session_id} full label="session id" />
                    <Link
                      to="/sessions/$sessionId"
                      params={{ sessionId: run.session_id }}
                      className="text-muted-foreground hover:text-foreground hover:underline"
                    >
                      Open session (turn {run.turn})
                    </Link>
                  </span>
                ) : undefined,
              ],
              [
                "Trace",
                <span key="t" className="flex items-center gap-2">
                  <CopyId id={run.trace_id} full label="trace id" />
                  <Link
                    to="/traces/$traceId"
                    params={{ traceId: run.trace_id }}
                    className="text-muted-foreground hover:text-foreground hover:underline"
                  >
                    Open whole trace
                  </Link>
                </span>,
              ],
              [
                "Tenant",
                tenant ? (
                  <CopyId id={tenant} full label="tenant id" />
                ) : undefined,
              ],
              [
                "Manifest",
                run.manifest_hash ? (
                  <CopyId id={run.manifest_hash} label="manifest hash" />
                ) : undefined,
              ],
              [
                "Started by",
                first.parent_run_id ? (
                  <Link
                    to="/runs/$runId"
                    params={{ runId: first.parent_run_id }}
                    className="font-mono hover:underline"
                  >
                    {first.parent_run_id}
                    {first.parent_call_id && ` · call ${first.parent_call_id}`}
                  </Link>
                ) : first.caller_run_id ? (
                  <Link
                    to="/runs/$runId"
                    params={{ runId: first.caller_run_id }}
                    className="font-mono hover:underline"
                  >
                    {first.caller_run_id}
                    {first.caller_call_id && ` · call ${first.caller_call_id}`}
                  </Link>
                ) : undefined,
              ],
              [
                "Fork of",
                first.forked_from ? (
                  <span className="font-mono">{first.forked_from}</span>
                ) : undefined,
              ],
              ["Started", dateTime(first.started)],
            ]}
          />
        </div>
      </Section>

      <Section title="Events">
        <div className={box}>
          <Inline
            label={`${count(run.event_count)} ${run.event_count === 1 ? "event" : "events"} of the run, as recorded`}
          >
            <Events runId={run.id} t0={t0} />
          </Inline>
        </div>
      </Section>

      <Section title="Raw JSON">
        <div className={box}>
          <Inline label="The run as the server returns it">
            <JsonView
              value={chain.runs.length === 1 ? run : chain.runs}
              depth={1}
            />
          </Inline>
        </div>
      </Section>
    </div>
  )
}
