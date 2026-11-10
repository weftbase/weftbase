# Rebuild progress

Resumable state of the rebuild. It started on branch `rebuild` (from
`main` `feef5feb`) and is now merged: `main` is the rebuild. The old
code is on the `legacy` branch and the `archive/studio-v0` tag; it was
never released, and the first weftbase release will be v0.1.0 from
`main`. The plan is in the design docs (private);
`docs/architecture.md` is the in-repo summary. Step numbers are the
plan's migration steps.

Rule for every commit: `go build ./... && go vet ./... && go test -race ./...`
green in both modules (`make build vet test`), plus the step's gate.
Nothing is pushed, tagged or deployed without the user's explicit OK.

Status words: **done**, **in progress**, **todo**, **skipped**.

## Step 0. Archive: done (by the user)

`legacy` and `archive/studio-v0` exist and are pushed.

## Step 1. Pin the arena: skipped

Needs a push; weft-arena is left alone. Arena follows `../weftbase`
through the parent `go.work` and will not build against `rebuild` until
it is re-pinned (§9.1).

## Step 2. Skeleton and deletes: done

| Item | Status | Notes |
|---|---|---|
| delete `scope`, `runtime` (but `id.go`), `studio/runtime` (but `http.go`), `internal/linkreg` | done | `runtime/id.go` and `studio/runtime/http.go` are MOVE rows, moved in step 3 |
| delete `studio/web`, `studio/dist`, `examples/devtools-vite`, `examples/studio-local`, `examples/approvals/serve*.go` | done | |
| delete the devtools files of `studio` (DELETE rows) | done | helpers of §2.3 moved into `studio/steps.go` first |
| `console/` skeleton (`console.go` with `//go:embed all:dist`, placeholder `dist/index.html`) | done | from `studio/assets.go` (git mv) |
| remove panel tokens, live grants, tag scoping and `readScope` from studio | done | reads are unscoped until tenancy lands (step 7) |
| obsdb deletes and trims (commands, experiments, args, bpset, playground fields, ScopeMeta, "Studio" words) | done | |
| squash obsdb migrations into one `0001_init.sql` per backend, `(project_id, env, tenant_id)` leading every key | done | defaults `prj_default`, `test`, `ten_project`; old databases refused (`ErrPredatesRebuild`); interim lookup indexes on the old keys until queries carry the Scope |
| regenerate the record-contract fixtures | done | `-update-contract`, scripted pipelines; weft `docs/record-contract.md` still lists retired rows (`retiredPackages` excuses them; edit in weft) |
| Makefile, CI, workspace action | done | studio/devtools targets and jobs removed |
| gate: `go test -race ./obsdb/... ./internal/...`; sqlite conformance; ClickHouse suite; `grep -ri studio obsdb` empty | done | full `go test -race ./...` green; ClickHouse 25.8 gated suite green, 0 skips (local Docker) |

## Step 3. Moves: done

| Item | Status | Notes |
|---|---|---|
| `client` module (`client/go.mod`) and committed root `go.work` (`. ./client ../weft ../weft/core`) | done | go.work replaces the weft and client placeholders |
| `runtime/id.go` → `client/ulid` | done | `ulid.New()` |
| `studio/runtime/http.go` → `internal/httpx` | done | shows as delete+add (the duplicate ULID was most of it) |
| `internal/discovery` → `client/discovery` | done | file `studio.json` → `dev.json` |
| `studio/internal/reqlog` → `internal/reqlog` | done | ingest class is `/otlp/` |
| `studio/ingest` → `observe/ingest` (`otlp.go`), `ingesthealth.go` → `observe/ingest/health.go` | done | mounted at `/otlp/v1/{traces,logs}`; writes before publishing |
| pricing → `observe/cost` (+ `pricingdata`, `internal/pricinggen`, `pricing.json`, testdata) | done | `table.go` is new (the table was a third of pricing.go); the coster stays with the read server |
| `exporter` split → `client/exporter` (`Remote(url, key)`, `Install`, `WithFallback`, `Named`) + `observe/local` | done | `remote.go`/`install.go` pair weakly (21%/45%) |
| `cmd/weftbase/mcp*.go` → `api/mcp` (`mcp.Serve`) over `client/rest` (hand-written seed from `cmd/weftbase/api.go`) | done | |
| studio read server → `api/http/v1` (package `v1`) | done | **deviation**: all of studio moved into one package; routes still `/api/*`. Peeling logic into `observe/query`, `sessions`, `approvals/readmodel`, `observe/export`, `registry/manifests` is step 5b |
| no `studio` package; vocabulary clean in Go | done | `studio_version` → `server_version`; CLI prefix `weftbase:` |
| gate: build/vet/test -race in both modules; client deps exclude the server | done | |

## Step 4. Layering check: done

| Item | Status | Notes |
|---|---|---|
| `scripts/layering` + `layers.txt` + seeded-violation tests | done | up-layer, DAG, forbidden layer (L4→L3), stdlib-only `console`, `tenant.NewScope` outside `authz`/`tenant`, unclassified package, retired word in text and path |
| `make layering`, `make client-deps`, `make vocab`, CI job `layering` | done | |
| client-module dependency check (no server package; module allow-list) | done | |
| Go vocabulary audit | done | scans .go/.md/.sql/.json/.yml/.txt/.html/.mod and paths; skips `CHANGELOG.md`, `docs/adr`, `REBUILD-TODO.md`, `console/app`, `console/dist` |
| gate: fails on the seeded violation, passes on the tree | done | also checked by hand with an `observe → api/mcp` import |
| README.md, AGENTS.md rewritten for the new layout | done | |

DAG deviations from the plan's §1.4 (in `layers.txt`): `sessions → observe` (timeline and export need cost and holes); `webhooks → queue` (§4.3 says webhooks depends on queue, the DAG omitted it).

## Step 5. Kernel and read baseline: done (codegen and logic extraction remain, see 5b)

| Item | Status | Notes |
|---|---|---|
| kernel `weftbase.go`/`options.go`/`roles.go`: `New`, `Handler`, `Run`, `Shutdown`, `Ready`, roles | done | data dir `<data>/control.db` + `<data>/traces.db` under `internal/dblock`; `postgres://` refused "not supported yet" |
| `store` + `store/sqlite` (migrations, refuse pre-rebuild file) | done | repositories write portable `?` SQL over `store.Tx` in the domain package; backends own connections, dialect, migrations |
| `storetest` (repository conformance suite) | done | step 6, `4ad3ade1` |
| `events`: outbox `Emit` in the caller's tx, relay with cursors, hub (replay then live, overflow) | done | hub pump runs under the `api` role (subscribers live there); sweeper under `scheduler` |
| `tenant` (Scope, `ten_project`) + `tenant/keys` (`wb_sk_`/`rk`/`pk`/`wk`, hashed, audited mint/revoke) | done | bootstrap `wb_sk_test_` key in `<data>/bootstrap.key` |
| `authz`: keys, Console sessions (one-time link → `wb_session` cookie), `Open`, `Can` | done | restricted keys have no scopes yet |
| `api/openapi/v1.yaml` for the read surface | done | 39 operations, `x-weftbase-action`, `x-weftbase-events` |
| code generation (oapi-codegen strict server, generated `client/rest`, Console types) | todo | `make api-check` runs the spec ↔ route-table test meanwhile; `client/rest` is hand-written |
| `api/http` (package `httpapi`): route table, request ids, guards, CORS, CSRF, `/healthz`, `/readyz`, `/metrics`, `/v1/meta`, auth sessions, Console last | done | no `/api/*` route remains |
| OTLP at `/otlp/v1/*`, tenant from the credential | done | no open-loopback ingest without `Open` |
| `/v1` read surface + streams (`/v1/runs/{id}/stream`, `/v1/sessions/{id}/stream`, `/v1/runs/stream`, `/v1/events`) | done | extras kept beyond §4.4: `/v1/runs/stream`, `/v1/public/{id}`, `/v1/sessions/{id}/public_id`, `/v1/manifest` (until `/v1/agents`) |
| route-completeness test (every route has an action; spec ↔ routes) | done | `api/http/routes_test.go` |
| `/v1` goldens re-cut from the legacy `/api` goldens | done | `api/http/v1/testdata/v1`; only the two meta goldens changed (meta redesigned) |
| CLI: `serve --roles`, `dev` (bootstrap key, discovery, sign-in link `/#signin=`), `runs`, `open`, `export`, `doctor`, `mcp` on `client/rest` | done | `token.go` removed |
| `make mcp-check` on `/v1` | done | MCP CHECK PASS |
| gate: `weftbase dev` + an app using `client/exporter` shows runs over `/v1` | partial | covered by the kernel test (OTLP POST → `GET /v1/runs`) and mcp-check; not run as a live app |

### Step 5b. Peel the read logic out of `api/http/v1`: todo

The step 3 deviation: the old read server moved whole into `api/http/v1`.
Move its logic into the plan's packages, leaving thin handlers:
`observe/query` (runs, steps, requests, traces, search, stats, logs,
diff, holes), `observe/export`, `sessions` (timeline, export, delete),
`approvals/readmodel.go` (decisions), `registry/manifests.go`,
`observe/retention.go` (the sweeper now in the kernel). Push
`tenant.Scope` into the obsdb queries (step 7).

## Step 6. Phase 1 domains: done

| Item | Status | Notes |
|---|---|---|
| `client/wire` (registration, command start/resume with AgentVersion pin, ack states accepted/rejected/busy/run_started/heartbeat/finished, cancel with an id, goldens) | done | |
| `registry` (agents, builds, AgentVersions `agv_`, labels, audited `label.moved` promote, Resolve, manifests by hash) | done | rows are project-level (`ten_project`); no selector → `prod`, else newest (`resolved_via latest`) |
| `queue` (items, leases + fencing, Extend/Done/Fail/Release, timers table) | done | `queue` may call `tenant.NewScope` to rebuild a stored row's scope (declared in `layers.txt`) |
| `sessions` run ledger (`agent_version`, `resolved_via`, tenant on every row; Admit, Resume, Cancel) | done | resume is a new run pinned to the parked run's version; cancel wins over a park |
| steer | done | `POST /v1/runs/{id}/steer` → ledger → hub → `Executor.Steer` → the build's Runner (weft `Steering`), embedded and local; `run.steered`; 409 not running, 400 `invalid_steer` |
| `worker` (Executor incl. `Cancel`, RunSink, Router), `worker/hub`, `worker/executor/embedded.go` | done | the Executor interface lives in `worker` (avoids a hub ↔ executor cycle) |
| `client/worker` (Serve, FromEnv, dial loop, ack-before-run, seen set, heartbeat, idle watchdog, backoff) | done | `Runner` is the engine both placements drive (embedded is now a thin transport over it); `--manifest`; orphaned builds exit after 2 min |
| `worker/executor/local` (child process over a Unix socket) | done | crash restart with backoff, `Retire` on a newer build (lazy restart), `Known` registrations; kernel `App.Local`, build store `<data>/builds/<sha256>`, boot reattach of stored builds; CLI `serve`/`dev --build` |
| kernel wiring (`App.Agent`, `App.Executor`, scheduler runs the hub; Shutdown drains) and `/v1` routes: agents, versions, labels, `POST /v1/runs`, status, cancel, resume | done | migration `0002_phase1.sql` |
| gate: every run carries an AgentVersion; embedded runs end to end through the Executor; a parked run survives a new build and resumes on its pin; restart reclaims after lease expiry | done | `phase1_test.go` |
| gate: the legacy SSE/link tests ported | done | `runtime/link_test.go` → `client/worker/worker_test.go` (register + ack before run, at most once, reconnect re-registers, idle watchdog, ping/cancel, stop drains, seen bounded, panic, undelivered accepted ack, busy, heartbeats, backoff); hardening → `worker/hub/link_test.go` (stalled worker lost, duplicate/unknown ack, late ack, disconnect, steer to the holder); restart → `TestRestartClaimsAgain`, `TestParkedLocalRunSurvivesANewBuild`. Not ported (no counterpart): breakpoints, HEAD on the SSE feed, playground budgets/scripted replay |
| gate: local placement with a real child | done | `phase1_local_test.go`: fixture `worker/executor/local/testdata/refunds` (wefttest scripted), builds A and B; parked on A survives B + restart, resumes on A's child; api-only node's work reclaimed; embedded + local through one hub; cancel/steer/crash on the child |
| timers fired (expiry, pin age), queue TTL → `worker_lost`, `worker.disconnected` events | done | `sessions/timers.go`, queue `RunTimers` under scheduler; migration `0003_timers.sql`; kernel options `QueueTTL`, `ApprovalExpiry`, `MaxPinAge`; hub silence bound (10 min / 3 beats) |
| `storetest` repository conformance suite | done | `store/storetest`, run by `store/sqlite` (L3; may build scopes) |

### weft integration points (TODO markers in code)

| Marker | Where | Now | Switch to |
|---|---|---|---|
| `TODO(weft#13)` | `client/worker/runner.go` (`MetaAgentVersion`) | every run gets `weftbase.agent_version` run metadata (spans, records) | weft's `agent_version` option on session entries (prompt entry, `TurnEntry`, `ApprovalRequestEntry`, `PoolReceiptEntry`, pool child header) once on weft `weftbase-gaps` |
| `TODO(weft#14)` | same file (`MetaBuildDigest`) | `weftbase.build_digest` run metadata | `weft.build.digest` on the OTel resource (weft `f057032b`, `version.Current()`); drop the metadata key |
| weft #3, #1, #2/#5 | not wired | the ledger resumes from the opaque checkpoint a parked run's ack carried; a lost run is never re-sent | scheduler seam (`thread.WithScheduler`) when sessions move onto weft `thread`; recovery-stable idempotency keys and interrupted-turn resume let a lost durable run get a `resume` command instead of `worker_lost` |

### Known limits (Phase 1)

- Sessions over weft `thread` are not built: a "turn" is a run; every run carries its AgentVersion in the ledger.
- A stored build restored at boot runs with the node's environment (per-build env waits for the deployment object, Phase 3). Explained in `docs/architecture.md`, Known limits.
- Sockets are fresh per executor: an orphaned child of a crashed node is not re-adopted; it exits after 2 min unreachable. Explained in `docs/architecture.md`, Known limits; the fix is resume-on-lost after weft #1, #2, #5.
- The record-contract doc test now scans `api/http/v1` (weft `cc7b4032` dropped the retired-package excuse for `weft.replay.view`, whose reader lives there).

### Before step 7

- Step 5b (peel the read logic out of `api/http/v1`) and code generation from `v1.yaml` are still open.
- Drain verbs (`wait`, `migrate`, `cancel`) as API, queue depth per version (`GET /v1/agents/{name}/versions/{agv}/queue`), session `pin` and sessions over weft `thread`.
- The weft integration points above, when weft's `weftbase-gaps` lands.
- weft-arena is still unpinned (step 1).

## v0.1.0 server surface: done (ahead of Phase 2, first slices)

What the Console and a first user need from the server, built on the
Phase 1 ledger (`docs/architecture.md`, "What the reads join").
Commits `d8ff2e34`, `68cef3ee`, `76b4a861` and the one after them.

| Item | Status | Notes |
|---|---|---|
| weft v0.24.2 | done | both `go.mod` files require the published tags; `go.work` no longer names `../weft` (CI and a fresh clone use v0.24.2 from the module proxy) |
| the demo | done | `examples/demo` (`make demo`): five agents, both placements, scripted models only; a thread-backed session, retries, a fallback model, typed output, an MCP tool, a run that parks twice, an expiring approval, a queued run, manifest drift, stock OTLP traces; `TestDemo` asserts each over `/v1` |
| local builds report to their node | done | the supervisor hands the child `WEFTBASE_URL` + `WEFTBASE_TOKEN` (a worker key it mints); the link secret is `WEFTBASE_WORKER_TOKEN`; `App.Serving(url)` |
| approvals as a resource | done (first slice of P§10) | `approvals` table (migration `0004`), one record per parked call; list, get, decide (bound by `args_sha256`, actor recorded, `resolve` too), resume when all are decided, expiry per agent (`AgentApprovalExpiry`); events `approval.requested`, `approval.decided` with `chain_run_id` |
| run rows say their state | done | `display_status`, `agent_version`, `resolved_via`, `placement`, `manifest_drift`, `approvals_outcome`, `usage_partial` (a running run's usage so far); queued runs listed from the ledger |
| chains (a park and its resumes are one run) | done | one row per chain by default on the list and the search (`chains=0`: every run; pages are filled; `total_exact`), `chain` on run detail, `spans?tree=1&chain=1`, `logs?chain=1` |
| delete | done | the whole logical run or session from both stores, audited; 409 `run_active` while in hand |
| agent inventory | done | `/v1/agents?include=summary`, `/v1/agents/{name}`, versions diff, `/v1/tools`, per-version manifest drift |
| stats | done | `by=total` (outcomes, approvals expired and denied, cost, durations, bucket timestamps), `buckets=`, `outcomes` on `by=agent`; agrees with the list for a window |
| sessions | done | the conversation endpoint (turns with an index, waits, marks, skipped turn numbers explained), `session_id` on `POST /v1/runs` |
| sign-in | done | `POST /v1/auth/links`, `weftbase signin`; the session cookie slides (12 h from last use, 30 days at most) |
| streams | done | `/v1/events?after=now`; every stream opens at once; `weftbase_http_requests_in_flight` in `/metrics` |

## v0.1.0: released 2026-11-10

Done, in order: CI green on the pull request (#1; it found and fixed a
local-executor race, the welcome written after the link was published,
and two test races); `make stress` by hand (exit 0); `console/dist`
fresh (CI's console job checks it); the CHANGELOG dated; `client/v0.1.0`
tagged; the root requires it, with no `replace` in `go.work` and plain
`make tidy` / `make gowork-off`; `v0.1.0` tagged; `go install
github.com/weftbase/weftbase/cmd/weftbase@v0.1.0` checked from a clean
directory.

weft: no v0.24.3 is needed. The `thread/pool` CI failures were three
tests that assumed a scheduling order; the fix is tests and comments
only, on weft `main` (7059609a) with CI green. weftbase stays on
v0.24.2, and the workspace no longer builds against a weft checkout:
CI and a fresh clone get v0.24.2 from the module proxy.

Known and accepted for v0.1.0 (each is in `CHANGELOG.md`, "Not in
v0.1.0", or below):

- Step 5b (peel the read logic out of `api/http/v1`) and code
  generation from `api/openapi/v1.yaml` are not done. The handlers and
  `client/rest` are written by hand; `make api-check` keeps the spec and
  the route table to the same operations, but the response schemas in
  the spec are loose for most reads.
- `store/storetest` has no conformance group for approvals or deletes
  (covered by `sessions/approvals_test.go` and the HTTP tests in
  `phase1_test.go`). It matters when a second control-store backend
  arrives.
- The fold of chains and `display_status` runs in the handler after the
  trace store pages: a filled page reads up to 40 store pages, and
  `total` is exact only where `total_exact` says so.
- `GET /v1/stats?by=total` and the `outcomes` of `by=agent` read run
  rows (at most 20,000 per answer, `capped`).
- `weftbase signin` has no CLI test (the endpoint has; it was run by
  hand).
- The local placement's two limits (a restored build runs with the
  node's environment; a crashed node does not re-adopt its children):
  `docs/architecture.md`, Known limits.
- weft-arena is not pinned to this repository (step 1).

## Commits

| Commit | Step | Summary |
|---|---|---|
| e96e5d4c | 2 | delete the devtools, trim obsdb, squash its migrations |
| fb1fbcd8 | 3 | moves, the client module and go.work |
| 0a54548c | 4 | the layering check |
| 744f3519 | 5 | kernel, control store, outbox, keys, authz and /v1 |
| a7ec3c35 | 6 (part) | wire, registry, queue, run ledger, hub, embedded executor |
| 842840ad | 6 | client/worker: the build side of the worker link; embedded on the shared Runner |
| d55e6acb | 6 | steer, the ledger's timers, lost workers, worker.disconnected |
| 7de34779 | 6 | the local placement, App.Local, --build, the Phase 1 gate with a real child |
| 4ad3ade1 | 6 | store/storetest conformance suite |
