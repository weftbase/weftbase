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

## Step 3. Moves: todo

## Step 4. Layering check: todo

## Step 5. Kernel and read baseline: todo

## Step 6. Phase 1 domains: todo

## Commits

| Commit | Step | Summary |
|---|---|---|
