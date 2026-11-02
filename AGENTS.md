# weftbase — guide for coding agents and contributors

The shortest accurate map of the platform for anyone (human or model)
writing code with or inside weftbase. The godoc is the authority; this
is the map. weft's own `AGENTS.md` (github.com/weftbase/weft) maps the
framework: the loop, thread, otel, mcp, mw, wefttest.

One Go module, `github.com/weftbase/weftbase` (studio/web's go.mod only
guards node_modules): `studio` (+ `ingest`, `runtime`, the embedded
`dist`, the Bun source in `web/`), `obsdb` (+ `sqlite`, `clickhouse`,
`obsdbtest`), `runtime` (the app side of the runtime link), `scope`,
`exporter` (the local destinations over weft's otel), `cmd/weftbase`
(the CLI) and `internal/` (discovery, linkreg, dblock, doctor, listen,
termsafe, release, weftsrc). It imports weft
(`github.com/weftbase/weft`, `github.com/weftbase/weft/core`); weft
never imports it. Until weft is tagged under that path, go.mod requires
it at the placeholder version and a go.work resolves it (README
"Development").

Working here: `make build vet test` (`make offline` under
`WEFT_MODEL_REQUESTS=deny`), `make tidy` (never commit a `replace`),
`make studio-build` / `make studio-check` when `studio/web` changes
(commit `studio/dist` and the panel-budget ledger row). The records the
store reads are weft's `docs/record-contract.md`;
`TestRecordContractDocNames` (obsdb) reads it from the weft module
(`internal/weftsrc`). Docs: `README.md`, `studio/README.md`,
`docs/mcp-recipe.md`, `docs/testing.md`, `docs/adr/`.

## The platform's API (blocks 6–7b of weft's map, as they moved)

```go
// 6. Record runs (package weftbase/exporter, the platform's destinations over
//    weft/otel — the store is gone, ADR 0024 step 5; the pipeline is the
//    recorder now, block 9 has the full destination menu):
//    defer exporter.Install()()  // local sink ./.weftbase/weftbase.db, content on, no network —
//    // every event, delta, transcript record and span leaves as it happens
//    // (weft.heartbeat records — otel.Heartbeat(d), default 10 s — keep a quiet
//    // run reading running); the durable run reads back through obsdb —
//    // exporter.LocalDB() — never through the process.
//    // weft/otel alone (otel.Install) reads only the OTEL_* variables and, with
//    // nothing configured, records nothing (one INFO line).
//    // With WEFTBASE_URL unset (and WEFTBASE_DISCOVERY not "off") exporter.Install also reads
//    // the discovery file a running `weftbase dev` wrote (./.weftbase, then
//    // $XDG_RUNTIME_DIR/weftbase, then the user cache dir; internal/discovery): the app
//    // exports to that Studio with no configuration, one INFO line names it. Trusted
//    // only when its url is loopback, fresh (pid alive, < 24 h) and, on unix, 0600 and
//    // this user's; else ignored at Debug. Explicit exporter.Studio(...) or exporter.Local(...) >
//    // WEFTBASE_URL > the file: any of them switches the read off, as do
//    // exporter.NoDiscovery() (the file alone), WEFTBASE_DISCOVERY=off and otel.NoEnv();
//    // NoGlobal() does not. Local + a Studio: name both (README "When weft/exporter
//    // reads the file"). Layering: framework packages never import exporter, obsdb,
//    // studio, runtime or scope — `make layering` (scripts/check-layering.sh) fails if one does.
//    weft.Metadata(map[string]string{"cwd": wd})  // run option: caller pairs on every record/span of the run
//    // (inherited by subagents; thread sessions stamp weft.session.id,
//    // weft.public_id, weft.turn — block 8).

// 7. Serve the Inspector (package weftbase/studio; S4 on obsdb — UI, JSON API,
//    OTLP ingest, live, the devtools panel, the playground): setup A,
//    embedded beside the app (§10.1's five lines):
//    mux.Handle("/studio/", http.StripPrefix("/studio",
//        studio.Handler(studio.DB(exporter.LocalDB()))))  // the pipeline's handle: history + live [D4]
//    // or studio.New(opts...) *Server with Handler()/Close()/Runtime()
//    //    (the runtime link's server under Playground(true), else nil),
//    //    options DB/Open/Base/Manifest/Title/Capabilities/Token/Live/NoIngest/
//    //    IngestToken/AllowOrigins/Playground/Retain (Retain(d): a sweeper deletes the runs not
//    //    running (interrupted included) last seen more than d ago, once at New then every
//    //    min(d/10, 1h), floor 1 min, one Debug line per sweep; off by default; d ≥ 1h (New panics
//    //    below: a 30 s silence reads interrupted); runs until Close — New + Close, not Handler — or a
//    //    closed DB; stored manifests are never swept)/Pricing (Pricing(studio.PricingTable{Dated, Models: []studio.ModelPrice{{Provider,
//    //    ModelGlob (path.Match over provider/model, first match wins), Input, Output, CachedInput?,
//    //    CacheWrite?, Reasoning?}}}) — USD per 1M; default studio.DefaultPricing() = studio/pricing.json,
//    //    dated, go generate ./studio/... from internal/pricingdata; else WEFT_PRICING=<path>
//    //    (studio.LoadPricing; replaces the table whole; malformed → New panics naming path + field; a row with no provider,
//    //    or "*", matches its model glob alone on any transport)); routes register through
//    //    routes.go's groups (panel.go/playground.go add theirs in their own files).
//    // API: meta (db {kind, path, size: path/size to loopback and the server token only},
//    //    auth_required, content, runtimes, manifest_check, pricing {source default|file|option, path?
//    //    (db.path's rule), dated, models}, retention (Retain's Go
//    //    duration string, null when off), ingest {dropped: [{service, records, last_heard}], accepted, refused, last_error?, last_at?} (accepted/refused: the OTLP POSTs this Studio answered 2xx / 4xx–5xx, last_error the last refusal "<status> <code>: <reason>"; U5: dropped is the largest
//    //    weft.records.dropped each service's heartbeats and run_start/run_finish reported on the OTLP path; in memory;
//    //    manifest_check's rule: dev token / setup A loopback only, omitted for a panel token) — `weftbase doctor` checks the
//    //    fields its line table names (internal/doctor.Lines: studio, token, discovery, db, content, data, otlp, ingest,
//    //    runtimes, panel, manifest), not every field: retention, pricing, live_all, debug_scope, interrupted_after_ms are
//    //    read with GET /api/meta (or weftbase mcp's meta tool); capabilities_off {capability: why} names the option/flag that left one off;
//    //    manifest_sources), agents/{name}/proposal (PUT {instructions} / DELETE, plan F6: proposed prompts in memory, 64, lost
//    //    on restart; dev token or setup A loopback only; capability proposals), manifest (each agent with `proposed`
//    //    {instructions, instructions_hash, since} when one is held; the weft.json, else the manifests runtimes registered with —
//    //    remembered in memory by (service, manifest_hash), each in sources[] live or remembered; a
//    //    Studio restart forgets them until the next registration; manifest_check.source file|runtime),
//    //    manifests/{hash} (U27, capability "manifests" — the DB an obsdb.ManifestStore, both backends: the
//    //    one-agent manifest a run's manifest_hash names, {manifest_hash, agent, source file|runtime, first_seen,
//    //    current_manifest_hash ("" when the current manifest names no such agent), manifest}; written from the
//    //    weft.json at New and from every runtime registration (the link's ordered writer); content-addressed,
//    //    the first write kept, kept forever — DELETE runs/sessions and Retain never touch them; /api/manifest's
//    //    rule: a read token 403 hidden; the run document carries manifest_available: true when its hash resolves),
//    //    runs (+status — running|succeeded|failed|interrupted and the read-time parked (open parks: pending, no resumer)|cancelled, one parser with search's, any other word a 400 (I3) — +session/public/playground/experiment_id/stopped_by/error_type/id_prefix filters — stopped_by exact, or a
//    //    prefix ending ":" (has_tool_call:); id_prefix the runs whose id starts with it, byte for byte (obsdb.RunQuery.IDPrefix,
//    //    both backends; the ⌘K palette's lookup of a pasted short id); session_compactions / delegated_sessions / ended_by / ended_by_turn / ended_kind on a page the request narrowed by public_id or session (U16: a turn's session compaction markers, the sessions its calls delegated and the interrupt record that felled it — the panel's ⌗, ↳ n and "✗ interrupted by tN" / "↶ rolled back by tN · off path" chips; read through obsdb.SessionEventQuery.Kinds, paged, ≤ 50 sessions, the rest the page's turn_marks_capped hole); turn_below / turn_above (obsdb.RunQuery.TurnBelow /
//    //    TurnAbove: with session and limit=1 a turn's neighbour, total the count that side — the turn stepper's O(1) read) and oldest; all=1 lists child
//    //    runs too; each row's stopped_by/cancel_cause (what ended it, U3.2; omitted when none); resumed_from / resumed_by (the approval link, U13.1: the parked run a resume names, and on the parked run the run that resumed it — read time, either ingest order; omitted when none); error_type (a failed run's kind, context errors read cancelled) and the problem counters attempts_retried/fallbacks/tool_errors/denied/truncated/repaired (U4, ingest time — retried counts a failed attempt a later one of its call followed, fallbacks the attempts weft.attempt.layer marks, tool_errors skips RETRY; log_warn/log_error on the run document, read time through obsdb.DB.CountLogs, logs_capped when they are floors (drawn ≥); each omitted when 0); each row's children_count / children_failed (H9: the runs whose parent is it, one level, from the cost's children read — no read per row; omitted when 0; children_unknown true when that read failed, the UI's "⑂ ?"); each row's own_usage / delegated_usage (U30.2: usage less its core Subagent children's, and theirs; omitted with no Subagent child, running, the children read failed, or a child a panel token cannot see); each row's prompt_preview: the turn's own prompt (input record's last user text — a resume's new prompt when it carries one — else first), 200 runes, absent content-off; settling: true while its root is stored (not failed: a failed root is terminal) and its run_finish not, within 30 s — the UI's isLive follows it, record contract §6),
//    //    search (?q= terms over prompt/answer/error/tool names — substrings, AND, a "phrase" one
//    //    term; agent/tool/status/model/error/since/until/min_tokens/sort=started|duration|tokens/
//    //    limit/offset + runs' filters; min_cost (USD, own + delegated; unpriced never matches) and
//    //    sort=cost (most first, unpriced last) applied in Studio over the match's first 2,000 runs,
//    //    cost_sort_cap when cut; run rows
//    //    only, scoped like runs; capability "search" — obsdb.Searcher: SQLite FTS5 trigram,
//    //    migration 0004; ClickHouse one derivation per search + positionCaseInsensitiveUTF8, no migration; short terms ASCII-folded on SQLite), runs/{id} (with its compactions: run-scope views, then session markers; summarizer (a session turn's summarizer calls priced, U15.1-fix); decided / resumes (U13.2, read time): a parked run's calls as the resume resumed_by names decided them, and on that resume the same — outcome approved|denied|resolved|expired|no_decision, via / key_id / grant_id / at, who, a deny reason and a resolve's result (content: hidden to a read token, not_recorded content-off), the actor (metadata: every token reads it; several, or a split verdict, name no call's decider), from weft.resume.calls else the pre-step results, else the step-0 spans, else a not_recorded hole — thread's records read by obsdb.SessionEventQuery.RunIDs, a met quorum's approvers all named, a split verdict's denier and approvers apart; caller_linked: the runs its calls started
//    //    that are no Subagent children — caller_run_id names it (U14), the first 100 in start order (obsdb.RunQuery.Oldest), caller_linked_more/_hidden/_unknown drawn as truncated/hidden/not_recorded badges; a panel token reads only its public id's child runs — children[], a step's children, every children_count and delegated figure — the rest one hidden hole with a count, P8-scope-fix),
//    //    runs/{id}/events|transcript|spans|requests|tools (spans: run_id on each; ?tree=1 — capability spans_tree, obsdb.TreeReader, U9.1 — every descendant run's spans too, by the weft.parent.run.id chain and core's id rule (<parent>/<step>/<call>), found from the run rows and read by run id — never the whole trace — keyed run_id + span_id with parent_run_id, at most 10,000 (obsdb.TreeSpanCap), holes: truncated/response_cap past it (and past a derived run's 50,000-span trace read), not_recorded/no_spans naming a finished child with no span (runs the first 100, count all; a running child is not missing), hidden for a panel token's other-public-id descendants (left out with their subtrees before the cap, counted — the hole's count, obsdb.RunTree.HiddenCount —, never a 403; obsdb.TreeQuery.PublicID); each requests row's `raw`: the attempt's wire pair under otel.RawBodies, else null + raw_badge not_recorded; transcript?step=N adds what step N's
//    //    model call carried — the record's truth, the replay prefix for N > 0 (from_step 0 re-runs
//    //    step 0's PrepareStep), compacted_at naming its view; null under hidden for a read token, ADR 0029),
//    //    runs/{id}/steps/{n} (one step
//    //    assembled: request, attempts (each with http_status / wait_ms / error, U25), events, tool calls,
//    //    children, holes, its own call's cost; capability "steps"; the run cards' words — exec line, park
//    //    word, typed child outcome, attempt cause, PrepareStep chip, thinking wire, attachments, token
//    //    split, OUTPUT card — are src/lib/cards.ts's, both renderers), cost on every run row (plan A5, read time, never stored): cost_usd
//    //    (own: usage minus its core Subagent children's — `<id>/…` only; pool children and nested Generates are not
//    //    subtracted — at its model's rates; null + cost_badge unpriced|unknown + cost_reason, never 0; a scripted model no
//    //    row matches — wefttest/*, weft/runtime/scripted only (any other wrapper ships its own row) — an exact 0 + cost_badge scripted, a Go rule, not a row), cost_known,
//    //    cache exact|not_reported (no cache split); running or end-never-arrived → own unknown, delegated_cost_usd/_known/_reason (descendants' own, never folded;
//    //    a summed reason one line: "2 of 3 subagent runs unpriced: no price for …"); unpriced_models / delegated_unpriced_models (deduped, five at most then one "(+n)" element, the reason's list the same; a page says
//    //    them once — lib/cost.ts unpricedLine, "N runs unpriced: no row for …" + a /settings?price= link prefilling a row); session rows
//    //    cost_usd = turns' own + delegated (no pool-child figure); settings/pricing (the effective
//    //    table in the file's shape + source, path?; capability "pricing", every identity reads it), runs/{id}/export?format=json|jsonl|otlp|wefttest (the whole run as one
//    //    download, capability "export"; &tree=1 on json/jsonl/otlp adds every subagent run under it, ≤ 200, each its own export (json subagents + subagent_holes, the file <run>.tree.<ext>; wefttest refuses it); otlp re-ingests through /v1/*, wefttest is a zip:
//    //    `<agent>_replay_test.go` (plan F6, a passing replay test) beside `testdata/TestReplay_<run>/` fixtures
//    //    wefttest.Replay reads — results keyed by (step, call id), steered steps via a generated wefttest.NewSteers source, a
//    //    compacted step keys on its view, noted compacted_at),
//    //    runs/{id}/logs?from=&limit=&severity= (the app's own log lines under the run's
//    //    spans, capability "logs"; refused to a read-scoped token: they may carry prompts;
//    //    the run row carries delta_count; drawn by the run page's logs view ?view=logs&lsev=,
//    //    the drawer's Logs tab, WARN/ERROR row ticks, jump to span ?sel=t:<span>; the panel's
//    //    step "📄 ⤢" hands off, U11.2), diff?a=&b= (two runs aligned by step ordinal, each
//    //    step reduced to system/tool_calls/tool_results/text/usage with changes and unknown per
//    //    row (a column a side did not record is never compared) and a summary; compaction and
//    //    subagent are marks, never a change; system hidden to a read token — the side's system_hidden: true,
//    //    a side's hidden hole keeping its count (child runs out of scope); past the step cap a
//    //    truncated hole, cause response_cap; capability "diff"; drawn through src/lib/stepdiff.ts —
//    //    unknown reads "not comparable" — by Studio's N-way /compare?a=&b=[…] page (N−1 diffs against
//    //    a; from the run header, the replay drawer, the playground) and the panel's 2-way result
//    //    pane, parity.test.ts holding both to the same rows), traces (the trace list: each
//    //    trace's root {span_id, name} — null + badge gap when only child spans arrived — service,
//    //    start/end/duration_ms, span_count, error, run_ids (the join key), root_run {id, agent, prompt_preview, status} (the first id without "/") and failed_runs (among it and its core children; failed_runs_capped when that count stopped at 500 — a floor; the root read by its id); since/until (RFC 3339, on
//    //    its earliest span)/service (exact, any span)/public_id/id_prefix (obsdb.TraceQuery.IDPrefix, U30.2: the ⌘K palette's), paged as runs; a panel token lists
//    //    the traces every span of which is its public id's; capability "traces" — obsdb.DB.Traces,
//    //    the since window narrowed on SQLite's spans_start, migration 0019), traces/{id},
//    //    sessions (?q= — every term in the title, first turn's prompt, id, public id or agent; ?id_prefix= — the sessions whose id starts with it, byte for byte (obsdb.SessionQuery.IDPrefix, U30.2-fix); each row's title
//    //    (weft.session.title, the newest turn's), first_prompt and content_off (the first turn stored no content);
//    //    obsdb.SessionQuery.Q; a session is its own turns — <session>-t<turn> or a run linked to no run, D3 —
//    //    so a thread/pool child session is listed, with its lineage parent_session/parent_turn/parent_call/parent_run,
//    //    obsdb.SessionQuery.Parents listing the sessions any of them delegated, one tree level per read;
//    //    tag.<k>=<v> — obsdb.SessionQuery.Meta: the sessions one of whose own turns carries every pair, U12.2; U16: each row's turn facts
//    //    (obsdb.SessionTurnFacts, both backends) pending (the newest turn an open park: the list's "⏸ awaiting a decision" and the header's
//    //    decide…), failed_turns, ended_turns (thread's busy policy: never failures), cancelled_turns (another cancel: never failures, U16-fix),
//    //    resumes, outcomes (the newest 40, the strip) and forked_from_session; ?awaiting=1 / failed=1 / compacted=1 / status= (the newest
//    //    turn's outcome as the session page words it, the four — a cancelled or busy-ended turn is not failed, a parked one not succeeded) / lineage=child|fork / since= — obsdb.SessionQuery Awaiting/Failed/Compacted/Status/Lineage/Since, a bad
//    //    value a 400; the UI page /conversations/$id is a public id's runs, experiments nested, live — lib/links.ts conversationLink),
//    //    sessions/{id} (+ delegated: its delegated sessions,
//    //    each with its cost — a session priced up to 10,000 runs, a level's reads bounded at 20 pages per 50 sessions and 20 more, the
//    //    sessions past either unknown with a reason; delegated_usage and delegated_sessions_cost_* the whole delegated tree's, beside its own, U14; delegated_holes:
//    //    one hidden hole counting the delegated sessions, at any depth, a panel token is not shown (under a hidden one too; count_floor past 500), the delegated cost then unknown with a reason;
//    //    a panel token reads an unknown session id as it reads another public id's, 403 — sessions/{id}, its timeline, entries, export;
//    //    a session of session-level marks alone answers sessions/{id} as its timeline, 200; the row status (and the document's) the newest turn's outcome — agreeing with ?status= for the four run words (succeeded, failed, running, interrupted); cancelled, ended, parked, resumed not filterable (a 400; awaiting=1 is the open park); before 0.18.0 failed for a cancelled or busy-ended turn, succeeded for a parked one: read the outcome word instead),
//    //    sessions/{id}/timeline (U12.2, capability "timeline" — obsdb.SessionEventReader over the session_events index,
//    //    SQLite 0022 / ClickHouse 0025, every thread record and compaction marker by session, a record filed under no run
//    //    included: the turns in order, the marks in record-time order — kind, lane approvals|grants|compaction|steer_queue|
//    //    branch_fork|pool(|other), run_id/turn (null: session-level), entry, fields, content (a read token: content_hidden), actor on a
//    //    decision (the weft.approval.actor of the turn that resumed it, never the parked run's own; one call's of several by key_id, else a content reader's who, else the list + actor_of resume: metadata, every token reads it) —
//    //    ≤ 2,000, the lanes' counts, holes not_recorded (only when a turn parked or resumed: a plain send writes no
//    //    record)/stripped/hidden (a panel token's counts exact, both routes alike)/truncated cause response_cap; summarizer on a turn and the
//    //    session, U15.1: thread's summarizer calls priced at read time, never in usage — the page /sessions/$id draws it as its
//    //    default view: exchanges on a time axis, a drawer per mark ?mark=, ?view=table|tree; a turn's run page steps { }; U15.2: forks — each
//    //    fork mark's forked session, its stored turns and status (0 turns: it ran none, no page yet; hidden to a panel token) — the lanes
//    //    filter ?lanes= (dims, never hides), ?view=tree the lines the turns continue (weft.turn.parent: abandoned turns off the path, forks
//    //    on the origin), a context lane per turn, interrupt / rollback ends worded as such, the page live on /api/live?session=; U15.2-fix:
//    //    ?view=approvals every decision (call, tool, outcome, who / via / key, grant, parked → resumed turn, refused signatures) and grant
//    //    (rule, scope, uses n of max with their calls, revoked / expired); the forks read in one obsdb.SessionQuery.IDs query, both backends) and sessions/{id}/entries?offset=&limit=&run=
//    //    (the same marks paged, ≤ 1,000, next_offset; run= one run's marks alone, its holes and hidden count too, obsdb.SessionEventQuery.RunIDs — a parked run's request expiry in one read; sessions/{id}'s scope, 501 without the capability),
//    //    sessions/{id}/export?format=jsonl (UX-B-29: the conversation as JSON lines, streamed — a session line
//    //    {format, session_id, public_id, agent, turns, marks}, every turn's run jsonl export in turn order (≤ 200),
//    //    the state log's session_mark lines (≤ 2,000), session_hole lines (truncated cause response_cap past a cap —
//    //    64 MiB of every line before the holes, the session line included, sessionExportByteCap — or a plain truncated
//    //    when a read fails mid-stream; the timeline's holes when the turns ran to the end, its hidden and not_recorded alone — the scope read's — when they stopped), a session_end line {turns_exported, marks_exported, bytes} last; the turns'
//    //    children in one ChildRuns read; sessions/{id}'s scope, a read token's requests hidden and marks
//    //    content_hidden; the session page's export / copy public id / open in app / delete verbs),
//    //    sessions/{id}/public_id (the reverse of public/{public_id}: the dev token's
//    //    alone, every panel token 403 hidden), public/{public_id}, DELETE runs/{id} and DELETE
//    //    sessions/{id} (capability "delete": a run with its subagent children, or a session's runs —
//    //    rows, records, search index, spans, app logs — from obsdb's tables only, the app's
//    //    conversation store and a shared file's thread tables untouched; the dev token or setup A's
//    //    loopback Host, every panel token 403; obsdb.DB.DeleteRun/DeleteSession/Sweep/Vacuum; stored manifests stay), /api/live (SSE; kinds event|delta|messages|run|attempt — attempt (H9, O17) a live-only frame from each `attempt` span as it lands {run_id, step, attempt, layer, status, error_type, wait_ms (weft.attempt.wait_ms, the wait slept before this attempt was sent — the step route's), retry_after_ms (the provider's ask)}, never backfilled; ?all=1 every run, dev token/loopback only — meta live_all; its resume
//    //    backfills the last hour / running runs, ≤ 500 — obsdb.RunQuery.LastSeenAfter), /api/panel-tokens (with a Token), /api/panel-handoff?to= (setup C hand-off, capability handoff, with a Token only:
//    //    a playground panel token → 303 <base><to>#token= with a 5-minute read token for its public id (capped at its own exp;
//    //    Accept: application/json → {location, exp}); the server token → 303 with no fragment; a read token 403, it cannot widen;
//    //    `to` a path under the base, else 400),
//    //    /panel.js (the devtools panel, WEFT-DEVTOOLS §5 — its host API, plan C4: the element's
//    //    open/close/toggle/isOpen/scope/select/on/studioLink, window.weft.devtools while connected
//    //    unless data-global="off", events weft:run|parked|error (a parked app turn is decided in the panel
//    //    and on the run page — continue/skip/resolve, 202 + the command polled, a refusal verbatim; steer while
//    //    it runs — while a connected runtime of the run's service registered runtime.Sessions and it is
//    //    its session's latest turn; else its verbs read refused in the server's words, "app-owned turn ·
//    //    read-only: …" + the one-line fix, plan F4.3); its layout, plan D1: float, dock
//    //    left|right|bottom|top, pill, hidden — data-position/data-mode/data-push (pads <html>)/
//    //    data-z-index (--weft-z), remembered in localStorage["weft.devtools"], every key but Alt+W
//    //    heard inside the panel only; its theme, plan D2: data-theme light|dark > the stored ◐ choice >
//    //    auto (<html data-theme|class="dark">, prefers-color-scheme, else dark), the Studio palette
//    //    (src/lib/palette.ts) as --weft-* tokens on :host a host rule overrides; its renderer, plan D3:
//    //    a keyed patch (run id, step ordinal — only the streaming card changes; focus/caret kept, never
//    //    restored), ARIA roles + roving tabindex + a trap only in a focused float, axe zero-violation
//    //    budget in a11y.test.ts (axe-core dev-only); its views, plan D4: tabs Story/Request (plan E1.2,
//    //    src/panel/request.ts: the run page's Request pane per step — J/K pick it — chips, prompt diff,
//    //    messages, catalog, params; a read token sees hidden only; plan F1: the Story's replay verbs —
//    //    replay from this step, edit this result, edit the prompt, re-run, continue here — open the
//    //    experiment drawer pre-filled from lib/replay.ts with its ack preview, gated by canReplay;
//    //    plan F2: the Story edits in place — prompt, steer, args (JSON checked against the step's catalog
//    //    schema, lib/edits.ts), result, reply, an insert at a boundary — into that drawer's one command, its
//    //    "will be sent" preview (lib/preview.ts, capability preview); a replayed run's weft.edits as chips)/Timeline (U8.3: the run page's run tree rows one level deep — lib/waterfall.ts's keys, labels, ROW_KIND/KIND_ICON, rowTone in lib/palette.ts's TONE; a child run one row, ⤢ to ?sel=, labelled by its agent (waterfallRows' agents, else its tool); a step's rows are buttons on one tab stop — click/Enter selects it; time axis with spans, seq without; U10.2: the Story's selected step — else the run page's landing step (the one it failed at, landStep's last drawn for an unstarted one, else step 0) — answer first, lib/summarize.ts's verdict · sent · replied · tools ran · then as Studio's drawer, Logs via the head's 📄 ⤢ (logsLink; no token or the dev token only), Raw OTel ⤢ to ?sel=s<n>&tab=raw; the request one line until opened; subagents inline at any depth)/Raw (src/panel/tree.ts: a lazy JSON
//    //    tree, "/" filter, copy-node/all, download), a turn-list filter, runs paged by before=/before_id=
//    //    with no cap, the tab stored as `tab`; its honesty, plan D5: every badge drawn by
//    //    src/panel/badges.ts from the A3 table (data-hole, reason — fix: as title; badge(hole, note) for
//    //    the Request tab), the footer's content line, turn chips from the run row (model
//    //    weft/runtime/scripted, weft.session.forked_from, weft.forked_from), parity.test.ts against the
//    //    run page; its cost, plan A5.3, capability "pricing" only: per turn (own, "+ $y delegated", "—" with
//    //    cost_reason), the header's sum ("≥" when one is unknown or older turns are unloaded), the result pane's run and "N replays of this turn", lib/cost.ts's
//    //    words; the build prints the per-item size table from
//    //    studio/web/panel-budget.json; configured by a data-weft
//    //    script tag, weft:* meta tags, or <base>/panel-config.json; studio/README.md
//    //    has the ladder; it follows a scope — data-scope="pub_…;session=…;flow=…;run=…" or
//    //    window.__WEFT__ (data-public-id deprecated), else the Weft-Scope header of the page's
//    //    same-origin fetches: a read-only window.fetch wrapper, on by default only on loopback with
//    //    no or a dev token, restored on disconnect, or a data-weft-scope DOM marker (the helpers set it;
//    //    attributes read through one MutationObserver, the marker holding focus wins, a header switcher
//    //    when several conversations are known; off only under a read panel token off loopback),
//    //    data-detect="headers"|"markers"|"headers,markers"|"off" (off: those two rungs only), or the page
//    //    URL's ?weft_scope= / #weft_scope= (below data-scope, above markers and headers; never gated,
//    //    never written); with none, "no conversation detected on this page · how to scope" over the latest
//    //    runs, streamed on agent=; the collapsed pill pulses "● step" while a run runs; the same bytes on npm
//    //    as @weftgo/devtools — import "@weftgo/devtools", mount (returns the element, or null under
//    //    enabled:false / on a server; idempotent; options endpoint/scope/token/detect/position/mode/push/
//    //    zIndex/open/auto/theme/target/enabled)/scope/open/close/toggle/isOpen/select/studioLink/on,
//    //    serializeScope/parseScope, /react /vue /svelte marker helpers (enabled too); import-safe on a
//    //    server; studio/web/npm, make devtools-npm, never published by the build), /panel-config.json
//    //    ({endpoint, version, capabilities}; loopback Host / same-origin only, else
//    //    404; capability "panel-config"); Token(tok) walls the
//    //    /api tree (the Authorization bearer only: ?token= is refused on every /api route, in every setup;
//    //    EventSource opens /api/live with POST /api/live-grant's sig: ?<selector>&kinds=…&sig= — a 60 s
//    //    window, reusable (a reconnect reuses it), each stream running until closed; a panel
//    //    token's stream ends at its expiry with `event: expired`) — the UI shell and /panel.js are static,
//    //    without a Token (setup A) the loopback Host is the wall and an unsafe method (not GET/HEAD/OPTIONS)
//    //    whose Origin's host:port is not the request's Host and not an AllowOrigins entry, or with no Origin but
//    //    Sec-Fetch-Site: cross-site, is 403 "cross-origin write refused (setup A)";
//    //    OTLP ingest (/v1/traces, /v1/logs) carries its own IngestToken. Every response
//    //    carries X-Request-Id (a 5xx body error.request_id too; exposed to CORS); slog.Default gets a WARN per
//    //    refused ingest batch (rate-limited per remote + route class /v1/traces|/v1/logs|/v1/*) and an ERROR per
//    //    5xx (the real error even when a panel token's body masks it) or handler panic, each naming it (U11.2);
//    //    weftbase dev --log-level (WEFT_LOG_LEVEL) --log-format text|json (WEFT_LOG_FORMAT) pick its handler.
//    // Setup B, any language (package cmd/weftbase — `go install github.com/weftbase/weftbase/cmd/weftbase@latest`;
//    // the one place the clickhouse driver lives): weftbase dev --db sqlite://path |
//    //    clickhouse://user:pass@host:9000/db [--addr --token --manifest weft.json (or
//    //    WEFT_MANIFEST; else the nearest weft.json upward) --open --no-playground] serves UI +
//    //    OTLP ingest + the playground on 127.0.0.1:7331. The dev token is stable per DB
//    //    (<db>.token beside the file, 0600, never printed; --rotate-token renews it,
//    //    --token / WEFTBASE_TOKEN override; a fileless DB gets a printed per-process
//    //    one); studio.json {url, token, db, pid, started, version} (0600, removed on exit;
//    //    none written under WEFTBASE_DISCOVERY=off, said in one start line) is the discovery file apps find it through (block 6). The API over a terminal (one resolver for runs/open/export/doctor/mcp: --url >
//    //    WEFTBASE_URL > the discovery file > 127.0.0.1:7331, a stderr line naming it when unnamed; --token >
//    //    WEFTBASE_TOKEN > the file's when it names that Studio): weftbase doctor checks one (+ discovery, data version, otlp); weftbase runs
//    //    [--agent --since --failed --status running|succeeded|failed|interrupted|parked|cancelled --limit --json] (DUR, COST, PUBLIC ID, PROMPT, STOPPED BY and ERROR columns — every cell's control characters as spaces; --json carries prompt_preview/stopped_by/cancel_cause); weftbase open [run|public|session|trace id] [--open --with-token] (no id: the Studio root)
//    //    (prints the bare <url>/runs/<id>; the token only to the browser or on --with-token);
//    //    weftbase export <run id> [--format json|jsonl|otlp | --wefttest ./pkg [--fixtures-only --test Name]
//    //    --force] (Studio's save-as-test tree: <agent>_replay_test.go + testdata/TestReplay_<run>/, a replay's
//    //    composed with its source's kept steps; --fixtures-only the fixtures where wefttest.Replay reads them;
//    //    --force replaces, never merges); weftbase mcp [--url --token] (a stdio MCP server over these read routes, read verbs
//    //    only — runs/search/run/step/request/events/tools/diff/export/sessions/session/meta +
//    //    logs/spans/trace/transcript/live/agents/experiments/runtimes (I5) + stats (/api/stats, compact groups)
//    //    and error_class on runs/search (U33.1) +
//    //    explain_failure (run + every step + the children + transcript composed: first_error, a digest,
//    //    decisions — a decided park "denied by <who>", this run's own last text); every tool a GET, outputs the route's
//    //    JSON cut at max_bytes with a note — the list tools' compact rows (empty fields out, hashes 12 chars,
//    //    cost_reasons once; full=true as served), spans/trace compact the same way; no --url: the discovery file; it serves before a
//    //    Studio answers (one WARN; a call isErrors "no Studio answers /api/meta" until one does)
//    //    and, after a transport failure, re-resolves per call (no --url: WEFTBASE_URL, then
//    //    the discovery file; --url retried as given); docs/mcp-recipe.md;
//    //    make mcp-check — build tag livecheck, not in make test — proves it over stdio against a real
//    //    weftbase dev seeded with the contract fixtures: the phase 6 gate's third clause);
//    //    weftbase vacuum [--db --retain] (one sweep when a retention is given, then VACUUM
//    //    / OPTIMIZE TABLE … FINAL; stored manifests are never swept; `make vacuum` on $WEFTBASE_DB or
//    //    ./.weftbase/weftbase.db); weft version.
//    //    --retain (WEFT_RETAIN) on weftbase dev is studio.Retain (under 1h: exit 2);
//    //    --pricing <path> (WEFT_PRICING; the flag wins) is studio.Pricing(studio.LoadPricing(path))
//    //    (a malformed file: exit 1 naming the path and the field).
//    //    weftbase dev [dev's flags --no-watch --watch dir] -- go run ./cmd/app (no "--": Studio alone):
//    //    Studio in-process + the app with WEFT_ENV=dev (kept if set), WEFTBASE_URL,
//    //    WEFTBASE_TOKEN, WEFTBASE_DB set (plain env vars the app may set itself), restarted
//    //    on a .go save (300 ms debounce; its process group stopped: SIGTERM, 5 s, SIGKILL);
//    //    one line per start: studio <url> (bare; #token= only for a fileless DB's generated
//    //    token) · app pid n · runtime rt_… registered.
//    //    One Studio per SQLite file: <db>.lock (internal/dblock, flock/LockFileEx, held while serving,
//    //    released on process death; the symlink-resolved file; the holder named only from this user's 0600 file
//    //    with a loopback-IP url and a live pid) taken before the port policy, weftbase dev's too; ClickHouse takes none.
//    //    Port policy (internal/listen): a Studio on the same DB (lock held, busy 7331, or
//    //    the discovery file's) — a weftbase dev is refused ("studio already running at … (pid n) on <db>", exit 1; weftbase dev -- cmd reuses it),
//    //    the discovery file's Studio probed first whatever its port (listen.Request.Known); anything else → the
//    //    next free port in 7331–7340, said in one line; --addr / WEFTBASE_ADDR pins
//    //    (busy → exit 1 naming it). /api/meta's pid follows db.path's guard.

// 7b. The scope header (package weftbase/scope; plan C3): the app's own chat handler tells the
//     devtools panel which conversation and run a response belongs to, in one line —
//     mux.Handle("POST /chat", scope.Header(chat, func(r *http.Request) scope.Scope { return scope.Scope{PublicID: pub} }))
//     // Weft-Scope: pub_…;session=…;flow=…;run=… (scope.Scope.String / scope.Parse, the web
//     // lib/scope.ts form, both pinned by studio/testdata/scope.golden.json); scope.Set(w, s) once
//     // the run id is known (before the first write); Access-Control-Expose-Headers gains
//     // Weft-Scope; never a token. Not in the root package: that one is core's generated facade.
//     // scope.Header also stamps the request's page — Referer, else Origin + "/", sanitised (http/https, no
//     // userinfo/query/fragment, ≤ 1024 bytes) — as weft.page_url on every run under the handler, via
//     // weft.WithMetadata on r.Context() (Generate and thread's Send alike; scope.PageURLFromContext, scope.PageURL,
//     // scope.PageURLKey); Studio's run meta serves it for "open in app".

// 7a. The playground (package weftbase/runtime): your app dials Studio out and
//     executes experiment commands as runs of the agents you register:
//     defer runtime.Install(runtime.Studio(url, tok) /* or runtime.Local(srv) */,
//         runtime.Agents(support), runtime.Models(map[string]weft.Model{"glm": m}),
//         runtime.Limits(runtime.Budget{MaxTokensPerExperiment: 200_000}),
//         runtime.AllowSideEffects("send_email"), runtime.Threads(store),
//         runtime.ModelResolver(resolve))() // a model outside Models: the app's func(ctx, name) decides, before the ack
//     runtime.Sessions(resolve) // opt-in (plan F4): func(ctx, publicID) (*thread.Session, release, error) — the app's own turns take approvals and steer (cancel is refused) through the app's Session; the lease is claimed before the ack (ErrLocked → "the app is writing this session; fork instead"); registration sessions:true, meta.debug_scope own+runtime; without it the three verbs are 403 "app-owned turn" + fix
//     // WEFT_ENV=dev (or runtime.Enabled(true)) opens the link; with no Studio option
//     // and no otel Studio destination it dials the discovery file's Studio (block 6's
//     // rule: WEFTBASE_URL wins, WEFTBASE_DISCOVERY=off, stale ignored); commands ack
//     // before they run (at-most-once: validation, source fetch and resolver included,
//     // is bounded at 25 s inside Studio's 30 s ack window; an accepted ack Studio did not
//     // take runs nothing — a 409/404/400 (409 once it marked the command lost) posts nothing,
//     // a transport error or 5xx is settled finished/failed "not run"; the accepted ack is
//     // re-posted every minute while the command is held — heartbeat_ms at registration, the
//     // beat stopped before the finished ack and on a refused beat — and Studio marks a
//     // heartbeating runtime's silent accepted command lost after max(10 min, 3 beats), stream
//     // open or not, the beat taken at most 10 min), a never-class tool's call is substituted
//     // with its recorded result or parked (weft.Replay(weft.ReplaySafe) vouches a
//     // read: it runs in every mode); side_effects substitute (default) | park |
//     // allow — only allow runs the AllowSideEffects tools for real, and is refused
//     // unless every tool left on is opted in or ReplaySafe; budgets cap each
//     // experiment; overrides: instructions, tools_enabled, model, thinking, options
//     // {max_steps, parallelism, temperature}, params {top_p, max_tokens, stop, seed},
//     // tool_choice {mode, name}, park_on, only_tools — narrowing or neutral, each refusal
//     // naming its rule; runtime ids are stable per (service, cwd, agents) — rt_ + 16 hex of
//     // sha256 — and `<id>-<pid>` when another process holds the id (a live stream, or queued/accepted
//     // work — a finished command's run route never holds it; register's runtime_id, adopted by the link; a client must send
//     // `instance`); commands, routes, breakpoints and registrations persist through obsdb.CommandStore
//     // (plan I3: studio.New passes its DB; one ordered writer off the request path; a failure holds
//     // its queue 30 s, then re-applies it), a restarted Studio deciding lost from the stored state (queued past its ack window or
//     // without a stored runtime, accepted past its watch from the stored last beat) and never
//     // re-sending an accepted command; /api/runtimes gives each runtime breakpoints_set_at and breakpoints_surface (the PUT body's optional surface, else "api"; both kept in the command store, U30.2) and connected (a live stream now; a dropped
//     // one stays listed, false, until pruned; cwd under db.path's rule; restored after a Studio
//     // restart until it re-registers) and each agent resolver + defaults (the form greys
//     // them; absent for a runtime older than the option lab — Studio then refuses the option lab's
//     // knobs, the user/tool_args/insert edit kinds and a replay across a compaction view; a read
//     // token gets no instructions, default tool_choice name or stop); runs carry weft.playground and never touch weft.session.id
//     // (ephemeral). Engines live | scripted (the source
//     // run's recorded turns, zero tokens); thread ephemeral | fork (a new session
//     // with lineage, the panel keeps chatting in it). Breakpoints park the runs this
//     // runtime starts and, opted in at New — weft.New(m, …, runtime.Breakpoints()), a
//     // core.ParkSource bound lazily to the live link (ADR 0030; inert with no link;
//     // process-wide; skips weft.playground runs, whose fingerprint decides) —
//     // the app's own runs (registration breakpoints_own; a plain Generate parked so is
//     // the app's to resume, Studio decides session turns only, through runtime.Sessions;
//     // a thread turn parks only when this process set runtime.Sessions and the session
//     // has a public id (thread.PublicID) — without both the source answers nothing for
//     // weft.session.id runs, a park nobody could decide would hold the session; a Subagent child parks itself, its parent sees SUBAGENT_PENDING);
//     // meta debug_scope reads own+runtime while a connected runtime registered Sessions or
//     // breakpoints_own. Steer acts on the runtime's runs (D7) and, with Sessions, the
//     // app's turns. Studio side:
//     // studio.New(..., studio.Playground(true)): /api/playground/runs (the §5.1
//     // command, transcript_edits validated on both sides — kind tool_result | reply |
//     // user | tool_args (schema-checked, obsdb.CheckToolArgs) | insert, absent = the
//     // pre-F2 shape, ADR 0029 §8; the replay carries weft.edits, <step>:<call>:args the
//     // "args edited" mark; a replay from step N > 0 carries weft.replay.view = transcript |
//     // compacted:<index> | derived | unknown (ADR 0029 §2), the last two the run's derived
//     // hole; scripted + edits refused), /api/playground/preview (the
//     // same body → the replay's exact first request beside the recorded step's and
//     // their diff, pure assembly, no runtime needed; capability preview), /api/playground/
//     // commands/{id}, /api/runs/{id}/approvals (a parked run's own verbs),
//     // /api/playground/fixtures (wefttest replay fixtures from a run's records),
//     // /api/experiments (the saved groups + the runs they label — the 50 most recently updated by default, ?limit= up to 200, with `total`; each saved with its whole command: engine,
//     // side_effects, thread, from_step, source_run_id, transcript_edits, per variant overrides + modes; listed with created,
//     // run_count, succeeded, last_run and cost; /api/runs and /api/search filter by experiment_id=; any experiment_id on a run command is
//     // accepted, so a re-run joins the saved experiment), /api/runtimes/
//     // {id}/breakpoints, /api/runs/{id}/steer, /api/runs/{id}/cancel (capability cancel: the link's cancel frame for the command
//     // holding a runtime-started run; 202 cancelling, 409 finished/parked — decide instead, 503 runtime gone; an app-owned turn 403 "app-owned turn" (202 with a runtime.Sessions runtime, whose command row then reads rejected: no per-turn cancel); best effort; steer's auth); the panel drawer and /playground
//     // (the Studio UI) render them, gated on capabilities — the option lab (plan F3.2)
//     // one form on both: every override above, defaults greyed, an override in colour
//     // with a reset, lib/experiment-body.ts's labOverrides (the server's rules by name,
//     // a refusal holds Run, the 400 still shown verbatim; one body, parity-tested).

```
