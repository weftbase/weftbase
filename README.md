# weftbase

The platform for agents built with [weft](https://github.com/weftbase/weft):
Studio (the UI and its JSON API), the store it reads (SQLite or ClickHouse),
the runtime link that lets Studio drive a running app (playground,
breakpoints, approvals), the local exporter, and one CLI, `weftbase`.

**weft emits; weftbase stores, shows and controls.** weftbase imports the
weft framework; weft never imports weftbase. An app that only uses weft
sends its records anywhere OpenTelemetry goes; pointing them at weftbase
is one option among others.

Version: **v0.1.0** (not tagged yet). weftbase has its own version line and
reports the weft version it embeds: `weftbase version` prints both, e.g.
`weftbase v0.1.0 (weft v0.20.3)`.

## Install

```sh
go install github.com/weftbase/weftbase/cmd/weftbase@latest
```

(Works once weftbase and weft are tagged under these paths; until then,
build from a checkout — see Development.)

## Quick start

```sh
weftbase dev                          # Studio on http://127.0.0.1:7331: UI, OTLP ingest, SQLite (./.weftbase/weftbase.db)
weftbase dev -- go run ./cmd/app      # Studio and your app, restarted on every .go save
```

`weftbase dev` without `--` serves Studio alone; with a command after `--`
it also runs your app with `WEFT_ENV=dev`, `WEFTBASE_URL`, `WEFTBASE_TOKEN`
and `WEFTBASE_DB` set (`--watch dir` / `--no-watch` steer the restarts).
Any OpenTelemetry app can send to it:
`OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:7331`.

In a Go app:

```go
import "github.com/weftbase/weftbase/exporter"

defer exporter.Install()() // the Studio a running `weftbase dev` names, else ./.weftbase/weftbase.db
```

The other commands are thin clients of Studio's API: `weftbase runs`,
`weftbase open`, `weftbase export`, `weftbase doctor`, `weftbase mcp`
(a read-only stdio MCP server, docs/mcp-recipe.md), `weftbase vacuum`
and `weftbase version`. `weftbase <command> -h` lists a command's flags.

The whole command line — every command and flag, the dev token, the
discovery file and the one rule for when an app reads it (explicit >
environment > file) — is [docs/cli.md](docs/cli.md); the rule is its
section [When exporter reads the file](docs/cli.md#when-exporter-reads-the-file).

## Packages

| Import path | What it is |
|---|---|
| `github.com/weftbase/weftbase/studio` | Studio: the web UI (embedded `studio/dist`), its JSON API, OTLP ingest, the devtools panel |
| `github.com/weftbase/weftbase/studio/ingest`, `studio/runtime` | OTLP decoding; the server side of the runtime link |
| `github.com/weftbase/weftbase/obsdb` | the store's model and queries |
| `github.com/weftbase/weftbase/obsdb/sqlite`, `obsdb/clickhouse` | the two backends |
| `github.com/weftbase/weftbase/runtime` | the app side of the runtime link (`runtime.Install`) |
| `github.com/weftbase/weftbase/scope` | the devtools scope header |
| `github.com/weftbase/weftbase/exporter` | the local destinations: `exporter.Local` (SQLite), `exporter.Studio` (a running Studio), discovery |
| `github.com/weftbase/weftbase/cmd/weftbase` | the CLI |
| `examples/studio-local`, `examples/approvals`, `examples/devtools-vite` | runnable examples |

Docs: `docs/cli.md` (the `weftbase` command, the discovery file), `studio/README.md` (Studio, the panel, the API), `docs/mcp-recipe.md`
(`weftbase mcp`), `docs/testing.md` (the platform's gates),
`docs/adr/0018-studio-ui-stack.md`, `docs/adr/0024-observability-data.md`.
The record contract the store reads is weft's `docs/record-contract.md`.

## Environment

| Variable | Meaning |
|---|---|
| `WEFTBASE_URL`, `WEFTBASE_TOKEN` | the Studio an app or a CLI command talks to, and its token |
| `WEFTBASE_ADDR` | the address `weftbase dev` listens on |
| `WEFTBASE_DB` | the SQLite file (default `./.weftbase/weftbase.db`) |
| `WEFTBASE_DISCOVERY=off` | skip the discovery file (`./.weftbase/studio.json`, `$XDG_RUNTIME_DIR/weftbase`, the user cache dir) |
| `WEFT_ENV=dev` | weft's dev gate (content capture, the runtime link) |
| `WEFT_MANIFEST`, `WEFT_RETAIN`, `WEFT_PRICING`, `WEFT_LOG_LEVEL`, `WEFT_LOG_FORMAT`, `WEFT_CLICKHOUSE_DSN` | unchanged from weft 0.20 |

Panel tokens are `wb_pt.…`, live grants `wb_lg.…`.

## Development

weftbase requires `github.com/weftbase/weft` and `github.com/weftbase/weft/core`
at the placeholder version `v0.0.0-00010101000000-000000000000` until the
two-phase release tags them. Check weft out beside this repository and join
both in a `go.work` one level up:

```
go 1.26.0

use (
	./weft
	./weft/core
	./weftbase
)

replace (
	github.com/weftbase/weft v0.0.0-00010101000000-000000000000 => ./weft
	github.com/weftbase/weft/core v0.0.0-00010101000000-000000000000 => ./weft/core
)
```

Then, from this directory: `make build vet test` (`make offline` for the
offline gate, `make tidy` to tidy against the checkout without committing a
`replace`). The web UI (`studio/web`, Bun) is rebuilt with
`make studio-build`; `make studio-check` proves the committed
`studio/dist` is fresh. CI (`.github/workflows/ci.yml`) builds the same
workspace from weft's `main`.

## License

MIT (LICENSE).
