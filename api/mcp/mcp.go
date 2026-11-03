// Package mcp is weftbase's MCP tool set (plan I4.1): a read-only MCP
// server over the server's read API, so a coding agent (Claude Code,
// Cursor) reads runs while you debug. `weftbase mcp` hosts it over
// stdio (Serve); the tools read through client/rest.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/weftbase/weftbase/client/rest"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/weftbase/weft/version"
)

// The tool set is a third client of the same routes `weftbase
// runs`/`open`/`export` call — it adds no route and computes nothing
// the server does not serve: each tool is one GET (explain_failure
// composes the run, its steps, its children and the transcript,
// explain.go), its output the route's JSON — projected to compact rows
// on the list tools and to compact spans on spans/trace (compact.go;
// full=true as served) — bounded by max_bytes with a visible note.
//
// Read only, by construction: every tool goes through mcpGet, which
// issues GET and nothing else, and no write verb (approve, deny, steer,
// cancel, delete, …) is registered. Write verbs stay out until the
// approval story for agents exists (PQ5's read half).

// mcpDefaultBytes is a tool's output bound when the call names none —
// weft's own tool-result default; mcpMaxBytes the most a call may ask.
const (
	mcpDefaultBytes = 64 << 10
	mcpMaxBytes     = 4 << 20
)

// mcpInstructions is what an MCP client reads about this server as a
// whole.
const mcpInstructions = `weftbase mcp is READ-ONLY: it reads a weftbase server's recorded agent runs ` +
	`(the weftbase JSON API, GET only); it never approves, denies, steers, cancels, deletes, ` +
	`replays or starts anything. Start with explain_failure <run id> for "why did run X fail"; ` +
	`find runs with runs (newest first, filters) or search (text and filters; its error_class facet groups the failures); ` +
	`measure with stats (runs, failures, tokens, cost, latency by agent, model, tool, error type or step); ` +
	`drill in with run (the row), step (one step assembled), request (the exact model input), ` +
	`events, tools, transcript, diff (two runs step by step), export (the whole run); ` +
	`investigate with logs (the app's log lines), spans (timing; tree=true with the child runs), trace (by trace id), ` +
	`live (running now), agents (manifests), experiments, runtimes. ` +
	`Outputs are the route's JSON; list tools (runs, search, sessions, session, live, stats) answer compact rows — ` +
	`empty, zero, false and null fields omitted, hashes cut to 12 characters, cost reasons said once in cost_reasons — ` +
	`full=true gives the route's JSON as served. A cut output says so in a final note (raise max_bytes or narrow the query). ` +
	`Badges and holes ("not_recorded", "stripped", "hidden", "gap", ...) mean the data is absent for that reason — never empty. ` +
	`A 403 means this token may not read that.`

// mcpTool is one registered tool: its name and the paths it reads,
// for the usage text and the tests.
type mcpTool struct {
	name, routes, use string
}

// mcpTools is the tool table, in the order --help and the docs list it.
var mcpTools = []mcpTool{
	{"explain_failure", "run + steps + children + transcript", "why a run failed (or how it ended): status, error, the first error anywhere in the run (its steps, its child runs), every step and child in a digest, decisions, holes, this run's last assistant text"},
	{"runs", "GET /api/runs", "list runs, newest first (agent, status, session, public_id, error_class, all, parent, limit, before cursor)"},
	{"search", "GET /api/search", "find runs by text (prompt, answer, error, tool names) and filters (since/until, model, min_tokens, min_cost, error_class, sort); the error_class facet"},
	{"stats", "GET /api/stats", "numbers across runs grouped by agent, model, tool, error_type or step: runs, failures, tokens, cost, latency, retries, denials (since, agent, tag, public_id)"},
	{"run", "GET /api/runs/{id}", "one run's row: status, error, steps, usage, cost, children, holes"},
	{"step", "GET /api/runs/{id}/steps/{n}", "one step assembled: request, attempts, events, tool calls and results, children, holes"},
	{"request", "GET /api/runs/{id}/requests", "the exact model input per step (prompt, catalog, params); transcript=true adds the step's messages"},
	{"events", "GET /api/runs/{id}/events", "the run's durable event stream, paged (after, limit)"},
	{"tools", "GET /api/runs/{id}/tools", "the tool catalogs the run offered the model"},
	{"diff", "GET /api/diff", "two runs compared step by step (what changed, what is not comparable)"},
	{"export", "GET /api/runs/{id}/export", "the whole run as one document: json, jsonl or otlp"},
	{"sessions", "GET /api/sessions", "list thread sessions"},
	{"session", "GET /api/sessions/{id}", "one session with its turns"},
	{"logs", "GET /api/runs/{id}/logs", "the app's own log lines under a run's spans (severity, from, limit)"},
	{"spans", "GET /api/runs/{id}/spans", "a run's spans, compact (timing, status, model, tokens, tool); tree=true adds every descendant run's"},
	{"trace", "GET /api/traces/{id}", "one trace's spans by trace id, weft or stock OTel, compact"},
	{"transcript", "GET /api/runs/{id}/transcript", "a run's messages, input and its own; step=N adds what step N's model call carried"},
	{"live", "GET /api/runs?status=running", "the runs running now, compact rows (a snapshot, not the stream)"},
	{"agents", "GET /api/manifest", "the agents weftbase knows: manifests (instructions, tools, options) and their sources"},
	{"meta", "GET /api/meta", "what this server is and can do: versions, capabilities, retention"},
}

// ToolNames are the registered tools' names, in the order --help lists
// them.
func ToolNames() []string {
	out := make([]string, 0, len(mcpTools))
	for _, t := range mcpTools {
		out = append(out, t.name)
	}
	return out
}

// ToolTable is the tool list as `weftbase mcp --help` prints it.
func ToolTable() string {
	var b strings.Builder
	for _, t := range mcpTools {
		fmt.Fprintf(&b, "  %-16s %s\n  %-16s   (%s)\n", t.name, t.use, "", t.routes)
	}
	return b.String()
}

// Options configures New and Serve.
type Options struct {
	// Client is the server the tools read. New walls its transport to
	// GET and HEAD: every request any tool makes goes through it.
	Client *rest.Client
	// Resolve, when set, names the server again (base URL and token)
	// after a call that got no answer: a server started later, or on
	// another port, is found without a restart. nil retries Client as
	// given.
	Resolve func() (base, token string)
	// RereadToken makes a 401 look again too (with Resolve): the
	// token came from a file the server rewrites when it restarts.
	RereadToken bool
	// TokenFrom says where the token came from, for the start line
	// (never the token): "from --token", "none", ….
	TokenFrom string
	// Stderr takes the start line, the WARN when nothing answers yet,
	// and the line saying the server moved; nil discards them.
	Stderr io.Writer
}

// New checks that the server answers /api/meta and builds the MCP
// server. Nothing answering is a WARN on Stderr, not an error: the
// server serves, and each tool call looks again (Resolve) and checks
// again. A server that answers and refuses (a 401, a 404 from
// something that is not a weftbase server) is the error.
func New(ctx context.Context, o Options) (*sdk.Server, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	c := o.Client
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	down := false
	if _, err := c.Get(ctx, "/api/meta", nil); err != nil {
		var se *rest.StatusError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("mcp: no server answers /api/meta: %w", err) // main prefixes "weftbase: "
		}
		down = true
		log.Warn("weftbase mcp: no server answers /api/meta yet; tools will retry per call", "err", err.Error())
	}
	from := o.TokenFrom
	if from == "" {
		from = "none"
	}
	_, _ = fmt.Fprintf(stderr, "weftbase mcp: %d read-only tools over stdio for the weftbase server at %s (token %s)\n",
		len(mcpTools), c.Redacted(), from)
	link := newLink(c, o.Resolve, down, stderr)
	link.rereadToken = o.Resolve != nil && o.RereadToken
	return newMCPServerLink(link, log), nil
}

// Serve is New, then the MCP protocol over stdin and stdout until
// stdin closes. stdout is the protocol: every line of ours goes to
// Options.Stderr.
func Serve(ctx context.Context, o Options, stdin io.Reader, stdout io.Writer) error {
	srv, err := New(ctx, o)
	if err != nil {
		return err
	}
	t := &sdk.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}}
	if err := srv.Run(ctx, t); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("mcp: %w", err)
	}
	return nil
}

// serverLink is the server the tools read: the one walled client, and
// — when the last attempt failed with a transport error, or a 401
// while the token is the discovery file's — a fresh look before the
// next request: re-resolve (no --url), then GET /api/meta. Nothing
// answering is the call's error, the startup line with its fix; a
// Server found again is said on stderr once.
type serverLink struct {
	mu      sync.Mutex
	c       *rest.Client
	resolve func() (base, token string) // nil: --url given, retried as given
	down    bool                        // the next request looks again first
	// rereadToken: no --token or WEFTBASE_TOKEN with no --url — a
	// 401 means the file's token may have changed, so it looks again.
	rereadToken bool
	stderr      io.Writer
}

// hint is what a failure that makes the next call look again says.
func (l *serverLink) hint() string {
	if l.resolve != nil {
		return "the next call looks again (WEFTBASE_URL, the discovery file): no restart needed"
	}
	return "the next call retries the --url given"
}

// newLink walls c's transport to GET and HEAD — every client the
// link ever uses shares that http.Client.
func newLink(c *rest.Client, resolve func() (string, string), down bool, stderr io.Writer) *serverLink {
	c.HTTP().Transport = readOnlyTransport{next: c.HTTP().Transport}
	if stderr == nil {
		stderr = io.Discard
	}
	return &serverLink{c: c, resolve: resolve, down: down, stderr: stderr}
}

// client is the client the next request goes through: the current one,
// or — after a transport failure — the one a fresh look found
// answering /api/meta; an error when none answers.
func (l *serverLink) client(ctx context.Context) (*rest.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.down {
		return l.c, nil
	}
	c := l.c
	if l.resolve != nil {
		u, tok := l.resolve()
		c = l.c.With(u, tok) // the same walled http.Client: never a second one
	}
	if _, err := c.Get(ctx, "/api/meta", nil); err != nil {
		var se *rest.StatusError
		if !errors.As(err, &se) {
			return nil, fmt.Errorf("no server answers /api/meta: %w; %s", err, l.hint())
		}
	}
	if c.Base() != l.c.Base() {
		_, _ = fmt.Fprintf(l.stderr, "weftbase mcp: now reading the server at %s\n", c.Redacted())
	}
	l.c, l.down = c, false
	return c, nil
}

// failed marks the link down after a failure on c that a fresh look
// may cure — a transport error (a request that got no answer), or a
// 401 while the token is the discovery file's (rereadToken) — and
// reports whether it was one: the error then says the next call looks
// again.
func (l *serverLink) failed(c *rest.Client, err error) bool {
	if err == nil {
		return false
	}
	var se *rest.StatusError
	if errors.As(err, &se) && (se.Status != http.StatusUnauthorized || !l.rereadToken) {
		return false
	}
	l.mu.Lock()
	if l.c == c {
		l.down = true
	}
	l.mu.Unlock()
	return true
}

func (l *serverLink) current() *rest.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.c
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// readOnlyTransport is the wall under every tool: a request with any
// method but GET or HEAD never leaves the process — whatever a handler
// does, a write cannot reach server.
type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("weftbase mcp is read-only: %s refused", r.Method)
	}
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(r)
}

// getter is all a tool can do with server: GET a path (the status
// it answered, its body, and an error for a non-2xx answer or a failed
// request), and name the UI's pages. The tools never hold the client.
type getter interface {
	get(ctx context.Context, path string, q url.Values) (status int, body []byte, err error)
	runLink(id string) string
	redacted() string
}

// linkGetter is getter over the link's current client.
type linkGetter struct{ l *serverLink }

func (g linkGetter) get(ctx context.Context, path string, q url.Values) (int, []byte, error) {
	c, err := g.l.client(ctx)
	if err != nil {
		return 0, nil, err
	}
	b, err := c.Get(ctx, path, q)
	if err != nil {
		if g.l.failed(c, err) {
			err = fmt.Errorf("%w; %s", err, g.l.hint())
		}
		var se *rest.StatusError
		if errors.As(err, &se) {
			return se.Status, se.Body, err
		}
		return 0, nil, err
	}
	return http.StatusOK, b, nil
}

func (g linkGetter) runLink(id string) string { return rest.RunLink(g.l.current().Base(), id, "") }
func (g linkGetter) redacted() string         { return g.l.current().Redacted() }

// newMCPServer builds the server and registers the read tools over c,
// its transport walled to GET and HEAD first, c answering or not.
func newMCPServer(c *rest.Client, log *slog.Logger) *sdk.Server {
	return newMCPServerLink(newLink(c, nil, false, nil), log)
}

// newMCPServerLink builds the server and registers the read tools over
// the link.
func newMCPServerLink(link *serverLink, log *slog.Logger) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{
		Name:        "weft",
		Title:       "weftbase server (read-only)",
		Description: "Read a weftbase server's recorded agent runs: GET routes only.",
		Version:     version.Runtime(),
	}, &sdk.ServerOptions{
		Instructions: mcpInstructions,
		Logger:       log,
		// No logging capability (the SDK's historical default); the
		// read-only promise as an experimental capability a client
		// can check.
		Capabilities: &sdk.ServerCapabilities{
			Experimental: map[string]any{"weft": map[string]any{"read_only": true, "methods": []string{"GET"}}},
		},
	})
	m := &mcpReader{s: linkGetter{link}}
	addTool(srv, "explain_failure", `Why did run X fail (or misbehave)? Start here. A convenience over run + steps + transcript (no new server logic): `+
		`fetches the run row, walks every step and the run's subagent children, and answers {status, error, stop_reason, step `+
		`(the step in flight when the run failed, else its last), first_error (the first error anywhere — a step's tool call, a model `+
		`attempt, a child run's own step — naming the run and step), decisions (a parked run's calls as the resume decided them: `+
		`"denied by alice: …"; only an undecided park reads "awaiting a decision"), digest (one line per step and per child), `+
		`last_assistant_text (this run's own output only, never an earlier turn's), tool_calls, attempts, holes, url}. `+
		`Works on any run: a succeeded run explains how it ended. Then use step/request for more.`,
		m.explainFailure)
	addTool(srv, "runs", `List runs, newest first (GET /api/runs): one compact row per run with status, error, steps, usage, cost (full=true: the route's rows). `+
		`Top-level runs only unless all=true or parent=<run id>. Filter by agent, status (running|succeeded|failed|interrupted), `+
		`session, public_id, error_class (a class string from search's error_class facet). Page with before + before_id `+
		`from the answer's next_before/next_before_id. For text or a time window use search.`,
		m.runs)
	addTool(srv, "search", `Find runs by text and filters (GET /api/search): q terms are case-insensitive substrings of the prompt, `+
		`answer, error or a called tool's name, all required ("quoted phrase" = one term). Filters: agent, tool, status, model, `+
		`error, error_class (exact, a class from the facet), since/until (RFC 3339), min_tokens, min_cost (USD), `+
		`sort=started|duration|tokens|cost, limit, offset. The answer's facets.error_class groups the match's failures by their `+
		`normalised error text (numbers N, ids <id>), most runs first — the classes error_class takes (capability stats; see meta).`,
		m.search)
	addTool(srv, "stats", `Numbers across runs (GET /api/stats): by=agent|model|tool|error_type|step, one row per group with runs, failed, `+
		`failure_rate, calls, tokens, cost, duration_ms and ttft_ms {p50, p95, n}, retry_rate, denials (agent groups: delegated, what `+
		`their subagents spent; runs_query, the search query listing the group's runs). Window: since (RFC 3339 or 90m/24h/7d back, `+
		`default 24h), until. Filters: agent, tag {k: v}, public_id, parent (* default: every run; "" top-level), `+
		`status. Compact rows: each group's 24-bucket series left out (full=true: as served). 404 when the database `+
		`cannot aggregate (see meta).`,
		m.stats)
	addTool(srv, "run", `One run's row (GET /api/runs/{id}): status, err, steps, stop_reason, usage, cost, its subagent children, `+
		`its holes (why something is absent) and compactions. Use after runs/search to look at one run.`,
		m.run)
	addTool(srv, "step", `One step of a run assembled (GET /api/runs/{id}/steps/{n}, n from 0): status, stop reason, the model asked and `+
		`the one that answered, attempt 1's request, every attempt with its outcome, events, tool calls with args and results, `+
		`child runs, usage, cost, holes. The view for "what happened at step n".`,
		m.step)
	addTool(srv, "request", `The exact model input (GET /api/runs/{id}/requests): one row per model-call attempt with the system prompt, `+
		`tool catalog, params and the messages it carried by reference; step narrows to one step. transcript=true (with step) `+
		`also returns GET /api/runs/{id}/transcript?step=N: the messages step N's model call carried.`,
		m.request)
	addTool(srv, "events", `The run's durable event stream (GET /api/runs/{id}/events): run_start, step_start, tool_start/finish, `+
		`step_finish, run_finish, … in order. Paged: pass after=<next_after> from the previous answer until next_after is absent. `+
		`run_terminal (the route's done) says the run has ended, not that this is the last page.`,
		m.events)
	addTool(srv, "tools", `The tool catalogs the run offered the model, by hash (GET /api/runs/{id}/tools): names, descriptions, `+
		`input schemas.`,
		m.tools)
	addTool(srv, "diff", `Compare two runs step by step (GET /api/diff?a=&b=): per step ordinal what changed (system, tool_calls, `+
		`tool_results, text, usage) and what is not comparable (unknown), with a summary {changed_steps, first_changed}. `+
		`Use for "what differs between the good run and the bad one".`,
		m.diff)
	addTool(srv, "export", `The whole run as one document (GET /api/runs/{id}/export): format json (default), jsonl or otlp. `+
		`Large: usually cut by max_bytes — prefer run/step/request for questions.`,
		m.export)
	addTool(srv, "sessions", `List thread sessions (GET /api/sessions), newest first: agent and public_id filter, before + before_id page. Compact rows (full=true: as served).`,
		m.sessions)
	addTool(srv, "session", `One thread session (GET /api/sessions/{id}): its row and its turns (runs) in order, compact rows `+
		`(a turn's agent, session and public id omitted when the session's; its model, cost_badge, stop_reason and stopped_by omitted `+
		`when equal to turn_defaults; prompt_preview 80 runes; cost reasons once with their row ids; full=true: as served).`,
		m.session)
	addTool(srv, "logs", `The app's own log lines under a run's spans (GET /api/runs/{id}/logs): time order, severity at or above `+
		`(trace|debug|info|warn|error|fatal), paged with from=<next_from>. A run without the slog bridge answers a not_recorded hole naming otel.Slog().`,
		m.logs)
	addTool(srv, "spans", `A run's spans (GET /api/runs/{id}/spans), compact: name, start_ms / dur_ms from the first span, status, `+
		`model, tokens, tool, step, attempt and error attributes (content left out; full=true: as served). tree=true adds every `+
		`descendant run's spans (run_id on each), with the tree's holes. The view for "where did the time go", retries and stock-OTel runs.`,
		m.spans)
	addTool(srv, "trace", `One trace by id (GET /api/traces/{id}), weft or stock OTel spans, compact as spans is (full=true: as served). `+
		`A run's trace_id is on the run tool's row.`,
		m.trace)
	addTool(srv, "transcript", `A run's transcript (GET /api/runs/{id}/transcript): its message batches in order, each marked input `+
		`(what the run was given — a thread turn's earlier turns) or the run's own; step=N also returns what step N's model call carried.`,
		m.transcript)
	addTool(srv, "live", `The runs running now (GET /api/runs?status=running&all=1): a snapshot in compact rows, subagent children included; `+
		`call again to see it move (the /api/live stream is not an MCP answer).`,
		m.live)
	addTool(srv, "agents", `The agents this server knows (GET /api/manifest): each agent's manifest — instructions, tools, options — `+
		`and where it came from (weft.json). A 404 means no manifest is loaded.`,
		m.agents)
	addTool(srv, "meta", `What this server is and can do (GET /api/meta): versions, database kind, capabilities and capabilities_off `+
		`(why one is off), retention, pricing source. Call when a tool answers 404/403 to see whether the route exists.`,
		m.meta)
	return srv
}

// addTool registers one read tool: its input schema reflected from In
// (the jsonschema tags), no output schema (the output is the route's
// JSON as text), annotated read-only, idempotent and closed-world.
func addTool[In any](srv *sdk.Server, name, desc string, h sdk.ToolHandlerFor[In, any]) {
	sdk.AddTool(srv, &sdk.Tool{Name: name, Description: desc, Annotations: &sdk.ToolAnnotations{
		Title: name, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(bool),
	}}, h)
}

// ── inputs ─────────────────────────────────────────────────────────

// mcpBound is every tool's output bound.
type mcpBound struct {
	MaxBytes int `json:"max_bytes,omitempty" jsonschema:"bound on the output in bytes (default 65536, at most 4194304); a cut output ends with a note saying so"`
}

type mcpMetaIn struct{ mcpBound }

// mcpFull is a list tool's opt-out of compact rows (mcp_compact.go).
type mcpFull struct {
	Full bool `json:"full,omitempty" jsonschema:"the route's JSON as served: every field, hash and cost reason (default: compact rows)"`
}

type mcpRunIn struct {
	ID string `json:"id" jsonschema:"the run id (a subagent child's id carries slashes, e.g. parent/0/call)"`
	mcpBound
}

type mcpRunsIn struct {
	Agent      string `json:"agent,omitempty" jsonschema:"only this agent's runs"`
	Status     string `json:"status,omitempty" jsonschema:"running, succeeded, failed, interrupted, parked (an open park: pending calls, no resume) or cancelled (a failed run whose context ended); any other word is a 400"`
	Session    string `json:"session,omitempty" jsonschema:"only this thread session's runs"`
	PublicID   string `json:"public_id,omitempty" jsonschema:"only this public id's runs"`
	Parent     string `json:"parent,omitempty" jsonschema:"this run's subagent children instead of top-level runs"`
	All        bool   `json:"all,omitempty" jsonschema:"every run, subagent children included"`
	Limit      int    `json:"limit,omitempty" jsonschema:"page size (server's default when 0)"`
	Before     string `json:"before,omitempty" jsonschema:"the previous answer's next_before (RFC 3339): the next page"`
	BeforeID   string `json:"before_id,omitempty" jsonschema:"the previous answer's next_before_id, with before"`
	ErrorClass string `json:"error_class,omitempty" jsonschema:"only failures of this class (exact: a class string from search's facets.error_class)"`
	mcpFull
	mcpBound
}

type mcpSearchIn struct {
	Q          string  `json:"q,omitempty" jsonschema:"terms: case-insensitive substrings of prompt, answer, error or a tool name, all required"`
	Agent      string  `json:"agent,omitempty" jsonschema:"exact agent name"`
	Tool       string  `json:"tool,omitempty" jsonschema:"runs that called this tool (exact name)"`
	Status     string  `json:"status,omitempty" jsonschema:"running, succeeded, failed, interrupted, parked (an open park: pending calls, no resume) or cancelled (a failed run whose context ended); any other word is a 400"`
	Model      string  `json:"model,omitempty" jsonschema:"exact model name"`
	Error      string  `json:"error,omitempty" jsonschema:"substring of the run's error"`
	ErrorClass string  `json:"error_class,omitempty" jsonschema:"a failure class, exact (a class string from facets.error_class)"`
	Since      string  `json:"since,omitempty" jsonschema:"started at or after (RFC 3339)"`
	Until      string  `json:"until,omitempty" jsonschema:"started before (RFC 3339)"`
	MinTokens  int64   `json:"min_tokens,omitempty" jsonschema:"input + output tokens at least"`
	MinCost    float64 `json:"min_cost,omitempty" jsonschema:"USD at least, own + delegated (unpriced runs never match)"`
	Sort       string  `json:"sort,omitempty" jsonschema:"started (default, newest first), duration, tokens or cost (most first)"`
	Session    string  `json:"session,omitempty" jsonschema:"only this thread session's runs"`
	PublicID   string  `json:"public_id,omitempty" jsonschema:"only this public id's runs"`
	All        bool    `json:"all,omitempty" jsonschema:"subagent children too"`
	Limit      int     `json:"limit,omitempty" jsonschema:"page size"`
	Offset     int     `json:"offset,omitempty" jsonschema:"the previous answer's next_offset"`
	mcpFull
	mcpBound
}

type mcpStepIn struct {
	ID string `json:"id" jsonschema:"the run id"`
	N  int    `json:"n" jsonschema:"the step ordinal, from 0"`
	mcpBound
}

type mcpRequestIn struct {
	ID         string `json:"id" jsonschema:"the run id"`
	Step       *int   `json:"step,omitempty" jsonschema:"only this step's attempts (ordinal from 0)"`
	From       int    `json:"from,omitempty" jsonschema:"the previous answer's next_from"`
	Limit      int    `json:"limit,omitempty" jsonschema:"page size"`
	Refs       bool   `json:"refs,omitempty" jsonschema:"prompt and catalog by hash instead of inline (smaller)"`
	Transcript bool   `json:"transcript,omitempty" jsonschema:"with step: also the messages that step's model call carried"`
	mcpBound
}

type mcpEventsIn struct {
	ID    string `json:"id" jsonschema:"the run id"`
	After int64  `json:"after,omitempty" jsonschema:"the previous answer's next_after"`
	Limit int    `json:"limit,omitempty" jsonschema:"page size"`
	mcpBound
}

type mcpDiffIn struct {
	A string `json:"a" jsonschema:"the base run id"`
	B string `json:"b" jsonschema:"the run compared with it"`
	mcpBound
}

type mcpExportIn struct {
	ID     string `json:"id" jsonschema:"the run id"`
	Format string `json:"format,omitempty" jsonschema:"json (default), jsonl or otlp"`
	mcpBound
}

type mcpSessionsIn struct {
	Agent    string `json:"agent,omitempty" jsonschema:"only this agent's sessions"`
	PublicID string `json:"public_id,omitempty" jsonschema:"only this public id's sessions"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size"`
	Before   string `json:"before,omitempty" jsonschema:"the previous answer's next_before (RFC 3339)"`
	BeforeID string `json:"before_id,omitempty" jsonschema:"the previous answer's next_before_id, with before"`
	mcpFull
	mcpBound
}

type mcpSessionIn struct {
	ID string `json:"id" jsonschema:"the session id"`
	mcpFull
	mcpBound
}

// ── the reader ─────────────────────────────────────────────────────

// mcpReader is what every tool reads through: GET only.
type mcpReader struct{ s getter }

// mcpErrBytes bounds the server body an error carries.
const mcpErrBytes = 2048

// fetch is the one way a tool reaches server. A non-2xx answer is the
// tool's error: server's own line plus the body it sent (its error and
// any badge, reason and fix), bounded with the same visible note.
func (m *mcpReader) fetch(ctx context.Context, path string, q url.Values) (int, []byte, error) {
	status, body, err := m.s.get(ctx, path, q)
	if err != nil && status == http.StatusNotFound {
		err = fmt.Errorf("%w%s", err, notFoundHint(path))
	}
	if err != nil {
		if len(body) > 0 {
			b := compact(body)
			if len(b) > mcpErrBytes {
				b = append(cut(b, mcpErrBytes), fmt.Sprintf("\n[truncated: showing %d of %d bytes of server's answer]", len(cut(b, mcpErrBytes)), len(b))...)
			}
			return status, nil, fmt.Errorf("%w\n%s", err, b)
		}
		return status, nil, err
	}
	return status, body, nil
}

// notFoundHint is what a 404 on an id adds: where ids come from
// (UX-H-29). A route that is not there at all says so through meta.
func notFoundHint(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/runs/"):
		return " — find run ids with runs or search {q}"
	case strings.HasPrefix(path, "/api/sessions/"):
		return " — find session ids with sessions"
	case strings.HasPrefix(path, "/api/traces/"):
		return " — a run row's trace_id (the run tool) names its trace"
	}
	return " — meta says whether this server serves the route"
}

// get is fetch without the status.
func (m *mcpReader) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	_, b, err := m.fetch(ctx, path, q)
	return b, err
}

// out is a tool's answer: the summary line, then each body (the
// route's JSON, compacted), cut together at the bound, and a final
// note when anything was cut — truncation visible, never silent.
func out(maxBytes int, summary string, bodies ...[]byte) (*sdk.CallToolResult, any, error) {
	bound := maxBytes
	if bound <= 0 {
		bound = mcpDefaultBytes
	}
	bound = min(bound, mcpMaxBytes)
	res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: summary}}}
	left, total, shown := bound, 0, 0
	for _, b := range bodies {
		b = compact(b)
		total += len(b)
		if len(b) > left {
			b = cut(b, left)
		}
		left -= len(b)
		shown += len(b)
		if len(b) == 0 {
			continue // cut to nothing: the note says so
		}
		res.Content = append(res.Content, &sdk.TextContent{Text: string(b)})
	}
	if shown < total {
		res.Content = append(res.Content, &sdk.TextContent{Text: fmt.Sprintf(
			"[truncated: showing %d of %d bytes — the JSON above is cut and does not parse; raise max_bytes (at most %d) or narrow the query (limit, step, refs)]",
			shown, total, mcpMaxBytes)})
	}
	return res, nil, nil
}

// compact is b without insignificant whitespace when it is one JSON
// value; anything else (jsonl) as it came.
func compact(b []byte) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, b) == nil {
		return buf.Bytes()
	}
	return b
}

// cut is b's first n bytes, backed off to a rune boundary.
func cut(b []byte, n int) []byte {
	if n >= len(b) {
		return b
	}
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return b[:n]
}

func setStr(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

func setInt(q url.Values, k string, v int64) {
	if v != 0 {
		q.Set(k, strconv.FormatInt(v, 10))
	}
}

func needID(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func (m *mcpReader) meta(ctx context.Context, _ *sdk.CallToolRequest, in mcpMetaIn) (*sdk.CallToolResult, any, error) {
	b, err := m.get(ctx, "/api/meta", nil)
	if err != nil {
		return nil, nil, err
	}
	var meta struct {
		Capabilities []string `json:"capabilities"`
	}
	_ = json.Unmarshal(b, &meta)
	return out(in.MaxBytes, fmt.Sprintf("Server at %s: capabilities %s", m.s.redacted(), strings.Join(meta.Capabilities, ", ")), b)
}

// pageSummary names how many rows a page holds and how to get the next.
func pageSummary(what string, b []byte, list string) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(b, &p) != nil {
		return what
	}
	var rows []json.RawMessage
	_ = json.Unmarshal(p[list], &rows)
	s := fmt.Sprintf("%s: %s", what, count(len(rows), strings.TrimSuffix(list, "s"), list))
	if t, ok := p["total"]; ok {
		s += " of " + string(t)
	}
	for _, k := range []string{"next_before", "next_before_id", "next_offset", "next_after", "next_from"} {
		if v, ok := p[k]; ok && string(v) != "null" {
			s += "; " + k + "=" + string(v)
		}
	}
	return s
}

func (m *mcpReader) runs(ctx context.Context, _ *sdk.CallToolRequest, in mcpRunsIn) (*sdk.CallToolResult, any, error) {
	q := url.Values{}
	setStr(q, "agent", in.Agent)
	setStr(q, "status", in.Status)
	setStr(q, "session", in.Session)
	setStr(q, "public_id", in.PublicID)
	setStr(q, "parent", in.Parent)
	if in.All {
		q.Set("all", "1")
	}
	setInt(q, "limit", int64(in.Limit))
	setStr(q, "before", in.Before)
	setStr(q, "before_id", in.BeforeID)
	setStr(q, "error_class", in.ErrorClass)
	b, err := m.get(ctx, "/api/runs", q)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, pageSummary("runs", b, "runs"), in.rows(b, "runs", nil))
}

func (m *mcpReader) search(ctx context.Context, _ *sdk.CallToolRequest, in mcpSearchIn) (*sdk.CallToolResult, any, error) {
	q := url.Values{}
	for k, v := range map[string]string{"q": in.Q, "agent": in.Agent, "tool": in.Tool, "status": in.Status, "model": in.Model,
		"error": in.Error, "error_class": in.ErrorClass, "since": in.Since, "until": in.Until, "sort": in.Sort, "session": in.Session, "public_id": in.PublicID} {
		setStr(q, k, v)
	}
	setInt(q, "min_tokens", in.MinTokens)
	if in.MinCost != 0 {
		q.Set("min_cost", strconv.FormatFloat(in.MinCost, 'f', -1, 64))
	}
	if in.All {
		q.Set("all", "1")
	}
	setInt(q, "limit", int64(in.Limit))
	setInt(q, "offset", int64(in.Offset))
	b, err := m.get(ctx, "/api/search", q)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, pageSummary("search", b, "runs")+errorClassSuffix(b), in.rows(b, "runs", nil))
}

// runHead is the part of a run row a summary names.
type runHead struct {
	ID         string          `json:"id"`
	Agent      string          `json:"agent"`
	Status     string          `json:"status"`
	Err        string          `json:"err"`
	Steps      int             `json:"steps"`
	Pending    int             `json:"pending"`
	StopReason string          `json:"stop_reason"`
	Holes      json.RawMessage `json:"holes"`
	// ResumedBy is, on a parked run, the run that resumed it: its calls
	// are decided, never "awaiting a decision" (UX-H-24).
	ResumedBy   string `json:"resumed_by"`
	ResumedFrom string `json:"resumed_from"`
}

func (h runHead) line(link string) string {
	s := fmt.Sprintf("run %s (%s): %s, %s", h.ID, agentLabel(h.Agent), h.Status, count(h.Steps, "step", "steps"))
	if h.Err != "" {
		s += ": " + h.Err
	}
	if h.StopReason != "" {
		s += "; stop_reason " + h.StopReason
	}
	switch {
	case h.Pending > 0 && h.ResumedBy != "":
		s += fmt.Sprintf("; parked (%s), decided → resumed as %s", count(h.Pending, "call", "calls"), h.ResumedBy)
	case h.Pending > 0:
		s += fmt.Sprintf("; %s awaiting a decision", count(h.Pending, "call", "calls"))
	}
	if h.ResumedFrom != "" {
		s += "; resumes " + h.ResumedFrom
	}
	return s + " — " + link
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// agentLabel is a summary's agent: its name, or "unnamed agent" for a
// run whose agent has none (UX-H-29).
func agentLabel(s string) string {
	if s == "" {
		return "unnamed agent"
	}
	return s
}

// count is n with its noun in agreement: "1 step", "3 steps", "0 steps".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (m *mcpReader) run(ctx context.Context, _ *sdk.CallToolRequest, in mcpRunIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	b, err := m.get(ctx, rest.RunPath(in.ID), nil)
	if err != nil {
		return nil, nil, err
	}
	var h runHead
	_ = json.Unmarshal(b, &h)
	return out(in.MaxBytes, h.line(m.s.runLink(in.ID)), b)
}

// stepHead is the part of a step document a summary names.
type stepHead struct {
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	ToolCalls []struct {
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Result *struct {
			IsError bool `json:"is_error"`
		} `json:"result"`
		Pending bool `json:"pending"`
	} `json:"tool_calls"`
	Holes []struct {
		Hole string `json:"hole"`
	} `json:"holes"`
}

func (h stepHead) line(id string, n int) string { return h.lineDecided(id, n, nil) }

// lineDecided is line with each parked call a resume decided read as
// its decision (decided, by call id: "denied by alice: …"), never
// "pending" (UX-H-24).
func (h stepHead) lineDecided(id string, n int, decided map[string]string) string {
	s := fmt.Sprintf("run %s step %d: %s", id, n, h.Status)
	if h.Reason != "" {
		s += " (" + h.Reason + ")"
	}
	if len(h.ToolCalls) > 0 {
		var calls []string
		for _, c := range h.ToolCalls {
			state := "no result"
			switch {
			case c.Pending && decided[c.CallID] != "":
				state, _, _ = strings.Cut(decided[c.CallID], ":")
			case c.Pending:
				state = "pending"
			case c.Result != nil && c.Result.IsError:
				state = "error"
			case c.Result != nil:
				state = "ok"
			}
			calls = append(calls, c.Name+" "+state)
		}
		s += "; tool calls: " + strings.Join(calls, ", ")
	}
	if len(h.Holes) > 0 {
		var hs []string
		for _, x := range h.Holes {
			hs = append(hs, x.Hole)
		}
		s += "; holes: " + strings.Join(hs, ", ")
	}
	return s
}

func stepPath(id string, n int) string { return rest.RunPath(id) + "/steps/" + strconv.Itoa(n) }

func (m *mcpReader) step(ctx context.Context, _ *sdk.CallToolRequest, in mcpStepIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	if in.N < 0 {
		return nil, nil, errors.New("n is a step ordinal, 0 or more")
	}
	b, err := m.get(ctx, stepPath(in.ID, in.N), nil)
	if err != nil {
		return nil, nil, err
	}
	var h stepHead
	_ = json.Unmarshal(b, &h)
	return out(in.MaxBytes, h.line(in.ID, in.N), b)
}

func (m *mcpReader) request(ctx context.Context, _ *sdk.CallToolRequest, in mcpRequestIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	if in.Transcript && in.Step == nil {
		return nil, nil, errors.New("transcript=true needs step: the messages one step's model call carried")
	}
	q := url.Values{}
	if in.Step != nil {
		q.Set("step", strconv.Itoa(*in.Step))
	}
	setInt(q, "from", int64(in.From))
	setInt(q, "limit", int64(in.Limit))
	if in.Refs {
		q.Set("refs", "1")
	}
	b, err := m.get(ctx, rest.RunPath(in.ID)+"/requests", q)
	if err != nil {
		return nil, nil, err
	}
	summary := pageSummary("run "+in.ID+" requests", b, "requests")
	if !in.Transcript {
		return out(in.MaxBytes, summary, b)
	}
	tb, err := m.get(ctx, rest.RunPath(in.ID)+"/transcript", url.Values{"step": {strconv.Itoa(*in.Step)}})
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, summary+"; then the transcript of step "+strconv.Itoa(*in.Step), b, tb)
}

func (m *mcpReader) events(ctx context.Context, _ *sdk.CallToolRequest, in mcpEventsIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	setInt(q, "after", in.After)
	setInt(q, "limit", int64(in.Limit))
	b, err := m.get(ctx, rest.RunPath(in.ID)+"/events", q)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, eventsSummary(in.ID, b), eventsProjection(b))
}

// eventsSummary is an events page's line (UX-H-36): the positions it
// holds, the cursor to the next page, and whether the run is terminal
// — said apart, because a terminal run's first page is not its last.
func eventsSummary(id string, b []byte) string {
	var p struct {
		Events []struct {
			Pos int64 `json:"pos"`
		} `json:"events"`
		NextAfter *int64 `json:"next_after"`
		Done      bool   `json:"done"`
	}
	if json.Unmarshal(b, &p) != nil {
		return "run " + id + " events"
	}
	s := "run " + id + " events: " + count(len(p.Events), "event", "events")
	if n := len(p.Events); n > 0 {
		s += fmt.Sprintf(" (pos %d–%d)", p.Events[0].Pos, p.Events[n-1].Pos)
	}
	if p.NextAfter != nil {
		s += fmt.Sprintf("; more: after=%d", *p.NextAfter)
	} else {
		s += "; no more recorded"
	}
	if p.Done {
		s += "; the run is terminal"
	} else {
		s += "; the run may still record events"
	}
	return s
}

// eventsProjection is the events page as the tool answers it: the
// route's body with done renamed run_terminal (UX-H-36) — the route's
// done says the run ended, not that the stream is exhausted (next_after
// says that). Everything else as served; a body that does not decode
// as it came.
func eventsProjection(b []byte) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(b, &doc) != nil {
		return b
	}
	v, ok := doc["done"]
	if !ok {
		return b
	}
	delete(doc, "done")
	doc["run_terminal"] = v
	return marshalCompact(doc)
}

func (m *mcpReader) tools(ctx context.Context, _ *sdk.CallToolRequest, in mcpRunIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	b, err := m.get(ctx, rest.RunPath(in.ID)+"/tools", nil)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, "run "+in.ID+": tool catalogs", b)
}

func (m *mcpReader) diff(ctx context.Context, _ *sdk.CallToolRequest, in mcpDiffIn) (*sdk.CallToolResult, any, error) {
	if err := needID("a", in.A); err != nil {
		return nil, nil, err
	}
	if err := needID("b", in.B); err != nil {
		return nil, nil, err
	}
	b, err := m.get(ctx, "/api/diff", url.Values{"a": {in.A}, "b": {in.B}})
	if err != nil {
		return nil, nil, err
	}
	// Server's diffSummary (api/http/v1/diff.go): changed_steps is the list
	// of changed ordinals. A body that does not decode gets the plain
	// line — never a count it did not read.
	var d struct {
		Summary struct {
			ChangedSteps []int `json:"changed_steps"`
			FirstChanged *int  `json:"first_changed"`
		} `json:"summary"`
	}
	s := fmt.Sprintf("diff %s → %s", in.A, in.B)
	if err := json.Unmarshal(b, &d); err == nil {
		s += ": " + count(len(d.Summary.ChangedSteps), "changed step", "changed steps")
		if d.Summary.FirstChanged != nil {
			s += fmt.Sprintf(", first at step %d", *d.Summary.FirstChanged)
		}
	}
	return out(in.MaxBytes, s, b)
}

func (m *mcpReader) export(ctx context.Context, _ *sdk.CallToolRequest, in mcpExportIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	format := in.Format
	switch format {
	case "":
		format = "json"
	case "json", "jsonl", "otlp":
	default:
		return nil, nil, fmt.Errorf("format must be json, jsonl or otlp, got %q", format)
	}
	b, err := m.get(ctx, rest.RunPath(in.ID)+"/export", url.Values{"format": {format}})
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, fmt.Sprintf("run %s exported as %s (%d bytes)", in.ID, format, len(b)), b)
}

func (m *mcpReader) sessions(ctx context.Context, _ *sdk.CallToolRequest, in mcpSessionsIn) (*sdk.CallToolResult, any, error) {
	q := url.Values{}
	setStr(q, "agent", in.Agent)
	setStr(q, "public_id", in.PublicID)
	setInt(q, "limit", int64(in.Limit))
	setStr(q, "before", in.Before)
	setStr(q, "before_id", in.BeforeID)
	b, err := m.get(ctx, "/api/sessions", q)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, pageSummary("sessions", b, "sessions"), in.rows(b, "sessions", nil))
}

func (m *mcpReader) session(ctx context.Context, _ *sdk.CallToolRequest, in mcpSessionIn) (*sdk.CallToolResult, any, error) {
	if err := needID("id", in.ID); err != nil {
		return nil, nil, err
	}
	b, err := m.get(ctx, "/api/sessions/"+url.PathEscape(in.ID), nil)
	if err != nil {
		return nil, nil, err
	}
	return out(in.MaxBytes, sessionSummary(in.ID, b), in.rows(b, "runs", map[string]string{"agent": "agent", "session_id": "id", "public_id": "public_id"}))
}

// rows is a list answer as the tool gives it: compact rows
// (mcp_compact.go), or the route's JSON under full=true.
func (f mcpFull) rows(b []byte, list string, sameAs map[string]string) []byte {
	if f.Full {
		return b
	}
	return compactList(b, list, sameAs)
}

// sessionSummary is a session answer's line: its turns and outcome.
func sessionSummary(id string, b []byte) string {
	var d struct {
		Agent  string            `json:"agent"`
		Status string            `json:"status"`
		Runs   []json.RawMessage `json:"runs"`
	}
	if json.Unmarshal(b, &d) != nil {
		return "session " + id
	}
	return fmt.Sprintf("session %s (%s): %s, %s", id, agentLabel(d.Agent), orDash(d.Status), count(len(d.Runs), "turn", "turns"))
}
