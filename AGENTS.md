# weftbase: guide for coding agents and contributors

The short map. The design is `docs/architecture.md` (and the plan it
points to); rebuild progress and next steps are in `REBUILD-TODO.md`.
weft's own `AGENTS.md` maps the framework.

## Modules

- `github.com/weftbase/weftbase` (root): the server.
- `github.com/weftbase/weftbase/client` (`client/`, own `go.mod`): what
  apps and agent builds import. It imports weft, the standard library and
  OpenTelemetry only, never the server (`make client-deps`).
- The committed `go.work` joins both with `../weft` and `../weft/core`.
  `go.mod` files never commit a `replace` (`make tidy`).

## Package map

| Package | Layer | Role |
|---|---|---|
| `client/ulid`, `client/discovery`, `client/exporter`, `client/rest` | L0 | ids, the dev discovery file, the OTLP destination (`Remote`, `Install`), the `/v1` client |
| `store` | L1 | `Tx`, `Runner`, error kinds, the migration runner |
| `tenant` | L1 | the `Scope` (project, env, tenant); `NewScope` only from `authz` and `tenant/...` |
| `events` | L1 | the outbox: `Emit` in the change's transaction, relay, hub |
| `obsdb` (+ `sqlite`, `clickhouse`, `obsdbtest`) | L1 | the trace store and its conformance suite |
| `internal/...` | L1 | dblock, listen, doctor, reqlog, httpx, release, termsafe, weftsrc |
| `observe/ingest`, `observe/cost`, `observe/local` | L2 | OTLP ingest at `/otlp/v1/*`, pricing, the in-process sink |
| `authz`, `registry`, `queue`, `sessions`, `approvals`, `audit`, `budgets`, `webhooks`, `worker` | L2 | domains (landing per `REBUILD-TODO.md`) |
| `store/sqlite`, `store/postgres` | L3 | control-store backends |
| `api/http/v1`, `api/mcp`, `console` | L4 | the API, the MCP tools, the embedded Console |
| `weftbase` (root), `cmd/weftbase`, `examples`, `scripts` | L5 | kernel, CLI, tooling |

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
make check        # build, vet, race tests in both modules, layering, vocabulary
make test         # race tests, both modules
make layering     # the import checks
```

ClickHouse: `WEFT_CLICKHOUSE_DSN=clickhouse://default:weft@127.0.0.1:9000/default go test -race ./obsdb/clickhouse/...`.
A gated test that skips is a failure in CI.
