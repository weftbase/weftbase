package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// The second audit pass's pins (2026-10-02): each test names the
// failure it was written against.

// seedRun writes one finished run with a two-message transcript under
// the given public id (and session, when one is named). finish is the
// run_finish body; "" writes a plain one.
func seedRun(t *testing.T, db obsdb.DB, runID, publicID, sessionID, finish string, extra map[string]any) {
	t.Helper()
	seedRunAs(t, db, "svc", runID, publicID, sessionID, finish, extra)
}

// seedRunAs is seedRun under another service.name: an app-owned run of
// the service a runtime.Sessions runtime registered (plan F4).
func seedRunAs(t *testing.T, db obsdb.DB, service, runID, publicID, sessionID, finish string, extra map[string]any) {
	t.Helper()
	if finish == "" {
		finish = `{"type":"run_finish","run_id":"` + runID + `","usage":{"input_tokens":1,"output_tokens":1},"steps":1}`
	}
	now := time.Now().UTC()
	mk := func(kind, evType string, pos int64, body string) obsdb.Record {
		attrs := map[string]any{
			"weft.record": kind, "weft.run.id": runID, "gen_ai.agent.name": "acme-support",
		}
		if publicID != "" {
			attrs["weft.public_id"] = publicID
		}
		if sessionID != "" {
			attrs["weft.session.id"] = sessionID
			attrs["weft.turn"] = "1"
		}
		switch kind {
		case "event":
			attrs["weft.event.type"] = evType
			attrs["weft.event.pos"] = pos
		case "messages":
			attrs["weft.messages.index"] = pos
			attrs["weft.messages.count"] = int64(1)
		}
		for k, v := range extra {
			attrs[k] = v
		}
		return obsdb.Record{
			Time:      now.Add(time.Duration(pos) * time.Millisecond),
			EventName: "weft." + kind, Severity: 9, Body: body, Service: service,
			Attrs: attrs, Resource: map[string]any{"service.name": service},
		}
	}
	recs := []obsdb.Record{
		mk("event", "run_start", 0, `{"type":"run_start","id":"`+runID+`","model":{"provider":"wefttest","name":"script"},"agent":"acme-support"}`),
		mk("messages", "", 0, `[{"role":"user","content":[{"type":"text","text":"secret of `+publicID+`"}]}]`),
		mk("messages", "", 1, `[{"role":"assistant","content":[{"type":"text","text":"done"}]}]`),
		mk("event", "run_finish", 1, finish),
	}
	if err := db.Write(context.Background(), obsdb.Batch{Records: recs}); err != nil {
		t.Fatal(err)
	}
}

// TestLiveHEADDoesNotHoldAStream: ServeMux routes HEAD to the GET
// pattern; the live handler subscribed and looped for a request that
// can carry no body.
func TestLiveHEADDoesNotHoldAStream(t *testing.T) {
	h := Handler(DB(fixtureDB(t)))
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "http://127.0.0.1/api/live?run=r_ok", nil))
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Errorf("HEAD = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HEAD /api/live never returned: the handler is streaming to a bodiless request")
	}
}

// TestLiveStaleCursorStillStreams: the hub's Seq restarts with the
// process, a browser's EventSource does not — it reconnects with the
// Last-Event-ID of the previous process. The handler passed it to
// Subscribe as the "after" cursor and the hub dropped every frame at
// or below it: after a server restart every open tab's live tail went
// silent (pings only) until a reload.
func TestLiveStaleCursorStillStreams(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := Handler(DB(db))
	resp := subscribeLive(t, h, "?run=run_live&kinds=event", "48213") // a seq this hub never issued
	seedRun(t, db, "run_live", "pub_x", "", "", nil)
	frames := readSSE(t, resp, 2, 2*time.Second)
	if len(frames) != 2 {
		t.Fatalf("frames after a stale Last-Event-ID = %d, want the run's 2 events", len(frames))
	}
}

// gateDB blocks the first Events read until released and counts them.
type gateDB struct {
	obsdb.DB
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateDB) Events(ctx context.Context, id string, after int64, limit int) (obsdb.EventPage, error) {
	g.calls.Add(1)
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.DB.Events(ctx, id, after, limit)
}

// TestLiveBackfillStopsWhenTheClientLeaves pins the audit's P3: the
// resume backfill read the database on context.Background(), so a
// client that left mid-backfill still cost a full walk of the
// selector's history.
func TestLiveBackfillStopsWhenTheClientLeaves(t *testing.T) {
	inner, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	for i := 0; i < 30; i++ {
		seedRun(t, inner, fmt.Sprintf("run_%02d", i), "pub_big", "s_big", "", nil)
	}
	db := &gateDB{DB: inner, entered: make(chan struct{}), release: make(chan struct{})}
	h := New(DB(db), Live(obsdb.NewHub())).Handler()

	ctx, cancel := context.WithCancel(context.Background())
	// A loopback Host: setup A's API answers nothing else (the
	// DNS-rebinding guard), and httptest's default is example.com.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/live?session=s_big", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-db.entered // the backfill is reading its first run
	cancel()     // the client leaves
	close(db.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never returned")
	}
	if n := db.calls.Load(); n > 2 {
		t.Errorf("the backfill read %d runs' events after the client left, want it to stop", n)
	}
}

// TestLiveSlowClientIsDropped: a client that stops reading without
// closing fills the socket; the handler then sat in Write forever — no
// deadline — holding its goroutine and hub subscription. Each frame
// write carries a deadline now.
func TestLiveSlowClientIsDropped(t *testing.T) {
	old := liveWriteTimeout
	liveWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { liveWriteTimeout = old })

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := obsdb.NewHub(obsdb.QueueSize(4096))
	inner := New(DB(db), Live(hub)).Handler()
	returned := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		close(returned)
	}))
	t.Cleanup(ts.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "GET /api/live?run=run_big HTTP/1.1\r\nHost: server\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Read the response head so the subscription is known to exist,
	// then never read again.
	head := make([]byte, 64)
	if _, err := conn.Read(head); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"tool_finish","content":"` + strings.Repeat("x", 64<<10) + `"}`
	at := time.Now().UTC()
	for i := 0; i < 1024; i++ { // 64 MiB: past any socket buffer
		hub.Publish(context.Background(), obsdb.RecordFrame(
			fxRecord("run_big", "event", "tool_finish", int64(i), at, body)))
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is still blocked writing to a client that stopped reading")
	}
}

// TestLiveDedupIsBounded: the per-connection dedup set kept every
// (run, kind, pos) it ever forwarded — a tab left on an agent's live
// stream grew the server by one key per record, deltas included, for
// as long as it stayed open.
func TestLiveDedupIsBounded(t *testing.T) {
	d := newLiveDedup(8)
	for i := 0; i < 1000; i++ {
		if d.seen(liveKey{run: "r", kind: "delta", pos: int64(i)}) {
			t.Fatalf("pos %d reads as a duplicate on first sight", i)
		}
	}
	if n := d.len(); n > 8 {
		t.Errorf("the set holds %d keys, want at most 8", n)
	}
	// What is recent still dedups (a transport retry, the database's
	// own publish beside ingest's).
	if !d.seen(liveKey{run: "r", kind: "delta", pos: 999}) {
		t.Error("the newest key no longer dedups")
	}
	// While held open (the resume backfill) nothing is evicted: every
	// backfilled record must still dedup the live frames queued behind it.
	h := newLiveDedup(8)
	h.hold = true
	for i := 0; i < 100; i++ {
		h.seen(liveKey{run: "r", kind: "event", pos: int64(i)})
	}
	if !h.seen(liveKey{run: "r", kind: "event", pos: 0}) {
		t.Error("a held set evicted a backfilled key")
	}
}

// TestLoopbackOpenIngestRefusesBrowsersAndProxies: with no ingest
// token, "loopback" was the socket's peer address alone. A page on any
// origin (DNS rebinding makes 127.0.0.1 same-origin) and every client
// of a reverse proxy on the same host arrive from loopback too. A
// request that names a foreign Origin, or that a proxy forwarded, is
// not a local exporter. And 127.0.0.0/8 and the IPv4-mapped form are
// loopback like 127.0.0.1.
func TestLoopbackOpenIngestRefusesBrowsersAndProxies(t *testing.T) {
	pb, err := os.ReadFile(filepath.Join("..", "..", "..", "obsdb", "testdata", "logs.pb"))
	if err != nil {
		t.Fatal(err)
	}
	postTo := func(h http.Handler, remote string, hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/otlp/v1/logs", bytes.NewReader(pb))
		req.RemoteAddr = remote
		req.Header.Set("Content-Type", "application/x-protobuf")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	open := New(DB(fixtureDB(t))).Handler()
	for _, tc := range []struct {
		name   string
		remote string
		hdr    map[string]string
		want   int
	}{
		{"plain loopback", "127.0.0.1:5", nil, http.StatusOK},
		{"127.0.0.0/8", "127.0.0.2:5", nil, http.StatusOK},
		{"ipv6 loopback", "[::1]:5", nil, http.StatusOK},
		{"ipv4-mapped loopback", "[::ffff:127.0.0.1]:5", nil, http.StatusOK},
		{"a localhost page", "127.0.0.1:5", map[string]string{"Origin": "http://localhost:3000"}, http.StatusOK},
		{"a foreign page", "127.0.0.1:5", map[string]string{"Origin": "https://evil.example"}, http.StatusUnauthorized},
		{"a rebound page", "127.0.0.1:5", map[string]string{"Origin": "http://evil.example:7331"}, http.StatusUnauthorized},
		{"the null origin", "127.0.0.1:5", map[string]string{"Origin": "null"}, http.StatusUnauthorized},
		{"behind a proxy (X-Forwarded-For)", "127.0.0.1:5", map[string]string{"X-Forwarded-For": "203.0.113.9"}, http.StatusUnauthorized},
		{"behind a proxy (Forwarded)", "127.0.0.1:5", map[string]string{"Forwarded": "for=203.0.113.9"}, http.StatusUnauthorized},
		{"behind a proxy (X-Real-IP)", "127.0.0.1:5", map[string]string{"X-Real-Ip": "203.0.113.9"}, http.StatusUnauthorized},
		{"remote", "10.0.0.5:5", nil, http.StatusUnauthorized},
	} {
		if got := postTo(open, tc.remote, tc.hdr); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
	// An explicitly allowed origin is a page the operator vouched for.
	allowed := New(DB(fixtureDB(t)), AllowOrigins("https://app.example")).Handler()
	if got := postTo(allowed, "127.0.0.1:5", map[string]string{"Origin": "https://app.example"}); got != http.StatusOK {
		t.Errorf("an allowed origin: %d, want 200", got)
	}
	// With an ingest token the bearer is the whole rule.
	tok := New(DB(fixtureDB(t)), IngestToken("s3cr3t")).Handler()
	if got := postTo(tok, "10.0.0.5:5", map[string]string{
		"Authorization": "Bearer s3cr3t", "X-Forwarded-For": "203.0.113.9", "Origin": "https://evil.example",
	}); got != http.StatusOK {
		t.Errorf("bearer through a proxy: %d, want 200", got)
	}
}

// TestCORSVaryOnEveryAnswer pins the audit's P3: Vary: Origin rode
// only the answers to allowed origins, so a shared cache could hand a
// header-less answer (made for a refused origin) to an allowed one.
func TestCORSVaryOnEveryAnswer(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Token("dev"))
	for _, origin := range []string{"https://evil.example", ""} {
		_, hdr, _ := getWith(t, h, "/weftbase/api/meta", "dev", origin)
		if !strings.Contains(strings.Join(hdr.Values("Vary"), ","), "Origin") {
			t.Errorf("Origin %q: Vary = %q, want Origin", origin, hdr.Values("Vary"))
		}
		if hdr.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("Origin %q was allowed", origin)
		}
	}
	// Setup A sends no CORS at all, Vary included.
	_, hdr, _ := getWith(t, Handler(DB(fixtureDB(t))), "/weftbase/api/meta", "", "https://evil.example")
	if v := hdr.Values("Vary"); len(v) != 0 {
		t.Errorf("setup A: Vary = %q, want none", v)
	}
}

// TestSSEHeadersDefeatProxyBuffering: an nginx in front buffers a
// response unless told not to; the live tail then arrives in bursts or
// not at all.
func TestSSEHeadersDefeatProxyBuffering(t *testing.T) {
	resp := liveSSE(t, Handler(DB(fixtureDB(t))), "?run=r_ok", "")
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
}

// TestRunsCursorSurvivesATiedPage: a database page may exceed the
// limit by the runs tied on the cursor time (they must ride along or
// the time cursor would skip them). The API derived next_before from
// "the page holds exactly limit rows", so such a page read as the last
// one and everything older became unreachable.
func TestRunsCursorSurvivesATiedPage(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	write := func(run, session string, at time.Time) {
		rec := fxRecord(run, "event", "run_start", 0, at, `{"type":"run_start","id":"`+run+`"}`)
		rec.Attrs["weft.session.id"] = session
		if err := db.Write(context.Background(), obsdb.Batch{Records: []obsdb.Record{rec}}); err != nil {
			t.Fatal(err)
		}
	}
	write("r_old", "s_old", at.Add(-time.Minute))
	for _, id := range []string{"a", "b", "c"} { // three runs in one instant
		write("r_tie_"+id, "s_tie_"+id, at)
	}
	h := Handler(DB(db))
	for _, list := range []string{"runs", "sessions"} {
		_, _, body := get(t, h, "/weftbase/api/"+list+"?limit=2")
		var page struct {
			Runs       []json.RawMessage `json:"runs"`
			Sessions   []json.RawMessage `json:"sessions"`
			NextBefore *time.Time        `json:"next_before"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		if n := len(page.Runs) + len(page.Sessions); n != 3 {
			t.Fatalf("%s: first page = %d rows, want the 3 tied ones (%s)", list, n, body)
		}
		if page.NextBefore == nil {
			t.Fatalf("%s: next_before is null on a page with an older row behind it: %s", list, body)
		}
		_, _, next := get(t, h, "/weftbase/api/"+list+"?limit=2&before="+page.NextBefore.Format(time.RFC3339Nano))
		if !strings.Contains(next, "_old") {
			t.Errorf("%s: the page after the cursor lacks the older row: %s", list, next)
		}
	}
}

// multiTurnBodies is a source run with prior context: its input record
// carries two earlier turns (one of them with a tool call, id ctx1),
// then the run's own four steps — step 0 calls c1, step 1 calls c1
// again and c2 (a deterministic model reuses ids), step 2 answers,
// a steered message arrives, step 3 answers again.
var multiTurnBodies = []string{
	`[{"role":"user","content":[{"type":"text","text":"q0"}]},
	  {"role":"assistant","content":[{"type":"text","text":"a0"}]},
	  {"role":"user","content":[{"type":"text","text":"q1"}]},
	  {"role":"assistant","content":[{"type":"tool_call","id":"ctx1","name":"lookup_order","args":{}}]},
	  {"role":"tool","content":[{"type":"tool_result","call_id":"ctx1","name":"lookup_order","content":"old","is_error":false}]},
	  {"role":"assistant","content":[{"type":"text","text":"a1"}]},
	  {"role":"user","content":[{"type":"text","text":"q2"}]}]`,
	`[{"role":"assistant","content":[{"type":"tool_call","id":"c1","name":"lookup_order","args":{}}]}]`,
	`[{"role":"tool","content":[{"type":"tool_result","call_id":"c1","name":"lookup_order","content":"r0","is_error":false}]}]`,
	`[{"role":"assistant","content":[{"type":"tool_call","id":"c1","name":"lookup_order","args":{}},{"type":"tool_call","id":"c2","name":"refund","args":{}}]}]`,
	`[{"role":"tool","content":[{"type":"tool_result","call_id":"c1","name":"lookup_order","content":"r1","is_error":false},{"type":"tool_result","call_id":"c2","name":"refund","content":"r2","is_error":false}]}]`,
	`[{"role":"assistant","content":[{"type":"text","text":"partial"}]}]`,
	`[{"role":"user","content":[{"type":"text","text":"steer"}]}]`,
	`[{"role":"assistant","content":[{"type":"text","text":"final"}]}]`,
}

// TestTranscriptBatchesNameTheirStepAndInput: the transcript route
// answered step 0 for every batch and did not say which batch is the
// run's input record, so a client had to guess — by shape — where the
// context ends and the run's own steps begin (the split from_step and
// transcript_edits count over). Each batch names its step and whether
// it is the input — as the record stored them (ADR 0028 §8), never
// inferred from the batches' order: here the stored steps disagree
// with what an assistant-order walk would derive, and the stored ones
// win. A record without weft.step.index answers step -1 with the
// not_recorded badge; the route never fills the hole itself.
func TestTranscriptBatchesNameTheirStepAndInput(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// input, then: step 0's call and result, step 1's, step 2's reply,
	// the steered message (delivered after step 2), step 3's reply —
	// stamped 7 for the last batch, which no order walk would say.
	stamped := []int{0, 0, 0, 1, 1, 2, 2, 7}
	var recs []obsdb.Record
	for i, body := range multiTurnBodies {
		r := fxRecord("run_multi", "messages", "", int64(i), time.Now().UTC(), body)
		r.Attrs["weft.step.index"] = int64(stamped[i])
		r.Attrs["weft.messages.input"] = i == 0
		recs = append(recs, r)
		bare := fxRecord("run_bare", "messages", "", int64(i), time.Now().UTC(), body)
		delete(bare.Attrs, "weft.step.index")
		delete(bare.Attrs, "weft.messages.input")
		recs = append(recs, bare)
	}
	if err := db.Write(context.Background(), obsdb.Batch{Records: recs}); err != nil {
		t.Fatal(err)
	}
	type batch struct {
		Index int64   `json:"index"`
		Step  int     `json:"step"`
		Input *bool   `json:"input"`
		Badge *string `json:"badge"`
	}
	read := func(run string) []batch {
		t.Helper()
		_, _, body := get(t, Handler(DB(db)), "/weftbase/api/runs/"+run+"/transcript")
		var doc struct {
			Batches []batch `json:"batches"`
		}
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Batches) != len(stamped) {
			t.Fatalf("%s: batches = %d, want %d", run, len(doc.Batches), len(stamped))
		}
		for i, b := range doc.Batches {
			if b.Input == nil {
				t.Fatalf("%s: batch %d carries no input field: %s", run, i, body)
			}
		}
		return doc.Batches
	}
	for i, b := range read("run_multi") {
		if b.Index != int64(i) || b.Step != stamped[i] || *b.Input != (i == 0) || b.Badge != nil {
			t.Errorf("batch %d = index %d step %d input %v badge %v, want index %d step %d input %v and no badge",
				i, b.Index, b.Step, *b.Input, b.Badge, i, stamped[i], i == 0)
		}
	}
	for i, b := range read("run_bare") {
		if b.Step != -1 || b.Badge == nil || *b.Badge != "not_recorded" || *b.Input {
			t.Errorf("unstamped batch %d = step %d input %v badge %v, want step -1, input false, badge not_recorded",
				i, b.Step, *b.Input, b.Badge)
		}
	}
}

// ── the third pass (2026-10-02, second review) ──────────────────────

// TestListCursorsWalkALongTie: obsdb bounds how far a page runs past
// its limit to finish a tie (MaxTies); a longer tie is cut, and only
// the (before, before_id) pair resumes inside it. The API took and
// returned the time alone, so a walk through 600+ runs (and sessions)
// sharing one instant skipped every row past the first cut — and the
// Go-internal pagers (live backfill, experiment detail) did the same.
func TestListCursorsWalkALongTie(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	const n = 650
	var recs []obsdb.Record
	for i := 0; i < n; i++ {
		run := fmt.Sprintf("r_tie_%04d", i)
		rec := fxRecord(run, "event", "run_start", 0, at, `{"type":"run_start","id":"`+run+`"}`)
		rec.Attrs["weft.session.id"] = fmt.Sprintf("s_tie_%04d", i)
		recs = append(recs, rec)
	}
	old := fxRecord("r_old", "event", "run_start", 0, at.Add(-time.Minute), `{"type":"run_start","id":"r_old"}`)
	old.Attrs["weft.session.id"] = "s_old"
	recs = append(recs, old)
	if err := db.Write(context.Background(), obsdb.Batch{Records: recs}); err != nil {
		t.Fatal(err)
	}
	h := Handler(DB(db))
	const limit = 50
	for _, list := range []string{"runs", "sessions"} {
		seen := map[string]int{}
		query := fmt.Sprintf("/weftbase/api/%s?limit=%d", list, limit)
		for pages := 0; ; pages++ {
			if pages > 100 {
				t.Fatalf("%s: the walk never ended", list)
			}
			code, _, body := get(t, h, query)
			if code != http.StatusOK {
				t.Fatalf("%s: %d %s", list, code, body)
			}
			var page struct {
				Runs         []struct{ ID string } `json:"runs"`
				Sessions     []struct{ ID string } `json:"sessions"`
				NextBefore   *time.Time            `json:"next_before"`
				NextBeforeID *string               `json:"next_before_id"`
			}
			if err := json.Unmarshal([]byte(body), &page); err != nil {
				t.Fatal(err)
			}
			rows := append(page.Runs, page.Sessions...)
			if len(rows) > limit+obsdb.MaxTies {
				t.Errorf("%s: a page of %d rows, past limit+MaxTies", list, len(rows))
			}
			for _, r := range rows {
				seen[r.ID]++
			}
			if page.NextBefore == nil {
				if page.NextBeforeID != nil {
					t.Errorf("%s: next_before_id %q without next_before", list, *page.NextBeforeID)
				}
				break
			}
			query = fmt.Sprintf("/weftbase/api/%s?limit=%d&before=%s", list, limit, page.NextBefore.Format(time.RFC3339Nano))
			if page.NextBeforeID != nil {
				query += "&before_id=" + url.QueryEscape(*page.NextBeforeID)
			}
		}
		if len(seen) != n+1 {
			t.Errorf("%s: the walk saw %d distinct rows, want %d", list, len(seen), n+1)
		}
		for id, c := range seen {
			if c != 1 {
				t.Errorf("%s: %s listed %d times", list, id, c)
			}
		}
	}

	// The Go-internal pager: the live backfill of a public id. 1 100
	// runs in one instant: past one internal page (500) plus its
	// MaxTies ride-along.
	const big = 1100
	exp := make([]obsdb.Record, 0, big)
	for i := 0; i < big; i++ {
		run := fmt.Sprintf("r_exp_%04d", i)
		rec := fxRecord(run, "event", "run_start", 0, at.Add(time.Hour), `{"type":"run_start","id":"`+run+`"}`)
		rec.Attrs["weft.public_id"] = "pub_tie"
		exp = append(exp, rec)
	}
	if err := db.Write(context.Background(), obsdb.Batch{Records: exp}); err != nil {
		t.Fatal(err)
	}
	hp := New(DB(db)).Handler()

	// The live backfill of a resumed public-id stream replays every run.
	resp := subscribeLive(t, hp, "?public_id=pub_tie", "1")
	frames := readSSE(t, resp, big, 20*time.Second)
	runs := map[string]bool{}
	for _, f := range frames {
		if f.event != "record" {
			continue
		}
		var rec struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal([]byte(f.data), &rec) == nil {
			runs[rec.RunID] = true
		}
	}
	if len(runs) != big {
		t.Errorf("live backfill replayed %d runs, want %d", len(runs), big)
	}
}
