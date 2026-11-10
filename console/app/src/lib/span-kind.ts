// What a span is, for the trace waterfall. The one map every waterfall,
// legend and detail view reads (CONVENTIONS.md, "Waterfall").
//
// Colour is the AGENT's lane (lib/lanes.ts), never the kind. Kind is
// carried by the icon and by the bar's style:
//   agent    Bot              summary bar: its children's segments on one line
//   model    Sparkles         solid pill in the lane hue
//   tool     Wrench           the lane hue dimmed, shorter (h-2.5)
//   handoff  CornerDownRight  as tool, plus a connector line in the child's hue
//   waiting  Hourglass        diagonal hatch outline on neutral (span-hatch)
//   failed   OctagonX         danger pill with a ✕; the row's name is red
//   other    Circle           thin neutral bar (a stock OTel span)
//
// Classification reads the OTel GenAI attributes weft writes:
//   gen_ai.operation.name = invoke_agent  → agent
//   gen_ai.operation.name = execute_tool  → tool, or handoff when the tool
//       is a subagent (weft.tool.origin = "subagent")
//   any other gen_ai.operation.name (chat, …) → model
//   no gen_ai.operation.name              → other
// "waiting" and "failed" are states laid over a kind.

import {
  BotIcon,
  CircleIcon,
  CornerDownRightIcon,
  HourglassIcon,
  OctagonXIcon,
  SparklesIcon,
  WrenchIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import type { Span } from "./api/types"

export type SpanKind =
  "agent" | "model" | "tool" | "handoff" | "waiting" | "failed" | "other"

/** How the bar is drawn. The lane supplies the hue for solid and dim. */
export type BarStyle = "summary" | "solid" | "dim" | "hatch" | "failed" | "thin"

export interface SpanKindInfo {
  label: string
  icon: LucideIcon
  bar: BarStyle
  /** In the footer legend (the reference shows five). */
  legend: boolean
}

export const SPAN_KINDS: Record<SpanKind, SpanKindInfo> = {
  agent: { label: "Agent", icon: BotIcon, bar: "summary", legend: false },
  model: {
    label: "Model call",
    icon: SparklesIcon,
    bar: "solid",
    legend: true,
  },
  tool: { label: "Tool call", icon: WrenchIcon, bar: "dim", legend: true },
  waiting: {
    label: "Waiting",
    icon: HourglassIcon,
    bar: "hatch",
    legend: true,
  },
  handoff: {
    label: "Handoff",
    icon: CornerDownRightIcon,
    bar: "dim",
    legend: true,
  },
  failed: { label: "Failed", icon: OctagonXIcon, bar: "failed", legend: true },
  other: { label: "Other span", icon: CircleIcon, bar: "thin", legend: false },
}

/** The order of the legend. */
export const SPAN_KIND_ORDER: readonly SpanKind[] = [
  "model",
  "tool",
  "waiting",
  "handoff",
  "failed",
]

const attr = (s: Span, k: string): string | undefined => {
  const v = s.attrs[k]
  return typeof v === "string" ? v : undefined
}

/** baseKind is what the span is, before any state is laid over it. */
export function baseKind(span: Span): Exclude<SpanKind, "waiting" | "failed"> {
  const op = attr(span, "gen_ai.operation.name")
  if (op === "invoke_agent" || op === "create_agent") return "agent"
  if (op === "execute_tool")
    return attr(span, "weft.tool.origin") === "subagent" ? "handoff" : "tool"
  if (op) return "model"
  return "other"
}

/** spanFailed: the span ended with an error. */
export function spanFailed(span: Span): boolean {
  return span.status === "error"
}

/** spanKind is the kind to draw: failed wins over the base kind. Pass
 * waiting for a tool call the run is parked on (the caller knows that
 * from the run's pending calls, not from the span). */
export function spanKind(span: Span, waiting = false): SpanKind {
  if (spanFailed(span)) return "failed"
  if (waiting) return "waiting"
  return baseKind(span)
}

// weft names its spans "<operation> <subject>" (invoke_agent planner,
// execute_tool search, chat gpt-x); the subject alone is the fallback.
const subject = (name: string) =>
  name.replace(/^(invoke_agent|create_agent|execute_tool|chat)\s+/, "")

/** spanLabel is the row's name: the tool for a tool call, the agent for
 * an agent run, the model for a model call, else the span's own name. */
export function spanLabel(span: Span): string {
  switch (baseKind(span)) {
    case "agent":
      return attr(span, "gen_ai.agent.name") ?? subject(span.name)
    case "tool":
    case "handoff":
      return attr(span, "gen_ai.tool.name") ?? subject(span.name)
    case "model":
      return (
        attr(span, "gen_ai.response.model") ??
        attr(span, "gen_ai.request.model") ??
        subject(span.name)
      )
    default:
      return span.name
  }
}

/** spanSecondary is the one muted word after the name: the model for an
 * agent, the provider for a model call, the source for a tool call. */
export function spanSecondary(span: Span): string | undefined {
  switch (baseKind(span)) {
    case "agent":
      return attr(span, "gen_ai.request.model")
    case "model":
      return attr(span, "gen_ai.provider.name")
    case "tool":
    case "handoff":
      return attr(span, "weft.tool.origin")
    default:
      return span.service || undefined
  }
}
