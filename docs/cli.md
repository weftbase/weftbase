# The `weftbase` command

One binary: the server (`serve`, `dev`, built on the kernel,
`weftbase.New`) and thin clients of its `/v1` API (`runs`, `open`,
`export`, `doctor`, `mcp`). Every subcommand is a convenience over
options an app can set itself. `weftbase <command> -h` lists a
command's flags.

| Command | What it does |
|---|---|
| `weftbase serve [node flags] [--roles api,ingest,scheduler,supervisor] [--console=true]` | One node with the roles named (default all): `api` serves `/v1`, the Console, `/healthz`, `/readyz`, `/metrics`; `ingest` serves `/otlp/v1/{traces,logs}`; `scheduler` runs the retention sweeper. Credentials are API keys; `WEFTBASE_BOOTSTRAP_KEY` (a `wb_sk_test_…` key) is imported when set. With the api role and the Console on, it prints a one-time Console sign-in link. Writes no discovery file. |
| `weftbase dev [node flags] [--open]` | `serve` with every role on SQLite under `./.weftbase`, a bootstrap secret key kept in `<data>/bootstrap.key` (0600, created once, never printed), the discovery file, and a one-time Console sign-in link (`http://127.0.0.1:7331/#signin=<token>`, 15 minutes; `--open` opens it instead). Takes 7331, else the next free port in 7331–7340; `--addr` pins. A second `dev` on a data dir a running server holds is refused, naming its URL and pid. |
| `weftbase dev [node flags] [--watch dir] [--no-watch] -- cmd args…` | The server (or the one already running on the data dir) plus your app, restarted on `.go` changes (below). |
| `weftbase runs [--agent] [--since 2h\|RFC3339] [--failed] [--status] [--limit] [--json]` | One row per run from `GET /v1/runs`; `--limit` defaults to 50 (0 lists all); `--json` for scripts. |
| `weftbase signin [--origin url] [--open]` | Prints a fresh one-time Console sign-in link from the running server (valid once, 15 minutes). The links `weftbase dev` prints at start are spent once; this is how to sign in later without a restart. Needs a secret key: `--token`, `WEFTBASE_TOKEN`, or the discovery file of a `weftbase dev` on this machine. `--origin http://localhost:3000` makes the link for a Console dev server. |
| `weftbase open [id] [--open]` | The id is a run id, a public id, a session id or a trace id (tried in that order); prints its Console page, or the server root with no id. Links carry no credential: the Console signs in with a one-time link. |
| `weftbase export <run id> [--format json\|jsonl\|otlp]` | `GET /v1/runs/<id>/export` to stdout. |
| `weftbase doctor` | One line per check, from `GET /v1/meta` and `GET /readyz`: reachable, key (who the key is and its scope; a 401 names `<data>/bootstrap.key`), ready, the discovery file, the trace db, content, the newest run, the OTLP door, ingest health, the manifest. Exits 1 on a failed check. |
| `weftbase mcp [--url] [--token]` | A read-only MCP server over stdio for a coding agent; every tool is a GET on `/v1`. The recipe is [mcp-recipe.md](mcp-recipe.md); `make mcp-check` runs it end to end. |
| `weftbase vacuum [--db] [--retain]` | One retention sweep and a VACUUM of the trace database (default `./.weftbase/traces.db`). |
| `weftbase version` | weftbase's and weft's versions. |

## Flags and environment

| Flag | Environment | Meaning |
|---|---|---|
| `--addr` | `WEFTBASE_ADDR` | listen address (`serve`, `dev`) |
| `--data` | `WEFTBASE_DATA` | the data dir, default `./.weftbase`: `control.db`, `traces.db`, `bootstrap.key` |
| `--control-db` | | `sqlite://path` (default `<data>/control.db`); `postgres://` is not supported yet |
| `--trace-db` | | `sqlite://path` (default `<data>/traces.db`) or `clickhouse://user:pass@host:9000/db` |
| `--manifest` | `WEFT_MANIFEST` | the weft.json; default the nearest one upward |
| `--retain` | `WEFT_RETAIN` | trace retention (a Go duration, at least 1h) |
| `--pricing` | `WEFT_PRICING` | a pricing override file |
| `--allow-origin` | | a CORS origin (repeatable) |
| `--log-level`, `--log-format` | `WEFT_LOG_LEVEL`, `WEFT_LOG_FORMAT` | the server log on stderr |
| `--url` | `WEFTBASE_URL` | the server the API commands talk to; unset, the discovery file's, else `http://127.0.0.1:7331` |
| `--token` | `WEFTBASE_TOKEN` | the API key the API commands send; unset, the discovery file's |

## The bootstrap key and the discovery file

`weftbase dev` keeps one secret key per data dir, `<data>/bootstrap.key`
(`wb_sk_test_…`, mode 0600, created on the first start and kept), and
imports it into the default project's test environment. Every API
request and OTLP batch carries it as `Authorization: Bearer <key>`.
Once bound, `dev` writes the discovery file — `dev.json`,
`{"url","token","pid",…}`, mode 0600, the token being the key — to the
first of `./.weftbase/` (when it exists), `$XDG_RUNTIME_DIR/weftbase/`,
`os.UserCacheDir()/weftbase/`, and removes it on a clean exit.
`exporter.Install()` reads it when `WEFTBASE_URL` is unset, trusting it
only when its url is loopback, it is fresh and, on unix, it is 0600 and
yours. `WEFTBASE_URL` always wins; `WEFTBASE_DISCOVERY=off` turns the
read and the write off.

The Console never sees the key: it signs in with the one-time link,
which `POST /v1/auth/sessions` exchanges for an `HttpOnly`,
`SameSite=Strict` session cookie.

### When exporter reads the file

The one rule: **explicit > environment > file.**

- **Explicit** — a server or a store named in code. `exporter.Install`
  and `exporter.Start` read the file only when their options name
  neither an `exporter.Remote` nor a `local.Local` (an `otel.OTLP`,
  `otel.Datadog`, `otel.Langfuse` or `otel.Exporters` destination does
  not switch it off — add `exporter.NoDiscovery()`). A pipeline that
  names its store writes only to what it names plus the environment's
  destinations: `exporter.Install(local.Local(path))` never joins a
  running server; to write to both, name both:
  `exporter.Install(local.Local(path), exporter.Remote(url, tok))`.
- **Environment** — `WEFTBASE_URL` (with `WEFTBASE_TOKEN`) adds a server
  destination and the file is not read. An explicit destination on the
  same URL — an `exporter.Remote` or a plain `otel.OTLP` — wins: the
  environment's is not added a second time.
- **File** — only then, and only when neither `exporter.NoDiscovery()`
  (this pipeline), `WEFTBASE_DISCOVERY=off` (the process) nor
  `otel.NoEnv()` is set. `exporter.NoDiscovery()` skips the file alone
  and keeps `WEFTBASE_URL`, `OTEL_*` and the `WEFT_ENV` gate;
  `otel.NoGlobal()` does not stop the read.

A plain `otel.Install` (weft alone) reads none of this: only the
`OTEL_*` variables, and with nothing configured it records nothing.
`local.Install` (`exporter.Install` with `exporter.WithFallback(local.Local(""))`) is the platform's zero-config entry point; with
nothing configured anywhere it writes `local.Local("")`
(`$WEFTBASE_DB`, else `./.weftbase/weftbase.db`).

## `weftbase dev -- cmd`

`weftbase dev -- cmd` starts the server as `weftbase dev` does — or
reuses the server already running on the same data dir — and runs the
command with these variables added (the last four override your
shell's):

| Variable | Value |
|---|---|
| `WEFT_ENV` | `dev` (kept when already set) |
| `WEFTBASE_URL` | the server's URL |
| `WEFTBASE_TOKEN` | the bootstrap key |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `<url>/otlp` |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Authorization=Bearer%20<key>` |

Any OTel SDK exports to the server with those two `OTEL_` variables; a
Go app with `exporter.Install` uses `WEFTBASE_URL` and `WEFTBASE_TOKEN`
and does not export twice.

The app runs in its own process group; a `.go` change under the
working directory (`--watch dir`, repeatable, replaces it; `.git`,
`node_modules`, `vendor`, `testdata`, `.weftbase` and `dist` are
skipped) restarts it after 300 ms of quiet: SIGTERM to the group, five
seconds, then SIGKILL, then the command again. A build failure is
printed and the next save retries. `--no-watch` turns watching off, and
`weftbase dev` then exits with the app's exit code (127 when the
command cannot start). Ctrl-C, SIGTERM and SIGHUP stop the app first,
then the server.
