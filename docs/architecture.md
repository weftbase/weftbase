# Architecture

Weftbase is the agent-native backend for agents built with weft. Agents
are versioned, deployable units. Runs are placed on workers (embedded in
the host app, local child processes, remote workers) through a durable
queue. Tenancy and approvals are built for embedding in a customer's own
product. It is not a database-as-a-service: the store exists to serve
agents, their runs, sessions and approvals.

The full design, the file-by-file disposition of the old code and the
migration plan are in **WEFTBASE-ARCHITECTURE.md** (the
`weft-server-research` notes beside this repository, 2026-11-03), with
its companions WEFTBASE-MODES.md, WEFTBASE-PROTOCOL.md and
WEFTBASE-NEXT-STEPS.md. This page is the short version the code
follows. Rebuild progress is tracked in `REBUILD-TODO.md` at the
repository root.

## Two modules

| Module | Path | Imports |
|---|---|---|
| `github.com/weftbase/weftbase` | repository root | weft, the client module, drivers |
| `github.com/weftbase/weftbase/client` | `client/` | weft, the standard library and OpenTelemetry only |

The server imports `client/wire`, `client/ulid` and friends for the
wire types. The client never imports the server, so an agent build
never compiles the server. The committed `go.work` joins both modules
with the weft checkout beside this repository (`../weft`).

## Layers

A package may import its own layer's allowed peers and anything below.
`make layering` (scripts/layering, table in `scripts/layering/layers.txt`)
enforces it in CI.

| Layer | Packages |
|---|---|
| L0 | weft, `client/...`, the standard library, third-party drivers |
| L1 | `store` (Tx, errors, migration runner), `tenant` (the `Scope` type), `events` (outbox, relay, hub), `obsdb` and its backends, `internal/...` |
| L2 | domains: `registry`, `queue`, `sessions`, `approvals`, `audit`, `budgets`, `webhooks`, `observe/...`, `worker/...`; and `authz` |
| L3 | `store/sqlite`, `store/postgres` (implement the domain repositories) |
| L4 | `api/http`, `api/mcp`, `console` (`console` imports only the standard library) |
| L5 | `weftbase` (the kernel), `cmd/weftbase` |

Domains import each other only along a declared DAG (`layers.txt`).
Where a lower domain needs a higher one, it declares a consumer
interface and the kernel wires it.

## Rules

- **Organize by domain.** A package owns one noun (approvals, sessions,
  registry…) with its types, service and repository interface. HTTP
  handlers live in `api/`, never in a domain.
- **One event outbox.** Every state change writes an `events` row in the
  same transaction as the change. Audit, webhooks, SSE and the Console
  read from it.
- **Store is authoritative.** No in-memory state that a restart or the
  next node cannot rebuild.
- **Tenant scope everywhere.** Every tenant-scoped row carries
  `(project_id, env, tenant_id)`. A `tenant.Scope` is built only in
  `authz` and `tenant`; every repository method takes one.
- **The public `/v1` API is the only door.** The Console, the CLI,
  `weftbase mcp`, SDKs and customers all use it. OTLP ingest is at
  `/otlp/v1/{traces,logs}`.
- **Scale by role from one binary.** `weftbase serve
  --roles=api,ingest,scheduler,supervisor`.

## Three stores

| Store | Holds | Backends |
|---|---|---|
| Control (`store/`) | tenants, keys, registry, run ledger, queue, approvals, audit, budgets, webhooks, the `events` outbox | SQLite; Postgres later |
| Thread (weft `thread`) | session entries, the journal | weft `thread/sqlite` |
| Trace (`obsdb`) | spans, records, derived runs, search, stats | SQLite, ClickHouse |
