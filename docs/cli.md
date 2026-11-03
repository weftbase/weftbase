# The `weftbase` command

Every subcommand is a thin client of the server API or of `v1.New`
— a convenience over options the app can set itself, so a team that
skips the CLI loses nothing. Every connection flag mirrors an
environment variable one to one (the second table). `weftbase <command> -h`
lists a command's flags; `weftbase -h` lists them all.

| Command | What it does |
|---|---|
| `weftbase dev [--addr] [--db] [--token] [--rotate-token] [--manifest] [--open] [--retain] [--pricing] [--log-level] [--log-format]` | The server alone (setup B): `v1.New` with the UI, OTLP ingest and a dev token stable per database. Takes 7331, else the next free port in 7331–7340; `--addr` pins. One server per SQLite file: the start takes `<db>.lock` beside a SQLite file — an exclusive OS lock (flock; LockFileEx on Windows) held while it serves, taken before any port is probed or bound, and released by the kernel when the process ends, a crash or `kill -9` included; a start that cannot (two at once, or from another directory) is refused, exit 1, naming the holder's URL and pid when its lock file or your discovery file says (`weftbase dev -- cmd` reuses it instead). A ClickHouse database is a shared server and takes no lock. Unless `--open` handed the browser the link, the banner ends `server: sign in with weftbase open --with-token`. Writes the discovery file apps find it through (below). The manifest is `--manifest`, else the nearest `weft.json` upward (one line says which). `--open` (default on a terminal) opens the UI with the token in the URL fragment. |
| `weftbase dev [dev's flags] [--no-watch] [--watch dir] -- command args…` | The server (as above, in-process, same port policy — or the running server on the same database, reused) plus your app run beside it and restarted on a `.go` save. The command is required (`go run .`, `go run ./cmd/app`; never `go run main.go`, whose file list has no build info). Prints one line per start: `server http://127.0.0.1:7331/ · app pid 4242 · runtime rt_… registered`. See [`weftbase dev -- cmd`](#weftbase-dev----cmd). |
| `weftbase runs [--agent] [--since 2h\|RFC3339] [--failed] [--status] [--limit] [--json]` | One row per run (id, agent, status, started with its zone, steps, duration, cost, public id when any, prompt, stopped by, and the error — its kind and why, so `--failed` is triageable) from `GET /api/runs`, the next step (`weftbase open`, `weftbase export`) on stderr; `--limit` defaults to 50 (0 lists all) and says so on stderr when it hid runs; `--json` for scripts. |
| `weftbase open [id] [--open] [--with-token]` | The id is a run id, a public id, a session id or a trace id (tried in that order); prints its page — `/runs`, `/conversations`, `/sessions`, `/traces` — and with no id the server root (`weftbase open --with-token` is a terminal's signed-in link to a fresh server), bare — a fixed token may be a signing key, so it stays out of logs; `--with-token` prints the `#token=` fragment too; `--open` hands the browser the link with the token. |
| `weftbase export <run id> [--format json\|jsonl\|otlp]` | `GET /api/runs/<id>/export` to stdout. |
| `weftbase export <run id> --wefttest ./pkg [--force]` | The run's save-as-test tree — the files of server's "save as test" zip, byte for byte: `<agent>_replay_test.go` beside `testdata/TestReplay_<run>/*.json`, a test that passes under `go test` (a replay's composes its source's fixtures for the steps it kept); a test file or fixtures already there need `--force`, which replaces the `*.json` fixtures (never merges). |
| `weftbase export <run id> --wefttest ./testdata --fixtures-only [--test TestName] [--force]` | The fixtures alone, unzipped into `./testdata/<TestName>/` (default: the run id), where `wefttest.Replay(t, "testdata")` reads them. |
| `weftbase doctor` | One line per check of a running server, each read from `GET /api/meta`: reachable, token (a missing one names `<db>.token` and `weftbase open --with-token`), the discovery file (this server, another, none), db, content, the newest run's age and weft version, the OTLP door, runtimes and the manifest — labelled by its source: `weft.json` (the configured file) or `runtime registrations` (no weft.json; the manifests runtimes registered), stale agents named. |
| `weftbase mcp [--url] [--token]` | A read-only MCP server over stdio for a coding agent (Claude Code, Cursor) to read runs while you debug — one tool per read route plus `explain_failure`; every tool is a GET. `claude mcp add weft -- weftbase mcp` (the discovery file finds the running server). The recipe is [mcp-recipe.md](mcp-recipe.md); `make mcp-check` runs it end to end. |
| `weftbase vacuum [--db] [--retain]` | One retention sweep (with `--retain` or `WEFT_RETAIN`), then `VACUUM` (SQLite) or `OPTIMIZE TABLE … FINAL` (ClickHouse); server's tables only. |
| `weftbase version` | The weftbase version and the weft version it embeds (`weftbase v0.1.0 (weft v0.20.3)`). |

| Flag | Environment | Default |
|---|---|---|
| `--addr` | `WEFTBASE_ADDR` | `127.0.0.1:7331` (unpinned) |
| `--db` | `WEFTBASE_DB` | `./.weftbase/weftbase.db` |
| `--token` | `WEFTBASE_TOKEN` | `weftbase dev`: the database's stable token, `<db>.token` (never printed); a generated one, printed, for a database with no file |
| `--manifest` | `WEFT_MANIFEST` | the nearest `weft.json` upward |
| `--retain` | `WEFT_RETAIN` | none: everything is kept |
| `--pricing` | `WEFT_PRICING` | the table shipped with this weft |
| `--log-level`, `--log-format` | `WEFT_LOG_LEVEL`, `WEFT_LOG_FORMAT` | info; text when a level is set |
| `--url` (runs, open, export, doctor, mcp) | `WEFTBASE_URL` | the discovery file's server, else `http://127.0.0.1:7331` — one resolver for every client, one stderr line naming the server and its source when you named none |

`weftbase dev`'s `--watch` and `--no-watch` shape the dev loop only and
have no environment mirror; like `--open` they
are not connection settings. `--rotate-token` has none either: it is an
action, not a setting.

## The dev token and the discovery file

The dev token is stable per database: the first start on a SQLite file
writes 32 random bytes (base64url) beside it — `<db>.token`, so
`./.weftbase/weftbase.db.token` by default, mode 0600 — and every later
start on that file serves the same token, so a restart keeps the
browser tab, the app's `WEFTBASE_TOKEN` and a second start's reuse
probe valid. `--rotate-token` writes a new one (scoped tokens signed
with the old one stop verifying); `--token` / `WEFTBASE_TOKEN` override
it and leave the file alone. The stable token is the scoped tokens'
signing key, like a fixed one, so it is never printed: the banner names
its file, `weftbase open --with-token` prints the fragment, `--open`
hands it to the browser. A database with no file (`:memory:`,
ClickHouse) gets a token generated for that process, printed.

Once its listener is bound, `weftbase dev` writes `dev.json` —
`{"url","token","db","pid","started","version"}`, mode 0600, the real
port in it — and removes it on a clean exit. It goes to the first of
`./.weftbase/` (when that directory exists: the project's own),
`$XDG_RUNTIME_DIR/weftbase/` (Linux), `os.UserCacheDir()/weftbase/`
(macOS, Windows, bare containers). `exporter.Install()` and
`runtime.Install()` read the same order when `WEFTBASE_URL` is unset,
so an app with `defer exporter.Install()()` exports to the running
server, and `runtime.Install` dials it, with no configuration — one
INFO line names the server joined. After a default start `./.weftbase`
always exists, so the file lands there: run the app from the directory
you ran `weftbase dev` in, or set `WEFTBASE_URL`. The file is trusted
only when its url is a loopback address (127.0.0.0/8, `::1`,
`localhost`), it is fresh (its pid alive, under 24 hours old) and, on
unix, it is mode 0600 and owned by you — a `dev.json` that arrived
through a git checkout (0644) or names another host is never used.
Anything untrusted is skipped without a word above Debug and the next
writer removes it. The writer also drops a `.gitignore` (`*`) into
`./.weftbase` when it has none, so the database, its token and the file
stay out of commits. Two servers never erase each other's file: the
second one's exit puts the first's back. A second `weftbase dev` on a
SQLite file a running server holds (`<db>.lock`) is refused before it
binds; a reusing `weftbase dev -- cmd` writes nothing (the running
server owns the file) and says so when that file is missing; its port
probe sends the stable token only to an address your discovery file
names (anything else on the port is probed bare). `weftbase dev` stops
gracefully on SIGHUP too, so a closed terminal removes the file.

The file is a convenience, never a requirement: `WEFTBASE_URL` always
wins (the file is not read), `WEFTBASE_DISCOVERY=off` turns the read
off (and the write: `weftbase dev` started under it writes no file and
says so in one line), `otel.NoEnv()` ignores it with the rest of the
environment, and a server named in code — `exporter.Remote(url, tok)` or
`runtime.server(url, tok)` — needs no file.

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

`runtime.Install` reads the file on its own, when it has no
`runtime.server` / `runtime.Local` and the installed pipeline has
neither a server nor a Local destination (`the remote endpoint()`,
`local.DB()` — any installed pipeline with an `exporter.Remote`
or `local.Local`, a plain `otel.Install`'s included): beside a
Local-only pipeline it opens no link and WARNs once (the link would
register with a server its runs never reach) — `runtime.Local(srv)`
dials the embedded server, `runtime.server(url, tok)` with
`exporter.Remote(url, tok)` a running one. The rule sees the
*installed* pipeline only: one started with
`exporter.Start(ctx, local.Local(p), otel.NoGlobal())` is not
installed, so `runtime.Install` without `runtime.server` /
`runtime.Local` still joins a discovered server, whose replays' spans
never arrive — name it there: `runtime.Local(srv)` or
`runtime.server(url, tok)`.

## `weftbase dev -- cmd`

```sh
weftbase dev -- go run ./cmd/app
```

`weftbase dev -- cmd` starts server exactly as `weftbase dev` does
(same flags, same port policy) — or reuses the
server already serving the same database — and runs the command after
`--` with four environment variables added to yours:

| Variable | Value | What reads it |
|---|---|---|
| `WEFT_ENV` | `dev` (kept when you already set it non-empty) | `runtime.Install` opens its link; weft's dev gate |
| `WEFTBASE_URL` | the server's URL | `exporter.Install`'s server destination; `runtime.Install`'s default endpoint |
| `WEFTBASE_TOKEN` | the server's token (the real one: stable, fixed or generated) | the same two |
| `WEFTBASE_DB` | the server's SQLite file, absolute (not set for ClickHouse) | `local.Local("")`'s path: the app's local sink and server share one file |

The last three override whatever your shell had. Everything here is an
env var or option the app can set itself — `weftbase dev` in one
terminal and `WEFT_ENV=dev WEFTBASE_URL=… WEFTBASE_TOKEN=… go run
./cmd/app` in another is the same thing without the command.

The app runs in its own process group; a `.go` change under the
working directory (`--watch dir`, repeatable, replaces it; `.git`,
`node_modules`, `vendor`, `testdata`, `.weftbase` and `dist` are
skipped) restarts it after 300 ms of quiet: SIGTERM to the group, five
seconds, then SIGKILL, then the command again. A build failure is
printed and the next save retries; an app that exits on its own is
reported with its exit code and the next save restarts it.
`--no-watch` turns the watching off, and `weftbase dev` then exits with
the app's exit code (127 when the command cannot start at all). A
directory that arrives with `.go` files in it (a checkout, a `mv`)
restarts too; editor lock files (`.#name.go`) do not; a directory that
cannot be watched (inotify's `max_user_watches` spent) is said in one
line. Ctrl-C, SIGTERM and SIGHUP (the terminal closing) stop the app
first — the same signal, SIGHUP sent as SIGTERM — then server. A
SIGKILL of `weftbase dev` cannot be caught: on Linux `go run` still
gets SIGTERM (Pdeathsig), but the binary it started — the grandchild —
can be orphaned; elsewhere the whole group can.

Each start prints one line: the UI link (bare by default — the stable
token and a fixed `--token` / `WEFTBASE_TOKEN` stay out of the log;
`#token=` only for a token generated for a database with no file), the
app's pid, and the first runtime that registered with this server
within five seconds — else `no runtime registered yet (the app needs
runtime.Install; WEFT_ENV=dev is set), or the app links its own
embedded server` and a later line when one does. With no browser
opened (`--open=false`, the default off a terminal) and a bare link,
one line first says where the token is: `server: token: <db>.token
(weftbase open --with-token)` when the file holds the token in use,
"the running server's, not <db>.token" for a reused server whose token
differs, or the fixed token's flag and variable. A server bound to
every interface (`--addr 0.0.0.0:7331`) is handed to the app, and
printed, as `127.0.0.1`.

Reuse follows the port policy, whose probe carries the fixed token
(`--token` / `WEFTBASE_TOKEN`), else the database's stable token — sent
only to an address a trusted discovery file names (from another
directory with `--db` on the same file the probe goes out bare and a
second server starts on the next port): a server already serving the
same database is reused (the app gets that URL and token;
`weftbase dev -- cmd` stops only the app), so two starts in a row
reuse. A running server walled by another token answers the probe 401
and is skipped — `weftbase dev -- cmd` takes the next port with its own
server. `weftbase dev` alone, by contrast, refuses a database a running
server holds.

`go run ./weftbase/examples/basic` records demo runs (a tool call, a
subagent, a failure) into an obsdb database and serves server on
`127.0.0.1:7331`; `examples/server-local` is setup A's five lines with
a live thread session.
