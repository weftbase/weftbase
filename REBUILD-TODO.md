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

## Step 5. Kernel and read baseline: todo

## Step 6. Phase 1 domains: todo

## Commits

| Commit | Step | Summary |
|---|---|---|
