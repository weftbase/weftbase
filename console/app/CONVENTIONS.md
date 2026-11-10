# Console conventions

One page. Read it before building a screen; the Overview
(`src/routes/index.tsx`) and Runs (`src/routes/runs/index.tsx`) are the
reference implementations of everything below.

## Rules

- Show what the server has. No field, page or action without a `/v1`
  backing. Unknown values read "unknown" or "—" (use `lib/format.ts`),
  never a made-up zero. `cost_usd: null` is "unknown", not `$0`.
- Network only in `src/lib/api/` (`http.ts` fetches, `stream.ts` opens
  SSE). Colour values only in `src/styles.css`. No credential in a URL or
  in storage. ESLint and `src/test/no-token-in-urls.test.ts` enforce it.
- Never the words Studio, panel (as a noun for a part of the UI),
  playground, devtools (`bun run vocab`). Say "detail", "view", "tab".
- Density comes from the system. If a screen needs a `h-…`, `p-…` or
  `text-[…]` override on a `ui/` component, fix the component instead.

## Primitives (`src/components/console/`)

| Use | Component | Notes |
|---|---|---|
| A screen's frame | `Page` (`page-header.tsx`) | `header`, `bar`, children scroll. `flush` for a full-page table (the table is the page). |
| The one-line header | `PageHeader` | `title`, `status` (a `StatusBadge`), `id` (a `CopyId`), `meta` (muted facts, `LivePill`), `actions` (right). 40px. No description. |
| Views of one page | `LinkTabs` | Tabs whose selection is a URL. `tabs: {label, link, active, count?}[]`. |
| A titled block on a padded page | `Section` | 11px uppercase label + `action`, then one bordered container. |
| Filters of a list | `FilterBar`, `SearchInput`, `FilterSelect` (`filter-bar.tsx`) | One bar per list. `SearchInput` debounces 250 ms and owns `/`. `FilterSelect` reports `undefined` for "any"; `any={false}` for a select that always has a value. |
| Any list | `DataTable` (`data-table.tsx`) | `columns: Column<T>[]` (`width` is a grid track), `rows`, `rowKey`, `rowLink` (a row is a real link), `bordered` inside a section, `footer`. ↑/↓, j/k, Home/End, Enter. |
| Figures in a row | `StatStrip` (`stat-strip.tsx`) | `stats: {label, value, hint?, title?, tone?}[]`. One bordered container, equal cells, hairlines. Never cards. |
| A run or session status | `StatusDot` (table rows), `StatusBadge` (headers), `LivePill` (`status.tsx`) | Take a `DisplayStatus` from `displayStatus(run)` / `sessionStatus(session)` (`lib/api/status.ts`). Never read `run.status` yourself. |
| Nothing to show / failed / loading | `EmptyState`, `ErrorState`, `LoadingRows`, `Rebuilding` (`empty-state.tsx`) | `ErrorState error={query.error} what="The run"` reads 404 and 501 as what they are. |
| An id | `CopyId` (`copy-id.tsx`) | Click copies. Shows the id's end; `full` for all of it. `useCopy()` for other copy buttons. |
| JSON | `JsonView` (`json-view.tsx`) | `value`, `depth` (levels open). Collapsible, copyable. |
| A time | `RelativeTime`, `useNow()` (`relative-time.tsx`) | One shared 10 s clock. Use `useNow()` for the duration of something still going. |
| Main + detail | `SplitPane` (`split-pane.tsx`) | `id` (remembers the width), `detail` (null closes it; main goes full width). Divider: drag, ←/→. |
| ⌘K | `CommandMenu` (`command-menu.tsx`) | Add a group there when a new kind of thing becomes findable. |
| Runs columns | `RUN_COLUMNS`, `RUN_COLUMNS_SHORT` (`run-columns.tsx`) | Reuse for any list of runs (a session's turns, approvals). |
| Bars over time | `RunsChart` (`runs-chart.tsx`) | Stacked, two series, legend + hover readout + a table for screen readers. |
| Top bar "New run" | `NewRun` (`new-run.tsx`) | The slot; renders nothing until built. `api.admit` is ready. |

Formatters (`src/lib/format.ts`): `duration(ms)`, `between(start, end, now)`,
`tokens(n)`, `count(n)`, `cost(costFields)`, `usd(n)`, `percent(ratio)`,
`ago(iso)`, `dateTime(iso)`, `bytes(n)`, `shortId(id)`. All take
`null`/`undefined` and answer "—" (cost: "unknown", or "≥ $x" for a
partly priced sum).

## Density recipes

| Thing | Classes |
|---|---|
| UI text / secondary | `text-sm` (13px, the body default) / `text-xs` (12px). Nothing else for UI text. |
| Ids, durations, tokens, cost | `font-mono text-xs tabular-nums` (a `DataTable` column: `mono: true`) |
| Column and section labels | `table-head` (11px uppercase, muted) |
| Controls | `Button` 28px (`size="sm"` 24px, `size="icon"` 28×28, `"icon-sm"` 24×24); `Input`, `SelectTrigger`, `TabsList` 28px (`h-7`) |
| Table | rows `h-8`, header `h-7`, cells `px-2`; no card around a full-page table |
| Page | padding `p-4` (from `Page`); gaps `gap-2`, `gap-3`, `gap-4` only |
| Container | `rounded-alert border bg-card` (8px); padded content `p-3`; rows inside split by `border-b` hairlines. One container, no cards inside cards. |
| Bars | top bar `h-10`, page header `h-10`, filter bar `h-10`, link tabs `h-8`, nav rows `h-7` |
| Radii | `rounded-input` 6px (controls), `rounded-alert` 8px (containers), `rounded-card` 10px (dialogs), `rounded-sm` (badges, menu items) |
| Icons | `size-3.5` in controls and rows, `size-4` in the nav |
| A big figure | `stat` (18px, medium, tabular) and only inside `StatStrip` |

## Queries and streams (`src/lib/api/`)

- `types.ts`: the wire types, hand-written from the Go structs and the
  goldens (the OpenAPI spec types most bodies as open objects). Extend
  them there when the server grows; fixtures in `src/test/fixtures/` are
  copies of `api/http/v1/testdata/v1/*.golden.json` (`cp`, never edit).
- `http.ts`: `api.*`, one function per endpoint. Screens do not call it
  for reads; they use `q.*` from `queries.ts` with `useQuery` /
  `useInfiniteQuery`. Mutations call `api.*` in a `useMutation` and then
  invalidate `afterRunWrite(id)`.
- Routes have no loaders. A screen renders `ErrorState` for
  `query.error`, `LoadingRows` while `!query.data`, `EmptyState` for an
  empty list, then the content. A 401 anywhere returns the app to the
  signed-out screen (`router.tsx`, `auth-gate.tsx`).
- `status.ts`: `displayStatus(run)` is the only mapping from server
  fields to a status (queued, running, waiting, succeeded, failed,
  cancelled, interrupted). It prefers the server's `display_status`.
- Live: mount a hook from `live.ts` and the queries stay current.
  `useRunsLive()` is mounted once by the shell (read its state with
  `useLiveState()` for a `LivePill`); a run page mounts
  `useRunLive(id, { enabled: running, onFrame })`, a session page
  `useSessionLive(id)`. A `run` frame is patched into every cached list
  and into `["run", id]`; everything else is invalidated at most once a
  second. Follow the key shapes at the top of `queries.ts` or the stream
  will not find your query: anything about one run lives under
  `["run", id, …]`, one session under `["session", id, …]`.
- Streams resume by themselves: the browser's EventSource sends
  `Last-Event-ID` on reconnect and the server backfills. After an HTTP
  error or an `overflow` frame the hooks refetch. Records are
  de-duplicated on (run, kind, pos).

## URL state (`src/lib/search.ts`)

Every filter, tab and selection is a search param with a validator that
drops what it does not know.

```tsx
export const Route = createFileRoute("/runs/")({ validateSearch: validateRuns, component: RunsPage })
const search = Route.useSearch()
const set = useSetSearch<RunsSearch>(Route.fullPath)
set({ status: "failed" })          // replaces the history entry
set({ span: id }, { push: true })  // a selection the Back button should undo
```

`undefined` removes a key; defaults are never written to the URL. Time
windows are `Range` (`1h | 24h | 7d | 30d`); turn one into `since` with
`sinceOf(range)` inside a `useMemo`, so the query key stays stable.
Validators already exist for the run page (`tab`, `span`, `step`), the
session page (`turn`, `span`), agents (`tab`), approvals (`run`) and a
raw trace (`span`).

## Waterfall: lanes and kinds

Colour is the **agent**, not the kind (`src/lib/lanes.ts`, tokens in
`styles.css`):

| Token | Use | Classes |
|---|---|---|
| `--lane-0` | the root agent, neutral | `bg-lane-0`, `bg-lane-0-dim`, `text-lane-0`, `border-lane-0` |
| `--lane-1` … `--lane-6` | subagents, in order of first appearance: blue, orange, aqua, violet, green, magenta | `bg-lane-N` (solid: model calls), `bg-lane-N-dim` (tool calls, summary bars), `text-lane-N`, `border-lane-N` |
| `--span-waiting` | waiting on a person: `span-hatch` (diagonal hatch outline on neutral) | `span-hatch` |
| `--span-failed` | failed: danger pill, `--span-failed-foreground` for the ✕ | `bg-span-failed text-span-failed-foreground` |
| `--span-slowest` | the "Slowest" badge | `bg-span-slowest-muted text-span-slowest` |

`assignLanes(spans, rootRunId)` gives each run id its `Lane`
(`{solid, dim, text, border}`). The hue order is fixed and never cycles:
it passes the colour-blind checks for neighbouring lanes in light and
dark (the first three for any pair). A 7th subagent is neutral.

Kind is carried by icon and bar style (`src/lib/span-kind.ts`,
`SPAN_KINDS[kind]` → `{label, icon, bar, legend}`):

| Kind | Icon (lucide) | Bar style | From |
|---|---|---|---|
| agent | `Bot` | `summary`: its children's segments on one line | `gen_ai.operation.name = invoke_agent` |
| model | `Sparkles` | `solid` pill in the lane hue | any other `gen_ai.operation.name` (`chat`) |
| tool | `Wrench` | `dim`: the lane hue dimmed, shorter | `execute_tool` |
| handoff | `CornerDownRight` | `dim`, plus a connector in the child's hue | `execute_tool` with `weft.tool.origin = subagent` |
| waiting | `Hourglass` | `hatch` | a tool call the run is parked on (`spanKind(span, true)`) |
| failed | `OctagonX` | `failed`: danger pill with ✕, name in red | span `status = error` |
| other | `Circle` | `thin` neutral bar | no `gen_ai.operation.name` (a stock OTel span) |

`spanKind(span, waiting?)`, `spanLabel(span)` (the name) and
`spanSecondary(span)` (the one muted word after it) are the readers.
`SPAN_KIND_ORDER` is the footer legend. Spans come from
`q.spans(runId)` (`tree=1`: every descendant run's spans, each with its
`run_id`).

## Running against real data

```sh
# repo root: a server on :7331 with scripted runs, a new one every 4 s
go run ./examples/demo
# console/app: Vite on :3000, proxying /v1 and /otlp to :7331
bun run dev
```

Open the sign-in link the demo prints for `http://localhost:3000`
(`/#signin=…`, one use, 15 minutes). If :3000 is taken:
`go run ./examples/demo -console http://localhost:3111` and
`bunx vite dev --port 3111`. `weftbase dev` works the same way with your
own agents. Gates: `bun run typecheck && bun run lint && bun run test &&
bun run build && bun run budget && bun run vocab`.
