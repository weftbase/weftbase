// The Agents list's header count. Pure.

import type { Agent, AgentsSummary, ManifestDrift } from "@/lib/api/types"
import { count, dateTime } from "@/lib/format"

const n = (k: number, one: string, many = `${one}s`) =>
  `${count(k)} ${k === 1 ? one : many}`

/** How many of the listed agents a build registered, and how many were
 * only seen in runs (a subagent, or traces an app sent). Counted from
 * the rows, so the two always add up to the rows of the table. */
export function agentCounts(agents: AgentsSummary["agents"]): {
  registered: number
  unregistered: number
} {
  const registered = agents.filter((a) => a.registered).length
  return { registered, unregistered: agents.length - registered }
}

/** The header line: "2 agents · 1 not registered · 13 tools ·
 * 2 placements". "not registered" is left out when every agent is. */
export function inventoryHeader(doc: AgentsSummary): string {
  const c = agentCounts(doc.agents)
  return [
    n(c.registered, "agent"),
    c.unregistered > 0 && `${count(c.unregistered)} not registered`,
    n(doc.totals.tools, "tool"),
    n((doc.totals.placements ?? []).length, "placement"),
  ]
    .filter(Boolean)
    .join(" · ")
}

/** The labels of an agent with the version each points to ("prod → v7"),
 * "prod" first, then by name. The server says the number
 * (`label_versions`); it is undefined only from a server that does not. */
export function labelVersions(
  a: Pick<Agent, "labels" | "label_versions">
): { label: string; version: string; number: number | undefined }[] {
  return Object.entries(a.labels)
    .map(([label, version]) => ({
      label,
      version,
      number: a.label_versions?.[label]?.number || undefined,
    }))
    .sort(
      (x, y) =>
        Number(y.label === "prod") - Number(x.label === "prod") ||
        x.label.localeCompare(y.label)
    )
}

/** The "differs" badge's tooltip: "6 of 6 runs since 10 Nov 2026, 12:49
 * ran a different manifest". undefined: no badge (no drift). */
export function driftNote(d: ManifestDrift | undefined): string | undefined {
  if (!d?.drift) return undefined
  const runs = `${count(d.runs_drifted)} of ${count(d.runs_checked)} ${d.runs_checked === 1 ? "run" : "runs"}`
  return `${runs}${d.since ? ` since ${dateTime(d.since)}` : ""} ran a different manifest than the one this version registered.`
}
