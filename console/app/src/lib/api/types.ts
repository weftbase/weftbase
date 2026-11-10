// The /v1 wire types, as the server returns them.
//
// Hand-written, not generated: api/openapi/v1.yaml types most bodies as
// "Loose" (an open object), so openapi-typescript would give `unknown`
// for runs' details, sessions, traces, stats and every content route.
// Each type here mirrors a Go struct's JSON tags in api/http/v1/*.go (the
// struct is named beside it) and was checked against the goldens in
// api/http/v1/testdata/v1 and a running server. Fields the Console does
// not read yet are still listed, so a later screen finds them typed.
//
// Rules that hold everywhere:
// - An absent optional field means "not applicable"; a null cost means
//   "unknown". Never read either as zero.
// - Times are RFC 3339 strings. Ids are opaque; a child run id contains
//   "/" (<parent>/<step>/<call>).

export interface Usage {
  input_tokens: number
  output_tokens: number
  cached_input_tokens?: number
  cache_write_tokens?: number
  reasoning_tokens?: number
  [k: string]: number | undefined
}

/** costFields (pricing.go). cost_usd is null when the price is unknown. */
export interface CostFields {
  cost_usd: number | null
  cost_known: boolean
  /** A word for the figure: "unknown", "unpriced", "scripted", … */
  cost_badge?: string
  cost_reason?: string
  cache?: string
  unpriced_models?: string[]
  /** An unknown sum's known part: `priced` of `of` parts, summing to usd. */
  cost_partial?: { usd: number; priced: number; of: number }
}

/** badgeFields (requests.go): a hole in the data, with its cause and fix. */
export interface Badge {
  badge?: string
  reason?: string
  fix?: string
}

/** stepHole / treeHole: something that is missing, and why. */
export interface Hole {
  hole: string
  cause?: string
  reason: string
  fix?: string
  count?: number
  runs?: string[]
}

// ── Runs ────────────────────────────────────────────────────────────

/** The four statuses a run row carries. */
export type RunRowStatus = "running" | "succeeded" | "failed" | "interrupted"

export interface ModelInfo {
  provider: string
  name: string
}

/** runRow (api.go). */
export interface RunRow extends CostFields {
  id: string
  /** "" for a top-level run. */
  parent_run_id: string
  parent_call_id: string
  trace_id: string
  agent: string
  model: ModelInfo
  manifest_hash: string
  weft_version: string
  service: string
  /** "" when the run belongs to no session. */
  session_id: string
  public_id: string
  turn: number
  forked_from: string
  meta: Record<string, string>
  started: string
  finished: string | null
  last_seen: string
  /** The trace store's word; absent on a ledger_only row (the trace
   * store does not hold the run). Draw display_status instead. */
  status?: RunRowStatus
  /** The status to show. Read it through displayStatus (status.ts). */
  display_status?:
    | "queued"
    | "running"
    | "waiting"
    | "succeeded"
    | "failed"
    | "cancelled"
    | "interrupted"
  /** What happened to the run's approvals, worst first (with chains=1
   * and on run detail). A run whose approval expired still ends
   * succeeded: the node resumes it with a deny and the model goes on. */
  approvals_outcome?: ApprovalsOutcome
  /** Usage and cost are what the run has spent so far (it is running). */
  usage_partial?: boolean
  /** The registry version the run was admitted on (agv_…), how it was
   * resolved (label:<name> | latest | pin | migrate) and where it ran
   * (embedded | local). Present on newer servers, and only for runs the
   * server admitted. */
  agent_version?: VersionRef
  resolved_via?: string
  placement?: string
  /** The manifest the run reported is not the one its version registered. */
  manifest_drift?: boolean
  /** A queued run the trace store does not hold yet: only the ledger's
   * fields are real, everything a trace would say is empty. */
  ledger_only?: boolean
  /** Set when a park and its resume link this run to others (one
   * logical run). Read it through loadChain (chain.ts). */
  chain?: ChainDoc
  settling?: boolean
  err: string
  steps: number
  /** Tool calls waiting on a person. With status "succeeded" and no
   * resumed_by, the run is parked (displayStatus in status.ts). */
  pending: number
  stop_reason: string
  stopped_by?: string
  cancel_cause?: string
  error_type?: string
  resumed_from?: string
  resumed_by?: string
  ended_by?: string
  ended_kind?: string
  caller_run_id?: string
  caller_call_id?: string
  usage: Usage
  own_usage?: Usage
  delegated_usage?: Usage
  delegated_cost_usd: number | null
  delegated_cost_known: boolean
  delegated_cost_reason?: string
  delegated_unpriced_models?: string[]
  event_count: number
  message_count: number
  delta_count: number
  events_emitted?: number
  messages_emitted?: number
  instructions_hash: string
  catalog_hash: string
  request_count: number
  requests_badge?: string
  derived?: boolean
  prompt_preview?: string
  children_count?: number
  children_failed?: number
  children_unknown?: boolean
  attempts_retried?: number
  fallbacks?: number
  tool_errors?: number
  denied?: number
  truncated?: number
  repaired?: number
  log_warn?: number
  log_error?: number
  session_compactions?: number
  delegated_sessions?: number
}

/** versionRef (runview.go): a run's AgentVersion, labels as they are now. */
export interface VersionRef {
  id: string
  number: number
  labels: string[] | null
}

/** events.Actor: who did something (an operator of the Console, or an
 * API key). */
export interface Actor {
  kind: "operator" | "key" | "worker" | "system" | (string & {})
  /** Absent for the server's own work (kind system: timers, sweeps). */
  id?: string
  /** The key id or session id it acted with. */
  credential?: string
  assurance?: string
  /** A display name, when the server has one. */
  name?: string
}

/** sessions.Approval: one tool call waiting on, or decided by, a person. */
export interface Approval {
  id: string
  run_id: string
  call_id: string
  agent: string
  /** The version the run ran, labels as they are now. */
  agent_version?: VersionRef
  tool: string
  args: unknown
  args_sha256: string
  /** pending | approved | denied | resolved | expired | cancelled */
  status: string
  requested_at: string
  outcome?: string
  reason?: string
  decided_at?: string
  actor?: Actor
  resumed_by?: string
  content?: string
}

/** approvalsPage: GET /v1/approvals. */
export interface ApprovalsPage {
  approvals: Approval[]
  total: number
  pending: number
  next_offset: number | null
}

/** waitDoc (runview.go): one wait on a person, from the park until the
 * resume started (`to` null while it lasts). */
export interface WaitDoc {
  run: string
  resumed_by?: string
  from: string
  to: string | null
  approvals?: Approval[] | null
  decided_by?: Actor[] | null
}

/** chainDoc (runview.go): one logical run across parks. */
/** approvals_outcome: pending > expired > denied > approved. */
export type ApprovalsOutcome = "pending" | "expired" | "denied" | "approved"

export interface ChainDoc {
  approvals_outcome?: ApprovalsOutcome
  approvals_expired?: number
  approvals_denied?: number
  approvals_approved?: number
  approvals_pending?: number
  runs: {
    id: string
    display_status: string
    started: string
    finished: string | null
  }[]
  display_status: string
  stopped_by?: string
  error_type?: string
  cancel_cause?: string
  err?: string
  started: string
  finished: string | null
  duration_ms: number
  waiting_ms: number
  steps: number
  usage: Usage
  cost_usd: number
  cost_known: boolean
  waits: WaitDoc[] | null
}

/** runsPage: GET /v1/runs without a search parameter (cursor-paged). */
export interface RunsPage {
  /** With a status, approvals or chain fold the match is folded after
   * it is counted: total is then an upper bound, exact only where
   * total_exact says so. */
  total: number
  total_exact: boolean
  /** The server stopped reading before the end (its time or row
   * budget): more runs may match than the pages show. */
  partial?: boolean
  runs: RunRow[]
  next_before: string | null
  next_before_id: string | null
  turn_marks_capped?: Hole
}

/** searchPage: GET /v1/runs with q, tool, model, error, since, until,
 * min_tokens, min_cost, sort or offset (offset-paged). */
export interface SearchPage {
  /** As on RunsPage: exact only where total_exact says so. */
  total: number
  total_exact: boolean
  /** As on RunsPage; also set past the deepest offset the server pages
   * to (next_offset is then null). */
  partial?: boolean
  runs: RunRow[]
  sort: "started" | "duration" | "tokens" | "cost"
  next_offset: number | null
  cost_sort_cap?: number
  facets?: {
    error_class: { class: string; count: number }[]
    error_class_capped?: boolean
  }
}

/** What the Console asks GET /v1/runs for. */
export interface RunsQuery {
  /** The status as shown. */
  display_status?: NonNullable<RunRow["display_status"]>
  agent?: string
  session?: string
  /** A run id: its children. "*": every run. Absent: top-level runs. */
  parent?: string
  id_prefix?: string
  /** The app's public id: the runs that carry it. */
  public_id?: string
  error_type?: string
  /** Runs whose approvals ended so (worst outcome of the chain). */
  approvals?: ApprovalsOutcome
  // Any of these makes the request a search:
  q?: string
  model?: string
  tool?: string
  since?: string
  until?: string
  sort?: SearchPage["sort"]
  min_cost?: number
  min_tokens?: number
  /** One metadata match, "key=value" (sent as tag.<key>=<value>). */
  tag?: string
  limit?: number
}

/** One page of runs, cursor- or offset-paged, in one shape. `next` is
 * what to pass back to api.runs for the following page; null on the last. */
export interface RunsList {
  /** An upper bound unless total_exact. */
  total: number
  total_exact: boolean
  partial?: boolean
  runs: RunRow[]
  next: RunsCursor | null
  facets?: SearchPage["facets"]
}
export type RunsCursor =
  { before: string; before_id?: string } | { offset: number }

/** decisionDoc (decisions.go): who decided a parked run's calls. */
export interface DecisionDoc {
  run: string
  at: string
  actor?: string
  actor_kind?: string
  source: string
  calls: {
    approval_id?: string
    call_id: string
    name?: string
    outcome: string
    via?: string
    who?: string
    reason?: string
    result?: string
    at?: string
    [k: string]: unknown
  }[]
  more?: number
  hole?: string
  notes?: string[]
  error?: string
}

export interface RunCompaction {
  scope: string
  index?: number
  step?: number
  from_seq?: number
  to_seq?: number
  hash: string
  replaced: number
  entries: number
  tokens_before?: number
  tokens_after?: number
  reason?: string
}

/** runPage: GET /v1/runs/{id}. */
export interface RunDetail extends RunRow {
  /** Subagent children, one level deep. */
  children: RunRow[]
  /** Every run under this one, at any depth; a floor when
   * children_total_truncated. */
  children_total: number
  children_total_truncated?: boolean
  holes: Hole[]
  compactions: RunCompaction[]
  manifest_available?: boolean
  caller_linked?: RunRow[]
  decided?: DecisionDoc | null
  resumes?: DecisionDoc | null
  /** The run's tool calls that wait on, or were decided by, a person. */
  approvals?: Approval[] | null
  summarizer?: { usage: Usage } & CostFields
}

/** LedgerRun (sessions/ledger.go): what admit, cancel and
 * GET /v1/runs/{id}/status answer. Its status words differ from a run
 * row's: queued and parked exist only here. */
export interface LedgerRun {
  id: string
  tenant_id: string
  agent: string
  agent_version: VersionRef
  /** The first run of the park-and-resume chain (the run itself when
   * it resumes nothing). */
  chain_id: string
  resolved_via: string
  status: "queued" | "running" | "parked" | "succeeded" | "failed" | "cancelled"
  metadata?: Record<string, string>
  input?: { prompt?: string; messages?: Message[] }
  resume_of?: string
  resumed_by?: string
  pending?: { call_id: string; tool: string; args?: unknown }[]
  error?: string
  cancel_cause?: string
  cancel_requested?: boolean
  usage: { input_tokens?: number; output_tokens?: number }
  created_at: string
  started_at?: string
  parked_at?: string
  finished_at?: string
}

/** AdmitRequest: POST /v1/runs. version is "label:<name>", an agv_… id,
 * or absent (the prod label, else the newest version). */
export interface AdmitRequest {
  agent: string
  version?: string
  input: { prompt?: string; messages?: Message[] }
  metadata?: Record<string, string>
}

// ── Run content ─────────────────────────────────────────────────────

/** A weft record body. `type` is run_start, step_start, tool_start,
 * tool_finish, step_finish, run_finish, …; the rest depends on it. */
export interface WeftEvent {
  type: string
  [k: string]: unknown
}

/** posEvent. */
export interface PosEvent {
  pos: number
  time: string
  event: WeftEvent
  attrs?: Record<string, unknown>
}

/** eventsPage: GET /v1/runs/{id}/events. */
export interface EventsPage extends Badge {
  events: PosEvent[]
  next_after: number | null
  done: boolean
  gaps: number[]
}

export type ContentPart =
  | { type: "text"; text: string }
  | { type: "tool_call"; id: string; name: string; args: unknown }
  | {
      type: "tool_result"
      call_id: string
      name: string
      content: string
      is_error: boolean
    }
  | { type: string; [k: string]: unknown }

export interface Message {
  role: "system" | "user" | "assistant" | "tool" | (string & {})
  content: ContentPart[]
}

export interface TranscriptBatch {
  index: number
  step: number
  /** true: the run's input; false: what the run produced. */
  input: boolean
  badge?: string
  messages: Message[] | null
}

/** transcript / transcriptAsOf: GET /v1/runs/{id}/transcript. With
 * step=, `messages` is the conversation as the model saw it at that step. */
export interface Transcript extends Badge {
  batches: TranscriptBatch[]
  step?: number
  messages?: Message[]
  compacted_at?: {
    index: number
    step: number
    from_seq: number
    to_seq: number
    hash: string
    replaced: number
    entries: number
  } | null
}

/** spanDTO: one OTel span. Kind is the OTel span kind (internal, client,
 * …); what the span *is* comes from attrs (see lib/span-kind.ts). */
export interface Span {
  trace_id: string
  span_id: string
  /** "" for a root span. */
  parent_span_id: string
  name: string
  kind: string
  start: string
  end: string
  status: "ok" | "error" | "unset" | (string & {})
  status_message: string
  service: string
  attrs: Record<string, unknown>
  events: { time: string; name: string; attrs: Record<string, unknown> }[]
  /** Set by tree=1: the run each span belongs to. */
  run_id?: string
  parent_run_id?: string | null
}

/** spansDoc / treeSpansDoc: GET /v1/runs/{id}/spans (tree=1 adds every
 * descendant run's spans and `holes`). */
export interface SpansDoc {
  spans: Span[]
  holes?: Hole[]
  /** With chain=1: the run ids of the chain, oldest first, and the
   * waits on a person between them. */
  chain?: string[]
  waits?: WaitDoc[] | null
}

export interface ToolEntry {
  name: string
  description: string
  schema: unknown
  approval: boolean
  /** local | subagent | an MCP server's name. */
  source: string
  timeout_ms?: number
  max_result_bytes?: number
  [k: string]: unknown
}

export interface Catalog {
  hash: string
  tools: ToolEntry[]
  content: string
  truncated_bytes: number
}

/** requestRow + requestWire: one model-call attempt. */
export interface ModelRequest {
  index: number
  step: number
  attempt: number
  time: string
  system_hash: string
  catalog_hash: string
  content: string
  truncated_bytes: number
  body: {
    step: number
    attempt: number
    messages_ref?: { index: number; count: number }
    tools?: { catalog_hash: string; names: string[] }
    params?: Record<string, unknown>
    model?: ModelInfo
    stream?: boolean
    [k: string]: unknown
  }
  prompt?: { hash: string; text: string; content: string } | Badge
  tools?: Catalog | Badge
  messages?: Message[] | null
  raw: unknown
  raw_badge?: string
  raw_reason?: string
  raw_fix?: string
}

/** requestsPage: GET /v1/runs/{id}/requests. */
export interface RequestsPage {
  requests: ModelRequest[]
  next_from?: number
}

/** stepDoc: GET /v1/runs/{id}/steps/{n}, one step assembled. */
export interface StepDoc {
  run_id: string
  step: number
  status: string
  reason?: string
  started?: string
  finished?: string
  latency_ms?: number
  ttft_ms?: number
  model: { provider?: string; requested: string; answered?: string }
  /** Attempt 1's request record; a badge when there is none (a derived
   * run, a request hole, a model call whose record was never stored).
   * Read it through stepRequest (lib/api/steps.ts). */
  request?: ModelRequest | Badge
  attempts: {
    attempt: number
    model: string
    provider?: string
    outcome?: string
    error_type?: string
    error?: string
    http_status?: number
    wait_ms?: number
    started?: string
    finished?: string
    span_id?: string
    request_index?: number
    badge?: string
  }[]
  attempts_badge?: Badge
  messages_in: { index?: number; count: number }
  events: PosEvent[]
  tool_calls: {
    call_id: string
    name: string
    seq?: number
    args: unknown
    result?: {
      content: string
      is_error: boolean
      bytes: number
      truncated?: boolean
    }
    span?: { id: string; started: string; finished: string; status: string }
    child_run_id?: string
    pending?: boolean
    badge?: string
  }[]
  children: {
    id: string
    call_id: string
    agent: string
    status: string
    usage: Usage
  }[]
  usage: Usage
  cost_usd?: number | null
  cost_known?: boolean
  cost_reason?: string
  holes: Hole[]
}

export interface LogRow {
  index: number
  time: string
  severity: string
  severity_number: number
  event_name?: string
  body: string
  attrs: Record<string, unknown>
  span_id?: string
}

/** logsPage: GET /v1/runs/{id}/logs. */
export interface LogsPage extends Badge {
  logs: LogRow[]
  next_from?: number
  partial?: boolean
  partial_reason?: string
  holes: Hole[]
}

// ── Sessions ────────────────────────────────────────────────────────

/** A session's status is its newest turn's outcome: a run status, or
 * "parked" (waiting on a person), "resumed" (a park whose resume has not
 * reported yet), "cancelled", "ended" (a newer message ended the turn). */
export type SessionStatus =
  RunRowStatus | "parked" | "resumed" | "cancelled" | "ended"

/** sessionRow. */
export interface SessionRow extends CostFields {
  id: string
  public_id: string
  agent: string
  /** Turns as a person counts them: a resume is part of the turn it
   * continues. */
  turns: number
  first_seen: string
  last_seen: string
  status: SessionStatus
  /** The one word a list draws (a run's display_status words): the
   * ledger's where it holds the newest turn. Read it through
   * sessionStatus (status.ts). */
  display_status: NonNullable<RunRow["display_status"]>
  usage: Usage
  /** What the thread's summarizer model calls cost the session: apart
   * from usage, never folded into it nor priced. Absent when it
   * summarized nothing. */
  summarizer_usage?: Usage
  title?: string
  first_prompt?: string
  content_off?: boolean
  pending?: number
  failed_turns?: number
  ended_turns?: number
  cancelled_turns?: number
  resumes?: number
  outcomes?: string[]
  forked_from_session?: string
  parent_session?: string
  parent_turn?: number
  parent_run?: string
  derived?: boolean
}

export interface SessionsPage {
  total: number
  sessions: SessionRow[]
  next_before: string | null
  next_before_id: string | null
}

export interface SessionsQuery {
  q?: string
  agent?: string
  /** running | succeeded | failed | interrupted (the newest turn). */
  status?: RunRowStatus
  /** true: sessions with a turn waiting on a person. */
  awaiting?: boolean
  /** Exact match on the app's own id for the conversation. */
  public_id?: string
  failed?: boolean
  since?: string
  id_prefix?: string
  before?: string
  before_id?: string
  limit?: number
}

/** sessionDoc: GET /v1/sessions/{id}. `runs` are its turns, oldest first. */
export interface SessionDetail extends SessionRow {
  runs: RunRow[]
  /** The raw count of runs is `runs.length` here; a row of the list and
   * the conversation's `session` carry it as a number (SessionFacts). */
  delegated: SessionRow[]
  delegated_usage: Usage
  delegated_sessions_cost_usd: number | null
  delegated_sessions_cost_known: boolean
}

/** sessionEntry: one state-log mark (approval, compaction, fork, …). */
export interface SessionEntry {
  at: string
  fact_at?: string
  record: string
  /** approval.request, approval.decision, compaction, receipt, … */
  kind: string
  event: string
  lane: string
  run_id: string | null
  turn: number | null
  entry?: string
  content_mark: string
  fields: Record<string, unknown>
  content?: Record<string, unknown>
  actor?: string
  /** "resume": `actor` lists the resume's several deciders. */
  actor_of?: string
}

export interface TimelineTurn {
  run_id: string
  turn: number
  agent: string
  status: RunRowStatus
  started: string
  finished: string | null
  last_seen: string
  steps: number
  pending?: number
  prompt_preview?: string
  resumed_from?: string
  resumed_by?: string
  stopped_by?: string
  error_type?: string
  usage: Usage
  marks: number
}

/** timelineDoc: GET /v1/sessions/{id}/timeline. */
export interface SessionTimeline {
  session_id: string
  public_id?: string
  agent: string
  status: SessionStatus
  turns: TimelineTurn[]
  marks: SessionEntry[]
  lanes: { lane: string; marks: number }[]
  marks_total: number
  holes: Hole[]
  forks?: {
    session_id: string
    entry?: string
    run_id: string | null
    turns: number
    status?: string
  }[]
}

/** conversationTurn (conversation.go): one turn as a person reads it. A
 * park and its resumes are one turn (`runs` has every run). */
export interface ConversationTurn {
  /** The number the app's thread gave the turn. It can jump: a resume
   * or a steered message takes a number of its own. */
  turn: number
  /** The turn's position in the session as a person counts (1-based);
   * absent from a server that does not send it yet. */
  index?: number
  /** What happened to the turn's approvals, worst first. */
  approvals_outcome?: ApprovalsOutcome
  /** The turn's first run; `runs` every run of it, oldest first. */
  run_id: string
  runs: string[]
  /** The user's message; with input_known false it is the prompt
   * preview or "" (the run's messages were not recorded). */
  input: string
  input_known: boolean
  /** null while there is none (the turn failed, waits or still runs). */
  reply: string | null
  display_status: NonNullable<RunRow["display_status"]>
  stopped_by?: string
  error_type?: string
  cancel_cause?: string
  err?: string
  /** What ended a turn a newer message felled: the run and turn that
   * did, and how (interrupt | rollback). */
  ended_by?: string
  ended_by_turn?: number
  ended_kind?: string
  started: string
  finished: string | null
  duration_ms: number
  waiting_ms: number
  model_calls: number
  tool_calls: number
  usage: Usage
  cost_usd: number | null
  cost_known: boolean
  agent_version?: VersionRef
  waits: WaitDoc[] | null
  marks: SessionEntry[] | null
  compactions?: number
  delegated_sessions?: number
  /** The turn was rolled back: the conversation went on without it. */
  off_path?: boolean
}

/** conversationDoc: GET /v1/sessions/{id}/conversation, paged by turn. */
export interface Conversation {
  session_id: string
  public_id?: string
  agent: string
  title?: string
  status: SessionStatus
  turns: ConversationTurn[]
  /** The session's turns, chains folded. */
  total_turns: number
  offset: number
  next_offset: number | null
  /** Marks filed under no turn. */
  session_marks: SessionEntry[] | null
  marks_capped?: boolean
  /** The whole session row: totals, cost, times, lineage. */
  session?: SessionFacts
  /** The sessions this one handed work to. */
  delegated?: SessionFacts[] | null
  /** Turn numbers no turn of the session carries, each with the
   * server's reason when it knows one. */
  skipped_turns?: SkippedTurn[] | null
}

/** A turn number with no turn of its own (GET …/conversation,
 * `skipped_turns`). */
export interface SkippedTurn {
  turn: number
  reason?: "resumed" | "steered" | "deferred" | "dropped" | "not_recorded"
  /** The turn number it went into (resumed, steered). */
  into?: number
  run_id?: string
  /** The server's sentence for it. */
  note?: string
}

/** sessionRow as GET /v1/sessions and the conversation send it: the
 * row plus `runs`, the raw number of runs (resumes included). */
export interface SessionFacts extends SessionRow {
  runs?: number
}

/** entriesDoc: GET /v1/sessions/{id}/entries. */
export interface SessionEntries {
  session_id: string
  entries: SessionEntry[]
  total: number
  offset: number
  next_offset: number | null
  holes: Hole[]
}

// ── Traces ──────────────────────────────────────────────────────────

/** traceRow. */
export interface TraceRow extends Badge {
  trace_id: string
  root: { span_id: string; name: string } | null
  service: string
  start: string
  end: string
  duration_ms: number
  span_count: number
  error: boolean
  run_ids: string[]
  root_run?: {
    id: string
    agent?: string
    prompt_preview?: string
    status: string
  }
  failed_runs?: number
}

export interface TracesPage {
  total: number
  traces: TraceRow[]
  next_before: string | null
  next_before_id: string | null
}

/** traceDoc: GET /v1/traces/{id}. */
export interface TraceDetail {
  spans: Span[]
  run_ids: string[]
  not_run?: { reason: string; fix: string }
}

// ── Stats ───────────────────────────────────────────────────────────

export type StatsBy = "agent" | "model" | "tool" | "error_type" | "step"

export interface StatsBucket {
  runs: number
  failed: number
  calls: number
  failed_calls: number
  input_tokens: number
  output_tokens: number
}

export interface StatsGroup extends CostFields {
  key: string
  runs: number
  failed: number
  failure_rate: number | null
  calls: number
  failed_calls: number
  input_tokens: number
  output_tokens: number
  duration_ms: { p50: number; p95: number; n: number } | null
  ttft_ms: { p50: number; p95: number; n: number } | null
  retried: number
  retry_rate: number | null
  denials: number
  /** One bucket per `bucket_ms` from `since`, oldest first. */
  series: StatsBucket[]
  runs_query?: string
  /** by=agent: the group's runs by display status, whole window and
   * per bucket. Prefer these to runs / failed (whose failed counts
   * cancelled runs too). */
  outcomes?: OutcomeBucket
  outcome_series?: OutcomeBucket[]
}

/** statsBody: GET /v1/stats. `by` is required by the server; there is no
 * ungrouped total, so the Console sums the groups (lib/stats.ts). */
export interface Stats {
  by: StatsBy
  since: string
  until: string
  buckets: number
  bucket_ms: number
  bucket_starts?: string[]
  groups: StatsGroup[]
  groups_cut?: number
}

// ── Agents ──────────────────────────────────────────────────────────

/** registry.Agent. */
export interface Agent {
  id: string
  name: string
  /** label → agv_… */
  labels: Record<string, string>
  versions: number
  created_at: string
  /** label → the version it points to, with its number. */
  label_versions?: Record<string, { id: string; number: number }>
}

/** manifestDrift: whether the runs of a version reported the manifest
 * the version registered. */
export interface ManifestDrift {
  drift: boolean
  runs_checked: number
  runs_drifted: number
  /** The oldest drifted run read; null when none drifted. */
  since: string | null
  last_run_id?: string
  manifest_hash?: string
}

/** registry.AgentVersion. */
export interface AgentVersion {
  id: string
  agent: string
  number: number
  build: string
  build_digest: string
  manifest_hash: string
  content_hash: string
  labels: string[] | null
  created_at: string
}

export interface StoredManifest {
  manifest_hash: string
  agent: string
  source: string
  first_seen: string
  current_manifest_hash: string
  manifest: Record<string, unknown>
}

// ── Meta, pricing, auth ─────────────────────────────────────────────

export interface Principal {
  /** operator: a Console session. key: an API key. local: `weftbase dev`
   * answering its own machine without a credential. */
  kind: "key" | "operator" | "local"
  id: string
  key_kind?: "sk" | "rk" | "pk" | "wk"
  scope: { project: string; env: "test" | "live"; tenant: string }
  all_tenants: boolean
}

/** metaDoc: GET /v1/meta. */
export interface Meta {
  weft_version: string
  server_version: string
  capabilities: string[]
  /** What this server cannot do, and why. */
  capabilities_off?: { name: string; reason: string }[]
  principal: Principal
  ingest?: {
    /** Services that reported records lost on the way here. */
    dropped: { service: string; records: number; last_heard?: string }[]
    accepted: number
    refused: number
    last_error?: string
    last_at?: string
  }
  pricing: {
    source: "default" | "file" | "option"
    path?: string
    dated: string
    models: number
  }
  db: { kind: string; path?: string; size?: number }
  interrupted_after_ms: number
  content: {
    ingest: string
    latest: { run_id: string; mark: string; note: string; fix?: string } | null
    error?: string
  }
  /** A Go duration ("720h"), or null: everything is kept. */
  retention: string | null
  manifest_check: {
    source: string
    agents: number
    checked: number
    stale: string[]
    error?: string
  } | null
  pid?: number
}

/** PricingTable: GET /v1/settings/pricing. USD per 1M tokens. */
export interface PricingTable {
  source: string
  path?: string
  dated: string
  note?: string
  models: {
    provider?: string
    model_glob: string
    input: number
    output: number
    cached_input?: number
    cache_write?: number
    reasoning?: number
  }[]
}

export interface SignInResponse {
  operator: string
  expires_at: string
}

// ── Live stream frames (SSE) ────────────────────────────────────────

/** `event: run`: a run row changed. */
export interface RunFrame {
  run: RunRow
}

/** `event: record`: one weft record as ingested. */
export interface RecordFrame {
  run_id: string
  session_id: string
  public_id: string
  kind: "event" | "delta" | "messages"
  pos: number
  time: string
  trace_id: string
  span_id: string
  event: WeftEvent
  attrs?: Record<string, unknown>
}

/** `event: attempt`: one try inside a model call. */
export interface AttemptFrame {
  run_id: string
  session_id?: string
  step: number
  attempt: number
  status: "ok" | "error"
  error_type?: string
  provider?: string
  model?: string
  start: string
  end: string
  wait_ms?: number
}

export type StreamFrame =
  | { type: "run"; id: string; data: RunFrame }
  | { type: "record"; id: string; data: RecordFrame }
  | { type: "attempt"; id: string; data: AttemptFrame }
  | { type: "backfill_error"; id: string; data: Record<string, unknown> }

// ── Inventory: agents and tools (agentview.go) ──────────────────────

/** agentStats: an agent's top-level runs over a window, by display
 * status. failure_rate = failed / (succeeded + failed). */
export interface AgentStats {
  since: string
  until: string
  runs: number
  succeeded: number
  failed: number
  cancelled: number
  waiting: number
  failure_rate: number | null
  last_run_id?: string
  last_run_at?: string
}

/** One row of GET /v1/agents?include=summary. `registered` false: the
 * agent was only seen in runs (a subagent, a trace); its registry
 * fields are then empty. */
export interface AgentSummary extends Agent {
  registered: boolean
  summary: {
    /** The newest version (agv_…) and its number; "" and 0 when none. */
    version: string
    number: number
    model: ModelInfo
    placement: string
    tools: number
    subagents: number
    needs_approval: number
    stats: AgentStats
  }
}

/** GET /v1/agents?include=summary. */
export interface AgentsSummary {
  agents: AgentSummary[]
  totals: { agents: number; tools: number; placements: string[] | null }
}

export interface ManifestTool {
  name: string
  description: string
  needs_approval: boolean
  /** The agent this tool hands off to. */
  subagent?: string
  timeout?: string
}

/** The limits a version's manifest sets; absent keys are not set. */
export interface ManifestLimits {
  max_steps?: number
  parallelism?: number
  timeout?: string
  max_result_bytes?: number
  max_model_retries?: number
  detect_loops?: number
  strict_input?: boolean
  stop_when?: string[]
  max_input_tokens?: number
  max_output_tokens?: number
}

/** What changed against the previous version (also the body of
 * GET /v1/agents/{name}/versions/{a}/diff/{b}). */
export interface VersionChanges {
  from: string
  to: string
  same_manifest: boolean
  build_changed: boolean
  model_changed: { from: ModelInfo; to: ModelInfo } | null
  instructions_changed: boolean
  tools_added: string[] | null
  tools_removed: string[] | null
  /** A tool both versions have, and the fields that differ. */
  tools_changed: { name: string; fields: string[] }[] | null
  limits_changed: { field: string; from: unknown; to: unknown }[] | null
}

/** A version as GET /v1/agents/{name} returns it. */
export interface VersionDetail extends AgentVersion {
  placement: string
  manifest_drift?: ManifestDrift
  /** null when the registered manifest could not be read. */
  manifest: {
    model: ModelInfo
    instructions: { present: boolean; length: number }
    tools: ManifestTool[] | null
    subagents: string[] | null
    limits: ManifestLimits
  } | null
  /** null for the first version. */
  changes: VersionChanges | null
}

/** GET /v1/agents/{name}. */
export interface AgentDetail extends Agent {
  placement: string
  model: ModelInfo
  newest: VersionDetail | null
  by_label: Record<string, VersionDetail>
  /** Newest first. */
  version_list: VersionDetail[]
  stats: AgentStats
}

/** One tool of GET /v1/tools: every agent version that has it. */
export interface ToolRow {
  name: string
  description: string
  needs_approval: boolean
  subagent?: string
  agents: {
    agent: string
    versions: {
      id: string
      number: number
      labels: string[] | null
      needs_approval: boolean
    }[]
  }[]
  /** null: no call in the window. */
  stats: {
    calls: number
    failed_calls: number
    failure_rate: number | null
    duration_ms: { p50: number; p95: number; n: number } | null
  } | null
}

export interface ToolsDoc {
  tools: ToolRow[]
  since: string
  until: string
}

// ── Stats by=total (stats.go) ───────────────────────────────────────

/** Runs by display status over one bucket, or over the whole window.
 * failure_rate = failed / (succeeded + failed): cancelled runs are not
 * failures. cost_known false: cost_usd is the priced part only. */
export interface OutcomeBucket {
  t: string
  runs: number
  /** Runs the ledger holds that have not started (by=total). */
  queued: number
  succeeded: number
  failed: number
  cancelled: number
  waiting: number
  running: number
  interrupted: number
  failure_rate: number | null
  input_tokens: number
  output_tokens: number
  /** Approvals that lapsed with nobody deciding, and that a person
   * refused, by when that happened (by=total; 0 elsewhere). */
  approvals_expired: number
  approvals_denied: number
  cost_usd: number
  cost_known: boolean
  duration_ms: { p50: number; p95: number; n: number } | null
}

/** GET /v1/stats?by=total. */
export interface StatsTotal {
  by: "total"
  since: string
  until: string
  buckets: number
  bucket_ms: number
  bucket_starts: string[]
  total: OutcomeBucket
  series: OutcomeBucket[]
  capped?: boolean
}

// ── The outbox stream (GET /v1/events) ──────────────────────────────

/** One outbox event. `data` depends on `type`. */
export interface OutboxEvent {
  seq: number
  id: string
  type: string
  resource_kind: string
  resource_id: string
  actor?: Actor
  data?: Record<string, unknown>
  created_at: string
}

// ── Compare (GET /v1/diff) ──────────────────────────────────────────

/** diffSide (diff.go): one run's step, reduced to the compared columns. */
export interface DiffSide {
  /** ok | parked | error | … (the step's own status). */
  status: string
  system_hash: string
  system: string | null
  /** args: canonical JSON (sorted keys). */
  tool_calls: { name: string; args: unknown }[]
  tool_results: {
    call_id: string
    name: string
    content: string
    is_error: boolean
  }[]
  /** null: no record of the step holds its assistant message. */
  text: string | null
  usage: Usage
  /** subagent, parked, error, compaction, … */
  marks: string[]
  holes: Hole[]
}

/** The columns a diff row can name in changes / unknown / differs. */
export type DiffColumn =
  "system" | "tool_calls" | "tool_results" | "text" | "usage" | "missing"

/** diffRow: one aligned step. a_step / b_step are each side's own
 * ordinal (null when that side has no step here). */
export interface DiffRow {
  step: number
  a_step: number | null
  b_step: number | null
  /** A source step the replaying side did not re-run: nothing compared. */
  kept: boolean
  kept_by?: "a" | "b"
  changed: boolean
  a: DiffSide | null
  b: DiffSide | null
  changes: DiffColumn[]
  /** Columns that could not be compared (a side did not record them). */
  unknown: DiffColumn[]
  /** Columns that differ without counting as a change. */
  differs: DiffColumn[]
}

/** diffDoc: GET /v1/diff?a=&b=. */
export interface DiffDoc {
  a: { run_id: string; steps: number; status: string }
  b: { run_id: string; steps: number; status: string }
  aligned: {
    replay: "a" | "b" | "both"
    source: string
    a_from: number
    b_from: number
  } | null
  steps: DiffRow[]
  summary: {
    changed_steps: number[]
    first_changed: number | null
    kept: number
  }
  holes: Hole[]
}
