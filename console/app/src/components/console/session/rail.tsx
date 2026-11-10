import { Link } from "@tanstack/react-router"
import * as React from "react"

import { CopyId } from "@/components/console/copy-id"
import { RelativeTime } from "@/components/console/relative-time"
import { STATUS_LABEL, sessionStatus } from "@/lib/api/status"
import type {
  ConversationTurn,
  SessionDetail,
  SessionRow,
} from "@/lib/api/types"
import { cost, count, tokens } from "@/lib/format"
import { cn } from "@/lib/utils"

import { SessionLink } from "./marker"
import { exchanges } from "./model"
import { OutcomeStrip } from "./outcome-strip"

function Group({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="flex flex-col gap-1.5 border-b px-3 py-3 last:border-b-0">
      <h2 className="table-head">{title}</h2>
      <dl className="grid grid-cols-[84px_minmax(0,1fr)] items-center gap-x-2 gap-y-1 text-xs">
        {children}
      </dl>
    </section>
  )
}

function Row({
  label,
  children,
  title,
  mono = false,
}: {
  label: string
  children: React.ReactNode
  title?: string
  mono?: boolean
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        title={title}
        className={cn(
          "flex min-h-5 min-w-0 items-center",
          mono && "font-mono tabular-nums"
        )}
      >
        {children}
      </dd>
    </>
  )
}

/** forkOrigin splits a fork's "<origin session>#<entry>". */
function forkOrigin(v: string): { session: string; entry?: string } {
  const i = v.lastIndexOf("#")
  return i < 0
    ? { session: v }
    : { session: v.slice(0, i), entry: v.slice(i + 1) }
}

function statusWords(s: SessionRow): string {
  const extra: string[] = []
  if (s.pending) extra.push(`${count(s.pending)} waiting on a person`)
  if (s.failed_turns) extra.push(`${count(s.failed_turns)} failed`)
  if (s.cancelled_turns) extra.push(`${count(s.cancelled_turns)} cancelled`)
  if (s.ended_turns)
    extra.push(`${count(s.ended_turns)} ended by a newer message`)
  return extra.join(" · ")
}

/** SessionRail: the session's facts, beside the conversation. Each fact
 * lives here once: the agent and the version it runs, how the turns
 * went, the totals, when, the ids, and where the session came from. */
export function SessionRail({
  session,
  newest,
  className,
}: {
  session: SessionDetail
  /** The newest loaded turn: the version the session runs now. */
  newest: ConversationTurn | undefined
  className?: string
}) {
  const s = session
  const v = newest?.agent_version
  const fork = s.forked_from_session
    ? forkOrigin(s.forked_from_session)
    : undefined
  const words = statusWords(s)
  return (
    <aside
      aria-label="Session facts"
      className={cn("flex flex-col text-xs", className)}
    >
      <Group title="Agent">
        <Row label="Agent">
          {s.agent ? (
            <Link
              to="/agents/$name"
              params={{ name: s.agent }}
              className="truncate font-medium underline-offset-2 hover:underline"
            >
              {s.agent}
            </Link>
          ) : (
            "—"
          )}
        </Row>
        <Row
          label="Version"
          title={
            v
              ? `${v.id}: the version of the newest turn`
              : "The newest turn was not admitted through this server, so it carries no version."
          }
        >
          {v ? (
            <span className="flex min-w-0 items-center gap-1.5">
              <span className="font-mono">v{v.number}</span>
              {(v.labels ?? []).map((l) => (
                <span
                  key={l}
                  className="rounded-sm border px-1 text-[11px] leading-4 text-muted-foreground"
                >
                  {l}
                </span>
              ))}
            </span>
          ) : (
            <span className="text-muted-foreground">unknown</span>
          )}
        </Row>
        <Row label="Status">
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span>{STATUS_LABEL[sessionStatus(s)]}</span>
            <OutcomeStrip session={s} />
          </span>
        </Row>
        {words && (
          <Row label="">
            <span className="text-muted-foreground">{words}</span>
          </Row>
        )}
      </Group>

      <Group title="Totals">
        <Row
          label="Turns"
          mono
          title={
            s.resumes
              ? `${count(s.turns)} runs: ${count(s.resumes)} continued a turn after a wait`
              : undefined
          }
        >
          {count(exchanges(s))}
        </Row>
        <Row label="Tokens" mono>
          <span>
            {tokens(s.usage.input_tokens + s.usage.output_tokens)}
            <span className="ml-1.5 font-sans text-muted-foreground">
              {tokens(s.usage.input_tokens)} in ·{" "}
              {tokens(s.usage.output_tokens)} out
            </span>
          </span>
        </Row>
        <Row label="Cost" mono title={s.cost_reason}>
          {cost(s)}
        </Row>
        {s.delegated.length > 0 && (
          <Row
            label="Delegated"
            mono
            title="The cost of the sessions this one handed work to; not part of Cost."
          >
            {s.delegated_sessions_cost_known &&
            s.delegated_sessions_cost_usd !== null
              ? cost({
                  cost_usd: s.delegated_sessions_cost_usd,
                  cost_known: true,
                })
              : "unknown"}
          </Row>
        )}
      </Group>

      <Group title="Time">
        <Row label="Started">
          <RelativeTime time={s.first_seen} />
        </Row>
        <Row label="Last activity">
          <RelativeTime time={s.last_seen} />
        </Row>
      </Group>

      <Group title="Identifiers">
        <Row label="Session">
          <CopyId id={s.id} label="session id" className="-ml-1" />
        </Row>
        {s.public_id && (
          <Row label="Public id">
            <CopyId id={s.public_id} label="public id" className="-ml-1" />
          </Row>
        )}
        {newest && (
          <Row label="Newest run">
            <CopyId id={newest.run_id} label="run id" className="-ml-1" />
          </Row>
        )}
      </Group>

      {(fork || s.parent_session || s.delegated.length > 0) && (
        <Group title="Lineage">
          {fork && (
            <Row
              label="Fork of"
              title={fork.entry ? `Forked at entry ${fork.entry}` : undefined}
            >
              <SessionLink id={fork.session} />
            </Row>
          )}
          {s.parent_session && (
            <Row
              label="Parent"
              title={
                s.parent_turn
                  ? `Started by turn ${s.parent_turn} of that session`
                  : undefined
              }
            >
              <SessionLink id={s.parent_session} />
            </Row>
          )}
          {s.parent_run && (
            <Row label="Started by">
              <Link
                to="/runs/$runId"
                params={{ runId: s.parent_run }}
                className="truncate font-mono underline-offset-2 hover:underline"
              >
                run {s.parent_run.slice(-8)}
              </Link>
            </Row>
          )}
          {s.delegated.map((d) => (
            <Row key={d.id} label="Handed to">
              <SessionLink id={d.id} />
            </Row>
          ))}
        </Group>
      )}
    </aside>
  )
}
