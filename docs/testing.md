# Testing weftbase

The platform's tests and gates: what each one proves, the command, when
it runs and what to do when it fails. The framework's own layers (the
Go suite's conventions, the offline gate, `wefttest`, fuzzing, the
record-contract fixtures' rules, API stability) are weft's
`docs/testing.md`; this page is what moved with Studio, the store and
the CLI. The sources are the `Makefile`, `.github/workflows/ci.yml`,
`scripts/` and the test helpers. When this page and one of them
disagree, the source wins; fix this page in the same change.

Every Go command here runs through the development workspace (README
"Development"): weftbase requires weft at a placeholder version until
the two-phase release tags it.

## At a glance

| Layer | Command | Every push (CI) | Release gate | Minutes (approx.) |
|---|---|---|---|---|
| Vet | `make vet` | yes (`test` job) | yes | < 1 |
| Lint | `make lint` | yes (`lint` job) | yes | < 1 (cached) |
| Unit + contract + examples, `-race` | `make test` | yes (`test` job, Go 1.26.x and stable) | yes | ~10 |
| Offline kill switch | `make offline` | yes (`offline` job) | yes | ~1 |
| ClickHouse backend | `WEFT_CLICKHOUSE_DSN=… go test …` | yes (`clickhouse` job) | yes | ~7 (`obsdb/clickhouse`) |
| Studio web app + panel | `make studio-check` | yes (`studio` job) | yes | ~1.5 |
| Panel in a real Vite app | `make devtools-vite-check` | yes (`devtools-vite` job) | yes | not timed (est. 1–2) |
| npm package dry run | `make devtools-npm` | no | when publishing @weftgo/devtools | not timed (est. 1–2) |
| `weftbase mcp` end to end | `make mcp-check` | no (listens on a port) | yes | < 1 |
| Loaded stress | `make stress` | no | yes (and before pushing concurrent code) | not timed |
| The arena | `make check-dev` in `../weft-arena` | no (separate repo) | by hand | not timed |

---

## 1. ClickHouse: `WEFT_CLICKHOUSE_DSN`

**What it proves.** The hosted backend (`obsdb/clickhouse`) meets the
same `obsdbtest` conformance table and the same record-contract
fixtures as SQLite, and `weftbase dev --db clickhouse://` serves.

**What skips without the DSN.** The conformance table and the pipeline
and upgrade tests in `obsdb/clickhouse`, `TestContractClickHouse` in
`obsdb/contract_test.go`, `TestNewServerClickhouse` in
`cmd/weftbase/main_test.go`, and the ClickHouse half of a few `studio` tests
(`status_filter_test.go`, `timeline_forks_test.go`,
`ingest/derived_test.go`). Each test creates its own database and drops
it afterwards.

**Run a local server** (from `obsdb/clickhouse/README.md`):

```sh
docker run -d --name weft-clickhouse -p 127.0.0.1:9000:9000 \
  -e CLICKHOUSE_PASSWORD=weft --ulimit nofile=262144:262144 \
  clickhouse/clickhouse-server:25.8-alpine
export WEFT_CLICKHOUSE_DSN='clickhouse://default:weft@127.0.0.1:9000/default'
cd obsdb/clickhouse && go test -race -count=1 ./...
go test -count=1 ./obsdb/... ./studio/... ./cmd/weftbase/
```

Use any free host port; the agent briefs in this repo use `19000`.

**When.** On every push, in the `clickhouse` job, which uses a service
container. The job **fails if any gated test skips** (it greps `go test
-v` output for `--- SKIP`), so a DSN that does not reach the server
cannot turn the job green. It also requires
`--- PASS: TestNewServerClickhouse`. Any change that touches a read in
`obsdb` must be checked on both backends.
## 2. Studio and the devtools panel

Contributors run these when `studio/web` changes. They need Bun, which
CI pins at 1.3.14. Users get the committed, embedded `studio/dist`.

```sh
make studio-check        # rebuild, prove dist is fresh, budgets, typecheck, vitest
make devtools-vite-check # the packed @weftgo/devtools in a real Vite app
make devtools-npm        # the npm package's suite + `npm pack --dry-run` (never publishes)
```

**`make studio-check`** (the `studio` job on every push) checks, in
order:

1. The build (`bun install --frozen-lockfile && bun run build`) leaves
   `studio/dist` byte-identical to the commit, with no stray files.
   Otherwise you see "studio/dist is stale: run 'make studio-build' and
   commit".
2. The gzipped dist fits **800 KiB** (ADR 0018 §7).
3. `studio/web/npm/package.json` carries the weft version.
4. `bun run typecheck` and `bun run test`, which is vitest over
   `src/**/*.test.ts(x)`. That includes:
   - `src/panel/budget.test.ts`: the **panel byte ledger**.
     `studio/web/panel-budget.json` is append-only, with one row per
     landed item (`{"label","item","gzip"}`). The last row must equal
     the committed `studio/dist/panel/panel.js`, measured with the
     version stamp normalised and gzipped with pako level 6. The cap
     (`cap`, 106,496 B = 104 KiB) is the single source the build, the
     printed table and `budget.test.ts` all read. A rebuild that changes
     `panel.js` without appending its row fails here. The build prints
     the per-item table, with headroom.
   - `src/panel/a11y.test.ts`: the **axe budget**. It runs axe-core's
     default rules over the panel's shadow root in every mode (float,
     docks, the bottom sheet, the pill, Raw, Timeline, the experiment
     drawer and option lab, the shortcuts overlay) in both themes. Any
     violation fails the test, and so does any incomplete rule other
     than the two that need layout, which jsdom does not have
     (`color-contrast`, held to WCAG AA by `theme.test.ts`, and
     `label-content-name-mismatch`).
   - `src/panel/parity.test.ts`: the panel and the Studio run page draw
     the same words for the same data: holes and badges, cost, the run
     tree, the step diff.
5. The npm package suite (`WEFT_DEVTOOLS_PKG=1 bunx vitest run src/npm`)
   and `scripts/npm-consumer.ts`, which type-checks a consumer of every
   subpath under node16 and bundler resolution.

**`make devtools-vite-check`** (the `devtools-vite` job, after
`studio`) installs the packed tarball into `examples/devtools-vite`
(`npm ci` from its committed lockfile, then the tarball offline and
unsaved), runs `vite build`, and smoke-tests the bundle in jsdom: the
element mounts, and the installed `panel.js` is
`studio/dist/panel/panel.js` byte for byte. It needs an assembled
`studio/web/npm` (`make studio-build` first) and npm registry access, or
a warm npm cache, for `npm ci`.

**Measured:** `make studio-check` took 98 s with `node_modules`
already installed. It printed `studio dist: 682 KiB gzipped (budget
700)`, then vitest reported 175 files and 2,909 tests passing, and the
npm suite 3 files and 42 tests.
`devtools-vite-check` and `devtools-npm` were not timed here. Both are
about a minute or two after a build.

`scripts/panel-gate.ts` (`bun run scripts/panel-gate.ts --endpoint …`)
drives the built panel inside jsdom against a real Studio, for setup A
and for setups B/C. It is a manual browser-gate harness, not part of
`studio-check`.
## 3. `make mcp-check`

**What it proves.** `weftbase mcp` answers "why did run X fail" over stdio.
This is the phase 6 gate's third clause.

```sh
make mcp-check   # WEFT_MODEL_REQUESTS=deny go test -tags livecheck -run '^TestMCPLiveCheck$' -count=1 -v ./cmd/weftbase/
```

The test builds `weft` and starts a real `weftbase dev` on a free
loopback port over a scratch SQLite file (`WEFTBASE_DISCOVERY=off`). It
seeds the studio with the record-contract fixtures through `/v1/traces`
and `/v1/logs`, then runs four investigation shapes on wefttest
scripted models through the otel pipeline (`mcp_check_seed_test.go`:
the arena's `sub.failed` and `agentic.chaos`, a denied thread turn, a
nine-turn session), drives `weftbase mcp` with the MCP SDK's client, and
prints one summary line per tool, ending with `MCP CHECK PASS`. Four of its
assertions: `sub.failed` answers the child's
error, `agentic.chaos` its four failure shapes, the denied turn
"denied by alice", and the nine-turn `session` answer is under 4 KB. It makes no
model calls. It sits behind the `livecheck` tag and stays out of
`make test` because it listens on a port. It is not in CI. Run it before
a release and after touching `cmd/weftbase/mcp*.go` or a route it reads.
**Measured:** 2 s (the test itself took 1.3 s), with `weft` already
built in the cache.
## 4. The arena (`../weft-arena`)

weft's own tests prove each promise in isolation. The arena is a
separate repository and harness that checks the framework the way a
user meets it. It has about 150 catalogued scenarios over every module,
mapped to around 170 tracked features. `weft-arena coverage` fails on a
feature that no scenario covers. The scenarios run offline (scripted
`wefttest` models, deterministic) or live through provider profiles.
They can be driven from a CLI, a browser UI, or MCP, and they can seed a
running Studio with realistic runs. It pins the published weft tags
(`make check`, `GOWORK=off`). `make check-dev` runs the same gate
against a sibling `../weft` checkout through `go.work`. The phase gates
use its offline seed as their data.

```sh
cd ../weft-arena
make check-dev                                   # vet, go test, every offline scenario, coverage vs ../weft
go run ./cmd/weft-arena run --suite pool18 --offline
go run ./cmd/weft-arena run --offline --studio --no-save --jobs 8   # seed a running Studio
```

For profiles, the live suite and the UI, see the arena's own README.
Live runs call real providers; keep them out of weft's gates.

---
## Before you push

- [ ] `make fmt vet lint test` all exit 0, and `make offline`.
- [ ] You touched concurrent code: `make stress STRESS_PKGS=<your
      packages> STRESS_COUNT=50`.
- [ ] You touched an `obsdb` read: the ClickHouse run (§1) as well as
      SQLite.
- [ ] You touched `studio/web`: `make studio-check`, then
      `make devtools-vite-check`. Commit `studio/dist` and the ledger
      row.

## Before a release

- [ ] `make fmt vet lint test` and `make offline`.
- [ ] `make tidy && git status --short`: no diff.
- [ ] `make studio-check` and `make devtools-vite-check`.
- [ ] `make mcp-check`: ends with `MCP CHECK PASS`.
- [ ] `make stress`, at the default packages and count.
- [ ] The ClickHouse suite with `WEFT_CLICKHOUSE_DSN` (§1): no
      `--- SKIP`.
