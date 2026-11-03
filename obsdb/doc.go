// Package obsdb is the observability database: what weft/otel's local
// sink writes and the weftbase server reads, so neither owns the
// schema (ADR 0024, S3).
//
// The model is OTLP-shaped — spans and log records, resource included —
// so a stock OpenTelemetry pipeline's data and weft's own land in the
// same tables. Weft identity (the run → session → public-id chain) is
// derived from attributes, never parsed out of ids, and is stored as
// indexed columns through the Weft struct.
//
// Three moving parts:
//
//   - DB is the backend interface: Write (idempotent on
//     (run, record kind, position) and (trace, span)), run/session
//     queries, the event page with its gap detector, the transcript a
//     replay reads, and the spans of a run or trace. Status is derived
//     from the four-row table (DeriveStatus), never stored.
//   - The hub (Hub, Frame) is the live lane: every Write publishes its
//     frames before returning, so an in-process subscriber — the
//     server's SSE handler — sees a record without a network hop.
//   - obsdbtest is the conformance table every backend runs (the
//     storetest pattern); sqlite is the default backend, clickhouse the
//     hosted one.
//
// Optional capabilities are interfaces a backend implements beside DB,
// both first-party backends included: Searcher (runs found by what they
// said and did) and ManifestStore (the agent configurations runs name).
//
// Every table carries project_id, env and tenant_id columns that lead
// its primary and dedup keys; they hold fixed defaults until tenancy
// fills them.
//
// Deltas travel but are not stored (their own counter means their
// absence never looks like a lost event); heartbeats are never stored,
// they only move a run's last-seen. Both rules are Write's, shared by
// every backend.
package obsdb
