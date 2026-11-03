// Resource shapes of the public /v1 API (WEFTBASE-ARCHITECTURE §4.4).
//
// Hand-written for now: the server and api/openapi/v1.yaml are not ready.
// When they are, `bun run api:gen` writes src/lib/api/schema.d.ts with
// openapi-typescript and these become aliases of the generated
// components["schemas"] types. Keep the names and fields close to the
// spec so that swap is mechanical.

/** The environment a project runs in. The top bar switches between them. */
export type Env = "test" | "live"

export type RunStatus =
  "queued" | "running" | "waiting" | "succeeded" | "failed" | "cancelled"

/** A cost in US dollars. null means the price is unknown; the UI shows
 * "unknown", never "$0.00". */
export type Cost = number | null

export interface Usage {
  input_tokens: number
  output_tokens: number
}

/** A row of GET /v1/runs. */
export interface RunSummary {
  id: string
  agent: string
  agent_version: string
  status: RunStatus
  model: string
  tenant: string
  session_id: string | null
  started_at: string
  /** Milliseconds; null while the run is still going. */
  duration_ms: number | null
  usage: Usage
  /** This run's own model spend. */
  cost: Cost
  /** Spend of the child runs it delegated to. */
  delegated_cost: Cost
  error_class: string | null
  tools: string[]
  tags: string[]
}

export type StepKind = "model" | "tool" | "subagent" | "approval"

/** One step of a run (GET /v1/runs/{id}, steps sub-resource). */
export interface Step {
  index: number
  kind: StepKind
  /** The tool, subagent or model name. */
  name: string
  status: "ok" | "error" | "waiting" | "running"
  /** Milliseconds from the run's start. */
  offset_ms: number
  duration_ms: number | null
  summary: string
  usage: Usage | null
  cost: Cost
  /** The raw request and response, as recorded. */
  request: unknown
  response: unknown
  /** For a subagent step: the child run id. */
  child_run_id?: string
  /** For an approval step: the approval id. */
  approval_id?: string
}

export interface TranscriptMessage {
  role: "system" | "user" | "assistant" | "tool"
  text: string
}

/** GET /v1/runs/{id} with its steps and transcript. */
export interface Run extends RunSummary {
  trace_id: string
  input: string
  output: string | null
  error: string | null
  steps: Step[]
  transcript: TranscriptMessage[]
}

export interface RunFilters {
  status?: RunStatus
  agent?: string
  model?: string
  tool?: string
  error_class?: string
  tenant?: string
  session?: string
  q?: string
  limit?: number
  cursor?: string
}

export interface Page<T> {
  items: T[]
  next_cursor: string | null
  total: number
}

export type SessionState = "active" | "idle" | "waiting" | "closed"

export interface SessionSummary {
  id: string
  agent: string
  tenant: string
  state: SessionState
  turns: number
  started_at: string
  last_activity_at: string
  usage: Usage
  cost: Cost
}

export interface SessionTurn {
  index: number
  run_id: string
  status: RunStatus
  at: string
  input: string
  output: string | null
  agent_version: string
}

export interface SessionEntry {
  at: string
  kind: "turn" | "approval" | "compaction" | "fork"
  text: string
  run_id?: string
}

export interface Session extends SessionSummary {
  turn_list: SessionTurn[]
  timeline: SessionEntry[]
}

export interface SessionFilters {
  state?: SessionState
  agent?: string
  q?: string
}

export interface TraceSummary {
  id: string
  root: string
  service: string
  /** "weft" for weft runs, "genai" for stock GenAI spans. */
  source: "weft" | "genai"
  span_count: number
  started_at: string
  duration_ms: number
  error: boolean
}

export interface Span {
  id: string
  parent_id: string | null
  name: string
  kind: "run" | "step" | "model" | "tool" | "http" | "internal"
  offset_ms: number
  duration_ms: number
  error: boolean
  attributes: Record<string, string | number | boolean>
}

export interface Trace extends TraceSummary {
  run_id: string | null
  spans: Span[]
}

export type Label = "dev" | "staging" | "prod"

export interface AgentVersion {
  version: string
  manifest_hash: string
  created_at: string
  labels: Label[]
  model: string
  note: string
}

export interface Agent {
  name: string
  description: string
  model: string
  tools: string[]
  /** Tools that pause for a person before they run. */
  approval_tools: string[]
  middleware: string[]
  budget: { max_tokens: number; max_cost: number } | null
  labels: Partial<Record<Label, string>>
  versions: AgentVersion[]
  stats: {
    runs_24h: number
    failed_24h: number
    p50_ms: number
    cost_24h: Cost
  }
}

export type ApprovalStatus = "pending" | "approved" | "denied" | "expired"

export interface Approval {
  id: string
  run_id: string
  agent: string
  tool: string
  tenant: string
  status: ApprovalStatus
  requested_at: string
  /** One sentence the agent wrote for the person deciding. */
  summary: string
  /** The raw tool arguments, exactly as the model produced them. */
  args: Record<string, unknown>
  /** sha256 of the canonical args; a decision binds to it. */
  args_sha256: string
  decided_at: string | null
  decided_by: string | null
  reason: string | null
}

export interface ApprovalFilters {
  status?: "pending" | "decided"
  agent?: string
  tool?: string
}

export interface Decision {
  approve: boolean
  reason?: string
  args_sha256: string
}

export interface Stats {
  window: "24h"
  runs_by_status: Record<RunStatus, number>
  failures_by_error_class: { error_class: string; count: number }[]
  cost_today: Cost
  tokens_today: number
  pending_approvals: number
}

export interface Meta {
  version: string
  commit: string
  workspace: string
  hosting: "self-hosted"
  env: Env
  data_dir: string
  database: string
  retention_days: number
  content_policy: "full" | "redacted" | "metadata"
  started_at: string
}

export interface Price {
  provider: string
  model: string
  input_per_mtok: number
  output_per_mtok: number
  source: "built-in" | "override"
}
