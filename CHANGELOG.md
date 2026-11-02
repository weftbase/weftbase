# Changelog

Notable changes to weftbase, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/); the project is
pre-1.0. weftbase has its own version line, independent of weft's, and
reports the weft version it embeds (`weftbase version` →
`weftbase v0.1.0 (weft v0.21.0)`).

## v0.1.0 — Unreleased

The first release. weftbase is the platform for agents built with
[weft](https://github.com/weftbase/weft): Studio (the web UI, its JSON
API, OTLP ingest and the devtools panel), the store it reads (SQLite or
ClickHouse), the runtime link that lets Studio drive a running app
(playground, breakpoints, approvals), the local exporter, and one CLI,
`weftbase`. **weft emits; weftbase stores, shows and controls**:
weftbase imports weft (`github.com/weftbase/weft`), weft never imports
weftbase.

### From weft

Everything here was part of weft up to 0.20.3 (module
`github.com/weftgo/weft`) and moved with its history; behaviour is
unchanged unless listed below. weft's
[`MIGRATION-0.21.md`](https://github.com/weftbase/weft/blob/main/MIGRATION-0.21.md)
is the step-by-step guide for an app moving across.

- `studio` (+ `studio/ingest`, `studio/runtime`, the web source and the
  embedded `studio/dist`) → `github.com/weftbase/weftbase/studio`.
- `obsdb` (+ `obsdb/sqlite`, `obsdb/clickhouse`, `obsdb/obsdbtest`) →
  `github.com/weftbase/weftbase/obsdb`.
- `runtime` → `github.com/weftbase/weftbase/runtime`; `scope` →
  `github.com/weftbase/weftbase/scope`.
- `cmd/weft` → `github.com/weftbase/weftbase/cmd/weftbase`; the binary
  is `weftbase`.
- The examples `studio-local`, `devtools-vite` and
  `thread/examples/approvals` (now `examples/approvals`); ADRs 0018 and
  0024; the `weft mcp` recipe (`docs/mcp-recipe.md`); the platform's
  testing page (`docs/testing.md`); the CLI chapter of weft's README
  (`docs/cli.md`).

### The exporter package

`github.com/weftbase/weftbase/exporter` is the local developer
platform's side of weft's observability, on top of weft's `otel`
(which since weft 0.21 reads only the standard `OTEL_*` variables and
knows no Studio):

- `exporter.Local(path)` (SQLite, written synchronously through
  `otel.Sink`), `exporter.Studio(url, token)` (OTLP/HTTP with a bearer
  token, content on, short batches) and `exporter.NoDiscovery()` —
  weft 0.20's `otel.Local`, `otel.Studio` and `otel.NoDiscovery`.
- `exporter.Install(…)` / `exporter.Start(ctx, …)`: `otel.Install` /
  `otel.Start` plus the platform's environment — `WEFTBASE_URL` /
  `WEFTBASE_TOKEN`, the discovery file of a running `weftbase dev`,
  `WEFTBASE_DB` — and, for `Install`, the `exporter.Local("")` fallback
  when nothing is configured (weft 0.20's zero-config `otel.Install`).
  The rule is explicit > environment > file (`docs/cli.md`, "When
  exporter reads the file").
- `exporter.LocalDB()` / `exporter.LocalDBOf(p)`,
  `exporter.StudioEndpoint()` / `exporter.StudioEndpointOf(p)` and
  `exporter.FromSDKSpans` / `exporter.FromSDKRecords` (weft 0.20's
  `otel.LocalDB`, `p.LocalDB`, `otel.StudioEndpoint`,
  `p.StudioEndpoint`, `otel.FromSDKSpans`, `otel.FromSDKRecords`). They
  read any pipeline with an `exporter.Local` / `exporter.Studio`
  destination — one a plain `otel.Install` built included, so
  `runtime.Install` dials the Studio of
  `otel.Install(exporter.Local(p), exporter.Studio(url, tok))` — through
  weft's `otel.Tag` and `Pipeline.Tags`; the environment's Studio is an
  `otel.Environment` destination, de-duplicated by URL against an
  explicit `exporter.Studio` or `otel.OTLP` (the explicit one wins).

### Renamed (breaking against weft 0.20; no fallback reads the old names)

- Commands: `weft studio` → `weftbase dev` (Studio alone; a database a
  running Studio holds is refused); `weft dev -- cmd` →
  `weftbase dev -- cmd` (the command is now required — `weft dev` alone
  ran `go run .`; this form reuses a running Studio on the same
  database); `weft runs`, `open`, `export`, `doctor`, `mcp`, `vacuum`,
  `version` → `weftbase …` with the same flags (`weftbase version`
  prints both versions).
- Environment: `WEFT_STUDIO_URL` → `WEFTBASE_URL`, `WEFT_STUDIO_TOKEN` →
  `WEFTBASE_TOKEN`, `WEFT_STUDIO_ADDR` → `WEFTBASE_ADDR`, `WEFT_DB` →
  `WEFTBASE_DB`, `WEFT_DISCOVERY` → `WEFTBASE_DISCOVERY`. Unchanged:
  `WEFT_ENV`, `WEFT_MODEL_REQUESTS`, `WEFT_MANIFEST`, `WEFT_RETAIN`,
  `WEFT_PRICING`, `WEFT_LOG_LEVEL`, `WEFT_LOG_FORMAT`,
  `WEFT_CLICKHOUSE_DSN`, the `OTEL_*` variables.
- Files: the local data directory `./.weft/` → `./.weftbase/`; the
  default database `./.weft/weft.db` → `./.weftbase/weftbase.db` (its
  dev token `<db>.token` beside it); the discovery file `studio.json`
  in `./.weftbase/`, `$XDG_RUNTIME_DIR/weftbase/` or
  `<user cache dir>/weftbase/`.
- Tokens: panel tokens `weft_pt.…` → `wb_pt.…`, live grants
  `weft_lg.…` → `wb_lg.…`; tokens a 0.20 Studio minted are not
  accepted.

The `@weftgo/devtools` npm package keeps its name.
