# Rebuild progress

Resumable state of the rebuild on branch `rebuild` (from `main`
`feef5feb`; the old code is on `legacy` and tag `archive/studio-v0`).
The plan is WEFTBASE-ARCHITECTURE.md (weft-server-research, beside this
repository); `docs/architecture.md` is the in-repo summary. Step numbers
are the plan's §9.

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
| `storetest` (repository conformance suite) | todo | lands with the first repositories of step 6 |
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

## Step 6. Phase 1 domains: in progress

| Item | Status | Notes |
|---|---|---|
| `client/wire` (registration, command start/resume with AgentVersion pin, ack states accepted/rejected/busy/run_started/heartbeat/finished, cancel with an id, goldens) | done | |
| `registry` (agents, builds, AgentVersions `agv_`, labels, audited `label.moved` promote, Resolve, manifests by hash) | done | rows are project-level (`ten_project`); no selector → `prod`, else newest (`resolved_via latest`) |
| `queue` (items, leases + fencing, Extend/Done/Fail/Release, timers table) | done | `queue` may call `tenant.NewScope` to rebuild a stored row's scope (declared in `layers.txt`) |
| `sessions` run ledger (`agent_version`, `resolved_via`, tenant on every row; Admit, Resume, Cancel) | done | resume is a new run pinned to the parked run's version; cancel wins over a park |
| steer | todo | `Steer` returns `ErrNotSupported` |
| `worker` (Executor incl. `Cancel`, RunSink, Router), `worker/hub`, `worker/executor/embedded.go` | done | the Executor interface lives in `worker` (avoids a hub ↔ executor cycle) |
| `client/worker` (Serve, FromEnv, dial loop, ack-before-run, seen set, heartbeat, idle watchdog, backoff) | todo | |
| `worker/executor/local` (child process over a Unix socket) | todo | |
| kernel wiring (`App.Agent`, `App.Executor`, scheduler runs the hub; Shutdown drains) and `/v1` routes: agents, versions, labels, `POST /v1/runs`, status, cancel, resume | done | migration `0002_phase1.sql` |
| gate: every run carries an AgentVersion; embedded runs end to end through the Executor; a parked run survives a new build and resumes on its pin; restart reclaims after lease expiry | done | `phase1_test.go` |
| gate: the legacy SSE/link tests ported | todo | wait for `client/worker` |
| timers fired (expiry, pin age), queue TTL → `worker_lost`, `worker.disconnected` events | todo | |
| `storetest` repository conformance suite | todo | |

## Commits

| Commit | Step | Summary |
|---|---|---|
