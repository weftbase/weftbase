// The inventory in words: what the Agents screens say about the
// registry's answers. Pure.

import { ApiError } from "./api/errors"
import type {
  AgentSummary,
  AgentsSummary,
  ManifestLimits,
  StoredManifest,
  ToolsDoc,
  VersionChanges,
} from "./api/types"
import { bytes, count } from "./format"

const n = (k: number, one: string, many = `${one}s`) =>
  `${count(k)} ${k === 1 ? one : many}`

/** The list's header count: "4 agents · 11 tools · 2 placements". */
export function inventoryLine(t: AgentsSummary["totals"]): string {
  return [
    n(t.agents, "agent"),
    n(t.tools, "tool"),
    n((t.placements ?? []).length, "placement"),
  ].join(" · ")
}

/** versionNumbers maps a version id (agv_…) to its number, from every
 * source the lists already hold: each agent's newest version and the
 * versions the tool inventory names. The agents list has no numbers for
 * the versions its labels point to; this fills them in. */
export function versionNumbers(
  agents: readonly AgentSummary[],
  tools?: ToolsDoc
): Map<string, number> {
  const out = new Map<string, number>()
  for (const a of agents)
    if (a.summary.version) out.set(a.summary.version, a.summary.number)
  for (const t of tools?.tools ?? [])
    for (const a of t.agents)
      for (const v of a.versions) out.set(v.id, v.number)
  return out
}

/** The labels of an agent with the version each points to, "prod"
 * first, then by name. number is undefined when it is not known. */
export function labelPointers(
  labels: Record<string, string>,
  numbers: ReadonlyMap<string, number>
): { label: string; version: string; number: number | undefined }[] {
  return Object.entries(labels)
    .map(([label, version]) => ({
      label,
      version,
      number: numbers.get(version),
    }))
    .sort(
      (a, b) =>
        Number(b.label === "prod") - Number(a.label === "prod") ||
        a.label.localeCompare(b.label)
    )
}

const list = (xs: readonly string[]) => xs.join(", ")

/** What changed against the previous version, as short phrases. null
 * changes: the agent's first version. */
export function changeLines(c: VersionChanges | null): string[] {
  if (!c) return ["first version"]
  const out: string[] = []
  if (c.model_changed)
    out.push(`model ${c.model_changed.from.name} → ${c.model_changed.to.name}`)
  if (c.instructions_changed) out.push("instructions changed")
  const added = c.tools_added ?? []
  const removed = c.tools_removed ?? []
  if (added.length > 0) out.push(`added ${list(added)}`)
  if (removed.length > 0) out.push(`removed ${list(removed)}`)
  for (const t of c.tools_changed ?? [])
    out.push(
      `${t.name}: ${list(t.fields.map((f) => f.replace(/_/g, " ")))} changed`
    )
  for (const l of c.limits_changed ?? [])
    out.push(
      `${l.field.replace(/_/g, " ")} ${String(l.from ?? "unset")} → ${String(l.to ?? "unset")}`
    )
  if (out.length === 0)
    return [
      c.same_manifest && c.build_changed
        ? "new build, same manifest"
        : c.build_changed
          ? "new build"
          : "no change",
    ]
  return out
}

/** Every limit the manifest sets, in a fixed order. A limit that is not
 * set is left out (the server omits or zeroes it). */
export function limitRows(l: ManifestLimits): [string, string][] {
  const rows: [string, string | undefined][] = [
    ["Max steps", l.max_steps ? count(l.max_steps) : undefined],
    [
      "Model retries",
      l.max_model_retries !== undefined
        ? count(l.max_model_retries)
        : undefined,
    ],
    [
      "Loop detection",
      l.detect_loops
        ? `after ${count(l.detect_loops)} repeated calls`
        : undefined,
    ],
    ["Parallel tool calls", l.parallelism ? count(l.parallelism) : undefined],
    [
      "Stop conditions",
      l.stop_when && l.stop_when.length > 0 ? list(l.stop_when) : undefined,
    ],
    [
      "Usage limit",
      l.max_input_tokens || l.max_output_tokens
        ? [
            l.max_input_tokens && `${count(l.max_input_tokens)} input tokens`,
            l.max_output_tokens &&
              `${count(l.max_output_tokens)} output tokens`,
          ]
            .filter(Boolean)
            .join(", ")
        : undefined,
    ],
    ["Tool timeout", l.timeout || undefined],
    [
      "Result cap",
      l.max_result_bytes
        ? `${bytes(l.max_result_bytes)} per tool result`
        : undefined,
    ],
    ["Strict input", l.strict_input ? "on" : undefined],
  ]
  return rows.filter((r): r is [string, string] => r[1] !== undefined)
}

/** One agent's entry in a stored manifest (weft.json's shape). */
export interface ManifestAgent {
  name?: string
  instructions?: string
  tools?: {
    name: string
    description?: string
    input_schema?: unknown
    [k: string]: unknown
  }[]
  [k: string]: unknown
}

/** manifestAgent finds an agent in a stored manifest: the one named,
 * else the first. */
export function manifestAgent(
  m: StoredManifest,
  name: string
): ManifestAgent | undefined {
  const agents = (m.manifest as { agents?: ManifestAgent[] }).agents ?? []
  return agents.find((a) => a.name === name) ?? agents.at(0)
}

/** moveError says why a label did not move. A 409 is the compare and
 * swap refusing: someone moved the label first. */
export function moveError(e: unknown, label: string): string {
  if (e instanceof ApiError && e.status === 409)
    return `The ${label} label was moved by someone else while this was open, so nothing changed. The versions below are current: check where ${label} points now and move it again if you still want to.`
  if (e instanceof ApiError && e.status === 403)
    return "This credential may not move labels."
  return e instanceof Error ? e.message : String(e)
}
