GO ?= go

# Where the release steps stage their artifacts (the release uploads
# from here); overridable per invocation, a relative path resolving
# from this directory:
#   make studio-panel-asset RELEASE_DIR=/tmp/rel
# The default is ignored by studio/web/.gitignore.
RELEASE_DIR ?= studio/web/dist-release

# weftbase is one Go module (github.com/weftbase/weftbase) at the
# repository root; studio/web's go.mod only keeps the Go toolchain out
# of node_modules. It imports the weft framework
# (github.com/weftbase/weft and github.com/weftbase/weft/core). Until
# the two-phase release tags weft under that path, go.mod requires it
# at the placeholder pseudo-version and a go.work beside the checkouts
# resolves it (README "Development"): every target below runs through
# that workspace, from this directory, on ./... only.

.PHONY: build test vet fmt lint tidy offline stress vacuum mcp-check live studio-build studio-bin studio-check studio-panel-asset devtools-npm devtools-vite-check

build:
	$(GO) build ./...

# The per-package timeout `make test` passes: the one CI's test job
# passes (.github/workflows/ci.yml). studio takes ~520 s under -race,
# close to Go's 10-minute default. Override per invocation:
#   make test TEST_TIMEOUT=30m
TEST_TIMEOUT ?= 20m

test:
	$(GO) test -race -timeout $(TEST_TIMEOUT) ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# Needs: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
# (the version weft's CI pins).
lint:
	PATH="$$PATH:$$(go env GOPATH)/bin" golangci-lint run

# go mod tidy reads no go.work: until weft is tagged under
# github.com/weftbase/weft it cannot resolve the placeholder. This
# target points the two weft requirements at the sibling checkout for
# the tidy and drops the replace lines again, so go.mod never commits
# one.
WEFT_DIR ?= ../weft
tidy:
	$(GO) mod edit -replace github.com/weftbase/weft=$(WEFT_DIR) -replace github.com/weftbase/weft/core=$(WEFT_DIR)/core
	GOWORK=off $(GO) mod tidy; status=$$?; \
	  $(GO) mod edit -dropreplace github.com/weftbase/weft -dropreplace github.com/weftbase/weft/core; \
	  sed -i.bak '/github.com\/weftbase\/weft /d;/github.com\/weftbase\/weft\/core /d' go.sum && rm -f go.sum.bak; \
	  exit $$status

# The offline gate (weft's ADR 0013 kill-switch clause): every suite
# stays green under WEFT_MODEL_REQUESTS=deny.
offline:
	WEFT_MODEL_REQUESTS=deny $(GO) test ./...

# The loaded stress (the pre-push gate for anything concurrent):
# GOMAXPROCS=2 and -cpu 1,2 beside two busy loops approximate CI's slow,
# shared runner. STRESS_PKGS and STRESS_COUNT are overridable.
STRESS_PKGS ?= ./obsdb/sqlite ./studio/runtime ./runtime
STRESS_COUNT ?= 10
stress:
	@sh -c '(while :; do :; done) & h1=$$!; (while :; do :; done) & h2=$$!; \
	  trap "kill $$h1 $$h2 2>/dev/null" EXIT INT TERM; \
	  GOMAXPROCS=2 $(GO) test -race -count=$(STRESS_COUNT) -cpu 1,2 -timeout 60m $(STRESS_PKGS)'

# Live tests behind the `live` build tag; never in CI (no keys).
live:
	$(GO) test -tags live ./...

# Studio's database, swept (with WEFT_RETAIN set, e.g. 720h) and
# vacuumed — studio's tables only: $WEFTBASE_DB, else
# ./.weftbase/weftbase.db.
vacuum:
	$(GO) run ./cmd/weftbase vacuum --db "sqlite://$${WEFTBASE_DB:-.weftbase/weftbase.db}"

# The phase 6 gate's third clause (plan I4.2): `weftbase mcp` answers
# "why did run X fail" over stdio. Builds weftbase, starts a real
# `weftbase dev` on a free loopback port over a scratch SQLite file
# (WEFTBASE_DISCOVERY=off, the discovery locations in the scratch
# directory), seeds it with the record-contract fixtures through
# /v1/traces and /v1/logs, drives `weftbase mcp` with the MCP SDK's
# client and prints each tool's summary line, ending "MCP CHECK PASS".
# No model calls. Not part of `make test`: it listens on a port
# (cmd/weftbase/mcp_check_test.go, tag livecheck).
mcp-check:
	WEFT_MODEL_REQUESTS=deny $(GO) test -tags livecheck -run '^TestMCPLiveCheck$$' -count=1 -v ./cmd/weftbase/

# ── Studio (ADR 0018) ────────────────────────────────────────────────
# Needs Bun. Contributors run these only when the UI changes; users of
# the module get the committed, embedded studio/dist.
#
# The panel is stamped with the weft version it speaks (B6):
# vite.panel.config.ts reads `const Version` from the weft module's
# version/version.go, found with `go list -m` (studio/web/scripts/
# weft-version.ts), so the build needs Go and the workspace (or a
# tagged weft in the module cache). A weft bump is: regenerate studio's
# meta golden (`go test ./studio -run TestMetaGolden -update`) and
# rerun this target, committing studio/dist and
# studio/web/npm/package.json (the build writes the version into
# @weftgo/devtools' manifest; studio-check fails on a stale one).
# studio's TestVersionIsTheModules fails on a stale stamp.

studio-build:
	cd studio/web && bun install --frozen-lockfile && bun run build

# The weftbase binary (cmd/weftbase: dev, runs, open, export, doctor,
# mcp, vacuum, version) built into ./weftbase (ignored). Users install
# it with
#   go install github.com/weftbase/weftbase/cmd/weftbase@latest
studio-bin:
	$(GO) build -o weftbase ./cmd/weftbase

# The devtools panel as a release asset (WEFT-DEVTOOLS §5.1): non-Go
# backends serve this file themselves (V2).
studio-panel-asset: studio-build
	cd studio/web && bun run scripts/panel-asset.ts $(abspath $(RELEASE_DIR))

# The freshness gate (ADR 0018 §4): rebuild the web app and prove the
# committed dist matches, fits the 800 KiB gzip budget (§7), and that
# its own checks pass (scripts/vocab-audit.ts: no retired word on
# screen, U21.3; typecheck; tests). CI runs this on every push.
studio-check: studio-build
	git diff --exit-code -- studio/dist || { echo "studio/dist is stale: run 'make studio-build' and commit"; exit 1; }
	test -z "$$(git status --porcelain -- studio/dist)" || { git status --short -- studio/dist; echo "studio/dist has files the commit does not: run 'make studio-build' and commit"; exit 1; }
	total=0; for f in $$(find studio/dist -type f); do \
	  sz=$$(gzip -c $$f | wc -c); total=$$((total+sz)); done; \
	kib=$$((total / 1024)); echo "studio dist: $$kib KiB gzipped (budget 800)"; \
	test $$kib -le 800 || { echo "studio dist exceeds the 800 KiB gzip budget (ADR 0018 §7)"; exit 1; }
	cd studio/web && bun run scripts/npm-package.ts --check
	git diff --exit-code -- studio/web/npm/package.json || { echo "studio/web/npm/package.json is not the weft version: run 'make studio-build' and commit"; exit 1; }
	cd studio/web && bun run scripts/vocab-audit.ts
	cd studio/web && bun run typecheck && bun run test
	cd studio/web && WEFT_DEVTOOLS_PKG=1 bunx vitest run src/npm
	cd studio/web && bun run scripts/npm-consumer.ts

# @weftgo/devtools (plan C1): the npm delivery of the panel, assembled
# in studio/web/npm by the build — the served panel.js byte for byte
# (+ panel.js.sha256), the thin ESM entry with mount/scope/open/close/
# on, the react/vue/svelte helpers, the DOM-free scope.js and their
# declarations. This target (and studio-check) drives the package
# suite against the assembled files and type-checks a consumer of
# every subpath under node16 and bundler resolution
# (scripts/npm-consumer.ts); this one also lists the tarball. It
# never publishes: `npm publish` from studio/web/npm is a release
# decision, not a build step.
devtools-npm: studio-build
	cd studio/web && bun run scripts/npm-package.ts --check
	cd studio/web && WEFT_DEVTOOLS_PKG=1 bunx vitest run src/npm
	cd studio/web && bun run scripts/npm-consumer.ts
	cd studio/web/npm && npm pack --dry-run

# The phase 3 gate's first clause (plan §12), pinned: a real Vite app
# (examples/devtools-vite) installs the packed @weftgo/devtools — the
# tarball `npm publish` would upload — builds with `vite build`, and its
# smoke test (check.ts, jsdom + a fake /api/meta, no browser) proves the
# bundled package put <weft-devtools> on the page and that the installed
# panel.js is studio/dist/panel/panel.js byte for byte. vite and jsdom
# are exact versions with a committed package-lock.json, installed by
# `npm ci` (the registry or a warm npm cache, nothing else); the tarball
# is not in the lockfile (its integrity changes with every panel build)
# and goes in after, offline and unsaved. CI runs it after studio-check.
# Needs the assembled studio/web/npm (make studio-build or
# devtools-npm).
DEVTOOLS_VITE := examples/devtools-vite
devtools-vite-check:
	@test -f studio/web/npm/panel.js || { echo "studio/web/npm is not assembled: run 'make studio-build' first"; exit 1; }
	rm -rf $(DEVTOOLS_VITE)/vendor && mkdir -p $(DEVTOOLS_VITE)/vendor
	cd studio/web/npm && npm pack --silent --pack-destination $(abspath $(DEVTOOLS_VITE))/vendor >/dev/null
	mv $(DEVTOOLS_VITE)/vendor/weftgo-devtools-*.tgz $(DEVTOOLS_VITE)/vendor/weftgo-devtools.tgz
	cd $(DEVTOOLS_VITE) && npm ci --no-audit --no-fund --loglevel=error
	cd $(DEVTOOLS_VITE) && npm install --offline --no-save --no-audit --no-fund --loglevel=error ./vendor/weftgo-devtools.tgz
	! grep -q weftgo $(DEVTOOLS_VITE)/package.json $(DEVTOOLS_VITE)/package-lock.json || { echo "the tarball was saved into package.json or package-lock.json"; exit 1; }
	cd $(DEVTOOLS_VITE) && bun run check
