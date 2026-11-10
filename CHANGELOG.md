# Changelog

Notable changes to weftbase, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/); the project is
pre-1.0, so a release may change the API, the wire formats and the data
directory, and says so here. weftbase has its own version line,
independent of weft's, and reports the weft version it was built with
(`weftbase version`, `GET /v1/meta`).

## v0.1.0 — Unreleased

The first release. weftbase is the backend for agents built with
[weft](https://github.com/weftbase/weft): it registers agents as
versioned units, places their runs on workers through a durable queue,
keeps the record of every run, asks a person when a call needs one, and
shows all of it in the Console. One binary, `weftbase`. **weft runs
agents; weftbase stores, places and governs them.**

Built against weft v0.24.2. Nothing reads the data of the server this
repository held before (branch `legacy`, never released): a v0.1.0 node
starts on a fresh data directory.

### The node

- **The kernel** (`weftbase.New`, `App.Handler`, `App.Run`,
  `App.Shutdown`): one node is its stores, its wiring and its roles. An
  app can embed it and mount the handler under a prefix; `weftbase
  serve` is the same kernel behind a listener.
- **Roles**: `api` (the `/v1` API, the streams, the Console), `ingest`
  (OTLP at `/otlp/v1/{traces,logs}`), `scheduler` (the queue's claim
  loop, timers, the retention sweeper) and `supervisor` (local builds).
  `weftbase serve --roles …` runs any subset; the default is all four.
- **Stores**: the control store (registry, run ledger, queue, approvals,
  keys, the event outbox) on one SQLite file, and the trace store
  (spans, records, derived runs, search, statistics) on SQLite or
  ClickHouse. Each SQLite file is held under an OS lock: one server per
  data directory.

### Agents, versions and placements

- **Registry**: agents, builds (by digest), AgentVersions (`agv_…`,
  numbered per agent) and labels. A version is a build plus the manifest
  it registered. `POST /v1/agents/{name}/labels/{label}` moves a label
  (promote, roll back), compare-and-swap, audited.
- **Two placements.** *Embedded*: weft agents in the node's own process
  (`App.Agent`). *Local*: an agent build, a Go binary that serves its
  agents with `client/worker`, run by the node as a supervised child
  process (`App.Local`, `weftbase serve --build`); it is restarted when
  it crashes (what it wrote before it exited, a finished run's last
  answer included, is read before its other runs are marked lost), and
  an older build stays available to the runs pinned to it. A local build reports its traces and logs to its node with no
  wiring: the supervisor hands it the node's URL and an ingest key of
  its own (a worker key named `build:<digest>`, revoked when the build
  is retired and when the node stops). That key ingests only the
  records and spans of the runs dispatched to its build and their
  subagent children: a batch naming any other run is refused (403), and
  the rows it writes carry the key's id (`weftbase.ingest.key` on the
  resource). The child never inherits the node's own `WEFTBASE_URL` or
  `WEFTBASE_TOKEN`.
- **The run ledger**: `POST /v1/runs` resolves a version (a label, a
  pin, or the `prod` label then the newest), writes the run and
  enqueues it in one transaction. A worker at an agent's `Concurrency`
  takes no more of its runs until one ends, and the waiting runs start
  in the order they were admitted. Runs are dispatched at most once;
  cancel, steer and resume go through the ledger; a run whose worker
  vanished is marked lost, never sent twice. `session_id` makes a run
  the next turn of a session.
- **Approvals**: every call a run parks on is one approval record.
  `GET /v1/approvals` is the inbox; `POST /v1/approvals/{id}/decision`
  approves, denies or answers for the tool, bound to the arguments by
  their hash, with the decider recorded. The run resumes when its last
  approval is decided. An agent's approvals can expire: the call is then
  denied by the node and the run resumes.
- **Timers**: a queue time-to-live for runs no worker serves, approval
  expiry, and a maximum age for a parked run's pin.

### The API

- **`/v1`** is the only door: the Console, the CLI and `weftbase mcp`
  all use it. The contract is `api/openapi/v1.yaml`; a test holds it and
  the route table to the same operations.
- **Runs**: the list and the search (text, tool, model, error, window,
  cost, tokens, sort), each row with its `display_status` (queued,
  running, waiting, succeeded, failed, cancelled, interrupted), its
  AgentVersion, how the version was chosen and its placement. A parked
  run and the runs that resumed it are one logical run, the list's and
  the search's default (`chains=0` lists every run as it is): one row,
  one timeline with the waits in it, one set of logs. Queued runs no
  trace names yet are merged into the list by start under one cursor,
  `limit` honoured and counted exactly; a chain whose resume is still
  queued keeps its row. `status` is always the trace store's word
  (absent on a queued `ledger_only` row), `display_status` the one to
  draw; sessions carry a `display_status` too. A read cut by its budget
  says `partial`. A control store that cannot be read answers 503
  `unavailable` instead of guessing a run's state. A project-level
  credential reads every tenant's ledger runs and approvals, and
  cancels, steers, decides and deletes them under the run's own tenant
  (the events are that tenant's, the actor the caller); a tenant's key
  gets 404 on another tenant's run.
- **Run control**: admit (`POST /v1/runs`, a session's next turn
  refused with `session_busy` in the admitting transaction while the
  previous one is queued, running or waiting, numbered past every turn
  the session ever had), cancel, steer; a parked run resumes only
  through its approvals (`POST /v1/approvals/{id}/decision`, bound to
  the arguments the decider saw). Conflicts answer `already_finished`,
  `already_resumed`, `not_parked`, `not_running`. One spelling,
  `cancelled` (the event is `run.cancelled`), and one AgentVersion
  shape, `{id, number, labels}`, everywhere; a session's run events
  carry its `session_id`. A run cancelled while it waited on a person
  keeps its wait and lasts until the cancel. Every approval
  change writes its own `approval.*` event (`approval.cancelled` when
  its run ends without resuming). Deleting a run or a session goes
  through the ledger first, audited even for a run only the trace store
  knows.
- **A run**: steps, spans (with every subagent's, `tree=1`), events,
  transcript (as of any step), the model requests, the tool catalog, app
  logs, export (JSON, JSONL, OTLP), a two-run diff.
- **Sessions**: the list, a session as a conversation (one entry per
  turn, with its waits and the session's own marks: approvals, grants,
  steered and queued messages, compaction, forks), the timeline, the
  entries, export.
- **Agents**: the inventory (`/v1/agents?include=summary`, an agent with
  its versions, what changed between two versions, `/v1/tools`).
- **Statistics**: by agent, model, tool, error type or step, and one
  series of runs by outcome with cost and durations (`by=total`). A
  parked call (`weft.tool.pending`) is not counted as a tool call; on
  SQLite the rollup is written at ingest, so that holds for spans
  ingested by this release (a data directory of a pre-release build
  keeps its older counts); ClickHouse reads it at query time.
- **Streams** (Server-Sent Events): live runs and records
  (`/v1/runs/stream`, one run's, one session's), and the control
  store's outbox (`/v1/events`), resumable. A replay streams at the
  reader's pace however long the outbox is (a replay longer than the
  buffer used to never answer).
- **OTLP ingest**: any OpenTelemetry app sends traces and logs to
  `/otlp/v1`. weft apps get runs, steps and transcripts; stock GenAI
  spans of other frameworks get a derived run.
- **Auth**: API keys (`wb_sk_…` secret, `wb_wk_…` worker, `wb_pk_…`
  publishable), and for the Console a one-time sign-in link exchanged
  for an HttpOnly session cookie that lasts 12 hours from its last use.
  `POST /v1/auth/links` mints a fresh link on a running node. A link a
  signed-in session mints belongs to that session's family: the session
  it is traded for keeps the family's 30-day cap and ends at the
  family's sign-out. `DELETE /v1/auth/sessions` (`weftbase signout
  --all`) ends every Console session and unused link. Sign-in and
  sign-out are audited (`console.session_created`,
  `console.session_ended`) under the session's ref (`ses_…`, never its
  id); ended sessions and links are pruned.
- **CORS** is off unless `--allow-origin` (`AllowOrigins`) names
  origins; a request without a credential (`Open`'s Local principal)
  never gets CORS headers.

### The Console

The operator UI, embedded in the binary and served at `/`: Overview
(runs by outcome, what needs attention, a breakdown by agent, model,
tool and error), Runs (one row per logical run, filters, sorting, raw
traces), the run page (the trace waterfall with playback, the detail of
a step, transcript, logs, details, the approval to decide), Sessions (a
conversation with a waterfall per turn), Approvals (the inbox), Agents
(the inventory, tools, versions, moving a label), Settings, "New run",
and a command menu. It shows what the server returns; there is no mock
data in it.

### The command line

`weftbase serve`, `dev` (the server alone, or the server and your app,
restarted on a `.go` save), `signin` (a fresh Console sign-in link from
a running server), `signout --all`, `runs`, `open`, `export`, `doctor`, `mcp` (a
read-only MCP server over `/v1` for a coding agent), `vacuum`,
`version`. See `docs/cli.md`.

### The client module

`github.com/weftbase/weftbase/client` is what an app or an agent build
imports: `exporter` (send traces to a node), `worker` (serve agents as
a build), `rest` (call `/v1`), `wire` (the worker protocol), `discovery`
and `ulid`. It imports weft, the standard library and OpenTelemetry
only, never the server.

### The demo

`go run ./examples/demo` is weftbase in one command with nothing to
configure: a node, five agents across both placements, and a steady
trickle of runs. Every model in it is scripted (weft's `wefttest`): no
model provider is called and no key is needed.

### Not in v0.1.0

- **Tenancy** beyond one project and its project tenant: no end-user
  tokens, no per-tenant keys or isolation tests, no roles (a secret key
  and a Console operator may do everything; a restricted key nothing).
  **The trace store is not tenant-scoped**: a tenant's key reads every
  tenant's traces, spans, logs and sessions (the ledger's fields, queued
  runs and approvals are scoped; a delete of a run another tenant holds
  in the ledger is refused).
- **Run content behind `runs.content`**: the run lists' prompt preview
  and session title, and the spans' content attributes, are redacted
  for a caller without `runs.content`; until restricted keys land every
  credential that reads runs has it.
- **Approvals**: no quorum, no signed decisions forwarded to the
  worker, no policies or routing, no webhooks or hosted decision pages,
  no approvals stream of its own, no batch decide.
- **The audit chain**: audited events are written to the outbox, not
  hash-chained, and there is no audit API or page.
- **Builds**: no `weftbase push` / promote from the command line, no
  per-build environment or secrets (a restored build runs with the
  node's environment), no drain verbs as API.
- **Placements**: no remote workers, no container placement.
- **Sessions over weft `thread` in the ledger**: a ledger session is its
  runs; an app that keeps sessions with weft's `thread` package reports
  them over OTLP and the Console shows them, but the node does not run
  them.
- **Postgres** for the control store; **webhooks**; **budgets**.
- **Code generation** from the OpenAPI contract (the handlers and
  `client/rest` are written by hand).
- **In the Console**: variants, matrices and experiments; re-executing a
  run (edit and run again, run again from a step, rerun a child); save
  as test; breakpoints; the runtimes page; the in-page embed; comparing
  more than two runs; the loop and agent graph views; the critical-path
  flag; the live "peek" pane; per-browser pricing edits; the glossary;
  an Audit page.
