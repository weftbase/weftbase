GO ?= go

# weftbase is two Go modules in one repository (docs/architecture.md):
#   github.com/weftbase/weftbase         the server (repository root)
#   github.com/weftbase/weftbase/client  the client SDK (client/), own go.mod,
#                                        imports weft, the standard library
#                                        and OpenTelemetry only
# The committed go.work joins the two (weft comes from the module proxy
# at the version both go.mod files require). Every target runs from
# this directory; the client module's targets run inside client/.
MODULES := . client

.PHONY: build vet test fmt lint tidy offline stress live vacuum check layering client-deps vocab \
	mcp-check console-build console-check api-gen api-check tenancy demo gowork-off clickhouse

build:
	$(GO) build ./...
	cd client && $(GO) build ./...

vet:
	$(GO) vet ./...
	cd client && $(GO) vet ./...

# The per-package timeout `make test` passes (the one CI passes).
TEST_TIMEOUT ?= 20m

test:
	$(GO) test -race -timeout $(TEST_TIMEOUT) ./...
	cd client && $(GO) test -race -timeout $(TEST_TIMEOUT) ./...

# The gate every commit passes: build, vet and race tests in both
# modules, the layering check, the client module's dependency
# allow-list and the vocabulary audit.
check: build vet test layering client-deps vocab

# The build a release makes, without the workspace: GOWORK=off, the
# client module's placeholder requirement pointed at ./client the way
# requiring the tagged client/vX.Y.Z will (the replace is dropped again
# afterwards; go.mod never commits one). The client module must also be
# tidy on its own.
gowork-off:
	@set -e; \
	$(GO) mod edit -replace github.com/weftbase/weftbase/client=./client; \
	trap 'cd "$(CURDIR)" && $(GO) mod edit -dropreplace github.com/weftbase/weftbase/client' EXIT; \
	GOWORK=off $(GO) build ./...; \
	GOWORK=off $(GO) vet ./...; \
	cd client && GOWORK=off $(GO) build ./... && GOWORK=off $(GO) mod tidy -diff

# The DSN-gated suites against a ClickHouse server
# (WEFT_CLICKHOUSE_DSN, e.g. clickhouse://default:weft@127.0.0.1:9000/default):
# every package with a ClickHouse half. A gated test that skips for want
# of the DSN fails the target. The verbose log is kept in
# CLICKHOUSE_LOG.
CLICKHOUSE_PKGS ?= ./obsdb/... ./api/http/v1/ ./observe/ingest/
CLICKHOUSE_LOG ?= $${TMPDIR:-/tmp}/weftbase-clickhouse.log
clickhouse:
	@test -n "$$WEFT_CLICKHOUSE_DSN" || { echo "clickhouse: set WEFT_CLICKHOUSE_DSN"; exit 1; }
	@log=$(CLICKHOUSE_LOG); \
	$(GO) test -race -count=1 -timeout 30m -v $(CLICKHOUSE_PKGS) > $$log 2>&1; status=$$?; \
	grep -E '^(ok|FAIL|--- FAIL|--- SKIP|panic)' $$log; \
	test $$status -eq 0 || { echo "clickhouse: tests failed (log: $$log)" >&2; exit 1; }; \
	if grep -n 'WEFT_CLICKHOUSE_DSN not set' $$log; then \
	  echo "clickhouse: gated tests skipped although WEFT_CLICKHOUSE_DSN is set (log: $$log)" >&2; exit 1; fi

fmt:
	$(GO) fmt ./...
	cd client && $(GO) fmt ./...

# Needs: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
# (the version weft's CI pins).
lint:
	PATH="$$PATH:$$(go env GOPATH)/bin" golangci-lint run ./...
	cd client && PATH="$$PATH:$$(go env GOPATH)/bin" golangci-lint run ./...

# go mod tidy reads no go.work. Both modules require weft at its
# published version, so the client module is a plain tidy. The server
# still requires the client module at the placeholder pseudo-version
# (it is not tagged yet): its tidy points that requirement at ./client,
# tidies, drops the replace again (go.mod never commits one) and strips
# the placeholder's lines from go.sum. Once client/vX is tagged (the
# two-phase release: tag client/vX.Y.Z first, then the root), it is a
# plain `GOWORK=off go mod tidy` there too.
tidy:
	@set -e; \
	(cd client && GOWORK=off $(GO) mod tidy); \
	$(GO) mod edit -replace github.com/weftbase/weftbase/client=./client; \
	GOWORK=off $(GO) mod tidy || status=$$?; \
	$(GO) mod edit -dropreplace github.com/weftbase/weftbase/client; \
	sed -i.bak '/github.com\/weftbase\/weftbase\/client /d' go.sum && rm -f go.sum.bak; \
	exit $${status:-0}

# The demo: a node on :7331 with scripted agents in both placements and
# a steady trickle of runs (examples/demo; no model provider, no key).
# Beside it, `cd console/app && bun run dev` serves the Console on :3000.
demo:
	$(GO) run ./examples/demo $(DEMO_ARGS)

# The offline gate: every suite stays green under
# WEFT_MODEL_REQUESTS=deny.
offline:
	WEFT_MODEL_REQUESTS=deny $(GO) test ./...
	cd client && WEFT_MODEL_REQUESTS=deny $(GO) test ./...

# The loaded stress (the pre-push gate for anything concurrent):
# GOMAXPROCS=2 and -cpu 1,2 beside two busy loops approximate CI's slow,
# shared runner. STRESS_PKGS and STRESS_COUNT are overridable.
STRESS_PKGS ?= ./obsdb/sqlite ./events/... ./queue/... ./worker/...
STRESS_COUNT ?= 10
stress:
	@sh -c '(while :; do :; done) & h1=$$!; (while :; do :; done) & h2=$$!; \
	  trap "kill $$h1 $$h2 2>/dev/null" EXIT INT TERM; \
	  GOMAXPROCS=2 $(GO) test -race -count=$(STRESS_COUNT) -cpu 1,2 -timeout 60m $(STRESS_PKGS)'

# Live tests behind the `live` build tag; never in CI (no keys).
live:
	$(GO) test -tags live ./...

# The trace database, swept (with WEFT_RETAIN set, e.g. 720h) and
# vacuumed: $$WEFTBASE_DB, else ./.weftbase/traces.db.
vacuum:
	$(GO) run ./cmd/weftbase vacuum --db "sqlite://$${WEFTBASE_DB:-.weftbase/traces.db}"

# The layering check (docs/architecture.md, plan §1.4): no import goes
# up a layer or across the domain DAG, tenant.NewScope is called only
# from authz and tenant, and the client module imports nothing of the
# server. scripts/layering has its own seeded-violation test.
layering:
	$(GO) run ./scripts/layering

# The client module's dependency allow-list: weft, the standard
# library, OpenTelemetry and their transitive dependencies; nothing of
# the server module.
client-deps:
	$(GO) run ./scripts/layering -client-only

# The Go vocabulary audit: the retired product words (the vocab lines
# of scripts/layering/layers.txt) appear nowhere in
# Go code, comments or stored values.
vocab:
	$(GO) run ./scripts/layering -vocab-only

# The MCP check: `weftbase mcp` answers "why did run X fail" over stdio
# against a real `weftbase dev` on a free loopback port, seeded with the
# record-contract fixtures over /otlp/v1. No model calls; listens on a
# port, so not part of `make test` (tag livecheck).
mcp-check:
	WEFT_MODEL_REQUESTS=deny $(GO) test -tags livecheck -run '^TestMCPLiveCheck$$' -count=1 -v ./api/mcp/

# ── Console ──────────────────────────────────────────────────────────
# The Bun project lives in console/app; its build is committed to
# console/dist and embedded by package console, so `go install` needs
# no Bun. Contributors run these when the Console changes.
CONSOLE_APP := console/app

console-build:
	@test -f $(CONSOLE_APP)/package.json || { echo "console/app is not on this branch yet"; exit 1; }
	cd $(CONSOLE_APP) && bun install --frozen-lockfile && bun run build

# The Console gate: the app's typecheck, lint, tests, vocabulary audit,
# formatting and size budget (800 KiB gzipped), on a fresh build that
# must equal the committed dist.
console-check: console-build
	cd $(CONSOLE_APP) && bun run typecheck
	cd $(CONSOLE_APP) && bun run lint
	cd $(CONSOLE_APP) && bun run test
	cd $(CONSOLE_APP) && bun run vocab
	cd $(CONSOLE_APP) && bun run check
	cd $(CONSOLE_APP) && bun run budget
	git diff --exit-code -- console/dist || { echo "console/dist is stale: run 'make console-build' and commit"; exit 1; }
	test -z "$$(git status --porcelain -- console/dist)" || { git status --short -- console/dist; echo "console/dist has files the commit does not"; exit 1; }

# ── The /v1 contract ─────────────────────────────────────────────────
# api/openapi/v1.yaml is the contract (spec-first). Code generation
# from it (the oapi-codegen strict server interface, a generated
# client/rest, the Console's types) lands in a later step: until then
# api-gen generates nothing, and api-check runs the spec/route
# consistency test (every route of the table is an operation of the
# spec with the same authz action, and every operation a route).
api-gen:
	@echo "api-gen: no code is generated from api/openapi/v1.yaml yet (client/rest and the handlers are hand-written); see api-check"

api-check:
	$(GO) test -count=1 -run 'TestRoute|TestOpenAPI' ./api/http/

# The tenancy conformance suite: every endpoint and stream, run with
# tenant A's credentials against tenant B's ids, answers 404 and no
# events (plan §9 step 7). It lands with Phase 2; until then the target
# runs the store's cross-tenant group.
tenancy:
	$(GO) test -race -run 'Tenan' ./store/... ./api/...
