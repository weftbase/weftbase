package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weft/wefttest"
	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// The honesty surface (plan A3, ADR 0028 §11): events and live frames
// carry the weft.content.* attributes the destination's chain stamped,
// and the run document carries the run's own holes, each with the
// reason and fix of obsdb.HoleNote's table.

// recordAttrsRun drives one tool call whose result is longer than a
// small cap, then an answer, through the pipeline into server.
func recordAttrsRun(t *testing.T, url, runID string, dest ...otel.DestOption) {
	t.Helper()
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(url, "", dest...), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	type in struct {
		Q string `json:"q" jsonschema:"the query"`
	}
	logs := core.Tool("read_logs", "Read the logs.", func(context.Context, in) (string, error) {
		return strings.Repeat("log line ", 12), nil
	})
	agent := core.New(wefttest.Script(
		wefttest.ToolCalls(wefttest.Call{Name: "read_logs", Args: `{"q":"1"}`, ID: "c1"}),
		wefttest.Say("ok"),
	), core.Name("logs"), core.Instructions("Read logs."), logs,
		core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := agent.Generate(ctx, core.RunID(runID), core.Prompt("read")); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type attrsPage struct {
	Events []struct {
		Pos   int64           `json:"pos"`
		Event json.RawMessage `json:"event"`
		Attrs map[string]any  `json:"attrs"`
	} `json:"events"`
}

type holesDoc struct {
	Status string `json:"status"`
	Holes  []struct {
		Hole   string `json:"hole"`
		Reason string `json:"reason"`
		Fix    string `json:"fix"`
	} `json:"holes"`
}

// msNorm puts a fixed 1 for the measured milliseconds a real run's
// events stamp (latency_ms, ttft_ms: wall clock, 1 on one run and 5 on
// the next), as stepNorm does, so the events goldens are byte-stable
// (P8-fix-b #18). The JSON type is kept; the test reads no value.
func msNorm(body string) string {
	return msRe.ReplaceAllString(body, `"$1":1`)
}

var msRe = regexp.MustCompile(`"(latency_ms|ttft_ms)":\s*[0-9]+`)

// TestEventAttrs: a capped run's events carry
// weft.content.truncated_bytes where the cap cut (the tool result),
// and nothing where it did not; a content-off run's events all carry
// weft.content = stripped, and its run document badges the run
// stripped. Only weft.content.* keys ride along.
func TestEventAttrs(t *testing.T) {
	ts, _ := requestsServer(t)
	recordAttrsRun(t, ts.URL, "r_cap", otel.WithContent(otel.ContentConfig{MaxBytes: 16}))
	recordAttrsRun(t, ts.URL, "r_strip", otel.NoContent())
	done := func(b string) bool { return strings.Contains(b, `"type":"run_finish"`) }

	capped := fetchJSON(t, ts, "/api/runs/r_cap/events?limit=1000", done)
	requestsGolden(t, "events-capped.golden.json", msNorm(capped))
	var cp attrsPage
	decode(t, capped, &cp)
	cut := 0
	for _, e := range cp.Events {
		var head struct{ Type string }
		_ = json.Unmarshal(e.Event, &head)
		for k := range e.Attrs {
			if k != "weft.content.truncated_bytes" {
				t.Errorf("capped event %d carries attr %q", e.Pos, k)
			}
		}
		if n, ok := e.Attrs["weft.content.truncated_bytes"].(float64); ok {
			cut++
			if n <= 0 {
				t.Errorf("event %d truncated_bytes = %v", e.Pos, n)
			}
		}
		if head.Type == "tool_finish" && e.Attrs == nil {
			t.Errorf("the capped tool result carries no truncated_bytes: %s", e.Event)
		}
		if head.Type == "run_start" && e.Attrs != nil {
			t.Errorf("run_start was not cut, yet carries attrs %v", e.Attrs)
		}
	}
	if cut == 0 {
		t.Fatalf("no capped event: %s", capped)
	}

	stripped := fetchJSON(t, ts, "/api/runs/r_strip/events?limit=1000", done)
	requestsGolden(t, "events-stripped.golden.json", msNorm(stripped))
	var sp attrsPage
	decode(t, stripped, &sp)
	for _, e := range sp.Events {
		if len(e.Attrs) != 1 || e.Attrs["weft.content"] != "stripped" {
			t.Errorf("content-off event %d attrs = %v, want {weft.content: stripped}", e.Pos, e.Attrs)
		}
	}

	var doc holesDoc
	decode(t, fetchJSON(t, ts, "/api/runs/r_strip", nil), &doc)
	reason, fix := obsdb.HoleNote(obsdb.HoleStripped)
	if len(doc.Holes) != 1 || doc.Holes[0].Hole != "stripped" || doc.Holes[0].Reason != reason || doc.Holes[0].Fix != fix {
		t.Errorf("content-off run holes = %+v, want [stripped] with the table's words", doc.Holes)
	}
	decode(t, fetchJSON(t, ts, "/api/runs/r_cap", nil), &doc)
	if len(doc.Holes) != 0 {
		t.Errorf("capped run holes = %+v, want none (the cut is the events')", doc.Holes)
	}
}

// TestLiveFrameAttrs: a live record frame carries the record's
// weft.content.* attributes — a numeric string normalized to a number
// — and none of its other attributes.
func TestLiveFrameAttrs(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := obsdb.NewHub()
	h := Handler(DB(db), Live(hub))
	resp := subscribeLive(t, h, "?run=r_1", "")

	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	rec := fxRecord("r_1", "event", "tool_finish", 0, at, `{"type":"tool_finish","run_id":"r_1"}`)
	rec.Attrs["weft.content.truncated_bytes"] = "12595"
	plain := fxRecord("r_1", "event", "step_finish", 1, at, `{"type":"step_finish","run_id":"r_1"}`)
	off := fxRecord("r_1", "event", "run_finish", 2, at, `{"type":"run_finish","run_id":"r_1"}`)
	off.Attrs["weft.content"] = "stripped"
	for _, r := range []obsdb.Record{rec, plain, off} {
		hub.Publish(context.Background(), obsdb.RecordFrame(r))
	}
	frames := readSSE(t, resp, 3, 5*time.Second)
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, want := range []string{
		`"attrs":{"weft.content.truncated_bytes":12595}`,
		``,
		`"attrs":{"weft.content":"stripped"}`,
	} {
		if want == "" {
			if strings.Contains(frames[i].data, `"attrs"`) {
				t.Errorf("frame %d carries attrs: %s", i, frames[i].data)
			}
			continue
		}
		if !strings.Contains(frames[i].data, want) {
			t.Errorf("frame %d misses %s: %s", i, want, frames[i].data)
		}
	}
}

// TestRunHolesInterrupted: a run that stopped reporting reads
// interrupted, and the event position its lost batch left reads gap —
// in the table's order. A run whose invoke_agent span arrived and no
// record did is over — succeeded, never interrupted (contract §6) — and
// reads not_recorded in no_logger's words (the app exported spans
// only); a run with a chat span alone and no record never ended
// (interrupted) and reads the same not_recorded. Derived is a stock-span
// run's badge alone.
func TestRunHolesInterrupted(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	old := time.Now().Add(-time.Hour).UTC()
	sp := fxSpanRec("r_ok", old, old.Add(time.Second), 0, "", 1, 1)
	sp.Attrs["weft.run.id"] = "r_spans"
	sp.SpanID = "0a0b0c0d0e0f1011"
	chat := fxSpanRec("r_ok", old, old.Add(time.Second), 0, "", 1, 1)
	chat.Attrs["weft.run.id"] = "r_chat"
	chat.Attrs["gen_ai.operation.name"] = "chat"
	chat.Name, chat.SpanID = "chat script", "0a0b0c0d0e0f1012"
	if err := db.Write(context.Background(), obsdb.Batch{
		Records: []obsdb.Record{
			fxRecord("r_int", "event", "run_start", 0, old, `{"type":"run_start","id":"r_int"}`),
			fxRecord("r_int", "event", "step_start", 2, old, `{"type":"step_start","run_id":"r_int","index":0}`),
		},
		Spans: []obsdb.Span{sp, chat},
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(DB(db))
	noLogger, noLoggerFix := obsdb.HoleNoteFor(obsdb.HoleNotRecorded, obsdb.CauseNoLogger)
	for run, c := range map[string]struct {
		status string
		holes  []obsdb.Hole
	}{
		"r_int":   {"interrupted", []obsdb.Hole{obsdb.HoleInterrupted, obsdb.HoleGap}},
		"r_spans": {"succeeded", []obsdb.Hole{obsdb.HoleNotRecorded}},
		"r_chat":  {"interrupted", []obsdb.Hole{obsdb.HoleInterrupted, obsdb.HoleNotRecorded}},
	} {
		want := c.holes
		code, _, body := get(t, h, "/weftbase/api/runs/"+run)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", run, code, body)
		}
		var doc holesDoc
		decode(t, body, &doc)
		var got []string
		for _, hl := range doc.Holes {
			got = append(got, hl.Hole)
			if hl.Reason == "" {
				t.Errorf("%s: hole %s has no reason", run, hl.Hole)
			}
			if hl.Hole == string(obsdb.HoleNotRecorded) && (hl.Reason != noLogger || hl.Fix != noLoggerFix) {
				t.Errorf("%s: not_recorded = %q / %q, want no_logger's words", run, hl.Reason, hl.Fix)
			}
		}
		var wantS []string
		for _, w := range want {
			wantS = append(wantS, string(w))
		}
		if strings.Join(got, " ") != strings.Join(wantS, " ") || doc.Status != c.status {
			t.Errorf("%s (%s) holes = %v, want %v (%s)", run, doc.Status, got, wantS, c.status)
		}
	}
}

// TestContentOffAgent: an agent with core.Content(false) captures no
// content itself — its events carry weft.content = none through a
// content-on destination — and the run reads stripped from its
// run_start.
func TestContentOffAgent(t *testing.T) {
	ts, _ := requestsServer(t)
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, ""), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	agent := core.New(wefttest.Script(wefttest.Say("ok")), core.Name("quiet"), core.Content(false),
		core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := agent.Generate(ctx, core.RunID("r_none"), core.Prompt("hi")); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	body := fetchJSON(t, ts, "/api/runs/r_none/events?limit=1000", func(b string) bool { return strings.Contains(b, `"type":"run_finish"`) })
	var page attrsPage
	decode(t, body, &page)
	for _, e := range page.Events {
		if len(e.Attrs) != 1 || e.Attrs["weft.content"] != "none" {
			t.Errorf("event %d attrs = %v, want {weft.content: none}", e.Pos, e.Attrs)
		}
	}
	var doc holesDoc
	decode(t, fetchJSON(t, ts, "/api/runs/r_none", nil), &doc)
	if len(doc.Holes) != 1 || doc.Holes[0].Hole != "stripped" {
		t.Errorf("content-off agent run holes = %+v, want [stripped]", doc.Holes)
	}
}

// TestStepHolesFromAttrs: the assembled step reads its events' attrs —
// a capped tool result is truncated with the bytes cut; a content-off
// run written before the request record is stripped and not_recorded.
func TestStepHolesFromAttrs(t *testing.T) {
	ts, _ := requestsServer(t)
	recordAttrsRun(t, ts.URL, "r_cap", otel.WithContent(otel.ContentConfig{MaxBytes: 16}))
	fetchJSON(t, ts, "/api/runs/r_cap", func(b string) bool { return strings.Contains(b, `"status":"succeeded"`) })
	var sd struct {
		Holes []struct{ Hole, Reason, Fix string } `json:"holes"`
	}
	decode(t, fetchJSON(t, ts, "/api/runs/r_cap/steps/0", nil), &sd)
	_, fix := obsdb.HoleNote(obsdb.HoleTruncated)
	// The 16-byte cap cut the tool catalog (its specific reason comes
	// first) and the tool result (the events' cut, beside it); both
	// carry the table's truncated fix, once.
	if len(sd.Holes) == 0 || sd.Holes[0].Hole != "truncated" ||
		!strings.Contains(sd.Holes[0].Reason, "the step's tool catalog was cut by a destination's cap") ||
		!strings.Contains(sd.Holes[0].Reason, "92 bytes") || sd.Holes[0].Fix != fix {
		t.Errorf("capped step holes = %+v, want truncated: the catalog's reason and the events' 92 bytes, with the table's fix %q", sd.Holes, fix)
	}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Now().UTC()
	var recs []obsdb.Record
	for i, ev := range []struct{ typ, body string }{
		{"run_start", `{"type":"run_start","id":"r_off"}`},
		{"step_start", `{"type":"step_start","run_id":"r_off","index":0}`},
		{"step_finish", `{"type":"step_finish","run_id":"r_off","index":0,"reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}`},
		{"run_finish", `{"type":"run_finish","run_id":"r_off","steps":1}`},
	} {
		r := fxRecord("r_off", "event", ev.typ, int64(i), at, ev.body)
		delete(r.Attrs, "weft.instructions.hash") // written before the request record
		r.Attrs["weft.content"] = "stripped"
		recs = append(recs, r)
	}
	if err := db.Write(context.Background(), obsdb.Batch{Records: recs}); err != nil {
		t.Fatal(err)
	}
	code, _, body := get(t, Handler(DB(db)), "/weftbase/api/runs/r_off/steps/0")
	if code != http.StatusOK {
		t.Fatalf("step: %d %s", code, body)
	}
	decode(t, body, &sd)
	var got []string
	for _, hl := range sd.Holes {
		got = append(got, hl.Hole)
	}
	if !strings.Contains(" "+strings.Join(got, " ")+" ", " stripped ") || !strings.Contains(" "+strings.Join(got, " ")+" ", " not_recorded ") {
		t.Errorf("content-off pre-A1 step holes = %v, want stripped and not_recorded", got)
	}
}

// TestLiveBackfillAttrs: a resume's backfill frames (Last-Event-ID set,
// read from the database) carry the stored events' attrs as the live
// ones do.
func TestLiveBackfillAttrs(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	start := fxRecord("r_bf", "event", "run_start", 0, at, `{"type":"run_start","id":"r_bf"}`)
	start.Attrs["weft.content"] = "stripped"
	fin := fxRecord("r_bf", "event", "tool_finish", 1, at, `{"type":"tool_finish","run_id":"r_bf"}`)
	fin.Attrs["weft.content.truncated_bytes"] = int64(12595)
	plain := fxRecord("r_bf", "event", "step_finish", 2, at, `{"type":"step_finish","run_id":"r_bf"}`)
	if err := db.Write(context.Background(), obsdb.Batch{Records: []obsdb.Record{start, fin, plain}}); err != nil {
		t.Fatal(err)
	}
	h := Handler(DB(db), Live(obsdb.NewHub()))
	frames := readSSE(t, subscribeLive(t, h, "?run=r_bf", "1"), 3, 5*time.Second)
	if len(frames) != 3 {
		t.Fatalf("got %d backfill frames, want 3", len(frames))
	}
	for i, want := range []string{`"attrs":{"weft.content":"stripped"}`, `"attrs":{"weft.content.truncated_bytes":12595}`, ``} {
		if want == "" {
			if strings.Contains(frames[i].data, `"attrs"`) {
				t.Errorf("backfill frame %d carries attrs: %s", i, frames[i].data)
			}
		} else if !strings.Contains(frames[i].data, want) {
			t.Errorf("backfill frame %d misses %s: %s", i, want, frames[i].data)
		}
	}
}

// TestStepHolesBothCuts: a step with one tool result the core's
// MaxResultBytes cut and another a destination's cap cut names both
// causes on its one truncated badge — the core's first,
// with its weft.MaxResultBytes fix, then the destination's.
func TestStepHolesBothCuts(t *testing.T) {
	ts, _ := requestsServer(t)
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, "", otel.WithContent(otel.ContentConfig{MaxBytes: 48})), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	type in struct {
		Q string `json:"q" jsonschema:"the query"`
	}
	big := core.Tool("big", "Big.", func(context.Context, in) (string, error) { return strings.Repeat("x", 100), nil },
		core.MaxResultBytes(10)) // the core cuts it: 10 bytes and the marker, under the destination's 48
	long := core.Tool("long", "Long.", func(context.Context, in) (string, error) { return strings.Repeat("y", 100), nil })
	agent := core.New(wefttest.Script(
		wefttest.ToolCalls(
			wefttest.Call{Name: "big", Args: `{"q":"1"}`, ID: "c1"},
			wefttest.Call{Name: "long", Args: `{"q":"2"}`, ID: "c2"}),
		wefttest.Say("ok"),
	), core.Name("both"), big, long, core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := agent.Generate(ctx, core.RunID("r_both"), core.Prompt("go")); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	fetchJSON(t, ts, "/api/runs/r_both", func(b string) bool { return strings.Contains(b, `"status":"succeeded"`) })
	var sd struct {
		Holes []struct{ Hole, Reason, Fix string } `json:"holes"`
	}
	decode(t, fetchJSON(t, ts, "/api/runs/r_both/steps/0", nil), &sd)
	var tr *struct{ Hole, Reason, Fix string }
	for i := range sd.Holes {
		if sd.Holes[i].Hole == "truncated" {
			tr = &sd.Holes[i]
		}
	}
	if tr == nil {
		t.Fatalf("holes = %+v, want truncated", sd.Holes)
	}
	byCore, byDest := strings.Index(tr.Reason, "a tool result was cut by its result cap"), strings.Index(tr.Reason, "a destination's cap cut")
	if byCore < 0 || byDest < 0 || byCore > byDest {
		t.Errorf("truncated reason = %q, want the core's result cap, then the destination's cut", tr.Reason)
	}
	if !strings.HasPrefix(tr.Fix, "raise the tool's weft.MaxResultBytes") {
		t.Errorf("truncated fix = %q, want the core cut's weft.MaxResultBytes first", tr.Fix)
	}
}

// TestRunHolesReplayView: a replay whose prefix no request
// record placed — weftbase/runtime's weft.replay.view derived or unknown
// (ADR 0029 §2) — carries the derived hole on its run document, its
// reason naming the mark and the source step (weft.forked_from), with a
// fix; a replay on a placed prefix (transcript, compacted:<index>)
// carries none. The unknown run's document is the golden
// run-replay-view.golden.json the web's badge tables draw from.
func TestRunHolesReplayView(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := func(d time.Duration) time.Time { return fixtureT0.Add(d) }
	// r_derived/0/call_1 is a Subagent child of the derived replay: it
	// inherits the replay's metadata (core's Metadata), not its prefix.
	for run, view := range map[string]string{"r_unknown": "unknown", "r_derived": "derived", "r_view": "compacted:3", "r_plain": "transcript",
		"r_derived/0/call_1": "derived"} {
		recs := []obsdb.Record{
			fxRecord(run, "event", "run_start", 0, at(0), `{"type":"run_start","id":"`+run+`","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
			fxRecord(run, "event", "step_start", 1, at(100*time.Millisecond), `{"type":"step_start","run_id":"`+run+`","index":0}`),
			fxRecord(run, "event", "step_finish", 2, at(time.Second), `{"type":"step_finish","run_id":"`+run+`","index":0,"reason":"stop","usage":{"input_tokens":10,"output_tokens":4}}`),
			fxRecord(run, "event", "run_finish", 3, at(2*time.Second), `{"type":"run_finish","run_id":"`+run+`","usage":{"input_tokens":10,"output_tokens":4},"steps":1}`),
			fxRecord(run, "messages", "", 0, at(50*time.Millisecond), `[{"role":"user","content":[{"type":"text","text":"Where is order 42?"}]}]`),
			fxRecord(run, "messages", "", 1, at(time.Second), `[{"role":"assistant","content":[{"type":"text","text":"Shipped."}]}]`),
			fxRecord(run, "prompt", "", 0, at(60*time.Millisecond), `{"hash":"`+fxInstructionsHash+`","text":"You handle orders."}`),
			fxRecord(run, "tools", "", 0, at(60*time.Millisecond), `{"hash":"`+fxCatalogHash+`","tools":[]}`),
			fxRecord(run, "request", "", 0, at(60*time.Millisecond), `{"step":0,"attempt":1,"system_hash":"`+fxInstructionsHash+`","messages_ref":{"index":0,"count":1},`+
				`"tools":{"catalog_hash":"`+fxCatalogHash+`","names":[]},"sequential_tools":false,"params":{},"model":{"provider":"wefttest","name":"script"},"stream":true}`),
		}
		for i := range recs {
			recs[i].Attrs["weft.replay"] = "true"
			recs[i].Attrs["weft.forked_from"] = "r_src#2"
			recs[i].Attrs["weft.replay.view"] = view
			if parent, _, ok := strings.Cut(run, "/"); ok {
				recs[i].Attrs["weft.parent.run.id"] = parent
				recs[i].Attrs["weft.parent.call.id"] = "call_1"
			}
		}
		if err := db.Write(context.Background(), obsdb.Batch{Records: recs}); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(DB(db))
	for run, want := range map[string]string{
		"r_unknown":          "this replay's prefix is the transcript before step 2 of r_src (weft.replay.view = unknown): the runtime could not read that step's records (weftbase unreachable, content off, or a weftbase without ?step=), so a compaction view its model saw may be missing",
		"r_derived":          "this replay's prefix was cut from the transcript at step 2 of r_src (weft.replay.view = derived): no request record placed that step's messages, so a compaction view its model saw is not known",
		"r_view":             "",
		"r_plain":            "",
		"r_derived/0/call_1": "",
	} {
		code, _, body := get(t, h, "/weftbase/api/runs/"+run)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", run, code, body)
		}
		var doc holesDoc
		decode(t, body, &doc)
		var got []string
		for _, hl := range doc.Holes {
			if hl.Hole == "derived" {
				got = append(got, hl.Reason)
				if hl.Fix == "" {
					t.Errorf("%s: the derived hole has no fix", run)
				}
			}
		}
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("%s: holes %+v, want no derived hole", run, doc.Holes)
		case want != "" && (len(got) != 1 || got[0] != want):
			t.Errorf("%s: derived reasons %q, want %q", run, got, want)
		}
		if run == "r_unknown" {
			golden(t, "run-replay-view.golden.json", regexp.MustCompile(`"last_seen": ?"[^"]*"`).ReplaceAllString(body, `"last_seen":"(norm)"`))
		}
		if run == "r_derived" {
			// The parent's children[] row for the child: no hole either.
			var kids struct {
				Children []runRow `json:"children"`
			}
			decode(t, body, &kids)
			if len(kids.Children) != 1 || kids.Children[0].ID != "r_derived/0/call_1" {
				t.Fatalf("r_derived children = %+v, want the one subagent child", kids.Children)
			}
			for _, hl := range kids.Children[0].Holes {
				if hl.Hole == "derived" {
					t.Errorf("the child row carries the parent's derived hole: %+v", hl)
				}
			}
		}
	}
}
