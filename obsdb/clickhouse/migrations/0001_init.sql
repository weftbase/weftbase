-- 0001 — the ClickHouse dialect of the obsdb schema (ADR 0024, S3.6),
-- one logical schema with obsdb/sqlite's 0001.
--
-- Tenancy: every table carries ProjectId, Env and TenantId, and those
-- three lead every ORDER BY — the sorting key ReplacingMergeTree and
-- AggregatingMergeTree collapse rows by — so an id colliding across
-- tenants never merges into another tenant's row. The materialized
-- views carry the source row's three into their targets. Until tenancy
-- fills them from the credential they hold the defaults below: writes
-- and a stock collector's INSERT (which never names them) leave them
-- to the defaults.
--
-- The dedup key is the transport-level idempotency (I4): (run, kind,
-- pos) for records, (trace, span) for spans — ReplacingMergeTree
-- collapses a retried batch's duplicates at merge time, exact reads use
-- FINAL or LIMIT 1 BY.
--
-- otel_traces and otel_logs are column-compatible with the OTel
-- Collector ClickHouse exporter pinned at v0.162.0 (2026-09-29): every
-- column name and type its INSERT names (traces_insert.sql and
-- logs_insert.sql at that tag, the optional EventName column
-- included), with the weft and tenancy columns beside them. Not
-- DDL-verbatim: we own the engines, the sorting keys, the TTLs and the
-- materialized weft columns; a stock collector pinned to v0.162.0 can
-- write into these tables (the collector-shape test inserts with its
-- exact column lists).
--
-- TTLs are the S3.6 defaults: content (otel_logs, weft_records,
-- weft_deltas, weft_session_events) 30 days, spans and runs 90 days;
-- clickhouse.TTL(...) overrides them per Open with ALTER TABLE ...
-- MODIFY TTL.
--
-- The runner splits statements on semicolons and drops whole-line
-- comments: no statement here carries an inline comment or a
-- semicolon inside a literal.

CREATE TABLE IF NOT EXISTS otel_traces (
    Timestamp DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    ParentSpanId String CODEC(ZSTD(1)),
    TraceState String CODEC(ZSTD(1)),
    SpanName LowCardinality(String) CODEC(ZSTD(1)),
    SpanKind LowCardinality(String) CODEC(ZSTD(1)),
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion String CODEC(ZSTD(1)),
    SpanAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    Duration UInt64 CODEC(ZSTD(1)),
    StatusCode LowCardinality(String) CODEC(ZSTD(1)),
    StatusMessage String CODEC(ZSTD(1)),
    Events Nested (
        Timestamp DateTime64(9),
        Name LowCardinality(String),
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    Links Nested (
        TraceId String,
        SpanId String,
        TraceState String,
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    -- weft additions. WeftAttrs/WeftEvents/WeftResource carry the typed
    -- (JSON) attribute maps for exact round-trips: the collector's map
    -- columns stringify every value, which loses the int64 the record
    -- contract pins. They default to '' so a stock collector's INSERT
    -- (which never names them) still lands; reads fall back to the map
    -- columns for such rows. The MATERIALIZED columns are identity
    -- extracted from SpanAttributes.
    WeftAttrs String DEFAULT '' CODEC(ZSTD(1)),
    WeftEvents String DEFAULT '' CODEC(ZSTD(1)),
    WeftResource String DEFAULT '' CODEC(ZSTD(1)),
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    RunId LowCardinality(String) MATERIALIZED SpanAttributes['weft.run.id'],
    SessionId String MATERIALIZED SpanAttributes['weft.session.id'],
    PublicId String MATERIALIZED SpanAttributes['weft.public_id'],
    Agent String MATERIALIZED SpanAttributes['gen_ai.agent.name'],
    ManifestHash String MATERIALIZED SpanAttributes['weft.manifest.hash'],
    PromptId String MATERIALIZED SpanAttributes['weft.prompt.id'],
    PromptVersion String MATERIALIZED SpanAttributes['weft.prompt.version'],
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_weft_session_id SessionId TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_weft_public_id PublicId TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_weft_agent Agent TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_span_attr_key mapKeys(SpanAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_weft_run_id RunId TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ProjectId, Env, TenantId, ServiceName, SpanName, toDateTime(Timestamp))
TTL Timestamp + toIntervalSecond(7776000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS otel_logs (
    Timestamp DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    TraceFlags UInt8,
    SeverityText LowCardinality(String) CODEC(ZSTD(1)),
    SeverityNumber UInt8,
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    Body String CODEC(ZSTD(1)),
    ResourceSchemaUrl LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeSchemaUrl LowCardinality(String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion LowCardinality(String) CODEC(ZSTD(1)),
    ScopeAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    LogAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    EventName String CODEC(ZSTD(1)),
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_res_attr_key mapKeys(ResourceAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_scope_attr_key mapKeys(ScopeAttributes) TYPE bloom_filter(0.01) GRANULARITY 1,
    INDEX idx_log_attr_key mapKeys(LogAttributes) TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (ProjectId, Env, TenantId, toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)
TTL Timestamp + toIntervalSecond(2592000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- The durable records (event, messages, request, prompt, tools, raw),
-- deduped on (run, kind, pos).
CREATE TABLE IF NOT EXISTS weft_records (
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    RunId String,
    Kind LowCardinality(String),
    Pos Int64,
    Time DateTime64(9),
    InsertTime DateTime DEFAULT now(),
    TraceId String,
    SpanId String,
    EventType LowCardinality(String),
    Step Int32 DEFAULT -1,
    Reason LowCardinality(String) DEFAULT '',
    Input Int8 DEFAULT -1,
    Content LowCardinality(String) DEFAULT '',
    TruncatedBytes Int64 DEFAULT 0,
    SystemHash LowCardinality(String) DEFAULT '',
    CatalogHash LowCardinality(String) DEFAULT '',
    SessionId String,
    PublicId String,
    Agent String,
    Turn Int32,
    Body String CODEC(ZSTD(1))
) ENGINE = ReplacingMergeTree(InsertTime)
ORDER BY (ProjectId, Env, TenantId, RunId, Kind, Pos)
TTL Time + toIntervalSecond(2592000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- Deltas, stored only under KeepDeltas (debugging), on their own
-- counter.
CREATE TABLE IF NOT EXISTS weft_deltas (
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    RunId String,
    Pos Int64,
    Time DateTime64(9),
    InsertTime DateTime DEFAULT now(),
    TraceId String,
    SpanId String,
    EventType LowCardinality(String),
    Body String CODEC(ZSTD(1))
) ENGINE = ReplacingMergeTree(InsertTime)
ORDER BY (ProjectId, Env, TenantId, RunId, Pos)
TTL Time + toIntervalSecond(2592000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- One row per run, folded from both views (and the derived runs the
-- writer inserts): identity strings, flags, usage and last-seen by max,
-- started by min, the metadata's union by maxMap (MetaKV).
CREATE TABLE IF NOT EXISTS weft_runs (
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    RunId String,
    ParentRunID SimpleAggregateFunction(max, String),
    ParentCallID SimpleAggregateFunction(max, String),
    TraceID SimpleAggregateFunction(max, String),
    Agent SimpleAggregateFunction(max, String),
    Provider SimpleAggregateFunction(max, String),
    Model SimpleAggregateFunction(max, String),
    ManifestHash SimpleAggregateFunction(max, String),
    WeftVersion SimpleAggregateFunction(max, String),
    Service SimpleAggregateFunction(max, String),
    SessionID SimpleAggregateFunction(max, String),
    PublicID SimpleAggregateFunction(max, String),
    Turn SimpleAggregateFunction(max, Int32),
    ForkedFrom SimpleAggregateFunction(max, String),
    Meta SimpleAggregateFunction(max, String),
    Started SimpleAggregateFunction(min, DateTime64(9)),
    Finished SimpleAggregateFunction(max, Nullable(DateTime64(9))),
    LastSeen SimpleAggregateFunction(max, DateTime64(9)),
    FinishedOK SimpleAggregateFunction(max, UInt8),
    Failed SimpleAggregateFunction(max, UInt8),
    Err SimpleAggregateFunction(max, String),
    Steps SimpleAggregateFunction(max, Int32),
    Pending SimpleAggregateFunction(max, Int32),
    StopReason SimpleAggregateFunction(max, String),
    InputTokens SimpleAggregateFunction(max, Int64),
    OutputTokens SimpleAggregateFunction(max, Int64),
    CachedInputTokens SimpleAggregateFunction(max, Int64),
    CacheWriteTokens SimpleAggregateFunction(max, Int64),
    ReasoningTokens SimpleAggregateFunction(max, Int64),
    DeltaCount SimpleAggregateFunction(max, Int64),
    InstructionsHash SimpleAggregateFunction(max, String),
    CatalogHash SimpleAggregateFunction(max, String),
    RequestCount SimpleAggregateFunction(max, Int64),
    Derived SimpleAggregateFunction(max, UInt8),
    RootEnded SimpleAggregateFunction(max, UInt8),
    StoppedBy SimpleAggregateFunction(max, String),
    CancelCause SimpleAggregateFunction(max, String),
    EventsEmitted SimpleAggregateFunction(max, Int64),
    MessagesEmitted SimpleAggregateFunction(max, Int64),
    ErrorType SimpleAggregateFunction(max, String),
    Problems SimpleAggregateFunction(groupUniqArrayArray, Array(String)),
    ResumedFrom SimpleAggregateFunction(max, String),
    CallerRunID SimpleAggregateFunction(max, String),
    CallerCallID SimpleAggregateFunction(max, String),
    SessionParent SimpleAggregateFunction(max, String),
    DerivedMessages SimpleAggregateFunction(max, Int64),
    MetaKV SimpleAggregateFunction(maxMap, Tuple(Array(String), Array(String))) DEFAULT (mapKeys(JSONExtract(toValidUTF8(Meta), 'Map(String, String)')), mapValues(JSONExtract(toValidUTF8(Meta), 'Map(String, String)'))),
    INDEX idx_resumed_from ResumedFrom TYPE bloom_filter GRANULARITY 4,
    INDEX idx_parent_run ParentRunID TYPE bloom_filter GRANULARITY 4,
    INDEX idx_caller_run CallerRunID TYPE bloom_filter GRANULARITY 4,
    INDEX idx_session_parent SessionParent TYPE bloom_filter GRANULARITY 4,
    INDEX idx_meta_kv toJSONString(CAST(MetaKV, 'Map(String, String)')) TYPE ngrambf_v1(4, 16384, 3, 0) GRANULARITY 1
) ENGINE = AggregatingMergeTree
ORDER BY (ProjectId, Env, TenantId, RunId)
TTL LastSeen + toIntervalSecond(7776000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- The session timeline: thread and compaction records carrying a
-- weft.session.id, a runless one (RunId '') included. BodyHash keys a
-- compaction marker by its hash, any other record by its body.
CREATE TABLE IF NOT EXISTS weft_session_events (
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    SessionId String,
    Time DateTime64(9),
    RunId String,
    Kind LowCardinality(String),
    BodyHash UInt64,
    EventName LowCardinality(String),
    PublicId String,
    Content LowCardinality(String),
    Body String CODEC(ZSTD(1)),
    InsertTime DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(InsertTime)
ORDER BY (ProjectId, Env, TenantId, SessionId, Time, RunId, Kind, BodyHash)
TTL Time + toIntervalSecond(2592000)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

-- Stored agent configurations (ManifestStore), content-addressed and
-- kept forever.
CREATE TABLE IF NOT EXISTS manifests (
    ProjectId LowCardinality(String) DEFAULT 'prj_default',
    Env LowCardinality(String) DEFAULT 'test',
    TenantId LowCardinality(String) DEFAULT 'ten_project',
    Hash String,
    Body String CODEC(ZSTD(1)),
    Source LowCardinality(String),
    FirstNs Int64,
    Ver UInt64
) ENGINE = ReplacingMergeTree(Ver)
ORDER BY (ProjectId, Env, TenantId, Hash);

-- weft_records from otel_logs: only the durable kinds — never 'delta'
-- (counted, never stored; the weft_runs view counts them) and never
-- 'heartbeat' (no position; it only moves weft_runs' last-seen).
CREATE MATERIALIZED VIEW IF NOT EXISTS weft_records_mv TO weft_records AS
SELECT
    ProjectId, Env, TenantId,
    LogAttributes['weft.run.id'] AS RunId,
    LogAttributes['weft.record'] AS Kind,
    multiIf(LogAttributes['weft.event.pos'] != '', toInt64OrZero(LogAttributes['weft.event.pos']),
      LogAttributes['weft.messages.index'] != '', toInt64OrZero(LogAttributes['weft.messages.index']),
      LogAttributes['weft.request.index'] != '', toInt64OrZero(LogAttributes['weft.request.index']),
      LogAttributes['weft.prompt.index'] != '', toInt64OrZero(LogAttributes['weft.prompt.index']),
      LogAttributes['weft.tools.index'] != '', toInt64OrZero(LogAttributes['weft.tools.index']),
      LogAttributes['weft.raw.index'] != '', toInt64OrZero(LogAttributes['weft.raw.index']), -1) AS Pos,
    Timestamp AS Time,
    TraceId AS TraceId,
    SpanId AS SpanId,
    LogAttributes['weft.event.type'] AS EventType,
    if(LogAttributes['weft.step.index'] != '', toInt32OrZero(LogAttributes['weft.step.index']), -1) AS Step,
    LogAttributes['weft.messages.reason'] AS Reason,
    toInt8(LogAttributes['weft.messages.input'] = 'true') AS Input,
    LogAttributes['weft.content'] AS Content,
    toInt64OrZero(LogAttributes['weft.content.truncated_bytes']) AS TruncatedBytes,
    LogAttributes['weft.system.hash'] AS SystemHash,
    LogAttributes['weft.catalog.hash'] AS CatalogHash,
    LogAttributes['weft.session.id'] AS SessionId,
    LogAttributes['weft.public_id'] AS PublicId,
    LogAttributes['gen_ai.agent.name'] AS Agent,
    toInt32OrZero(LogAttributes['weft.turn']) AS Turn,
    Body AS Body
FROM otel_logs
WHERE LogAttributes['weft.record'] IN ('event', 'messages', 'request', 'prompt', 'tools', 'raw')
  AND LogAttributes['weft.run.id'] != '';

-- weft_runs from the records: the run_start/run_finish facts, the
-- request counters, the delta count. Both views exclude exactly
-- obsdb.MetaOf's key set from Meta (contractKeys), as SQLite does at
-- write time.
CREATE MATERIALIZED VIEW IF NOT EXISTS weft_runs_logs_mv TO weft_runs AS
WITH mapFilter((k, v) -> NOT has([
        'weft.run.id', 'weft.parent.run.id', 'weft.parent.call.id',
        'weft.session.id', 'weft.public_id', 'weft.turn', 'gen_ai.agent.name',
        'weft.record', 'weft.event.type', 'weft.event.pos', 'weft.delta.pos',
        'weft.messages.index', 'weft.step.index', 'weft.tool.seq',
        'weft.forked_from',
        'weft.messages.count', 'weft.messages.input', 'weft.content',
        'weft.content.truncated_bytes', 'weft.version', 'weft.manifest.hash',
        'weft.run.steps', 'weft.run.pending', 'weft.run.stop_reason',
        'weft.stop.raw', 'weft.model.tool_calls', 'weft.tool.approved',
        'weft.tool.pending', 'weft.tool.result_bytes', 'weft.metadata.dropped',
        'weft.override.hash',
        'gen_ai.operation.name', 'gen_ai.provider.name', 'gen_ai.request.model',
        'gen_ai.response.finish_reasons', 'gen_ai.usage.input_tokens',
        'gen_ai.usage.output_tokens', 'gen_ai.usage.cache_read.input_tokens',
        'gen_ai.usage.cache_creation.input_tokens', 'gen_ai.usage.reasoning.output_tokens',
        'gen_ai.tool.name', 'gen_ai.tool.call.id', 'gen_ai.conversation.id',
        'session.id', 'user.id', 'error.type',
        'weft.request.index', 'weft.prompt.index', 'weft.tools.index',
        'weft.system.hash', 'weft.catalog.hash', 'weft.attempt.index',
        'weft.instructions.hash', 'weft.messages.reason', 'weft.messages.from_seq',
        'weft.messages.to_seq', 'weft.compaction.hash', 'weft.compaction.scope',
        'gen_ai.response.model', 'weft.stream', 'weft.ttft_ms', 'weft.latency_ms',
        'weft.attempt.retry_after_ms',
        'weft.override.instructions', 'weft.override.max_steps', 'weft.override.model',
        'weft.override.parallelism', 'weft.override.params', 'weft.override.park_all_except',
        'weft.override.park_on', 'weft.override.thinking', 'weft.override.tool_choice',
        'weft.override.tools',
        'weft.raw.index',
        'weft.run.stopped_by', 'weft.cancel.cause',
        'weft.run.events', 'weft.run.messages',
        'weft.records.dropped',
        'weft.run.denied', 'weft.tool.truncated_bytes', 'weft.tool.input_error',
        'weft.panic.layer',
        'weft.attempt.layer',
        'weft.tool.retry', 'weft.tool.retry_budget', 'weft.tap.panics',
        'weft.resumed_from', 'weft.resume.calls', 'weft.tool.denied', 'weft.tool.denied_kind',
        'weft.logs.bridged',
        'weft.attempt.wait_ms', 'http.response.status_code', 'weft.tool.pending.source',
        'weft.tool.timeout_ms', 'weft.tool.queue_ms', 'weft.subagent.pending', 'weft.output.submitted',
        'weft.caller.run.id', 'weft.caller.call.id',
        'weft.chat.role', 'weft.tool.approved_by', 'weft.grant.id',
        'weft.tool.denied_by', 'weft.tool.error_masked', 'weft.tool.args_repaired',
        'weft.layer.name', 'weft.layer.kind', 'weft.layer.index',
        'weft.tool.origin', 'weft.caller.remote',
        'weft.attempt.giveup_reason', 'weft.output.type'], k), LogAttributes) AS metaMap
SELECT
    ProjectId, Env, TenantId,
    LogAttributes['weft.run.id'] AS RunId,
    LogAttributes['weft.parent.run.id'] AS ParentRunID,
    LogAttributes['weft.parent.call.id'] AS ParentCallID,
    TraceId AS TraceID,
    LogAttributes['gen_ai.agent.name'] AS Agent,
    if(LogAttributes['weft.event.type'] = 'run_start', JSONExtractString(Body, 'model', 'provider'), '') AS Provider,
    if(LogAttributes['weft.event.type'] = 'run_start', JSONExtractString(Body, 'model', 'name'), '') AS Model,
    LogAttributes['weft.manifest.hash'] AS ManifestHash,
    LogAttributes['weft.version'] AS WeftVersion,
    ServiceName AS Service,
    LogAttributes['weft.session.id'] AS SessionID,
    LogAttributes['weft.public_id'] AS PublicID,
    toInt32OrZero(LogAttributes['weft.turn']) AS Turn,
    LogAttributes['weft.forked_from'] AS ForkedFrom,
    if(length(metaMap) = 0, '', toJSONString(metaMap)) AS Meta,
    Timestamp AS Started,
    if(LogAttributes['weft.event.type'] = 'run_finish', Timestamp, NULL) AS Finished,
    Timestamp AS LastSeen,
    toUInt8(LogAttributes['weft.event.type'] = 'run_finish') AS FinishedOK,
    toUInt8(0) AS Failed,
    '' AS Err,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt32(JSONExtractInt(Body, 'steps')), 0) AS Steps,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt32(length(JSONExtractArrayRaw(Body, 'pending'))), 0) AS Pending,
    '' AS StopReason,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt64(JSONExtractInt(Body, 'usage', 'input_tokens')), 0) AS InputTokens,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt64(JSONExtractInt(Body, 'usage', 'output_tokens')), 0) AS OutputTokens,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt64(JSONExtractInt(Body, 'usage', 'cached_input_tokens')), 0) AS CachedInputTokens,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt64(JSONExtractInt(Body, 'usage', 'cache_write_tokens')), 0) AS CacheWriteTokens,
    if(LogAttributes['weft.event.type'] = 'run_finish', toInt64(JSONExtractInt(Body, 'usage', 'reasoning_tokens')), 0) AS ReasoningTokens,
    if(LogAttributes['weft.record'] = 'delta', toInt64OrZero(LogAttributes['weft.delta.pos']) + 1, 0) AS DeltaCount,
    if(LogAttributes['weft.event.type'] = 'run_start', LogAttributes['weft.instructions.hash'], '') AS InstructionsHash,
    if(LogAttributes['weft.record'] = 'request' AND LogAttributes['weft.request.index'] = '0', LogAttributes['weft.catalog.hash'], '') AS CatalogHash,
    if(LogAttributes['weft.record'] = 'request' AND LogAttributes['weft.request.index'] != '', toInt64OrZero(LogAttributes['weft.request.index']) + 1, 0) AS RequestCount,
    if(LogAttributes['weft.event.type'] = 'run_finish', JSONExtractString(Body, 'stopped_by'), '') AS StoppedBy,
    LogAttributes['weft.resumed_from'] AS ResumedFrom,
    if(LogAttributes['weft.caller.remote'] = 'true', '', LogAttributes['weft.caller.run.id']) AS CallerRunID,
    if(LogAttributes['weft.caller.remote'] = 'true', '', LogAttributes['weft.caller.call.id']) AS CallerCallID,
    LogAttributes['weft.session.parent'] AS SessionParent
FROM otel_logs
WHERE LogAttributes['weft.run.id'] != '' AND LogAttributes['weft.record'] NOT IN ('thread', 'compaction');

-- weft_runs from the spans: the invoke_agent span's outcome and usage,
-- the problem tokens, the ids every weft span carries.
CREATE MATERIALIZED VIEW IF NOT EXISTS weft_runs_traces_mv TO weft_runs AS
WITH if(mapContains(SpanAttributes, 'gen_ai.operation.name'),
       SpanAttributes['gen_ai.operation.name'] = 'invoke_agent',
       startsWith(SpanName, 'invoke_agent')) AS isInvoke,
     StatusCode IN ('Error', 'STATUS_CODE_ERROR') AS isError,
     SpanAttributes['gen_ai.operation.name'] AS op,
     mapFilter((k, v) -> NOT has([
        'weft.run.id', 'weft.parent.run.id', 'weft.parent.call.id',
        'weft.session.id', 'weft.public_id', 'weft.turn', 'gen_ai.agent.name',
        'weft.record', 'weft.event.type', 'weft.event.pos', 'weft.delta.pos',
        'weft.messages.index', 'weft.step.index', 'weft.tool.seq',
        'weft.forked_from',
        'weft.messages.count', 'weft.messages.input', 'weft.content',
        'weft.content.truncated_bytes', 'weft.version', 'weft.manifest.hash',
        'weft.run.steps', 'weft.run.pending', 'weft.run.stop_reason',
        'weft.stop.raw', 'weft.model.tool_calls', 'weft.tool.approved',
        'weft.tool.pending', 'weft.tool.result_bytes', 'weft.metadata.dropped',
        'weft.override.hash',
        'gen_ai.operation.name', 'gen_ai.provider.name', 'gen_ai.request.model',
        'gen_ai.response.finish_reasons', 'gen_ai.usage.input_tokens',
        'gen_ai.usage.output_tokens', 'gen_ai.usage.cache_read.input_tokens',
        'gen_ai.usage.cache_creation.input_tokens', 'gen_ai.usage.reasoning.output_tokens',
        'gen_ai.tool.name', 'gen_ai.tool.call.id', 'gen_ai.conversation.id',
        'session.id', 'user.id', 'error.type',
        'weft.request.index', 'weft.prompt.index', 'weft.tools.index',
        'weft.system.hash', 'weft.catalog.hash', 'weft.attempt.index',
        'weft.instructions.hash', 'weft.messages.reason', 'weft.messages.from_seq',
        'weft.messages.to_seq', 'weft.compaction.hash', 'weft.compaction.scope',
        'gen_ai.response.model', 'weft.stream', 'weft.ttft_ms', 'weft.latency_ms',
        'weft.attempt.retry_after_ms',
        'weft.override.instructions', 'weft.override.max_steps', 'weft.override.model',
        'weft.override.parallelism', 'weft.override.params', 'weft.override.park_all_except',
        'weft.override.park_on', 'weft.override.thinking', 'weft.override.tool_choice',
        'weft.override.tools',
        'weft.raw.index',
        'weft.run.stopped_by', 'weft.cancel.cause',
        'weft.run.events', 'weft.run.messages',
        'weft.records.dropped',
        'weft.run.denied', 'weft.tool.truncated_bytes', 'weft.tool.input_error',
        'weft.panic.layer',
        'weft.attempt.layer',
        'weft.tool.retry', 'weft.tool.retry_budget', 'weft.tap.panics',
        'weft.resumed_from', 'weft.resume.calls', 'weft.tool.denied', 'weft.tool.denied_kind',
        'weft.logs.bridged',
        'weft.attempt.wait_ms', 'http.response.status_code', 'weft.tool.pending.source',
        'weft.tool.timeout_ms', 'weft.tool.queue_ms', 'weft.subagent.pending', 'weft.output.submitted',
        'weft.caller.run.id', 'weft.caller.call.id',
        'weft.chat.role', 'weft.tool.approved_by', 'weft.grant.id',
        'weft.tool.denied_by', 'weft.tool.error_masked', 'weft.tool.args_repaired',
        'weft.layer.name', 'weft.layer.kind', 'weft.layer.index',
        'weft.tool.origin', 'weft.caller.remote',
        'weft.attempt.giveup_reason', 'weft.output.type'], k), SpanAttributes) AS metaMap
SELECT
    ProjectId, Env, TenantId,
    SpanAttributes['weft.run.id'] AS RunId,
    SpanAttributes['weft.parent.run.id'] AS ParentRunID,
    SpanAttributes['weft.parent.call.id'] AS ParentCallID,
    TraceId AS TraceID,
    SpanAttributes['gen_ai.agent.name'] AS Agent,
    SpanAttributes['gen_ai.provider.name'] AS Provider,
    SpanAttributes['gen_ai.request.model'] AS Model,
    SpanAttributes['weft.manifest.hash'] AS ManifestHash,
    if(isInvoke, if(SpanAttributes['weft.version'] != '', SpanAttributes['weft.version'], ResourceAttributes['weft.version']), '') AS WeftVersion,
    ServiceName AS Service,
    SpanAttributes['weft.session.id'] AS SessionID,
    SpanAttributes['weft.public_id'] AS PublicID,
    toInt32OrZero(SpanAttributes['weft.turn']) AS Turn,
    SpanAttributes['weft.forked_from'] AS ForkedFrom,
    if(length(metaMap) = 0, '', toJSONString(metaMap)) AS Meta,
    Timestamp AS Started,
    if(isInvoke, Timestamp + toIntervalNanosecond(toInt64(Duration)), NULL) AS Finished,
    Timestamp + toIntervalNanosecond(toInt64(Duration)) AS LastSeen,
    toUInt8(0) AS FinishedOK,
    toUInt8(isInvoke AND isError) AS Failed,
    if(isInvoke AND isError, StatusMessage, '') AS Err,
    if(isInvoke, toInt32OrZero(SpanAttributes['weft.run.steps']), 0) AS Steps,
    if(isInvoke, toInt32OrZero(SpanAttributes['weft.run.pending']), 0) AS Pending,
    if(isInvoke, SpanAttributes['weft.run.stop_reason'], '') AS StopReason,
    if(isInvoke, toInt64OrZero(SpanAttributes['gen_ai.usage.input_tokens']), 0) AS InputTokens,
    if(isInvoke, toInt64OrZero(SpanAttributes['gen_ai.usage.output_tokens']), 0) AS OutputTokens,
    if(isInvoke, toInt64OrZero(SpanAttributes['gen_ai.usage.cache_read.input_tokens']), 0) AS CachedInputTokens,
    if(isInvoke, toInt64OrZero(SpanAttributes['gen_ai.usage.cache_creation.input_tokens']), 0) AS CacheWriteTokens,
    if(isInvoke, toInt64OrZero(SpanAttributes['gen_ai.usage.reasoning.output_tokens']), 0) AS ReasoningTokens,
    toInt64(0) AS DeltaCount,
    if(isInvoke, SpanAttributes['weft.instructions.hash'], '') AS InstructionsHash,
    '' AS CatalogHash,
    toInt64(0) AS RequestCount,
    toUInt8(isInvoke) AS RootEnded,
    if(isInvoke, SpanAttributes['weft.run.stopped_by'], '') AS StoppedBy,
    if(isInvoke, SpanAttributes['weft.cancel.cause'], '') AS CancelCause,
    if(isInvoke, toInt64OrZero(SpanAttributes['weft.run.events']), 0) AS EventsEmitted,
    if(isInvoke, toInt64OrZero(SpanAttributes['weft.run.messages']), 0) AS MessagesEmitted,
    if(isInvoke, multiIf(SpanAttributes['error.type'] IN ('context.Canceled', 'context.DeadlineExceeded'), 'cancelled', SpanAttributes['error.type']), '') AS ErrorType,
    arrayFilter(x -> x != '', [
        if(SpanName = 'attempt' AND op = '',
           concat('attempt:', SpanId, ':', ParentSpanId, ':', toString(toInt64OrZero(SpanAttributes['weft.attempt.index'])), ':', if(isError, '1', '0')), ''),
        if(SpanName = 'attempt' AND op = '' AND NOT isError AND SpanAttributes['weft.attempt.layer'] = 'fallback', concat('fallback_answer:', SpanId), ''),
        if(op = 'execute_tool' AND isError AND SpanAttributes['weft.tool.denied'] != 'true' AND SpanAttributes['error.type'] NOT IN ('RETRY', 'context.Canceled', 'context.DeadlineExceeded'), concat(if(SpanAttributes['error.type'] = 'DENIED', 'denied:', 'tool_failed:'), SpanId), ''),
        if(op = 'execute_tool' AND toInt64OrZero(SpanAttributes['weft.tool.truncated_bytes']) > 0, concat('truncated:', SpanId), ''),
        if(op = 'execute_tool' AND SpanAttributes['weft.tool.args_repaired'] = 'true', concat('repaired:', SpanId), ''),
        if(op = 'invoke_agent' AND toInt64OrZero(SpanAttributes['weft.run.denied']) > 0,
           concat('denied_approval:', SpanId, ':', toString(toInt64OrZero(SpanAttributes['weft.run.denied']))), ''),
        if(op = 'invoke_agent' AND toInt64OrZero(SpanAttributes['weft.tap.panics']) > 0,
           concat('tap_panics:', SpanId, ':', toString(toInt64OrZero(SpanAttributes['weft.tap.panics']))), ''),
        if(op = 'invoke_agent' AND toInt64OrZero(SpanAttributes['weft.metadata.dropped']) > 0,
           concat('metadata_dropped:', SpanId, ':', toString(toInt64OrZero(SpanAttributes['weft.metadata.dropped']))), '')
    ]) AS Problems,
    SpanAttributes['weft.resumed_from'] AS ResumedFrom,
    if(SpanAttributes['weft.caller.remote'] = 'true', '', SpanAttributes['weft.caller.run.id']) AS CallerRunID,
    if(SpanAttributes['weft.caller.remote'] = 'true', '', SpanAttributes['weft.caller.call.id']) AS CallerCallID,
    SpanAttributes['weft.session.parent'] AS SessionParent
FROM otel_traces
WHERE SpanAttributes['weft.run.id'] != ''
  AND SpanAttributes['weft.chat.role'] = '';

CREATE MATERIALIZED VIEW IF NOT EXISTS weft_session_events_mv TO weft_session_events AS
SELECT
    ProjectId, Env, TenantId,
    LogAttributes['weft.session.id'] AS SessionId,
    Timestamp AS Time,
    LogAttributes['weft.run.id'] AS RunId,
    LogAttributes['weft.record'] AS Kind,
    if(LogAttributes['weft.record'] = 'compaction', cityHash64(LogAttributes['weft.compaction.hash']), cityHash64(Body)) AS BodyHash,
    EventName AS EventName,
    LogAttributes['weft.public_id'] AS PublicId,
    LogAttributes['weft.content'] AS Content,
    Body AS Body
FROM otel_logs
WHERE LogAttributes['weft.record'] IN ('thread', 'compaction') AND LogAttributes['weft.session.id'] != '';
