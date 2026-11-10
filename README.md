# weftbase

The backend for agents built with
[weft](https://github.com/weftbase/weft). Agents are versioned,
deployable units; their runs are placed on workers through a durable
queue; a call that needs a person waits for one; and every run is
recorded and shown. One binary, `weftbase`, serves the `/v1` API, OTLP
ingest and the Console.

**weft runs agents; weftbase stores, places and governs them.** weftbase
imports weft; weft never imports weftbase.

> **Status: v0.1.0, not tagged yet.** What is in it and what is not:
> [`CHANGELOG.md`](CHANGELOG.md). What remains before the tag:
> [`REBUILD-TODO.md`](REBUILD-TODO.md). Design:
> [`docs/architecture.md`](docs/architecture.md).

## Quick start: the demo

From a checkout (with the weft checkout beside it, see Development):

```sh
go run ./examples/demo
```

It starts a node on `http://127.0.0.1:7331` over a throwaway data
directory, registers five agents of a make-believe kitchenware store in
both placements, plays a first batch of runs and then starts a new one
every few seconds. It prints an API key and a sign-in link for the
Console: open the link.

Every model in the demo is scripted (weft's `wefttest`): **no model
provider is called and no key is needed.** What you see is real
otherwise: the runs go through the public API, the queue, the workers
and OTLP ingest.

```sh
go run ./examples/demo -every 0          # the first batch only
go run ./examples/demo -speed 0          # no scripted latency
go run ./examples/demo -pending-every 3  # leave every third refund for you to approve
```

Press Enter in its terminal for a fresh sign-in link.

## Your own agent

```sh
go install github.com/weftbase/weftbase/cmd/weftbase@latest   # once v0.1.0 is tagged; until then: go build ./cmd/weftbase
weftbase dev                          # a node on http://127.0.0.1:7331, SQLite under ./.weftbase
```

`weftbase dev` prints a Console sign-in link and keeps a secret API key
in `.weftbase/bootstrap.key`.

**Send it traces.** Any weft app reports to the node with one line, and
any other OpenTelemetry app with one variable:

```go
import "github.com/weftbase/weftbase/client/exporter"

defer exporter.Install()() // WEFTBASE_URL + WEFTBASE_TOKEN, else the running `weftbase dev` on this machine
```

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:7331/otlp   # plus an Authorization header with the key
weftbase dev -- go run ./cmd/app      # the node and your app together, restarted on every .go save
```

**Let it run the agent.** An agent build is a Go binary that serves weft
agents with `client/worker`:

```go
func main() {
	defer exporter.Install()() // the node hands the build its URL and an ingest key
	support := weft.New(model, weft.Name("support"), lookup, refund)
	if err := worker.Serve(worker.FromEnv(), worker.Agent(support)); err != nil {
		log.Fatal(err)
	}
}
```

```sh
go build -o bin/support ./cmd/support
weftbase serve --build ./bin/support  # the local placement: a supervised child process per build
```

The node copies the build into its data directory, starts it, and
records an AgentVersion per agent. A newer build of the same agents
takes over; runs parked on the older one still resume there. Agents
that live in your own process use the embedded placement instead
(`weftbase.New(…)`, `app.Agent(support)`; see `examples/demo`).

Then, with the API key as a bearer:

```sh
curl -X POST :7331/v1/runs -d '{"agent":"support","input":{"prompt":"Where is order 1042?"}}'
curl -X POST :7331/v1/agents/support/labels/prod -d '{"version":"agv_…"}'   # promote
curl :7331/v1/approvals?status=pending                                       # calls waiting for a person
curl -X POST :7331/v1/approvals/appr_…/decision -d '{"decision":"approve","args_sha256":"…"}'
```

`api/openapi/v1.yaml` is the whole contract.

## The Console

Served by the node at `/`. A sign-in link works once and for 15
minutes; a session lasts 12 hours from its last use. For a fresh link
from a running node:

```sh
weftbase signin                                               # beside `weftbase dev`: no flag needed
weftbase signin --url http://127.0.0.1:7331 --token wb_sk_…   # any node, with a secret key
```

## Modules

| Module | What |
|---|---|
| `github.com/weftbase/weftbase` | the server: kernel, domains, stores, `/v1`, the Console, the CLI |
| `github.com/weftbase/weftbase/client` | what your app imports: `exporter` (send traces), `worker` (serve agents as a build), `rest` (call `/v1`), `wire` (the worker protocol), `discovery`, `ulid` |

The client module imports weft, the standard library and OpenTelemetry
only, so an agent build never compiles the server.

## Layout

See [`docs/architecture.md`](docs/architecture.md) for the layers and
the rules; `make layering` enforces them.

```
api/http/v1   the /v1 API: runs, sessions, approvals, agents, stats, streams, OTLP ingest
api/mcp       the MCP tool set behind `weftbase mcp`
console       the embedded Console build (console/app is the Bun project)
observe/      ingest (OTLP), cost (pricing), local (an in-process sink)
obsdb/        the trace store: sqlite, clickhouse, the conformance suite
store, tenant the control store contract and the tenant Scope
registry, queue, sessions   agents and versions, the durable queue and timers, the run ledger and approvals
worker/       the worker hub, the router, the executors (embedded, local)
cmd/weftbase  the CLI (docs/cli.md)
client/       the client module
examples/     demo (the tour), approvals (weft's thread approvals, standalone)
```

## Development

Both modules require weft at its published version (v0.24.2). The
committed `go.work` joins `.`, `./client`, `../weft` and `../weft/core`, so
development builds against the weft checkout beside this repository; the
client module also builds without the workspace (`GOWORK=off`). The
server does not yet: it requires the client module at a placeholder
version until `client/v0.1.0` is tagged.

```sh
make build vet test    # both modules, race tests
make check             # those plus the layering and vocabulary checks
make api-check         # the OpenAPI contract against the route table
make mcp-check         # `weftbase mcp` end to end against a real node
make demo              # go run ./examples/demo
make tidy              # never commit a replace
```

Tests never call a model: `make offline` runs them under
`WEFT_MODEL_REQUESTS=deny`. ClickHouse tests are gated on
`WEFT_CLICKHOUSE_DSN` (see `obsdb/clickhouse/README.md`). The Console
has its own checks (`console/app/README.md`).

## License

See [LICENSE](LICENSE).
