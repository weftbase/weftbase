# weftbase

The agent-native backend for agents built with
[weft](https://github.com/weftbase/weft). Agents are versioned,
deployable units; runs are placed on workers (embedded in your app, local
child processes, remote workers) through a durable queue; tenancy and
approvals are built to be embedded in your own product. One binary,
`weftbase`, serves the public `/v1` API, OTLP ingest and the Console.

**weft runs agents; weftbase stores, places and governs them.** weftbase
imports weft; weft never imports weftbase.

> **Status: rebuild in progress** on branch `rebuild` (a new structure,
> a fresh data directory; nothing reads a database of the old server).
> Progress and what works today: [`REBUILD-TODO.md`](REBUILD-TODO.md).
> Design: [`docs/architecture.md`](docs/architecture.md). The previous
> code is on the `legacy` branch (and its archive tag).

## Modules

| Module | What |
|---|---|
| `github.com/weftbase/weftbase` | the server: kernel, domains, stores, `/v1`, Console, CLI |
| `github.com/weftbase/weftbase/client` | what your app imports: `exporter` (send traces), `rest` (call `/v1`), `discovery`, `ulid`; later `worker` and `wire` |

The client module imports weft, the standard library and OpenTelemetry
only, so an agent build never compiles the server.

## Quick start

```sh
weftbase dev                          # the server on http://127.0.0.1:7331, SQLite under ./.weftbase
weftbase dev -- go run ./cmd/app      # the server and your app, restarted on every .go save
```

Any OpenTelemetry app can send to it:
`OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:7331/otlp` (the SDK
appends `/v1/traces` and `/v1/logs`).

In a Go app:

```go
import "github.com/weftbase/weftbase/client/exporter"

defer exporter.Install()() // WEFTBASE_URL + WEFTBASE_TOKEN, else the discovery file `weftbase dev` writes
```

Recording into an in-process SQLite file instead (no server):
`observe/local.Install()`.

## Layout

See [`docs/architecture.md`](docs/architecture.md) for the layers and
rules; `make layering` enforces them.

```
api/http/v1   the read API (being re-mounted from /api to /v1)
api/mcp       the MCP tool set behind `weftbase mcp`
console       the embedded Console build (console/app is the Bun project)
observe/      ingest (OTLP), cost (pricing), local (in-process sink)
obsdb/        the trace store: sqlite, clickhouse, conformance suite
store, tenant the control store contract and the tenant Scope
internal/     dblock, listen, doctor, reqlog, httpx, release, termsafe, weftsrc
cmd/weftbase  the CLI
client/       the client module
scripts/layering  the layering, client-dependency and vocabulary checks
```

## Development

weftbase builds against the weft checkout beside it: the committed
`go.work` joins `.`, `./client`, `../weft` and `../weft/core` (weft is not
tagged under its module path yet).

```sh
make build vet test    # both modules
make layering          # imports, client deps, retired words
make check             # all of the above
make tidy              # never commit a replace
```

ClickHouse tests are gated on `WEFT_CLICKHOUSE_DSN` (see
`obsdb/clickhouse/README.md`). Tests never call a model:
`make offline` runs them under `WEFT_MODEL_REQUESTS=deny`.

## License

See [LICENSE](LICENSE).
