# Weftbase Console

The operator UI for Weftbase: a TanStack Start SPA (no SSR, the shell is
prerendered), TanStack Router file routes, TanStack Query, shadcn on the
radix base, Bun, TypeScript strict. The build goes to `../dist`, which
`console/console.go` embeds. Plan: WEFTBASE-ARCHITECTURE §7.

Created with:

```sh
bunx --bun shadcn@latest init --preset b7BFgTjg8 --base radix --template start --pointer
```

## Commands

```sh
bun install
bun run dev        # Vite on :3000; proxies /v1 and /otlp to weftbase dev on :7331
bun run build      # SPA build, then scripts/clean-dist.ts writes ../dist (deterministic, with csp.json)
bun run typecheck
bun run lint       # also: no fetch outside src/lib/api, no colour literals, no server functions
bun run test       # Vitest: API layer on golden fixtures, streams, routes, no token in URLs, primitives
bun run vocab      # retired words: Studio, panel, playground, devtools
bun run budget     # ../dist must stay at or under 800 KiB gzip
```

## Layout

Read `CONVENTIONS.md` before building a screen: the primitives, the
density recipes, the query and stream pattern, URL state and the
waterfall's lane and kind maps.

- `src/styles.css`: the Weftbase design system theme, compact (13px
  text, 28px controls, 32px rows), light and dark, with the waterfall's
  lane tokens. The only place colour values live.
- `src/components/ui/`: shadcn components, restyled to the compact theme.
- `src/components/console/`: the shell, the auth gate and the shared
  primitives (`PageHeader`, `FilterBar`, `DataTable`, `StatStrip`,
  `StatusBadge`, `EmptyState`, `ErrorState`, `CopyId`, `JsonView`,
  `RelativeTime`, `SplitPane`, the command menu).
- `src/lib/api/`: the `/v1` layer on the real server. `types.ts` (wire
  types), `http.ts` (the client, the only file that calls `fetch`),
  `stream.ts` (SSE, the only file that opens an `EventSource`),
  `queries.ts` (TanStack Query options), `live.ts` (streams wired into
  the query cache), `status.ts` (the one status mapping), `auth.ts` (the
  sign-in link). There is no mock client: the Console shows what the
  server returns.
- `src/lib/search.ts`: URL search schemas; every view and selection is a URL.
- `src/test/fixtures/`: copies of the server's goldens
  (`api/http/v1/testdata/v1`), the only data tests use.
- Fonts (Geist, Geist Mono) are self-hosted from `public/fonts`.

## Running against a server

`go run ./examples/demo` (repo root) serves real, scripted data on
:7331; `bun run dev` proxies to it. Open the sign-in link it prints
(`/#signin=…`): the Console exchanges the one-time token for the
HttpOnly `wb_session` cookie and removes it from the address. Without a
session the Console shows a signed-out screen where the link can be
pasted.

## Screens

Overview (tiles, runs by outcome, Needs attention, the Breakdown table,
one window switch), Runs (chains folded, "+ Filter", sortable headers,
the Traces tab), Agents (the inventory, the Tools tab, an agent's
capabilities, versions and Move label), Approvals (the inbox and the
decision) and Settings are complete and live, as are the command menu
(⌘K) and the shortcuts sheet (`?`). The run
page (`src/routes/runs/$runId.tsx`, parts in
`src/components/console/run/`) is the trace waterfall
(`src/components/console/trace-waterfall/`) with playback, the detail of
a step, Transcript, Logs and Details; a run that parked and was resumed
is one page (`src/lib/api/chain.ts`). A raw trace
(`src/routes/traces/$traceId.tsx`) is the same waterfall. "New run" is
`src/components/console/new-run.tsx`. Not built: comparing two runs
(`/v1/diff`).
