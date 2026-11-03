-- 0001 — the obsdb schema (ADR 0024, S3.4). One logical schema for two
-- dialects: this one and obsdb/clickhouse's.
--
-- Tenancy: every table carries project_id, env and tenant_id, and those
-- three lead every primary key and unique index, so an id colliding
-- across tenants never dedups into another tenant's row. Until tenancy
-- fills them from the credential they hold the defaults below; writes
-- leave them to the defaults, and every ON CONFLICT target names them.
-- The lookup indexes on the old keys (spans_key, records_key, runs_id,
-- …) serve the reads, which do not filter by tenant yet.
--
-- The dedup key is the transport-level idempotency (I4): (run, kind,
-- pos) for records, (trace, span) for spans — a retried batch is a
-- no-op (INSERT OR IGNORE). Deltas and heartbeats are never inserted
-- (their rules live in Write); KeepDeltas turns delta rows on for
-- debugging, keyed by their own counter so they can never collide with
-- the durable sequence.
--
-- obsdb_migrations is created by the runner before this body runs.

CREATE TABLE spans (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  trace_id TEXT NOT NULL, span_id TEXT NOT NULL, parent_span_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL, kind INTEGER NOT NULL,
  start_ns INTEGER NOT NULL, end_ns INTEGER NOT NULL,
  status_code INTEGER NOT NULL, status_message TEXT NOT NULL DEFAULT '',
  service TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '', step INTEGER NOT NULL DEFAULT -1, tool_seq INTEGER NOT NULL DEFAULT -1,
  session_id TEXT NOT NULL DEFAULT '',
  attrs TEXT NOT NULL,      -- JSON object
  resource TEXT NOT NULL,   -- JSON object
  events TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (project_id, env, tenant_id, trace_id, span_id)
) WITHOUT ROWID;
CREATE INDEX spans_key ON spans(trace_id, span_id);
CREATE INDEX spans_run ON spans(run_id, start_ns) WHERE run_id <> '';
CREATE INDEX spans_start ON spans(start_ns, trace_id);
CREATE INDEX spans_trace_start ON spans(trace_id, start_ns, span_id);
CREATE INDEX spans_attempt ON spans(trace_id, parent_span_id) WHERE name = 'attempt';

CREATE TABLE records (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  run_id TEXT NOT NULL, kind TEXT NOT NULL, pos INTEGER NOT NULL,
  time_ns INTEGER NOT NULL,
  trace_id TEXT NOT NULL DEFAULT '', span_id TEXT NOT NULL DEFAULT '',
  event_type TEXT NOT NULL DEFAULT '', step INTEGER NOT NULL DEFAULT -1,
  body TEXT NOT NULL,
  attrs TEXT NOT NULL,
  PRIMARY KEY (project_id, env, tenant_id, run_id, kind, pos)   -- the dedup key: INSERT OR IGNORE
) WITHOUT ROWID;
CREATE INDEX records_key ON records(run_id, kind, pos);

-- Non-weft records (slog lines, other services). id is a surrogate the
-- database assigns: no dedup key.
CREATE TABLE other_logs (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  id INTEGER PRIMARY KEY, time_ns INTEGER NOT NULL, trace_id TEXT, span_id TEXT,
  severity INTEGER, event_name TEXT, body TEXT, service TEXT, attrs TEXT, resource TEXT
);
CREATE INDEX other_logs_trace ON other_logs(trace_id);

CREATE TABLE runs (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  run_id TEXT NOT NULL,
  parent_run_id TEXT NOT NULL DEFAULT '', parent_call_id TEXT NOT NULL DEFAULT '',
  trace_id TEXT NOT NULL DEFAULT '',
  agent TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  manifest_hash TEXT NOT NULL DEFAULT '', weft_version TEXT NOT NULL DEFAULT '', service TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '', public_id TEXT NOT NULL DEFAULT '', turn INTEGER NOT NULL DEFAULT 0,
  forked_from TEXT NOT NULL DEFAULT '',
  resumed_from TEXT NOT NULL DEFAULT '',   -- weft.resumed_from: the parked run this one resumes
  caller_run_id TEXT NOT NULL DEFAULT '', caller_call_id TEXT NOT NULL DEFAULT '',
  meta TEXT NOT NULL DEFAULT '{}',
  started_ns INTEGER NOT NULL, finished_ns INTEGER, last_seen_ns INTEGER NOT NULL,
  finished_ok INTEGER NOT NULL DEFAULT 0,   -- a run_finish arrived
  failed INTEGER NOT NULL DEFAULT 0,        -- the invoke_agent span ended in error
  root_ended INTEGER NOT NULL DEFAULT 0,    -- the invoke_agent span is stored
  derived INTEGER NOT NULL DEFAULT 0,       -- built from stock GenAI spans (record contract §9)
  err TEXT NOT NULL DEFAULT '', error_type TEXT NOT NULL DEFAULT '',
  steps INTEGER NOT NULL DEFAULT 0, pending INTEGER NOT NULL DEFAULT 0, stop_reason TEXT NOT NULL DEFAULT '',
  stopped_by TEXT NOT NULL DEFAULT '', cancel_cause TEXT NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0, delta_count INTEGER NOT NULL DEFAULT 0, message_count INTEGER NOT NULL DEFAULT 0,
  events_emitted INTEGER NOT NULL DEFAULT 0, messages_emitted INTEGER NOT NULL DEFAULT 0,
  instructions_hash TEXT NOT NULL DEFAULT '', catalog_hash TEXT NOT NULL DEFAULT '', request_count INTEGER NOT NULL DEFAULT 0,
  problems TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (project_id, env, tenant_id, run_id)
);
CREATE INDEX runs_id ON runs(run_id);
CREATE INDEX runs_session ON runs(session_id, turn) WHERE session_id <> '';
CREATE INDEX runs_agent ON runs(agent, started_ns DESC);
CREATE INDEX runs_public ON runs(public_id, parent_run_id, started_ns DESC, run_id DESC) WHERE public_id <> '';
CREATE INDEX runs_session_started ON runs(session_id, parent_run_id, started_ns DESC, run_id DESC) WHERE session_id <> '';
CREATE INDEX runs_started ON runs(parent_run_id, started_ns DESC, run_id DESC);
CREATE INDEX runs_all ON runs(started_ns DESC, run_id DESC);
CREATE INDEX runs_last_seen ON runs(last_seen_ns);
CREATE INDEX runs_failed ON runs(parent_run_id, started_ns DESC, run_id DESC) WHERE failed = 1;
CREATE INDEX runs_failed_all ON runs(started_ns DESC, run_id DESC) WHERE failed = 1;
CREATE INDEX runs_resumed_from ON runs(resumed_from, run_id) WHERE resumed_from <> '';
CREATE INDEX runs_caller ON runs(caller_run_id, started_ns) WHERE caller_run_id <> '';
CREATE INDEX runs_session_parent ON runs(json_extract(meta, '$."weft.session.parent"'))
  WHERE session_id <> '';
CREATE INDEX runs_session_turns ON runs(session_id, started_ns DESC, run_id DESC)
  WHERE session_id <> '';
CREATE INDEX runs_derived_trace ON runs(trace_id) WHERE derived = 1;

-- The search index (Searcher): one run_search row per run, its id the
-- run's runs_fts rowid; run_tools the names of the tools it called.
CREATE TABLE run_search (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  id INTEGER PRIMARY KEY,                  -- the run's runs_fts rowid
  run_id TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '', prompt_idx INTEGER NOT NULL DEFAULT -1,
  answer TEXT NOT NULL DEFAULT '', answer_idx INTEGER NOT NULL DEFAULT -1,
  UNIQUE (project_id, env, tenant_id, run_id)
);
CREATE INDEX run_search_run ON run_search(run_id);

CREATE TABLE run_tools (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  run_id TEXT NOT NULL, name TEXT NOT NULL,
  PRIMARY KEY (project_id, env, tenant_id, run_id, name)
) WITHOUT ROWID;
CREATE INDEX run_tools_key ON run_tools(run_id, name);
CREATE INDEX run_tools_name ON run_tools(name);

CREATE VIRTUAL TABLE runs_fts USING fts5(  -- rowid = run_search.id
  prompt, answer, err, tools,
  tokenize = 'trigram'
);

-- Stored agent configurations (ManifestStore), content-addressed and
-- kept forever.
CREATE TABLE manifests (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  hash     TEXT NOT NULL,
  body     TEXT NOT NULL,
  source   TEXT NOT NULL DEFAULT '',
  first_ns INTEGER NOT NULL,
  PRIMARY KEY (project_id, env, tenant_id, hash)
);
CREATE INDEX manifests_hash ON manifests(hash);

-- The session timeline (SessionEventReader): thread and compaction
-- records carrying a weft.session.id, a runless one (run_id '') included.
CREATE TABLE session_events (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  pos INTEGER NOT NULL,
  time_ns INTEGER NOT NULL,
  event_name TEXT NOT NULL DEFAULT '',
  public_id TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  PRIMARY KEY (project_id, env, tenant_id, session_id, run_id, kind, pos)
) WITHOUT ROWID;
CREATE INDEX session_events_time ON session_events(session_id, time_ns, run_id, kind, pos);
CREATE INDEX session_events_run ON session_events(run_id) WHERE run_id <> '';
CREATE INDEX session_events_runless ON session_events(time_ns) WHERE run_id = '';

-- The stats rollup (Aggregator): one row per stored weft span of the
-- three GenAI operations, written at ingest beside the span and
-- rebuildable from the spans table.
CREATE TABLE span_stats (
  project_id TEXT NOT NULL DEFAULT 'prj_default', env TEXT NOT NULL DEFAULT 'test', tenant_id TEXT NOT NULL DEFAULT 'ten_project',
  trace_id TEXT NOT NULL, span_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  op TEXT NOT NULL,                       -- invoke_agent | chat | execute_tool
  name TEXT NOT NULL DEFAULT '',          -- the agent, the model asked for, the tool
  provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
  step INTEGER NOT NULL DEFAULT -1,
  start_ns INTEGER NOT NULL,
  dur_ms REAL NOT NULL,                   -- (end − start) / 1e6, floored at 0
  ttft_ms REAL,                           -- chat: weft.ttft_ms
  attempts INTEGER NOT NULL DEFAULT 1,    -- chat: the highest weft.attempt.index of its attempt spans, else 1
  failed INTEGER NOT NULL DEFAULT 0,      -- status Error (execute_tool: the tool_errors rule)
  error_type TEXT NOT NULL DEFAULT '',
  denied INTEGER NOT NULL DEFAULT 0,      -- problems.denied's share: weft.run.denied, or 1 for a DENIED tool call
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, env, tenant_id, trace_id, span_id)
) WITHOUT ROWID;
CREATE INDEX span_stats_key ON span_stats(trace_id, span_id);
CREATE INDEX span_stats_op ON span_stats(op, name, start_ns);
CREATE INDEX span_stats_run ON span_stats(run_id);
CREATE TRIGGER span_stats_gone AFTER DELETE ON spans BEGIN
  DELETE FROM span_stats WHERE project_id = old.project_id AND env = old.env AND tenant_id = old.tenant_id
    AND trace_id = old.trace_id AND span_id = old.span_id;
END;
