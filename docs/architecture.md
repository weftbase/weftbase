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

## The kernel and the HTTP front

`weftbase.New(opts…)` opens the stores — by default `<data>/control.db`
and `<data>/traces.db` under `DataDir` (`./.weftbase`), each SQLite file
under its OS lock (`internal/dblock`) — wires the services and returns
an `App`. `App.Handler()` serves the node's roles and mounts under a
prefix (`http.StripPrefix` plus `Base`); `App.Run(ctx)` runs the
background work (the outbox hub's pump on `api`, the retention sweeper
on `scheduler`); `App.Shutdown` stops it and closes what `New` opened.

`api/http` is the one router. Every route is a row of one table —
method, pattern, `authz` action, handler — and the middleware runs in
this order: request id (`internal/reqlog`), CORS (never with
credentials), the URL-credential refusal, authentication (bearer key,
then the `wb_session` cookie, then `Open`'s Local principal), the guards
(Local: loopback Host and same-origin writes; a cookie session: the
`Weftbase-CSRF: 1` header and same-origin writes), then
`authz.Can(principal, action)`. `/v1/*`, `/otlp/*`, `/healthz`,
`/readyz` and `/metrics` never fall through to the Console: an unknown
one is a JSON 404, a wrong method a 405 with `Allow`. Ids in paths are
percent-encoded (a child run id carries `/`) and decoded once.

`api/openapi/v1.yaml` is the contract. A test (`make api-check`) holds
it and the route table to the same operations and actions; code
generation from it lands later.
