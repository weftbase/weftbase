# weftbase: guide for coding agents and contributors

The short map. The design is `docs/architecture.md` (and the plan it
points to); rebuild progress and next steps are in `REBUILD-TODO.md`.
weft's own `AGENTS.md` maps the framework.

## Modules

- `github.com/weftbase/weftbase` (root): the server.
- `github.com/weftbase/weftbase/client` (`client/`, own `go.mod`): what
  apps and agent builds import. It imports weft, the standard library and
  OpenTelemetry only, never the server (`make client-deps`).
- The committed `go.work` joins both; weft comes from the module proxy
  at the version the `go.mod` files require (to work against a weft
  checkout, `go work use ../weft ../weft/core` locally, never
  committed). `go.mod` files never commit a `replace` (`make tidy`).

## Package map

| Package | Layer | Role |
|---|---|---|
| `client/ulid`, `client/discovery`, `client/exporter`, `client/rest`, `client/wire` | L0 | ids, the dev discovery file, the OTLP destination (`Remote`, `Install`), the `/v1` client, the worker protocol's types |
| `store` | L1 | `Tx`, `Runner`, error kinds, the migration runner |
| `store/sqlite` | L3 | the control store on one SQLite file (`BEGIN IMMEDIATE`, `AfterCommit`, migrations) |
| `tenant`, `tenant/keys` | L1 | the `Scope` (project, env, tenant); `NewScope` only from `authz` and `tenant/...`; API keys (`wb_<sk\|rk\|pk\|wk>_<env>_…`, hashed, audited) |
| `events` | L1 | the outbox: `Emit` in the change's transaction, relay, hub |
| `obsdb` (+ `sqlite`, `clickhouse`, `obsdbtest`) | L1 | the trace store and its conformance suite |
| `internal/...` | L1 | dblock, listen, doctor, reqlog, httpx, release, termsafe, weftsrc |
| `observe/ingest`, `observe/cost`, `observe/local` | L2 | OTLP ingest at `/otlp/v1/*`, pricing, the in-process sink |
| `authz` | L2 | credential → `Principal` + `Scope` (bearer key, then the `wb_session` cookie, then `Open`), `Can(principal, action)`, Console sign-in links and sessions |
| `registry` | L2 | agents, builds, AgentVersions, labels (`Promote`, `Resolve`), registered manifests |
| `queue` | L2 | durable items with leases and fencing, timers; the only package besides authz and tenant that rebuilds a `Scope` (from its rows) |
| `sessions` | L2 | the run ledger: `Admit`, `Cancel`, `Resume`, and the hub's `RunSink` side |
| `worker`, `worker/hub`, `worker/executor` | L2 | the `Executor` interface and the router; the hub (claim, dispatch, acks); the embedded executor |
| `approvals`, `audit`, `budgets`, `webhooks` | L2 | domains (landing per `REBUILD-TODO.md`) |
| `store/postgres` | L3 | the Stage 1 control-store backend (later) |
| `api/http` (package `httpapi`) | L4 | the router: request ids, CORS, guards, authentication, `authz.Can` per route from one table, `/healthz` `/readyz` `/metrics`, Console sign-in, the Console at `/` |
| `api/http/v1` | L4 | the `/v1` read handlers, streams and `/otlp/v1/*` ingest, each route with its action (`Routes`, `IngestRoutes`) |
| `api/openapi/v1.yaml` | — | the `/v1` contract; `make api-check` tests it against the route table |
| `api/mcp`, `console` | L4 | the MCP tools, the embedded Console |
| `weftbase` (root) | L5 | the kernel: `New(opts…)`, `Handler`, `Run`, `Shutdown`, `Ready`, roles; opens the stores under the data dir and holds their locks |
| `cmd/weftbase`, `examples`, `scripts` | L5 | CLI, examples, tooling |

## Rules

- A package imports only its layer's declared peers and lower layers;
  domains import each other only along the DAG in
  `scripts/layering/layers.txt`. Add every new package to that table.
- HTTP handlers live in `api/`, never in a domain.
- Every state change writes its `events` row in the same transaction.
- Every tenant-scoped row carries `(project_id, env, tenant_id)`; every
  repository method takes a `tenant.Scope`.
- The public `/v1` API is the only door; OTLP is `/otlp/v1/*`.
- The retired product words of the old UI do not appear in
  code, paths or docs (`make vocab`); the UI is the Console.
- Tests run offline: no model calls (`make offline`).

## Working here

```sh
make check        # build, vet, race tests in both modules, layering, client-deps, vocabulary
make test         # race tests, both modules
make layering     # the import checks
make api-check    # the OpenAPI spec against the route table
```

ClickHouse: `WEFT_CLICKHOUSE_DSN=clickhouse://default:weft@127.0.0.1:9000/default go test -race ./obsdb/clickhouse/...`.
A gated test that skips is a failure in CI.
