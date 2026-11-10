import { Link } from "@tanstack/react-router"
import { ShieldCheckIcon } from "lucide-react"

import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import type { ToolRow } from "@/lib/api/types"
import { count, duration, percent } from "@/lib/format"
import type { Window } from "@/lib/search"

// The Tools tab: every tool once, with the agents that have it and
// whether it needs approval, so "who can call issue_refund?" has one
// answer. Calls, failure rate and p50 are over the window.

/** Which of an agent's versions have the tool, as "v1 prod, v2 staging". */
const versionsOf = (a: ToolRow["agents"][number]) =>
  a.versions
    .map((v) => `v${v.number}${(v.labels ?? []).map((l) => ` ${l}`).join("")}`)
    .join(", ")

/** Approval can differ per version: all, none, or some of them. */
function gate(t: ToolRow): "all" | "some" | "none" {
  const all = t.agents.flatMap((a) => a.versions.map((v) => v.needs_approval))
  if (all.length === 0) return t.needs_approval ? "all" : "none"
  return all.every(Boolean) ? "all" : all.some(Boolean) ? "some" : "none"
}

export function toolColumns(range: Window): Column<ToolRow>[] {
  return [
    {
      key: "name",
      header: "Tool",
      width: "minmax(140px, 220px)",
      cell: (t) => (
        <span className="font-mono text-xs font-medium">{t.name}</span>
      ),
    },
    {
      key: "description",
      hideBelow: 1000,
      header: "Description",
      width: "minmax(180px, 1fr)",
      muted: true,
      cell: (t) =>
        t.subagent
          ? `${t.description || "Hands off"} (hands off to ${t.subagent})`
          : t.description || "—",
    },
    {
      key: "agents",
      header: "Used by",
      width: "minmax(140px, 260px)",
      cell: (t) => (
        <span className="flex min-w-0 items-center gap-2">
          {t.agents.map((a) => (
            <Link
              key={a.agent}
              to="/agents/$name"
              params={{ name: a.agent }}
              title={`${a.agent}: ${versionsOf(a)}`}
              className="truncate font-medium underline-offset-4 hover:underline"
            >
              {a.agent}
              <span className="ml-1 font-mono text-[11px] font-normal text-subtle-foreground">
                {a.versions.map((v) => `v${v.number}`).join(" ")}
              </span>
            </Link>
          ))}
          {t.agents.length === 0 && (
            <span className="text-subtle-foreground">no agent</span>
          )}
        </span>
      ),
    },
    {
      key: "approval",
      header: "Approval",
      width: "128px",
      cell: (t) => {
        const g = gate(t)
        return g === "none" ? (
          <span className="text-subtle-foreground">—</span>
        ) : (
          <span
            className="inline-flex h-5 items-center gap-1 rounded-sm bg-warning-muted px-1.5 text-xs font-medium text-warning-foreground"
            title={
              g === "some"
                ? "Some versions ask a person before this tool runs, others do not."
                : "A person decides before this tool runs."
            }
          >
            <ShieldCheckIcon aria-hidden className="size-3" />
            {g === "some" ? "some versions" : "needs approval"}
          </span>
        )
      },
    },
    {
      key: "calls",
      hideBelow: 700,
      header: "Calls",
      width: "64px",
      align: "right",
      mono: true,
      cell: (t) =>
        t.stats && t.stats.calls > 0 ? (
          <Link
            to="/runs"
            search={{ tool: t.name, sub: true, range }}
            title="Runs that called this tool"
            className="hover:underline"
          >
            {count(t.stats.calls)}
          </Link>
        ) : (
          "—"
        ),
    },
    {
      key: "failure",
      header: "Failure rate",
      width: "96px",
      align: "right",
      mono: true,
      cell: (t) =>
        t.stats && t.stats.calls > 0 ? (
          <span
            title={`${count(t.stats.failed_calls)} of ${count(t.stats.calls)} calls failed`}
            className={
              t.stats.failed_calls > 0 ? "text-danger-foreground" : undefined
            }
          >
            {percent(t.stats.failure_rate)}
          </span>
        ) : (
          "—"
        ),
    },
    {
      key: "p50",
      hideBelow: 860,
      header: "p50",
      width: "72px",
      align: "right",
      mono: true,
      cell: (t) => duration(t.stats?.duration_ms?.p50),
    },
  ]
}

export function ToolsTable({
  tools,
  range,
}: {
  tools: readonly ToolRow[]
  range: Window
}) {
  return (
    <DataTable
      label="Tools"
      columns={toolColumns(range)}
      rows={tools}
      rowKey={(t) => t.name}
      footer={
        <span>
          {count(tools.length)} {tools.length === 1 ? "tool" : "tools"}
        </span>
      }
    />
  )
}
