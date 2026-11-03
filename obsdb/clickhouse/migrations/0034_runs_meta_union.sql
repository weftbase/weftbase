-- 0034 · a run's metadata is the union of its rows' (the conformance
-- row MetaUnion; docs/record-contract.md §2's ClickHouse debt). Meta is
-- SimpleAggregateFunction(max, String): one row's JSON map, the
-- greatest string, wins every collapse and every background merge, so
-- a row carrying fewer or other pairs — an execute_tool span with only
-- weft.session.title, whose JSON sorts above {"tenant":…} — dropped the
-- caller's pairs from the run, a tag-scoped panel token's tag with
-- them. SQLite merges the maps (mergeMetaJSON).
--
-- MetaKV holds the row's pairs as (keys, values) under maxMap: a merge
-- keeps every key any row carried; on a key two rows carry with
-- different values the greater value wins (SQLite: the later write) —
-- the core stamps one map on every row of a run, so only an app's own
-- attributes can disagree. Its DEFAULT fills it from the inserted row's
-- own Meta, so both views, the derived-row insert and every backfill
-- fill it unchanged; the reads (clickhouse.metaUnion, rowMetaMap) take
-- it instead of Meta. A part written before this version computes it
-- from its Meta on read: a run whose rows were merged before keeps the
-- one map the merge kept — no backfill can restore what max dropped.
--
-- The tag narrowing (0026's idx_meta over Meta) moves to the union: an
-- ngram bloom filter over the row's MetaKV as JSON, the expression
-- metaNarrow's LIKE asks (clickhouse.rowMetaJSON) — over Meta it would
-- prune a run whose pair only the union keeps. MATERIALIZE INDEX builds
-- it for the parts written before, in the background (0016's rule);
-- idx_meta reads nothing now and is dropped.

ALTER TABLE weft_runs ADD COLUMN IF NOT EXISTS MetaKV SimpleAggregateFunction(maxMap, Tuple(Array(String), Array(String))) DEFAULT (mapKeys(JSONExtract(Meta, 'Map(String, String)')), mapValues(JSONExtract(Meta, 'Map(String, String)')));

ALTER TABLE weft_runs ADD INDEX IF NOT EXISTS idx_meta_kv toJSONString(CAST(MetaKV, 'Map(String, String)')) TYPE ngrambf_v1(4, 16384, 3, 0) GRANULARITY 1;

ALTER TABLE weft_runs MATERIALIZE INDEX idx_meta_kv;

ALTER TABLE weft_runs DROP INDEX IF EXISTS idx_meta;
