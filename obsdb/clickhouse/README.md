# obsdb/clickhouse

The hosted backend of `obsdb` (ADR 0024, S3.6): the observability
schema as ClickHouse tables that a stock OTel Collector can also feed.

```go
import (
    "github.com/weftbase/weftbase/obsdb"
    "github.com/weftbase/weftbase/obsdb/clickhouse"
)

db, err := clickhouse.Open("clickhouse://default:pass@localhost:9000/weft",
    clickhouse.TTL(30*24*time.Hour, 90*24*time.Hour)) // content, spans+runs
// db implements obsdb.DB: Write, Runs, Run, Events, Transcript,
// RunSpans, Trace, Sessions, Session, ResolvePublicID, Close, and the
// optional Searcher, Aggregator, TreeReader, SessionEventReader and
// ManifestStore.
```

**ClickHouse 25.6 or later.** The schema's TTLs are on `DateTime64(9)`
columns (migration 0001), which a server accepts from 25.6: older ones
— 23.3, 23.8, 24.8, 25.3, 25.4 and 25.5 tried — refuse 0001 with "TTL
expression result column should have DateTime or Date type, but has
DateTime64(9)". CI pins 25.8 (the recipe below).

`Open` creates and versions the schema in `obsdb_migrations` (the
SQLite backend's numbering rule; one `0001_init.sql` holds the whole
schema, and a database written before that squash is refused with
`ErrPredatesRebuild`), writes go through async inserts
(`async_insert=1, wait_for_async_insert=1`), and the weft tables are
maintained by materialized views:

- `otel_traces`, `otel_logs` — column-compatible with the OTel Collector
  ClickHouse exporter **pinned at v0.162.0** (the version named in
  `migrations/0001_init.sql`): we keep every column its INSERT names,
  and add the weft identity as materialized columns with bloom skip
  indexes on `SessionId`, `PublicId`, `Agent` and `TraceId`. A stock
  collector pinned to that version, configured with
  `create_schema: false`, writes into the same database.
- Tenancy: every table carries `ProjectId`, `Env` and `TenantId`
  (defaults `prj_default`, `test`, `ten_project` until tenancy fills
  them), and those three lead every `ORDER BY`, so an id colliding
  across tenants never merges into another tenant's row.
- `weft_records` — `ReplacingMergeTree(InsertTime) ORDER BY (ProjectId,
  Env, TenantId, RunId, Kind, Pos)` (the transport idempotency key),
  filled from `otel_logs` where `weft.record` is a durable kind —
  never `delta`, never `heartbeat`. Exact reads use `FINAL`.
- `weft_runs` — `AggregatingMergeTree ORDER BY (ProjectId, Env,
  TenantId, RunId)` filled from both
  `otel_logs` (heartbeats included, for last-seen) and `otel_traces`;
  `Sessions` groups it by `SessionId`.

Options: `TTL(content, meta)` overrides the retention windows (content
30 days, spans and runs 90 by default; values ≤ 0 keep a class's
default), `KeepDeltas()` stores delta rows in `weft_deltas` for
debugging (counted, never stored by default). A TTL drops an expired
row only when ClickHouse merges its part, so every read that lists or
opens runs cuts at `weft_runs`' own window itself (the table's
`TTL LastSeen + toIntervalSecond(n)` clause, read from `system.tables`
at most once a minute): `Runs` and `Search` (every filter), `Sessions`
(rows, `Total`, turn facts, the tag and `Failed` filters, the
summarizer usage), `Session` and its runs, `Run` and its children,
`ChildRuns`, the run tree (`RunTreeSpans`: its rows and the spans of the
runs it keeps — a run whose parent is cut is out with it, whether its
row names that parent or only core's id rule does; a cut run, a
derived one too, not found as in `Run`) and `ResolvePublicID`. A
resume past it is no resumer, in the same call's instant: its parked
run reads parked — no `resumed_by`, kept by `?status=parked` and
`SessionQuery.Awaiting`, its session's pending calls, resumes and
outcomes agreeing — as SQLite, which cuts nothing, reads a parked run
whose resume `DeleteRun` removed. A run
past it is neither counted nor paged nor found, merged away or not,
`Total` always matches the rows, and no read keeps a turn another has
cut; a run's own records and spans, read by id, stay until the merge
drops them. The boundary is taken once per call — one instant for
every statement of it — so a run crossing it between `Sessions`' count,
page, turn facts and summarizer read is in all of them or in none. It
is on the server's clock, the one TTL merges use: the window's read
takes the server's `now64(9)` beside it, and the boundary is the host's
now plus that offset minus the window, so a host clock that drifts from
the server's moves no run across it. A failed `system.tables` read
keeps the last window known for a minute (logged at Debug), a read the
caller cancelled holds nothing; with none known yet, or a window past
about 100 years (DateTime's range), the reads cut nothing, as before
the cut existed.

A run's metadata (`RunRow.Meta`, and everything that filters or scopes
by it: the tag filters, session titles) is
the union of its rows' pairs, as on SQLite: `MetaKV`,
a `maxMap` over each row's pairs that an insert fills from the row's
own `Meta`, so a background merge keeps every key any row carried. On
a key two rows carry with different values the greater value wins
(SQLite: the later write); the core stamps one map on every row of a
run, so only an app's own attributes can disagree (never one row's
map, `max(Meta)`: a row with fewer pairs could drop the caller's tag).
The column reads `toValidUTF8(Meta)`: a byte that is not
UTF-8 reads as U+FFFD, as on SQLite, and the rest of the map is kept.

The tag filters (`SessionQuery.Meta`, `RunQuery.Meta`) narrow their
row scan on `idx_meta_kv`, an ngram bloom filter over a row's `MetaKV` as
JSON: each pair whose key and value are printable ASCII without `"`,
`\` or `/` also asks that JSON `LIKE '%"<k>":"<v>"%'` (the pair's JSON
text — a superset; the exact match on the union decides), so the
granules holding none of it are skipped; any other pair reads every
granule, as before.

Deletion (`DeleteRun`, `DeleteSession`, `Sweep` — the server's delete
verbs and its retention) is a lightweight `DELETE FROM … WHERE` on
`otel_traces`, `otel_logs`, `weft_records`, `weft_deltas` and
`weft_runs` (lightweight deletes, GA since ClickHouse 23.3, below the
schema's own 25.6 floor): rows are masked at once and
leave the parts at the next merge; `Vacuum` (`weftbase vacuum`) forces that
with `OPTIMIZE TABLE … FINAL` on the same tables — heavy on a large
database. Each chunk of a deletion (400 runs, 200 span keys) issues up
to seven synchronous (`mutations_sync = 2`) lightweight DELETE
mutations, so a large sweep is many mutations. The runs' own spans go
after the non-weft spans below them, and `weft_runs` last: a deletion
cut short leaves the run listed and a retry finishes it. The TTLs stay
as they are: the server's retention is additive.

The read-time status `parked` (`RunQuery.Status = obsdb.StatusParked`,
`/api/runs?status=parked`) is an anti-join: a run with pending calls
whose id no row's `ResumedFrom` names. That `NOT IN (SELECT
ResumedFrom … WHERE ResumedFrom <> '')` set is built from the
`ResumedFrom` column of every `weft_runs` row — `idx_resumed_from` (a
bloom filter) serves a lookup of given ids, not "every non-empty
value" — so the parked filter reads one String column across the whole
table per query (the resumes only are kept in the set, small next to
the runs). Under the retention cut the set holds the kept resumes
alone: the resumes are collapsed per run (their newest `LastSeen`) and
cut at the call's one boundary, which adds a primary-key read of the
resumes' own rows to that scan; `SessionQuery.Awaiting` reads the same
set. On a large table expect that column scan; the other status
words read only the grouped rows the order index walks.

DSN forms: `clickhouse://user:pass@host:9000/database` (native
protocol, the port is 9000 not 8123), `clickhouses://...` or
`?secure=1` for TLS, extra query parameters become clickhouse-go
settings.

## Running the tests

Unit tests run offline. The conformance table
(`obsdbtest.Run`), the collector-shape insert test and the option
tests are gated on `WEFT_CLICKHOUSE_DSN` and need a server; each test
creates and drops its own database.

### Container recipe (one line)

```sh
docker run -d --name weft-clickhouse -p 127.0.0.1:9000:9000 \
  -e CLICKHOUSE_PASSWORD=weft --ulimit nofile=262144:262144 \
  clickhouse/clickhouse-server:25.8-alpine

WEFT_CLICKHOUSE_DSN='clickhouse://default:weft@127.0.0.1:9000/default' \
  go test -race ./...
```

### docker-compose

```yaml
services:
  clickhouse:
    image: clickhouse/clickhouse-server:25.8-alpine
    environment:
      CLICKHOUSE_PASSWORD: weft
    ports:
      - "127.0.0.1:9000:9000"
    ulimits:
      nofile: 262144:262144
```

### CI job (service container)

The job below is what `.github/workflows/ci.yml` runs for this module —
`clickhouse/clickhouse-server` as a service container, the DSN pointing
at the port the service maps onto the runner. A service container takes
`docker create` flags through `options:` (GitHub Actions has no
`ulimits:` key — one makes the whole workflow file invalid), and the
health check holds the steps until the server answers a query on the
native port. A gated test that skips fails the job, so a DSN the tests
cannot read never reads green; the same DSN un-gates `cmd/weftbase`'s
`--db clickhouse://` test.

```yaml
clickhouse:
  runs-on: ubuntu-latest
  services:
    clickhouse:
      image: clickhouse/clickhouse-server:25.8-alpine
      env:
        CLICKHOUSE_PASSWORD: weft
      ports:
        - 9000:9000
      options: >-
        --ulimit nofile=262144:262144
        --health-cmd "clickhouse-client --password weft --query 'SELECT 1'"
        --health-interval 5s
        --health-timeout 5s
        --health-retries 20
  env:
    WEFT_CLICKHOUSE_DSN: clickhouse://default:weft@127.0.0.1:9000/default
  steps:
    - uses: actions/checkout@v7
    - uses: actions/setup-go@v7
      with:
        go-version: stable
    - name: test (obsdb/clickhouse, gated suite)
      working-directory: obsdb/clickhouse
      run: |
        set -o pipefail
        go test -race -count=1 -v ./... | tee "$RUNNER_TEMP/clickhouse.log"
        if grep -E '^\s*--- SKIP' "$RUNNER_TEMP/clickhouse.log"; then
          echo "gated tests skipped: WEFT_CLICKHOUSE_DSN did not reach them" >&2
          exit 1
        fi
    - name: test (cmd/weftbase, gated --db clickhouse:// test)
      working-directory: cmd/weftbase
      run: |
        set -o pipefail
        go test -race -count=1 -v -run '^TestNewServerClickhouse$' ./... | tee "$RUNNER_TEMP/weft-cmd.log"
        grep -q -- '--- PASS: TestNewServerClickhouse' "$RUNNER_TEMP/weft-cmd.log"
```
