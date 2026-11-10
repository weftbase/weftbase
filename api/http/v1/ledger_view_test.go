package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/obsdb"
	obsqlite "github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

// The run views over a real ledger: how many times a page asks each
// store, and what a page answers when the control store fails.

// countingRunner counts the control store's reads and transactions,
// and fails every one of them while fail is set.
type countingRunner struct {
	store.Runner
	calls atomic.Int64
	fail  atomic.Bool
}

var errControlDown = errors.New("control store down (test)")

func (c *countingRunner) InTx(ctx context.Context, fn func(store.Tx) error) error {
	c.calls.Add(1)
	if c.fail.Load() {
		return errControlDown
	}
	return c.Runner.InTx(ctx, fn)
}

func (c *countingRunner) Read(ctx context.Context, fn func(store.Tx) error) error {
	c.calls.Add(1)
	if c.fail.Load() {
		return errControlDown
	}
	return c.Runner.Read(ctx, fn)
}

// runCountingDB counts the trace store's run reads, and fails its
// session reads while failSession is set.
type runCountingDB struct {
	obsdb.DB
	runs        atomic.Int64
	failSession atomic.Bool
}

func (c *runCountingDB) Session(ctx context.Context, id string) (obsdb.SessionDetail, error) {
	if c.failSession.Load() {
		return obsdb.SessionDetail{}, errors.New("trace store down (test)")
	}
	return c.DB.Session(ctx, id)
}

func (c *runCountingDB) Runs(ctx context.Context, q obsdb.RunQuery) (obsdb.RunPage, error) {
	c.runs.Add(1)
	return c.DB.Runs(ctx, q)
}

func (c *runCountingDB) Run(ctx context.Context, id string) (obsdb.RunDetail, error) {
	c.runs.Add(1)
	return c.DB.Run(ctx, id)
}

// The trace store's tree and aggregate reads, passed through (the
// children read the cost makes per page counts as a read).
func (c *runCountingDB) ChildRuns(ctx context.Context, parents []string) (map[string][]obsdb.RunRow, error) {
	c.runs.Add(1)
	return c.DB.(obsdb.TreeReader).ChildRuns(ctx, parents)
}

func (c *runCountingDB) RunTreeSpans(ctx context.Context, q obsdb.TreeQuery) (obsdb.RunTree, error) {
	return c.DB.(obsdb.TreeReader).RunTreeSpans(ctx, q)
}

func (c *runCountingDB) Stats(ctx context.Context, q obsdb.StatsQuery) (obsdb.StatsPage, error) {
	return c.DB.(obsdb.Aggregator).Stats(ctx, q)
}

func (c *runCountingDB) ErrorCounts(ctx context.Context, q obsdb.SearchQuery) ([]obsdb.ErrorCount, bool, error) {
	return c.DB.(obsdb.Aggregator).ErrorCounts(ctx, q)
}

type ledgerFixture struct {
	control *countingRunner
	traces  *runCountingDB
	ledger  *sessions.Ledger
	q       *queue.Queue
	h       http.Handler
	s       tenant.Scope
	at      time.Time
}

func newLedgerFixture(t *testing.T) *ledgerFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tdb, err := obsqlite.Open(filepath.Join(dir, "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tdb.Close() })
	f := &ledgerFixture{control: &countingRunner{Runner: db}, traces: &runCountingDB{DB: tdb}, s: tenant.Default(),
		at: time.Now().Add(-time.Hour).Truncate(time.Second)}
	log := events.New(f.control)
	reg := registry.New(f.control, log)
	f.q = queue.New(f.control)
	f.ledger = sessions.New(f.control, log, reg, f.q)
	body := []byte(`{"weft":1,"agents":[{"name":"support"}]}`)
	if _, err := reg.RegisterIn(ctx, f.s, "sha256:aaa", "embedded", []registry.AgentSpec{{Name: "support", Manifest: body, ManifestHash: registry.ManifestHash(body)}}); err != nil {
		t.Fatal(err)
	}
	f.h = New(DB(f.traces), Ledger(f.ledger), Registry(reg)).Handler()
	return f
}

// run dispatches the next queued item and ends it with ack, as the hub
// would.
func (f *ledgerFixture) run(t *testing.T, ack wire.Ack) string {
	t.Helper()
	ctx := context.Background()
	l, err := f.q.Claim(ctx, queue.ClaimFilter{Owner: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := sessions.ItemRunID(l.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.control.InTx(ctx, func(tx store.Tx) error {
		if _, _, err := f.ledger.Prepare(tx, l.Scope, l.Payload, wire.NewCommandID(), "wk_test"); err != nil {
			return err
		}
		if err := f.ledger.Started(tx, l.Scope, runID); err != nil {
			return err
		}
		if err := f.ledger.Finished(tx, l.Scope, runID, ack); err != nil {
			return err
		}
		return f.q.Done(tx, l)
	}); err != nil {
		t.Fatal(err)
	}
	return runID
}

// trace writes a run's start to the trace store, as its worker's
// exporter would (a resume names the run it resumes).
func (f *ledgerFixture) trace(t *testing.T, id, resumedFrom string) {
	t.Helper()
	f.at = f.at.Add(time.Second)
	attrs := map[string]any{"weft.record": "event", "weft.run.id": id, "weft.event.type": "run_start", "weft.event.pos": int64(0),
		"gen_ai.agent.name": "support"}
	if resumedFrom != "" {
		attrs["weft.resumed_from"] = resumedFrom
	}
	rec := obsdb.Record{Time: f.at, EventName: "weft.event", TraceID: fmt.Sprintf("%032x", f.at.UnixNano()), SpanID: "0102030405060708",
		Severity: 9, Body: `{"type":"run_start","id":"` + id + `"}`, Service: "support", Attrs: attrs, Resource: map[string]any{"service.name": "support"}}
	if err := f.traces.Write(context.Background(), obsdb.Batch{Records: []obsdb.Record{rec}}); err != nil {
		t.Fatal(err)
	}
}

// chain admits a run, parks it on one call, decides it and runs the
// resume to its end: a chain of two runs, both traced.
func (f *ledgerFixture) chain(t *testing.T) (head, resume string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund"}}); err != nil {
		t.Fatal(err)
	}
	head = f.run(t, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{}`),
		Pending: []wire.PendingCall{{CallID: "c1", Tool: "refund", Args: json.RawMessage(`{"order":1}`)}}})
	page, err := f.ledger.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: head})
	if err != nil || len(page.Approvals) != 1 {
		t.Fatalf("approvals of %s = %+v, %v", head, page, err)
	}
	a := page.Approvals[0]
	decided, err := f.ledger.Decide(ctx, f.s, events.Actor{Kind: "operator", ID: "alice"}, a.ID, "approve", "", "", a.ArgsSHA256)
	if err != nil {
		t.Fatal(err)
	}
	resume = f.run(t, wire.Ack{Status: wire.StatusSucceeded})
	if resume != decided.ResumedBy {
		t.Fatalf("ran %s, want the resume %s", resume, decided.ResumedBy)
	}
	f.trace(t, head, "")
	f.trace(t, resume, head)
	return head, resume
}

func (f *ledgerFixture) get(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// B8: a 50-row page of 2-run chains asks each store a bounded number of
// times, not a few per row.
func TestChainPageReadsAreBounded(t *testing.T) {
	f := newLedgerFixture(t)
	const chains = 50
	for range chains {
		f.chain(t)
	}
	f.control.calls.Store(0)
	f.traces.runs.Store(0)
	code, page := f.get(t, "/v1/runs?limit=50")
	rows, _ := page["runs"].([]any)
	if code != http.StatusOK || len(rows) != chains {
		t.Fatalf("GET /v1/runs: %d, %d rows; want %d", code, len(rows), chains)
	}
	for _, r := range rows {
		ch, _ := r.(map[string]any)["chain"].(map[string]any)
		if runs, _ := ch["runs"].([]any); len(runs) != 2 {
			t.Fatalf("a row without its chain of two: %v", r)
		}
	}
	// The trace store: the page, then one read for the chains' other
	// runs. The control store: the page's ledger rows with their
	// chains, the approvals, the registry (versions, placements).
	control, traces := f.control.calls.Load(), f.traces.runs.Load()
	t.Logf("a %d-row page: %d control-store reads, %d trace-store run reads", chains, control, traces)
	if traces > 4 || control > 8 {
		t.Errorf("a %d-row page made %d trace and %d control reads; want a bound that does not grow with the rows", chains, traces, control)
	}
}

// B5: a control store that cannot be read is never guessed around: the
// run views answer 503 unavailable rather than the trace store's word.
func TestControlStoreDownIsUnavailable(t *testing.T) {
	f := newLedgerFixture(t)
	head, _ := f.chain(t)
	ctx := context.Background()
	queued, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "wait"}})
	if err != nil {
		t.Fatal(err)
	}
	f.control.fail.Store(true)
	for _, path := range []string{
		"/v1/runs",
		"/v1/runs?display_status=queued",
		"/v1/runs/" + head,
		"/v1/runs/" + queued.ID,
		"/v1/stats?by=total",
	} {
		code, body := f.get(t, path)
		e, _ := body["error"].(map[string]any)
		if code != http.StatusServiceUnavailable || e["code"] != "unavailable" {
			t.Errorf("GET %s with the control store down: %d %v, want 503 unavailable", path, code, body)
		}
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/v1/runs/"+head, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("DELETE with the control store down: %d %s", rec.Code, rec.Body)
	}
	f.control.fail.Store(false)
	if code, doc := f.get(t, "/v1/runs/"+head); code != 200 || doc["chain"] == nil {
		t.Errorf("after the store is back: %d %v", code, doc)
	}
}

// B10: each run-state conflict has its own code and a stable message.
func TestStateConflictCodes(t *testing.T) {
	f := newLedgerFixture(t)
	head, resume := f.chain(t)
	post := func(path string) (int, string, string) {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)))
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Error.Code, body.Error.Message
	}
	for _, c := range []struct{ path, code string }{
		{"/v1/runs/" + resume + "/cancel", "already_finished"},
		{"/v1/runs/" + head + "/cancel", "already_resumed"},
		{"/v1/runs/" + resume + "/steer", "not_running"},
	} {
		status, code, msg := post(c.path)
		if status != http.StatusConflict || code != c.code || strings.Contains(msg, "run_") {
			t.Errorf("POST %s: %d %s %q, want 409 %s with a stable message", c.path, status, code, msg, c.code)
		}
	}
	if status, _, _ := post("/v1/runs/" + head + "/resume"); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Errorf("POST /resume: %d, want no such route", status)
	}
	// status is the ledger's own row, with the one agent_version shape.
	code, doc := f.get(t, "/v1/runs/"+resume+"/status")
	v, _ := doc["agent_version"].(map[string]any)
	if code != 200 || doc["status"] != "succeeded" || doc["chain_id"] != head || v["number"] != float64(1) || v["labels"] == nil {
		t.Errorf("GET status: %d %v", code, doc)
	}
}

// B2: queued runs page with the trace store's rows, merged by start,
// limit honoured, a real cursor both ways, counted exactly.
func TestQueuedRowsPage(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	f.chain(t) // one traced logical run, older than every queued one
	var queued []string
	for range 25 {
		run, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "wait"}})
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, run.ID)
	}
	for _, oldest := range []bool{false, true} {
		seen := map[string]bool{}
		var order []string
		path := fmt.Sprintf("/v1/runs?limit=10&oldest=%v", oldest)
		for pages := 0; ; pages++ {
			code, page := f.get(t, path)
			rows, _ := page["runs"].([]any)
			if code != 200 || len(rows) > 10 || pages > 6 {
				t.Fatalf("GET %s: %d, %d rows", path, code, len(rows))
			}
			// The trace store's two runs (an upper bound: they fold into
			// one) and the 25 queued.
			if pages == 0 && (page["total"] != float64(27) || page["total_exact"] != false) {
				t.Errorf("first page: total %v exact %v, want 27 (an upper bound)", page["total"], page["total_exact"])
			}
			for _, r := range rows {
				id := r.(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatalf("%s listed twice", id)
				}
				seen[id] = true
				order = append(order, id)
			}
			next, _ := page["next_before"].(string)
			if next == "" {
				break
			}
			nextID, _ := page["next_before_id"].(string)
			path = fmt.Sprintf("/v1/runs?limit=10&oldest=%v&before=%s&before_id=%s", oldest, next, nextID)
		}
		if len(order) != 26 {
			t.Fatalf("oldest=%v: %d rows over the pages, want 26", oldest, len(order))
		}
		newest := queued[len(queued)-1]
		if oldest && order[len(order)-1] != newest {
			t.Errorf("oldest=1 ends with %s, want the newest queued run %s", order[len(order)-1], newest)
		}
		if !oldest && order[0] != newest {
			t.Errorf("newest first starts with %s, want %s", order[0], newest)
		}
	}
	code, page := f.get(t, "/v1/runs?display_status=queued&limit=10")
	if rows, _ := page["runs"].([]any); code != 200 || len(rows) != 10 || page["total"] != float64(25) || page["total_exact"] != true || page["next_before"] == nil {
		t.Errorf("display_status=queued&limit=10: %d %d rows, total %v exact %v", code, len(rows), page["total"], page["total_exact"])
	}
}

// B2: a chain whose current run is a queued resume (no trace yet) is
// still one row: at its first run in the list, under queued, and in the
// statistics.
func TestQueuedResumeKeepsItsChain(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	if _, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund"}}); err != nil {
		t.Fatal(err)
	}
	head := f.run(t, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{}`),
		Pending: []wire.PendingCall{{CallID: "c1", Tool: "refund", Args: json.RawMessage(`{}`)}}})
	f.trace(t, head, "")
	page, _ := f.ledger.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: head})
	a := page.Approvals[0]
	decided, err := f.ledger.Decide(ctx, f.s, events.System, a.ID, "approve", "", "", a.ArgsSHA256)
	if err != nil {
		t.Fatal(err)
	}
	// status= is the trace store's word (the parked run's trace has its
	// start only, long ago: interrupted): a mapped read, where the chain
	// shows at its last traced run.
	for _, path := range []string{"/v1/runs", "/v1/runs?display_status=queued", "/v1/runs?status=interrupted"} {
		code, page := f.get(t, path)
		rows, _ := page["runs"].([]any)
		if code != 200 || len(rows) != 1 {
			t.Errorf("GET %s: %d %v, want the chain's one row", path, code, page)
			continue
		}
		row := rows[0].(map[string]any)
		ch, _ := row["chain"].(map[string]any)
		runs, _ := ch["runs"].([]any)
		if row["id"] != head || row["display_status"] != "queued" || len(runs) != 2 || runs[1].(map[string]any)["id"] != decided.ResumedBy {
			t.Errorf("GET %s row = %v", path, row)
		}
	}
	_, stats := f.get(t, "/v1/stats?by=total&since=48h")
	total, _ := stats["total"].(map[string]any)
	if total["runs"] != float64(1) || total["queued"] != float64(1) {
		t.Errorf("stats total = %v, want the one chain, queued", total)
	}
}

// B11: a search cursor never points past the deepest offset the search
// accepts; the page says partial instead.
func TestSearchCursorStopsAtItsMaximum(t *testing.T) {
	if next, cut := searchCursor(obsdb.MaxSearchOffset+5, false); next != nil || !cut {
		t.Errorf("past the maximum: next %v, cut %v; want none, cut", next, cut)
	}
	if next, cut := searchCursor(obsdb.MaxSearchOffset, false); next == nil || *next != obsdb.MaxSearchOffset || cut {
		t.Errorf("at the maximum: next %v, cut %v", next, cut)
	}
	if next, cut := searchCursor(40, true); next != nil || cut {
		t.Errorf("exhausted: next %v, cut %v", next, cut)
	}
}

// B13: without runs.content a row's prompt preview and session title,
// and the spans' content attributes, are left out.
func TestContentRedaction(t *testing.T) {
	c := &control{content: false}
	r := runRow{PromptPreview: "refund order 42", Meta: map[string]string{obsdb.SessionTitleKey: "Refunds", "plan": "pro"}}
	c.redact(&r)
	if r.PromptPreview != "" || r.Meta[obsdb.SessionTitleKey] != "" || r.Meta["plan"] != "pro" {
		t.Errorf("redacted row = %+v", r)
	}
	for k, want := range map[string]bool{"weft.content.text": true, "gen_ai.input.messages": true, "gen_ai.prompt.0.content": true,
		"gen_ai.tool.call.arguments": true, "gen_ai.request.model": false, "weft.run.id": false} {
		if contentAttr(k) != want {
			t.Errorf("contentAttr(%s) = %v", k, !want)
		}
	}
	c.content = true
	r = runRow{PromptPreview: "kept"}
	if c.redact(&r); r.PromptPreview != "kept" {
		t.Error("a caller with runs.content lost the preview")
	}
}

// B9: a live stream reads the ledger once per controlTTL, not once per
// run frame.
func TestLiveFramesShareOneControl(t *testing.T) {
	f := newLedgerFixture(t)
	head, _ := f.chain(t)
	srv := New(DB(f.traces), Ledger(f.ledger))
	det, err := f.traces.Run(context.Background(), head)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/runs/stream", nil).WithContext(authzContext())
	ctl := srv.control(req)
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, rc: http.NewResponseController(rec)}
	f.control.calls.Store(0)
	for range 20 {
		ctl.fresh()
		writeFrame(sw, obsdb.Frame{Kind: obsdb.FrameRun, Run: &det.RunRow}, srv.coster(req.Context()), ctl)
	}
	if n := f.control.calls.Load(); n > 1 {
		t.Errorf("20 run frames read the control store %d times, want once per %s", n, controlTTL)
	}
	if !strings.Contains(rec.Body.String(), `"display_status"`) {
		t.Errorf("the frames carry no ledger fields: %.300s", rec.Body)
	}
}

// authzContext is a request context as the router leaves it: the Local
// principal in the default scope.
func authzContext() context.Context {
	return authz.With(context.Background(), localPrincipal, tenant.Default())
}

// B3: a session's next turn over HTTP — refused while the previous one
// is queued (the ledger knows; no trace does), refused with
// session_settling while the turn that just ended has no trace yet
// (its history would lose it), and 503 when the trace store cannot be
// read, never admitted on a guess.
func TestSessionTurnAdmission(t *testing.T) {
	f := newLedgerFixture(t)
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/runs", strings.NewReader(body)))
		var out struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Error.Code
	}
	turn := `{"agent":"support","session_id":"s1","input":{"prompt":"hi"}}`
	if code, _ := post(turn); code != http.StatusAccepted {
		t.Fatalf("the first turn: %d", code)
	}
	if code, ec := post(turn); code != http.StatusConflict || ec != "session_busy" {
		t.Errorf("a turn while the first is queued: %d %s, want 409 session_busy", code, ec)
	}
	f.run(t, wire.Ack{Status: wire.StatusSucceeded}) // it ran and ended; its trace has not arrived
	if code, ec := post(turn); code != http.StatusConflict || ec != "session_settling" {
		t.Errorf("a prompt-only turn before the last one's trace: %d %s, want 409 session_settling", code, ec)
	}
	withMessages := `{"agent":"support","session_id":"s1","input":{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}}`
	f.traces.failSession.Store(true)
	if code, ec := post(withMessages); code != http.StatusServiceUnavailable || ec != "unavailable" {
		t.Errorf("a turn with the trace store down: %d %s, want 503 unavailable", code, ec)
	}
	f.traces.failSession.Store(false)
	if code, ec := post(withMessages); code != http.StatusAccepted {
		t.Errorf("a turn that carries its own history: %d %s, want 202", code, ec)
	}
}

// A run cancelled while it waited on a person: the logical run lasted
// until the cancel, the wait with it — not the trace's own end (the
// park), which would make five minutes of waiting read as seconds.
func TestCancelledWhileParkedKeepsItsWait(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	if _, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund"}}); err != nil {
		t.Fatal(err)
	}
	head := f.run(t, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{}`),
		Pending: []wire.PendingCall{{CallID: "c1", Tool: "refund", Args: json.RawMessage(`{}`)}}})
	f.trace(t, head, "") // its trace: the start, an hour ago
	if _, err := f.ledger.Cancel(ctx, f.s, events.System, head, ""); err != nil {
		t.Fatal(err)
	}
	lr, _ := f.ledger.Get(ctx, f.s, head)
	code, doc := f.get(t, "/v1/runs/"+head)
	ch, _ := doc["chain"].(map[string]any)
	waits, _ := ch["waits"].([]any)
	if code != 200 || doc["display_status"] != "cancelled" || ch == nil || len(waits) != 1 {
		t.Fatalf("the cancelled parked run: %d %v", code, doc)
	}
	w := waits[0].(map[string]any)
	finished, _ := time.Parse(time.RFC3339Nano, ch["finished"].(string))
	to, _ := time.Parse(time.RFC3339Nano, w["to"].(string))
	from, _ := time.Parse(time.RFC3339Nano, w["from"].(string))
	// The trace started an hour before the cancel: the logical run's
	// duration runs to the cancel, the wait from the park to it.
	if !finished.Equal(*lr.FinishedAt) || !to.Equal(*lr.FinishedAt) || lr.ParkedAt == nil || !from.Equal(*lr.ParkedAt) ||
		ch["duration_ms"].(float64) < float64((59*time.Minute).Milliseconds()) {
		t.Errorf("chain = finished %v, wait %v → %v, duration_ms %v; want the wait from the park (%v) to the cancel (%v)",
			ch["finished"], w["from"], w["to"], ch["duration_ms"], lr.ParkedAt, lr.FinishedAt)
	}
	// One run alone: the row carries the chain on the list too.
	_, page := f.get(t, "/v1/runs")
	if rows, _ := page["runs"].([]any); len(rows) != 1 || rows[0].(map[string]any)["chain"] == nil {
		t.Errorf("the list's row = %v", page)
	}
}

// A session whose waiting turn is cancelled reads cancelled — the
// sessions list row, the session document and the conversation's
// session alike — as the turn itself does: the ledger ended the turn
// after the trace store last heard of it, so the ledger's word wins.
func TestSessionReadsItsCancelledTurn(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	if _, err := f.ledger.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "refund"}}); err != nil {
		t.Fatal(err)
	}
	head := f.run(t, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{}`),
		Pending: []wire.PendingCall{{CallID: "c1", Tool: "refund", Args: json.RawMessage(`{}`)}}})
	attrs := map[string]any{"weft.record": "event", "weft.run.id": head, "weft.event.type": "run_start", "weft.event.pos": int64(0),
		"gen_ai.agent.name": "support", "weft.session.id": "s1", "weft.turn": int64(1)}
	if err := f.traces.Write(ctx, obsdb.Batch{Records: []obsdb.Record{{Time: time.Now(), EventName: "weft.event",
		TraceID: "0102030405060708090a0b0c0d0e0f10", SpanID: "0102030405060708", Severity: 9, Body: `{"type":"run_start","id":"` + head + `"}`,
		Service: "support", Attrs: attrs, Resource: map[string]any{"service.name": "support"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ledger.Cancel(ctx, f.s, events.System, head, ""); err != nil {
		t.Fatal(err)
	}
	_, list := f.get(t, "/v1/sessions")
	rows, _ := list["sessions"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["display_status"] != "cancelled" {
		t.Errorf("the sessions list = %v, want the session cancelled", list)
	}
	if _, doc := f.get(t, "/v1/sessions/s1"); doc["display_status"] != "cancelled" {
		t.Errorf("the session = %v / %v, want cancelled", doc["status"], doc["display_status"])
	}
	_, conv := f.get(t, "/v1/sessions/s1/conversation")
	sess, _ := conv["session"].(map[string]any)
	turns, _ := conv["turns"].([]any)
	if sess["display_status"] != "cancelled" || len(turns) != 1 || turns[0].(map[string]any)["display_status"] != "cancelled" {
		t.Errorf("the conversation: session %v, turns %v; want both cancelled", sess["display_status"], turns)
	}
}
