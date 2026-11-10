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
| `github.com/weftbase/weftbase/client` | what your app imports: `exporter` (send traces), `rest` (call `/v1`), `worker` (serve agents as a build), `wire` (the worker protocol), `discovery`, `ulid` |

The client module imports weft, the standard library and OpenTelemetry
only, so an agent build never compiles the server.

## Quick start

```sh
weftbase dev                          # the server on http://127.0.0.1:7331, SQLite under ./.weftbase
weftbase dev -- go run ./cmd/app      # the server and your app, restarted on every .go save
```

To see it with data and no model provider, run the demo: a node on
`:7331`, two scripted agents (one embedded, one as a local build), two
versions each, and a new run every few seconds.

```sh
go run ./examples/demo                # or: make demo; prints the API key and a Console sign-in link
go run ./examples/demo -every 0       # the first batch only; -speed 0 removes the scripted latency
cd console/app && bun run dev         # beside it: the Console dev server on :3000, proxying /v1 to :7331
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

## Running agents

An agent build is a Go binary that serves weft agents with
`client/worker`:

```go
func main() {
	support := weft.New(model, weft.Name("support"), lookup, refund)
	if err := worker.Serve(worker.FromEnv(), worker.Agent(support)); err != nil {
		log.Fatal(err)
	}
}
```

```sh
weftbase serve --build ./bin/support   # the local placement: a supervised child per build
```

The supervisor copies the build into `<data>/builds/<sha256>`, starts
it with `WEFTBASE_SOCKET`, `WEFTBASE_TOKEN`, `WEFTBASE_BUILD_DIGEST` and
`WEFTBASE_PLACEMENT=local`, and records the build and an AgentVersion
per agent. A newer build of the same agents retires the older one;
runs parked on it still resume there (it is started again on demand).
`./bin/support --manifest` prints the build's manifests. In-process
agents use the embedded placement instead: `app.Agent(support)`.

Then `POST /v1/runs {"agent": "support", "input": {"prompt": "…"}}`
(the `prod` label, else the newest version), `POST
/v1/agents/support/labels/prod {"version": "agv_…"}` to promote,
`POST /v1/runs/{id}/cancel`, `/steer` and `/resume`.

## Layout

See [`docs/architecture.md`](docs/architecture.md) for the layers and
rules; `make layering` enforces them.

```
api/http/v1   the /v1 API: reads, agents, runs (admit, cancel, steer, resume)
api/mcp       the MCP tool set behind `weftbase mcp`
console       the embedded Console build (console/app is the Bun project)
observe/      ingest (OTLP), cost (pricing), local (in-process sink)
obsdb/        the trace store: sqlite, clickhouse, conformance suite
store, tenant the control store contract and the tenant Scope
registry, queue, sessions   agents and versions, the durable queue and timers, the run ledger
worker/       the worker hub, the router, the executors (embedded, local)
internal/     dblock, listen, doctor, reqlog, httpx, release, termsafe, weftsrc
cmd/weftbase  the CLI
client/       the client module
scripts/layering  the layering, client-dependency and vocabulary checks
```

## Development

Both modules require weft at its published version (v0.24.2). The
committed `go.work` joins `.`, `./client`, `../weft` and `../weft/core`, so
development builds against the weft checkout beside this repository; the
client module also builds without the workspace (`GOWORK=off`). The
server does not yet: it requires the client module at a placeholder
version until `client/v0.1.0` is tagged.

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
