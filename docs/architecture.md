# Architecture

Weftbase is the agent-native backend for agents built with weft. Agents
are versioned, deployable units. Runs are placed on workers (embedded in
the host app, local child processes, remote workers) through a durable
queue. Tenancy and approvals are built for embedding in a customer's own
product. It is not a database-as-a-service: the store exists to serve
agents, their runs, sessions and approvals.

The full design, the file-by-file disposition of the old code and the
migration plan are in the design docs (private). This page is the
short version the code follows. Rebuild progress is tracked in
`REBUILD-TODO.md` at the repository root. `main` is the rebuild; the
old code is on the `legacy` branch (and its archive tag), never
released.

## Two modules

| Module | Path | Imports |
|---|---|---|
| `github.com/weftbase/weftbase` | repository root | weft, the client module, drivers |
| `github.com/weftbase/weftbase/client` | `client/` | weft, the standard library and OpenTelemetry only |

The server imports `client/wire`, `client/ulid` and friends for the
wire types. The client never imports the server, so an agent build
never compiles the server. The committed `go.work` joins both modules;
weft comes from the module proxy at the version the `go.mod` files
require.

## Layers

A package may import its own layer's allowed peers and anything below.
`make layering` (scripts/layering, table in `scripts/layering/layers.txt`)
enforces it in CI.

| Layer | Packages |
|---|---|
| L0 | weft, `client/...`, the standard library, third-party drivers |
| L1 | `store` (Tx, errors, migration runner), `tenant` (the `Scope` type), `events` (outbox, relay, hub), `obsdb` and its backends, `internal/...` |
| L2 | domains: `registry`, `queue`, `sessions`, `approvals`, `audit`, `budgets`, `webhooks`, `observe/...`, `worker/...`; and `authz` |
| L3 | `store/sqlite`, `store/postgres` (implement the domain repositories), `store/storetest` (their conformance suite) |
| L4 | `api/http`, `api/mcp`, `console` (`console` imports only the standard library) |
| L5 | `weftbase` (the kernel), `cmd/weftbase` |

Domains import each other only along a declared DAG (`layers.txt`).
Where a lower domain needs a higher one, it declares a consumer
interface and the kernel wires it.

## Rules

- **Organize by domain.** A package owns one noun (approvals, sessions,
  registry…) with its types, service and repository interface. HTTP
  handlers live in `api/`, never in a domain.
- **One event outbox.** Every state change writes an `events` row in the
  same transaction as the change. Audit, webhooks, SSE and the Console
  read from it.
- **Store is authoritative.** No in-memory state that a restart or the
  next node cannot rebuild.
- **Tenant scope everywhere.** Every tenant-scoped row carries
  `(project_id, env, tenant_id)`. A `tenant.Scope` is built only in
  `authz` and `tenant`; every repository method takes one.
- **The public `/v1` API is the only door.** The Console, the CLI,
  `weftbase mcp`, SDKs and customers all use it. OTLP ingest is at
  `/otlp/v1/{traces,logs}`.
- **Scale by role from one binary.** `weftbase serve
  --roles=api,ingest,scheduler,supervisor`.

## Two stores

| Store | Holds | Backends |
|---|---|---|
| Control (`store/`) | keys, Console sessions, registry, run ledger, queue, timers, approvals, the `events` outbox | SQLite; Postgres later |
| Trace (`obsdb`) | spans, records, derived runs, search, stats | SQLite, ClickHouse |

A third, weft's `thread` store (session entries, the journal), is in
the design for sessions the node runs itself; v0.1.0 does not open one.
An app that keeps sessions with weft `thread` reports them over OTLP.

## The kernel and the HTTP front

`weftbase.New(opts…)` opens the stores — by default `<data>/control.db`
and `<data>/traces.db` under `DataDir` (`./.weftbase`), each SQLite file
under its OS lock (`internal/dblock`) — wires the services and returns
an `App`. `App.Handler()` serves the node's roles and mounts under a
prefix (`http.StripPrefix` plus `Base`); `App.Run(ctx)` runs the
background work (the outbox hub's pump on `api`; the worker hub's claim
loop and the retention sweeper on `scheduler`); `App.Shutdown` stops
it, drains the executors and closes what `New` opened.

`api/http` is the one router. Every route is a row of one table —
method, pattern, `authz` action, handler — and the middleware runs in
this order: request id (`internal/reqlog`), CORS (only for the
origins `AllowOrigins` names, never with credentials, never for the
Local principal), the URL-credential refusal, authentication (bearer key,
then the `wb_session` cookie, then `Open`'s Local principal), the guards
(Local: loopback Host and same-origin writes; a cookie session: the
`Weftbase-CSRF: 1` header and same-origin writes), then
`authz.Can(principal, action)`. `/v1/*`, `/otlp/*`, `/healthz`,
`/readyz` and `/metrics` never fall through to the Console: an unknown
one is a JSON 404, a wrong method a 405 with `Allow`. Ids in paths are
percent-encoded (a child run id carries `/`) and decoded once.

`api/openapi/v1.yaml` is the contract. A test (`make api-check`) holds
it and the route table to the same operations and actions; code
generation from it lands later.

## Agents, versions and runs (Phase 1)

- `registry`: agents, builds (by digest), AgentVersions (`agv_…`,
  numbered per agent; `(agent, build digest, manifest hash)` unique;
  content hash = sha256 of digest and manifest hash), labels and the
  manifests builds registered. `Promote` moves a label and writes an
  audited `label.moved`. `Resolve`: `label:<l>`, `agv_…` (pin), or none
  (the `prod` label, else the newest version, `latest`).
- `queue`: durable items (kind, route = the AgentVersion, priority,
  `run_at`, concurrency key, payload) claimed under a lease with a
  fencing token, and the timer table. The store is authoritative.
- `sessions`: the run ledger. `Admit` resolves the version, writes the
  run (`agent_version`, `resolved_via`, `tenant_id`), enqueues it and
  emits `run.queued` in one transaction; `Cancel` (409
  `already_finished` once finished), `Steer`, and `Decide` on a parked
  run's approvals, which resumes it — a new run pinned to the parked
  run's version — once the last is decided. A project-level scope reads
  every tenant's runs and acts on them under the run's own tenant
  (`tenant.Scope.Within`): the events are that tenant's, the actor the
  caller; a tenant's scope sees its own runs only.
- `client/wire`: the worker protocol's types (registration, commands,
  acks split into accepted, rejected, busy, run_started, heartbeat,
  finished; cancel and steer with their own ids; the socket handshake;
  newline-delimited frames).
- `client/worker`: the build's side. `Runner` is the command engine
  every placement drives (the seen set, ack before run, per-agent
  capacity, cancel, steer, heartbeats, drain); `Serve(FromEnv(),
  Agent(a))` runs a build under a supervisor: dial `WEFTBASE_SOCKET`,
  register on every connect, the idle watchdog, backoff, drain on a
  drain frame or SIGTERM.
- `worker`: the `Executor` interface every placement implements, the
  `RunSink` the ledger implements for the hub, and the router
  (by AgentVersion, then lease holder). The router knows each worker's
  per-agent `Concurrency`: a version whose workers are all full is not
  claimed, so its queued runs keep their order; a worker that answers
  busy is passed over for that version until one of its runs ends (or
  a second), and the run goes back to its place in the queue. `worker/hub` attaches executors
  (registering their builds creates the versions), claims queue items
  for the versions they serve, dispatches at most once and records the
  acks; a run whose worker vanished is marked lost once its lease
  expires, never sent twice. `worker/executor` holds the embedded
  placement (`App.Agent(...)`: weft agents in-process, the build digest
  is the executable's sha256 or `BuildDigest`) and `executor/local`,
  the default placement (`App.Local(path)`, `serve --build`): the build
  is copied into `<data>/builds/<sha256>` and run as a supervised child
  over a Unix socket, restarted with backoff when it crashes, retired
  (drained, started again on demand) when a newer build of the same
  agents arrives; at boot every stored build is attached lazily, so a
  parked run resumes on its pinned build. A child that exits loses the
  runs it held (`worker_lost`); every ended link writes
  `worker.disconnected`; a held run unheard for max(10 min, three
  heartbeats) is lost.
- Timers (the scheduler role): the queue time-to-live (default 24 h)
  fails a run no worker served; approval expiry (off by default)
  resumes a parked run with its calls denied; maximum pin age (default
  30 days) migrates a parked run to the agent's prod version
  (`resolved_via migrate`) or cancels it `version_retired`.
- `store/storetest`: the control store's conformance suite (the
  transaction contract, the outbox, registry, queue, timers, ledger, and
  a cross-tenant group), run by every backend.

`/v1`: `GET /v1/agents`, `GET /v1/agents/{name}/versions`,
`POST /v1/agents/{name}/labels/{label}`, `POST /v1/runs`,
`GET /v1/runs/{id}/status`, `POST /v1/runs/{id}/cancel`,
`POST /v1/runs/{id}/steer`. A parked run resumes through its approvals
(`POST /v1/approvals/{id}/decision`), never by a resume call.

## What the reads join (v0.1.0)

A run is in two stores: the trace store has what it did, the ledger
what the platform decided about it. The reads join them, so no client
derives a run's state itself (`api/http/v1/runview.go`):

- Every run row carries `display_status` (queued, running, waiting,
  succeeded, failed, cancelled, interrupted) and, for a run the ledger
  admitted, `agent_version` (id, number, labels now), `resolved_via`
  and `placement`. `GET /v1/runs?display_status=` filters on the word;
  a queued run, which no trace names yet, is a row from the ledger
  alone (`ledger_only`).
- A parked run and the run that resumed it are a chain: one logical
  run. `GET /v1/runs` lists one row per chain by default (`chains=0`
  lists every run as it is), run detail
  carries `chain` (the runs, the totals, each wait with its approvals
  and who decided), and `GET /v1/runs/{id}/spans?tree=1&chain=1`
  returns the whole chain's spans with the waits between them.
- Approvals are a resource of the ledger (`sessions/approvals.go`):
  each call a run parks on is one record, written in the park's
  transaction. `GET /v1/approvals` is the inbox,
  `POST /v1/approvals/{id}/decision` decides one, bound to its
  arguments by `args_sha256` (409 `args_changed`), and records who
  decided. The run resumes when its last approval is decided: weft
  denies a pending call a resume carries no decision for, so a resume
  is never sent early. Decisions are made only there (`approvals.decide`,
  bound to the arguments); there is no resume call. Quorum, signed
  forwarding, policies and expiry-by-policy are Phase 2.
- The inventory reads the registered manifests
  (`api/http/v1/agentview.go`): `GET /v1/agents?include=summary`,
  `GET /v1/agents/{name}`, `GET /v1/agents/{name}/versions/{a}/diff/{b}`
  and `GET /v1/tools`.
- `GET /v1/stats?by=total` is one series by outcome with bucket
  timestamps, cost and durations; `GET /v1/sessions/{id}/conversation`
  is a session turn by turn (a park and its resume are one turn).

Deleting a run (`DELETE /v1/runs/{id}`) removes the whole logical run
from both stores: every run of its chain, their children, the ledger
rows and the approval records, with one audited `run.deleted` event. A
run the ledger still has in hand (queued, running, waiting) is refused
with 409 `run_active`. `GET /v1/events` replays the outbox from its
start by default; `after=now` starts at the tail.

An approval that expires is denied by the node and the run resumes;
it usually goes on to an answer and ends succeeded. The status stays
what the run did. What happened to its approvals is beside it:
`approvals_outcome` (pending, expired, denied, approved) on the row,
the chain and the conversation turn, `?approvals=expired` on the list,
and `approvals_expired` / `approvals_denied` in `stats?by=total`.

A folded list (the default) or one filtered on `display_status` is
filled: the handler keeps reading the trace store until it has `limit`
rows. In an unfiltered
list a chain is the row of its first run, at its start; in a filtered
or sorted read it is shown where its last run is read. `total` is the
store's count before the fold, exact where `total_exact` says so.

## Console sign-in

A one-time link (`<origin>/#signin=<token>`, 15 minutes) is exchanged
for the `wb_session` cookie. The session lasts 12 hours from its last
use: a request more than an hour after the last renewal renews it and
sets the cookie again, up to 30 days after the sign-in.
`POST /v1/auth/links` mints a fresh link on a running node for a secret
key or a signed-in operator (audited, `console.link_created`);
`weftbase signin` calls it.

Sessions come in families (`authz/sessions.go`). A link minted with a
key begins one; a link a signed-in session mints, and the session it is
traded for, join that session's family. A family shares its sign-in
time, so chaining links never resets the 30-day cap, and signing out of
any of its sessions ends all of them and its unused links.
`DELETE /v1/auth/sessions` (`weftbase signout --all`, a secret key or
an operator) ends every session and unused link of the project and env.
Sign-in and sign-out are audited (`console.session_created`,
`console.session_ended`); a session appears in audit records as its ref
(`ses_` and a prefix of its id's hash), never its id. Ended sessions and
links are pruned on the next sign-in, link or sign-out.

## Telemetry of a local build

The supervisor hands every child `WEFTBASE_URL` (what `App.Serving`
was told) and `WEFTBASE_TOKEN`, a worker key the node mints for that
build alone (named `build:<digest>`; it may ingest and nothing else).
`client/exporter`'s `Install()` reads that pair, so a build reports to
its node with no wiring. The child never inherits the node's own
`WEFTBASE_URL`, `WEFTBASE_TOKEN` or the other variables the supervisor
sets.

Ingest checks what a build's key writes (`observe/ingest.Guard`, the
kernel's `buildGate`): every run a batch names must be one the ledger
dispatched to that build, or a subagent child of one
(`sessions.Ledger.DispatchedToBuild`); a batch naming any other run is
refused whole with 403, and an admitted batch's rows carry the key's id
(`weftbase.ingest.key` on the resource). Spans and records that name no
run pass. A worker key minted for no build writes no run's records;
secret keys are not checked. A build's key is revoked when the build is
retired (its next start for a pinned run mints another), at Shutdown,
and, for keys earlier starts minted, at New. The worker link's own secret
is `WEFTBASE_WORKER_TOKEN`. The remote placement will dial the link
with the same `WEFTBASE_URL` and `WEFTBASE_TOKEN` (a worker key the
operator mints).

## Known limits (Phase 1)

Two gaps in the local placement are deliberate for now. Each one fails
safe: no run is lost silently and nothing runs twice.

### A restored build runs with the node's environment

`App.Local(path, LocalEnv(...))` and `serve --build` start a build's
child with the environment given at that moment. The build binary is
stored by digest in `<data>/builds/<sha256>` and re-attached when the
node starts again, but the per-build environment is not stored with it.
So after a restart the child runs with the node's own environment only.

Why: an environment is deployment configuration, and secrets in it
must not be written to the control database in clear. Where it belongs
is the deployment object of Phase 3 (a build plus its config and
secret references, versioned with the AgentVersion), not the build
store.

What it means today: a build that needs its own variables (an API key,
a feature flag) must get them from the node's environment, or be
registered again after a restart. Parked runs still resume on their
pinned build; only the variables differ.

Fixed by: the Phase 3 deployment object, which stores per-build
config and secret references and replays them on every start.

### A crashed node does not re-adopt its children

The supervisor talks to each child over a Unix socket it creates fresh
for every executor. If the node process crashes (not a clean stop),
its children keep running, but the socket they dialled is gone. The new
node starts its own children and does not take over the old ones.

An orphaned child keeps redialling, then exits once it has been
unreachable for `MaxDisconnected` (`client/worker`, default 2 minutes)
with no run in hand. The runs it held are marked lost (`worker_lost`)
once their lease expires and are never sent twice, and every ended
link writes `worker.disconnected`.

Why: re-adoption needs a stable socket path per build and a handshake
that proves the old child is the same build at the same version, plus
fencing so two nodes never drive one child. That is real work for a
rare event (a node crash, not a deploy), so Phase 1 keeps the simple,
safe rule.

What it means today: after a node crash, work a child had in flight
ends `worker_lost` instead of continuing, and old children linger for up
to 2 minutes. A clean stop (SIGTERM) drains children first, so deploys
are not affected.

Fixed by: a lost durable run getting a `resume` command instead of
failing, once weft's recovery-stable idempotency keys and
interrupted-turn resume land (weft work items #1, #2, #5). With those,
re-adopting a child is no longer needed for correctness, only to save a
restart.
