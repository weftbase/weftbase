package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// The fixtures (plan §3, rebuilt on obsdb for the step-5 interim read
// path): a success with a tool call, a failure, a parent with a
// subagent (the child is its own run, linked by parent_run_id and
// parent_call_id), and a crash-orphaned "running" row whose stale
// last-seen reads interrupted. Each run is one obsdb Batch — the
// events, messages and invoke span a real pipeline would write — with
// every time pinned, so the goldens are byte-stable. The only
// normalization left is the run views' weft_version, the record's
// stamp; api/meta's own weft_version is version.Runtime, which reads
// the tag under go test, so meta.golden.json pins it unnormalized.

var fixtureTags = map[string]string{"cwd": "/tmp/demo"}

// fixtureT0 anchors every fixture time: 2020-01-01T09:00:00Z — far
// enough in the past that the derived status of the stale row reads
// interrupted at any future "now", so the goldens never flip.
var fixtureT0 = time.Date(2020, 1, 1, 9, 0, 0, 123000000, time.UTC)

const (
	fxTrace = "0102030405060708090a0b0c0d0e0f10"
	fxSpan  = "0102030405060708"
	// fxHash mirrors what core.New stamps at construction (the
	// manifest hash rides every record and span).
	fxHash = "sha256:demo-fleet"
	// The request record's hashes (ADR 0028): every fixture run's
	// run_start carries the instructions hash a current run does, and
	// r_ok's one model call has its request, prompt and tools records,
	// so the fixture goldens read like current runs (not not_recorded —
	// TestRequestsNotRecorded's hand-built file is the pre-A1 one).
	fxInstructionsHash = "9d1f0a5c3b7e2a4d6f8091b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6"
	fxCatalogHash      = "4e3d2c1b0a99887766554433221100ffeeddccbbaa0099887766554433221100"
)

// fxRecord builds one weft log record for run at time at: kind is
// event | messages, pos its durable position, body its wire JSON.
// The identity chain, the record contract attributes and the caller
// metadata a real run carries ride Attrs.
func fxRecord(run, kind, eventType string, pos int64, at time.Time, body string) obsdb.Record {
	attrs := map[string]any{
		"weft.run.id":        run,
		"weft.record":        kind,
		"weft.version":       "v0.6.0",
		"weft.manifest.hash": fxHash,
		"gen_ai.agent.name":  "orders",
	}
	switch kind {
	case "event":
		attrs["weft.event.type"] = eventType
		attrs["weft.event.pos"] = pos
		if eventType == "run_start" {
			attrs["weft.instructions.hash"] = fxInstructionsHash
		}
	case "request":
		attrs["weft.request.index"] = pos
		attrs["weft.step.index"] = int64(0)
		attrs["weft.attempt.index"] = int64(1)
		attrs["weft.content"] = "full"
		attrs["weft.system.hash"] = fxInstructionsHash
		attrs["weft.catalog.hash"] = fxCatalogHash
	case "prompt":
		attrs["weft.prompt.index"] = pos
		attrs["weft.step.index"] = int64(0)
		attrs["weft.system.hash"] = fxInstructionsHash
	case "tools":
		attrs["weft.tools.index"] = pos
		attrs["weft.step.index"] = int64(0)
		attrs["weft.catalog.hash"] = fxCatalogHash
	case "messages":
		// Every fixture run makes one model call: its input record and
		// its one assistant batch both joined at step 0.
		attrs["weft.messages.index"] = pos
		attrs["weft.step.index"] = int64(0)
		if pos == 0 {
			attrs["weft.messages.input"] = true
		}
	}
	for k, v := range fixtureTags {
		attrs[k] = v
	}
	return obsdb.Record{
		Time: at, TraceID: fxTrace, SpanID: fxSpan, Severity: 9,
		EventName: "weft." + kind, Body: body, Service: "weftbase-test",
		Attrs: attrs, Resource: map[string]any{"service.name": "weftbase-test"},
	}
}

// fxSpanRec builds a run's invoke_agent span: status 1 ok or 2 error,
// ending at end with the usage and step count the core reports.
func fxSpanRec(run string, start, end time.Time, status int, statusMsg string, inTok, outTok int64) obsdb.Span {
	spanID := map[string]string{
		"r_ok": fxSpan, "r_fail": "0203040506070809", "r_sub": "030405060708090a",
	}[run]
	attrs := map[string]any{
		"gen_ai.operation.name":      "invoke_agent",
		"weft.run.id":                run,
		"gen_ai.agent.name":          "orders",
		"gen_ai.provider.name":       "wefttest",
		"gen_ai.request.model":       "script",
		"gen_ai.usage.input_tokens":  inTok,
		"gen_ai.usage.output_tokens": outTok,
		"weft.run.steps":             int64(1),
		"weft.version":               "v0.6.0",
		"weft.manifest.hash":         fxHash,
	}
	for k, v := range fixtureTags {
		attrs[k] = v
	}
	if spanID == "" {
		spanID = fxSpan
	}
	return obsdb.Span{
		TraceID: fxTrace, SpanID: spanID, Name: "invoke_agent orders", Kind: 1,
		Start: start, End: end, StatusCode: status, StatusMessage: statusMsg,
		Service: "weftbase-test", Attrs: attrs, Resource: map[string]any{"service.name": "weftbase-test"},
	}
}

// sessionOf maps each fixture run onto its thread identity: r_ok,
// r_fail and r_stale are turns 1..3 of one session (pub_orders);
// r_sub is a session of its own (pub_research); the subagent child
// runs under its parent, not a session.
var sessionOf = map[string]struct {
	session, public, agent string
	turn                   int
}{
	"r_ok":    {"s_orders", "pub_orders", "orders", 1},
	"r_fail":  {"s_orders", "pub_orders", "orders", 2},
	"r_stale": {"s_orders", "pub_orders", "orders", 3},
	"r_sub":   {"s_research", "pub_research", "researcher", 1},
}

// stampSession writes a run's thread identity onto its records and
// spans (what thread's runMetadata puts there).
func stampSession(recs []obsdb.Record, spans []obsdb.Span, run string) ([]obsdb.Record, []obsdb.Span) {
	id, ok := sessionOf[run]
	if !ok {
		return recs, spans
	}
	for i := range recs {
		recs[i].Attrs["weft.session.id"] = id.session
		recs[i].Attrs["weft.public_id"] = id.public
		recs[i].Attrs["weft.turn"] = id.turn
		recs[i].Attrs["gen_ai.agent.name"] = id.agent
	}
	for i := range spans {
		spans[i].Attrs["weft.session.id"] = id.session
		spans[i].Attrs["weft.public_id"] = id.public
		spans[i].Attrs["weft.turn"] = id.turn
		spans[i].Attrs["gen_ai.agent.name"] = id.agent
		// The span's name as the core writes it: "<operation> <agent>".
		spans[i].Name = "invoke_agent " + id.agent
	}
	return recs, spans
}

// fixtureDB writes the four fixture runs into one in-memory obsdb.
//
//   - r_ok: one tool call, then the answer (t0 .. t0+2s).
//   - r_fail: the model stream fails mid-run; the partial transcript
//     and the error text are part of the row (t0+1m .. +2s).
//   - r_sub: a parent that delegates to a researcher subagent; the
//     child run records itself and links back (t0+2m .. +5s, child
//     +3s .. +4s). The child's id carries slashes (childRunID).
//   - r_stale: a hand-built crash orphan — no run_finish, no span, a
//     last-seen minutes stale, events mid-step (t0+3m).
func fixtureDB(t *testing.T) obsdb.DB {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	okAt := func(d time.Duration) time.Time { return fixtureT0.Add(d) }

	// r_ok
	okEvents := []obsdb.Record{
		fxRecord("r_ok", "event", "run_start", 0, okAt(0),
			`{"type":"run_start","id":"r_ok","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
		fxRecord("r_ok", "event", "step_start", 1, okAt(300*time.Millisecond),
			`{"type":"step_start","run_id":"r_ok","index":0}`),
		fxRecord("r_ok", "event", "tool_start", 2, okAt(500*time.Millisecond),
			`{"type":"tool_start","run_id":"r_ok","seq":1,"call_id":"call_1","name":"lookup_order","args":{"order_id":"42"}}`),
		fxRecord("r_ok", "event", "tool_finish", 3, okAt(time.Second),
			`{"type":"tool_finish","run_id":"r_ok","seq":1,"call_id":"call_1","name":"lookup_order","content":"order 42: shipped","is_error":false}`),
		fxRecord("r_ok", "event", "step_finish", 4, okAt(1500*time.Millisecond),
			`{"type":"step_finish","run_id":"r_ok","index":0,"reason":"stop","usage":{"input_tokens":10,"output_tokens":4}}`),
		fxRecord("r_ok", "event", "run_finish", 5, okAt(2*time.Second),
			`{"type":"run_finish","run_id":"r_ok","usage":{"input_tokens":10,"output_tokens":4},"steps":1}`),
		fxRecord("r_ok", "messages", "", 0, okAt(100*time.Millisecond),
			`[{"role":"user","content":[{"type":"text","text":"Where is order 42?"}]}]`),
		fxRecord("r_ok", "messages", "", 1, okAt(2*time.Second),
			`[{"role":"assistant","content":[{"type":"text","text":"Order 42 shipped this morning."}]}]`),
		// Step 0's model call: its prompt and catalog, then the request.
		fxRecord("r_ok", "prompt", "", 0, okAt(200*time.Millisecond),
			`{"hash":"`+fxInstructionsHash+`","text":"You handle orders."}`),
		fxRecord("r_ok", "tools", "", 0, okAt(200*time.Millisecond),
			`{"hash":"`+fxCatalogHash+`","tools":[{"name":"lookup_order","description":"Look up an order by ID.",`+
				`"schema":{"type":"object","properties":{"order_id":{"type":"string","description":"the order to look up"}},"required":["order_id"]},`+
				`"timeout_ms":0,"approval":false,"replay":"never","max_result_bytes":65536,"sequential":false,"source":"local"}]}`),
		fxRecord("r_ok", "request", "", 0, okAt(200*time.Millisecond),
			`{"step":0,"attempt":1,"system_hash":"`+fxInstructionsHash+`","messages_ref":{"index":0,"count":1},`+
				`"tools":{"catalog_hash":"`+fxCatalogHash+`","names":["lookup_order"]},"sequential_tools":false,"params":{},`+
				`"model":{"provider":"wefttest","name":"script"},"stream":true}`),
	}
	okEvents, okSpans := stampSession(okEvents, []obsdb.Span{
		fxSpanRec("r_ok", okAt(0), okAt(2*time.Second), 1, "", 10, 4)}, "r_ok")
	if err := db.Write(ctx, obsdb.Batch{Records: okEvents, Spans: okSpans}); err != nil {
		t.Fatal(err)
	}
	// r_ok's app logs (plan A7): two lines the tool handler logged
	// through the app's own logger, under the run's span — non-weft
	// records, kept in other_logs.
	if err := db.Write(ctx, obsdb.Batch{Records: []obsdb.Record{
		{Time: okAt(600 * time.Millisecond), TraceID: fxTrace, SpanID: fxSpan, Severity: 9,
			Body: "looking up order 42", Service: "weftbase-test",
			Attrs: map[string]any{"order_id": "42"}, Resource: map[string]any{"service.name": "weftbase-test"}},
		{Time: okAt(800 * time.Millisecond), TraceID: fxTrace, SpanID: fxSpan, Severity: 13,
			Body: "orders cache miss", Service: "weftbase-test",
			Attrs: map[string]any{"cache": "orders"}, Resource: map[string]any{"service.name": "weftbase-test"}},
	}}); err != nil {
		t.Fatal(err)
	}

	// r_fail
	failAt := func(d time.Duration) time.Time { return fixtureT0.Add(time.Minute + d) }
	failEvents := []obsdb.Record{
		fxRecord("r_fail", "event", "run_start", 0, failAt(0),
			`{"type":"run_start","id":"r_fail","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
		fxRecord("r_fail", "event", "step_start", 1, failAt(300*time.Millisecond),
			`{"type":"step_start","run_id":"r_fail","index":0}`),
		fxRecord("r_fail", "event", "tool_start", 2, failAt(500*time.Millisecond),
			`{"type":"tool_start","run_id":"r_fail","seq":1,"call_id":"call_2","name":"lookup_order","args":{"order_id":"43"}}`),
		fxRecord("r_fail", "event", "tool_finish", 3, failAt(time.Second),
			`{"type":"tool_finish","run_id":"r_fail","seq":1,"call_id":"call_2","name":"lookup_order","content":"order 43: backordered","is_error":false}`),
		fxRecord("r_fail", "messages", "", 0, failAt(100*time.Millisecond),
			`[{"role":"user","content":[{"type":"text","text":"Where is order 43?"}]}]`),
	}
	failEvents, failSpans := stampSession(failEvents, []obsdb.Span{
		fxSpanRec("r_fail", failAt(0), failAt(2*time.Second), 2, "wefttest: injected provider 500", 8, 2)}, "r_fail")
	// A thread summarizer call filed under r_fail (record contract §3):
	// its usage is the session row's summarizer_usage (§6), never r_fail's.
	failSpans = append(failSpans, obsdb.Span{
		TraceID: fxTrace, SpanID: "0203040506070810", ParentSpanID: "0203040506070809",
		Name: "chat summary-mini", Kind: 3,
		Start: failAt(1500 * time.Millisecond), End: failAt(1600 * time.Millisecond), StatusCode: 1,
		Service: "weftbase-test",
		Attrs: map[string]any{
			"gen_ai.operation.name":      "chat",
			obsdb.AttrChatRole:           obsdb.ChatRoleSummarizer,
			"weft.run.id":                "r_fail",
			"gen_ai.provider.name":       "xai",
			"gen_ai.request.model":       "summary-mini",
			"gen_ai.usage.input_tokens":  int64(120),
			"gen_ai.usage.output_tokens": int64(7),
		},
		Resource: map[string]any{"service.name": "weftbase-test"},
	})
	if err := db.Write(ctx, obsdb.Batch{Records: failEvents, Spans: failSpans}); err != nil {
		t.Fatal(err)
	}

	// r_sub and its child. The parent's durable stream carries no
	// Nested wrappers — the child is its own run, joined by
	// parent_run_id / parent_call_id (S4.3's fold input, step 6).
	subAt := func(d time.Duration) time.Time { return fixtureT0.Add(2*time.Minute + d) }
	const childID = "r_sub/0/call_3"
	subEvents := []obsdb.Record{
		fxRecord("r_sub", "event", "run_start", 0, subAt(0),
			`{"type":"run_start","id":"r_sub","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
		fxRecord("r_sub", "event", "step_start", 1, subAt(300*time.Millisecond),
			`{"type":"step_start","run_id":"r_sub","index":0}`),
		fxRecord("r_sub", "event", "tool_start", 2, subAt(500*time.Millisecond),
			`{"type":"tool_start","run_id":"r_sub","seq":1,"call_id":"call_3","name":"research","args":{"prompt":"status of order 42"}}`),
		fxRecord("r_sub", "event", "tool_finish", 3, subAt(4*time.Second),
			`{"type":"tool_finish","run_id":"r_sub","seq":1,"call_id":"call_3","name":"research","content":"order 42 shipped this morning","is_error":false}`),
		fxRecord("r_sub", "event", "step_finish", 4, subAt(4500*time.Millisecond),
			`{"type":"step_finish","run_id":"r_sub","index":0,"reason":"stop","usage":{"input_tokens":12,"output_tokens":6}}`),
		fxRecord("r_sub", "event", "run_finish", 5, subAt(5*time.Second),
			`{"type":"run_finish","run_id":"r_sub","usage":{"input_tokens":12,"output_tokens":6},"steps":1}`),
		fxRecord("r_sub", "messages", "", 0, subAt(100*time.Millisecond),
			`[{"role":"user","content":[{"type":"text","text":"Where is order 42?"}]}]`),
		fxRecord("r_sub", "messages", "", 1, subAt(5*time.Second),
			`[{"role":"assistant","content":[{"type":"text","text":"Order 42 shipped."}]}]`),
	}
	subEvents, subSpans := stampSession(subEvents, []obsdb.Span{
		fxSpanRec("r_sub", subAt(0), subAt(5*time.Second), 1, "", 12, 6)}, "r_sub")
	if err := db.Write(ctx, obsdb.Batch{Records: subEvents, Spans: subSpans}); err != nil {
		t.Fatal(err)
	}
	childEvents := []obsdb.Record{
		fxRecord(childID, "event", "run_start", 0, subAt(3*time.Second),
			`{"type":"run_start","id":"`+childID+`","model":{"provider":"openai","name":"kimi-k2.7-code"},"agent":"researcher"}`),
		fxRecord(childID, "event", "step_start", 1, subAt(3200*time.Millisecond),
			`{"type":"step_start","run_id":"`+childID+`","index":0}`),
		fxRecord(childID, "event", "step_finish", 2, subAt(3700*time.Millisecond),
			`{"type":"step_finish","run_id":"`+childID+`","index":0,"reason":"stop","usage":{"input_tokens":9,"output_tokens":5}}`),
		fxRecord(childID, "event", "run_finish", 3, subAt(4*time.Second),
			`{"type":"run_finish","run_id":"`+childID+`","usage":{"input_tokens":9,"output_tokens":5},"steps":1}`),
		fxRecord(childID, "messages", "", 0, subAt(3*time.Second),
			`[{"role":"user","content":[{"type":"text","text":"status of order 42"}]}]`),
		fxRecord(childID, "messages", "", 1, subAt(4*time.Second),
			`[{"role":"assistant","content":[{"type":"text","text":"order 42 shipped this morning"}]}]`),
	}
	// The child's records carry the parent link the subagent stamps.
	for i := range childEvents {
		childEvents[i].Attrs["weft.parent.run.id"] = "r_sub"
		childEvents[i].Attrs["weft.parent.call.id"] = "call_3"
		childEvents[i].Attrs["gen_ai.agent.name"] = "researcher"
	}
	if err := db.Write(ctx, obsdb.Batch{Records: childEvents, Spans: []obsdb.Span{
		{TraceID: fxTrace, SpanID: "0a0b0c0d0e0f0102", ParentSpanID: fxSpan,
			Name: "invoke_agent researcher", Kind: 1,
			Start: subAt(3 * time.Second), End: subAt(4 * time.Second), StatusCode: 1,
			Service: "weftbase-test",
			Attrs: map[string]any{
				"gen_ai.operation.name":      "invoke_agent",
				"weft.run.id":                childID,
				"weft.parent.run.id":         "r_sub",
				"weft.parent.call.id":        "call_3",
				"gen_ai.agent.name":          "researcher",
				"gen_ai.provider.name":       "openai",
				"gen_ai.request.model":       "kimi-k2.7-code",
				"gen_ai.usage.input_tokens":  int64(9),
				"gen_ai.usage.output_tokens": int64(5),
				"weft.run.steps":             int64(1),
				"weft.version":               "v0.6.0",
				"weft.manifest.hash":         fxHash,
				"cwd":                        "/tmp/demo",
			},
			Resource: map[string]any{"service.name": "weftbase-test"}},
	}}); err != nil {
		t.Fatal(err)
	}

	// r_stale: the crash orphan. No run_finish, no span; its two events
	// are all the table ever learns.
	staleStart := fixtureT0.Add(3 * time.Minute)
	stale := []obsdb.Record{
		fxRecord("r_stale", "event", "run_start", 0, staleStart,
			`{"type":"run_start","id":"r_stale","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
		fxRecord("r_stale", "event", "step_start", 1, staleStart.Add(200*time.Millisecond),
			`{"type":"step_start","run_id":"r_stale","index":0}`),
	}
	stale, _ = stampSession(stale, nil, "r_stale")
	if err := db.Write(ctx, obsdb.Batch{Records: stale}); err != nil {
		t.Fatal(err)
	}
	return db
}

// fixtureManifest is a minimal manifest document for the manifest
// endpoint's golden — pass-through bytes, so a literal is the honest
// fixture.
const fixtureManifest = `{
  "weft": 1,
  "agents": [
    {
      "name": "orders",
      "model": {"provider": "wefttest", "name": "script"},
      "instructions": "You handle orders.",
      "policy": {"parallelism": 4, "max_steps": 10, "max_result_bytes": 65536, "max_model_retries": 3},
      "tools": [
        {
          "name": "lookup_order",
          "description": "Look up an order by ID.",
          "input_schema": {"type": "object", "properties": {"order_id": {"type": "string", "description": "the order to look up"}}, "required": ["order_id"]}
        }
      ]
    }
  ]
}
`

var weftVersionRe = regexp.MustCompile(`"weft_version": ?"[^"]*"`)

// get issues a GET against a handler mounted as the docs show and
// returns status, headers, and the body.
func get(t *testing.T, h http.Handler, path string) (int, http.Header, string) {
	t.Helper()
	srv := httptest.NewServer(mounted(h))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// mounted wraps a Handler in the mux the doc comment shows, so tests
// exercise the same path shape users mount (StripPrefix "/weftbase").
func mounted(h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/weftbase/", http.StripPrefix("/weftbase", h))
	return mux
}

// post issues a POST with a body against a mounted handler.
func post(t *testing.T, h http.Handler, path, ctype, body string) (int, http.Header, string) {
	t.Helper()
	srv := httptest.NewServer(mounted(h))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+path, ctype, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// pretty re-indents a compact JSON body so goldens read like the API.
func pretty(t *testing.T, body string) string {
	t.Helper()
	out := weftVersionRe.ReplaceAllString(indent(t, body), `"weft_version": "(test)"`)
	// A chain's figures are measured on the wall clock (a wait lasts
	// until now, a fixture's park is "just now"): pinned by shape.
	out = chainMSRe.ReplaceAllString(out, `"duration_ms": 0,${1}"waiting_ms": 0`)
	return waitTimeRe.ReplaceAllString(out, `"${1}": "(time)"`)
}

var (
	chainMSRe  = regexp.MustCompile(`"duration_ms": \d+,(\s*)"waiting_ms": \d+`)
	waitTimeRe = regexp.MustCompile(`"(from|to)": "\d{4}-[^"]*"`)
)

// indent re-indents a compact JSON body, normalizing nothing.
func indent(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(body), "", "  "); err != nil {
		t.Fatalf("indent: %v", err)
	}
	return buf.String() + "\n"
}

func golden(t *testing.T, name, body string) {
	t.Helper()
	wefttest.Golden(t, "testdata/v1/"+name, []byte(pretty(t, body)))
}

// metaPIDRe finds api/meta's pid in an indented body.
var metaPIDRe = regexp.MustCompile(`"pid": \d+`)

func TestMetaGolden(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	code, _, body := get(t, h, "/weftbase/v1/meta")
	if code != http.StatusOK {
		t.Fatalf("meta: %d", code)
	}
	if !strings.Contains(body, `"db":{"kind":"sqlite"`) || !strings.Contains(body, `"interrupted_after_ms":30000`) {
		t.Errorf("meta db kind / clock: %s", body)
	}
	// pid (plan B2) is this test process's: a loopback caller with no
	// Token reads it, under db.path's rule; the golden normalizes it
	// to 1234 (a number: the shape test compares leaf types).
	if want := fmt.Sprintf(`"pid":%d`, os.Getpid()); !strings.Contains(body, want) {
		t.Errorf("meta pid: %s, want %s", body, want)
	}
	// Not golden(): weft_version is pinned too (version.Runtime is the
	// tag under go test; the release bump regenerates this file).
	wefttest.Golden(t, "testdata/v1/meta.golden.json",
		[]byte(metaPIDRe.ReplaceAllString(indent(t, body), `"pid": 1234`)))

	// Capabilities are the optional features the kernel declares: none
	// by default, the declared ones otherwise. The principal is the
	// caller's (the tests' Local one in the default scope).
	_, _, plain := get(t, Handler(DB(fixtureDB(t))), "/weftbase/v1/meta")
	if !strings.Contains(plain, `"capabilities":[]`) {
		t.Errorf("default capabilities = %s, want []", plain)
	}
	if !strings.Contains(plain, `"principal":{"kind":"local","id":"local","scope":{"project":"prj_default","env":"test","tenant":"ten_project"},"all_tenants":true}`) {
		t.Errorf("meta principal = %s", plain)
	}
	_, _, caps := get(t, Handler(DB(fixtureDB(t)), Capabilities("ingest", "clickhouse")), "/weftbase/v1/meta")
	if !strings.Contains(caps, `"capabilities":["ingest","clickhouse"]`) {
		t.Errorf("declared capabilities = %s", caps)
	}
}

func TestRunsGolden(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	code, _, body := get(t, h, "/weftbase/v1/runs")
	if code != http.StatusOK {
		t.Fatalf("runs: %d", code)
	}
	// Top-level only, newest first: the stale crash-orphan reads
	// interrupted and is shown (A1), the child never floods the list.
	for _, want := range []string{
		`"id":"r_stale"`, `"status":"interrupted"`,
		`"id":"r_sub"`, `"id":"r_fail"`, `"id":"r_ok"`,
		`"total":4`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("runs list missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, `"r_sub/`) {
		t.Error("runs list includes a child run")
	}
	// The row's metadata (what the store era called tags).
	if !strings.Contains(body, `"meta":{"cwd":"/tmp/demo"}`) {
		t.Errorf("runs list missing metadata: %s", body)
	}
	golden(t, "runs.golden.json", body)
}

// TestRunsParentFilter pins the runs list's subagent filters (plan
// A10): the default is top-level only; parent=<run id> lists that
// run's children (golden); parent=* and all=1 list every run, children
// included; a parent id beside all=1 wins; a malformed all is a 400.
func TestRunsParentFilter(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	ids := func(path string) []string {
		t.Helper()
		code, _, body := get(t, h, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		var raw struct {
			Runs []map[string]any `json:"runs"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range raw.Runs {
			out = append(out, r["id"].(string))
		}
		return out
	}
	has := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}
	if got := ids("/weftbase/v1/runs"); has(got, "r_sub/0/call_3") || !has(got, "r_sub") {
		t.Errorf("default = %v, want top-level only", got)
	}
	if got := ids("/weftbase/v1/runs?parent=r_sub"); len(got) != 1 || got[0] != "r_sub/0/call_3" {
		t.Errorf("parent=r_sub = %v, want its one child", got)
	}
	for _, q := range []string{"parent=*", "all=1", "all=true"} {
		if got := ids("/weftbase/v1/runs?" + q); !has(got, "r_sub/0/call_3") || !has(got, "r_sub") {
			t.Errorf("%s = %v, want every run, the child included", q, got)
		}
	}
	if got := ids("/weftbase/v1/runs?all=0"); has(got, "r_sub/0/call_3") {
		t.Errorf("all=0 = %v, want top-level only", got)
	}
	if got := ids("/weftbase/v1/runs?all=1&parent=r_sub"); len(got) != 1 || got[0] != "r_sub/0/call_3" {
		t.Errorf("all=1&parent=r_sub = %v, want the parent filter to win", got)
	}
	if code, _, body := get(t, h, "/weftbase/v1/runs?all=maybe"); code != http.StatusBadRequest || !strings.Contains(body, `"bad_request"`) {
		t.Errorf("all=maybe = %d %s, want 400", code, body)
	}
	_, _, body := get(t, h, "/weftbase/v1/runs?parent=r_sub")
	golden(t, "runs-children.golden.json", body)
}

func TestRunGolden(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	code, _, body := get(t, h, "/weftbase/v1/runs/r_sub")
	if code != http.StatusOK {
		t.Fatalf("run: %d", code)
	}
	// The document carries children but never events (ADR 0018 §8),
	// and event_count sizes the replay scrubber (plan §3).
	if strings.Contains(body, `"events"`) {
		t.Error("run document carries inline events")
	}
	if !strings.Contains(body, `"parent_run_id":"r_sub"`) {
		t.Errorf("run document misses children: %s", body)
	}
	if !strings.Contains(body, `"event_count":6`) || !strings.Contains(body, `"message_count":2`) {
		t.Errorf("run document misses the counts: %s", body)
	}
	golden(t, "run-sub.golden.json", body)

	// A failed run's document keeps the error text; the result
	// document died with the store — the transcript replaces it.
	_, _, fail := get(t, h, "/weftbase/v1/runs/r_fail")
	for _, want := range []string{`"status":"failed"`, `"err":"wefttest: injected provider 500"`} {
		if !strings.Contains(fail, want) {
			t.Errorf("r_fail missing %s", want)
		}
	}
	if strings.Contains(fail, `"result"`) {
		t.Errorf("run document still carries a result field: %s", fail)
	}
	golden(t, "run-fail.golden.json", fail)
}

func TestEventsGolden(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	code, _, body := get(t, h, "/weftbase/v1/runs/r_ok/events?limit=1000")
	if code != http.StatusOK {
		t.Fatalf("events: %d", code)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("finished run's first page not done: %s", body)
	}
	golden(t, "events-ok.golden.json", body)

	_, _, paged := get(t, h, "/weftbase/v1/runs/r_ok/events?after=2&limit=3")
	golden(t, "events-ok-paged.golden.json", paged)

	// The subagent parent's stream: no Nested wrappers inline — the
	// child run is a separate row joined by parent_call_id (S4.3).
	_, _, sub := get(t, h, "/weftbase/v1/runs/r_sub/events?limit=1000")
	if strings.Contains(sub, `"nested"`) {
		t.Errorf("parent stream carries nested wrappers: %s", sub)
	}
	golden(t, "events-sub.golden.json", sub)

	// A child run's id carries slashes (childRunID: parent/step/callID)
	// and its page is a full run page (B7): the document and the events
	// endpoint must both answer for it.
	kid := childID(t, h, "r_sub")
	if code, _, b := get(t, h, "/weftbase/v1/runs/"+esc(kid)); code != http.StatusOK ||
		!strings.Contains(b, `"id":"`+kid+`"`) || !strings.Contains(b, `"parent_run_id":"r_sub"`) {
		t.Errorf("child run document: %d %s", code, b)
	}
	if code, _, b := get(t, h, "/weftbase/v1/runs/"+esc(kid)+"/events"); code != http.StatusOK ||
		!strings.Contains(b, `"done":true`) {
		t.Errorf("child run events: %d %s", code, b)
	}
}

// childID returns the parent's first child run id from the run document.
func childID(t *testing.T, h http.Handler, parent string) string {
	t.Helper()
	_, _, body := get(t, h, "/weftbase/v1/runs/"+esc(parent))
	var doc struct {
		Children []struct {
			ID string `json:"id"`
		} `json:"children"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Children) == 0 {
		t.Fatalf("no children under %s: %v %s", parent, err, body)
	}
	return doc.Children[0].ID
}

func TestEventsPaging(t *testing.T) {
	db := fixtureDB(t)
	h := Handler(DB(db))
	ctx := context.Background()

	// The store of record for the walk: the database's own event order.
	full, err := db.Events(ctx, "r_ok", -1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	total := len(full.Events)
	want := make([]string, total)
	for i, pe := range full.Events {
		want[i] = string(pe.Event)
	}

	// Walk the whole stream in pages of 2 and reassemble it: the pages
	// concatenated must equal the database's order byte-for-byte, and
	// the last page reports done.
	var walked int
	after := 0
	for {
		_, _, body := get(t, h, fmt.Sprintf("/weftbase/v1/runs/r_ok/events?after=%d&limit=2", after))
		var page struct {
			Events []struct {
				Pos   int64           `json:"pos"`
				Event json.RawMessage `json:"event"`
			} `json:"events"`
			NextAfter *int64 `json:"next_after"`
			Done      bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for i, pe := range page.Events {
			// Positions are dense and continuous across pages: each
			// page starts exactly at the cursor it was given.
			if pe.Pos != int64(after+i) {
				t.Fatalf("event %d has pos %d, want %d", walked, pe.Pos, after+i)
			}
			if walked >= total || string(pe.Event) != want[walked] {
				t.Fatalf("event %d = %s, want %s", walked, pe.Event, want)
			}
			walked++
		}
		if page.NextAfter == nil {
			if !page.Done {
				t.Fatalf("last page not done: %s", body)
			}
			break
		}
		after = int(*page.NextAfter)
	}
	if walked != total {
		t.Fatalf("walked %d events, database holds %d", walked, total)
	}

	// -1 is the documented "from the start" default and reads like 0.
	if code, _, b := get(t, h, "/weftbase/v1/runs/r_ok/events?after=-1&limit=2"); code != http.StatusOK ||
		!strings.Contains(b, `"pos":0`) {
		t.Errorf("after=-1: %d %s", code, b)
	}

	// Past the end: empty page, no cursor, still done.
	for _, after := range []int64{int64(total), int64(total) + 5, 1<<63 - 1} {
		code, _, tail := get(t, h, fmt.Sprintf("/weftbase/v1/runs/r_ok/events?after=%d", after))
		if code != http.StatusOK || !strings.Contains(tail, `"events":[]`) ||
			!strings.Contains(tail, `"next_after":null`) || !strings.Contains(tail, `"done":true`) {
			t.Errorf("after=%d past the end: %d %s", after, code, tail)
		}
	}

	// A run that is live right now (fresh last-seen, no run_finish) is
	// never done.
	liveAt := time.Now()
	live := []obsdb.Record{
		fxRecord("r_live", "event", "run_start", 0, liveAt,
			`{"type":"run_start","id":"r_live","model":{"provider":"wefttest","name":"script"},"agent":"orders"}`),
		fxRecord("r_live", "event", "step_start", 1, liveAt.Add(50*time.Millisecond),
			`{"type":"step_start","run_id":"r_live","index":0}`),
	}
	if err := db.Write(ctx, obsdb.Batch{Records: live}); err != nil {
		t.Fatal(err)
	}
	_, _, body := get(t, h, "/weftbase/v1/runs/r_live/events")
	if !strings.Contains(body, `"done":false`) {
		t.Errorf("live run reported done: %s", body)
	}
	// The stale orphan is finished-in-effect: done once drained.
	_, _, stale := get(t, h, "/weftbase/v1/runs/r_stale/events")
	if !strings.Contains(stale, `"done":true`) {
		t.Errorf("stale run not done: %s", stale)
	}
}

func TestAPIErrors(t *testing.T) {
	h := Handler(DB(fixtureDB(t)))
	code, _, body := get(t, h, "/weftbase/v1/runs/nope")
	if code != http.StatusNotFound || !strings.Contains(body, `"not_found"`) {
		t.Errorf("unknown id: %d %s", code, body)
	}
	if code, _, b := get(t, h, "/weftbase/v1/runs?before=yesterday"); code != http.StatusBadRequest || !strings.Contains(b, "bad_request") {
		t.Errorf("bad before: %d %s", code, b)
	}
	if code, _, b := get(t, h, "/weftbase/v1/runs/r_ok/events?after=-2"); code != http.StatusBadRequest {
		t.Errorf("after=-2: %d %s", code, b)
	}
	if code, _, b := get(t, h, "/weftbase/v1/runs?limit=lots"); code != http.StatusBadRequest {
		t.Errorf("bad limit: %d %s", code, b)
	}
	if code, _, b := get(t, h, "/weftbase/v1/nope"); code != http.StatusNotFound {
		t.Errorf("unknown api route: %d %s", code, b)
	}

	// POST on a read route is 405 (the router answers it with Allow;
	// api/http's tests pin the header).
	srv := httptest.NewServer(mounted(h))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/weftbase/v1/traces", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/traces: %d", resp.StatusCode)
	}

	// A database that fails reads answers 500 internal naming the op —
	// loud over silent.
	stub := stubDB{err: errors.New("disk on fire")}
	for _, path := range []string{"/weftbase/v1/runs", "/weftbase/v1/runs/r_x", "/weftbase/v1/runs/r_x/events"} {
		code, _, b := get(t, Handler(DB(stub)), path)
		if code != http.StatusInternalServerError || !strings.Contains(b, `"internal"`) {
			t.Errorf("%s: %d %s", path, code, b)
		}
	}
}

// stubDB fails every read with err.
type stubDB struct {
	obsdb.DB
	err error
}

func (s stubDB) Runs(context.Context, obsdb.RunQuery) (obsdb.RunPage, error) {
	return obsdb.RunPage{}, s.err
}
func (s stubDB) Run(context.Context, string) (obsdb.RunDetail, error) {
	return obsdb.RunDetail{}, s.err
}
func (s stubDB) Events(context.Context, string, int64, int) (obsdb.EventPage, error) {
	return obsdb.EventPage{}, s.err
}

func TestManifestEndpoint(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)))
	code, _, body := get(t, h, "/weftbase/v1/manifest")
	if code != http.StatusOK {
		t.Fatalf("manifest: %d", code)
	}
	// The file's bytes verbatim, sources spliced in as the last key
	// (plan B4): one file source, no runtime registered.
	head := strings.TrimRight(strings.TrimRight(strings.TrimSpace(fixtureManifest), "}"), " \n")
	if !strings.HasPrefix(body, head+`,"sources":[{"source":"file","manifest_hash":"`) {
		t.Errorf("manifest bytes not passed through verbatim: %s", body)
	}
	var doc struct {
		Sources []map[string]any `json:"sources"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Sources) != 1 {
		t.Errorf("manifest with sources: %v %s", err, body)
	}
	code, _, body = get(t, Handler(DB(fixtureDB(t))), "/weftbase/v1/manifest")
	if code != http.StatusNotFound {
		t.Errorf("manifest without option: %d %s", code, body)
	}
}

func TestHandlerNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Handler(DB(nil)) did not panic")
		}
	}()
	_ = Handler(DB(nil))
}

// TestServerLifecycle pins S4.1's New/Server surface: Handler() is the
// server's handler and Close closes only what New opened.
func TestServerLifecycle(t *testing.T) {
	srv := New(DB(fixtureDB(t)))
	code, _, body := get(t, srv.Handler(), "/weftbase/v1/meta")
	if code != http.StatusOK || !strings.Contains(body, `"server_version"`) {
		t.Errorf("Server.Handler(): %d %s", code, body)
	}
	// A passed-in DB stays the caller's: Close must not close it (the
	// fixture's cleanup would then fail on a second close, and later
	// reads would hit ErrClosed).
	if err := srv.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := srv.db.Run(context.Background(), "r_ok"); err != nil {
		t.Errorf("Close closed a caller-owned DB: %v", err)
	}

	// What New opened itself, Close closes.
	dir := t.TempDir()
	owned := New(Open(filepath.Join(dir, "owned.db")))
	if err := owned.Close(); err != nil {
		t.Errorf("Close(owned): %v", err)
	}
	if _, err := owned.db.Run(context.Background(), "nope"); !errors.Is(err, obsdb.ErrClosed) {
		t.Errorf("owned DB not closed: %v", err)
	}
}

// TestLiveDefault pins S4.1's Live default: the DB's own hub when it
// implements Hub() (setup A's lane), else a fresh hub.
func TestLiveDefault(t *testing.T) {
	db := fixtureDB(t)
	srv := New(DB(db))
	if srv.live == nil {
		t.Fatal("no live hub")
	}
	if h, ok := db.(interface{ Hub() obsdb.Hub }); !ok || srv.live != h.Hub() {
		t.Error("Live default is not the DB's own hub")
	}
	custom := obsdb.NewHub()
	if s2 := New(DB(db), Live(custom)); s2.live != custom {
		t.Error("Live(h) not honoured")
	}
}

// TestSessionsAndPublic pin the S4.3 sessions shapes: the list, the
// detail with its turns in order, and the public-id resolution.
func TestSessionsAndPublic(t *testing.T) {
	h := Handler(DB(fixtureDB(t)))

	code, _, body := get(t, h, "/weftbase/v1/sessions")
	if code != http.StatusOK {
		t.Fatalf("sessions: %d", code)
	}
	for _, want := range []string{
		`"total":2`,
		`"id":"s_orders"`, `"public_id":"pub_orders"`, `"agent":"orders"`, `"turns":3`,
		`"id":"s_research"`, `"turns":1`,
		`"status":"interrupted"`, // the newest turn's (r_stale)
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions missing %s: %s", want, body)
		}
	}
	golden(t, "sessions.golden.json", body)

	// Filters: public_id and agent.
	_, _, pub := get(t, h, "/weftbase/v1/sessions?public_id=pub_research")
	if !strings.Contains(pub, `"total":1`) || !strings.Contains(pub, `"id":"s_research"`) {
		t.Errorf("sessions?public_id: %s", pub)
	}
	_, _, ag := get(t, h, "/weftbase/v1/sessions?agent=orders")
	if strings.Contains(ag, `"id":"s_research"`) {
		t.Errorf("sessions?agent=orders leaked s_research: %s", ag)
	}

	code, _, det := get(t, h, "/weftbase/v1/sessions/s_orders")
	if code != http.StatusOK {
		t.Fatalf("session detail: %d %s", code, det)
	}
	// Turns in order (1, 2, 3), each a run row.
	for _, want := range []string{
		`"runs":[`, `"id":"r_ok"`, `"id":"r_fail"`, `"id":"r_stale"`,
		`"turn":1`, `"turn":2`, `"turn":3`,
	} {
		if !strings.Contains(det, want) {
			t.Errorf("session detail missing %s: %s", want, det)
		}
	}
	golden(t, "session-orders.golden.json", det)

	// The public id resolves to the session.
	code, _, res := get(t, h, "/weftbase/v1/public/pub_orders")
	if code != http.StatusOK || !strings.Contains(res, `{"session_id":"s_orders"}`) {
		t.Errorf("public resolve: %d %s", code, res)
	}
	golden(t, "public.golden.json", res)
	if code, _, _ := get(t, h, "/weftbase/v1/public/pub_nope"); code != http.StatusNotFound {
		t.Errorf("unknown public id: %d", code)
	}
}

// TestRunsFilters pins the runs list's session and public-id filters
// (S4.2).
func TestRunsFilters(t *testing.T) {
	h := Handler(DB(fixtureDB(t)))
	_, _, bySession := get(t, h, "/weftbase/v1/runs?session=s_orders")
	if !strings.Contains(bySession, `"total":3`) || strings.Contains(bySession, `"id":"r_sub"`) {
		t.Errorf("runs?session: %s", bySession)
	}
	_, _, byPub := get(t, h, "/weftbase/v1/runs?public_id=pub_research")
	if !strings.Contains(byPub, `"total":1`) || !strings.Contains(byPub, `"id":"r_sub"`) {
		t.Errorf("runs?public_id: %s", byPub)
	}
}

// TestSpanKindName pins the OTLP int → name mapping S4.3's example
// requires ("kind": "client"): the wire ints never reach the API.
func TestSpanKindName(t *testing.T) {
	for kind, want := range map[int]string{
		0: "unspecified", 1: "internal", 2: "server", 3: "client",
		4: "producer", 5: "consumer", 99: "unspecified",
	} {
		if got := spanKindName(kind); got != want {
			t.Errorf("spanKindName(%d) = %q, want %q", kind, got, want)
		}
	}
}

// TestDBKind pins the meta db field's backend naming by dynamic type —
// the server library never imports the driver, so the clickhouse case
// is pinned through a stand-in type with the same package name in its
// dynamic type string.
func TestDBKind(t *testing.T) {
	type memDB struct{ obsdb.DB }
	type sqliteDB struct{ obsdb.DB }
	type clickhouseDB struct{ obsdb.DB }
	for db, want := range map[obsdb.DB]string{
		memDB{}: "memory", sqliteDB{}: "sqlite", clickhouseDB{}: "clickhouse",
	} {
		if got := dbKind(db); got != want {
			t.Errorf("dbKind(%T) = %q, want %q", db, got, want)
		}
	}
}

// TestTranscriptAndSpans pins the run-scoped transcript and spans
// shapes (S4.3) and the trace route any application can use.
func TestTranscriptAndSpans(t *testing.T) {
	h := Handler(DB(fixtureDB(t)))

	code, _, tr := get(t, h, "/weftbase/v1/runs/r_ok/transcript")
	if code != http.StatusOK {
		t.Fatalf("transcript: %d", code)
	}
	for _, want := range []string{
		`"batches":[`,
		`"index":0,"step":0,"input":true,"messages":[{"role":"user","content":[{"type":"text","text":"Where is order 42?"}]}]`,
		`"index":1,"step":0,"input":false`,
		`[{"role":"assistant","content":[{"type":"text","text":"Order 42 shipped this morning."}]}]`,
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript missing %s: %s", want, tr)
		}
	}
	golden(t, "transcript-ok.golden.json", tr)

	code, _, sp := get(t, h, "/weftbase/v1/runs/r_sub/spans")
	if code != http.StatusOK {
		t.Fatalf("spans: %d", code)
	}
	for _, want := range []string{
		`"spans":[`,
		`"name":"invoke_agent researcher"`, `"kind":"internal"`, `"status":"ok"`,
		`"trace_id":"0102030405060708090a0b0c0d0e0f10"`, `"span_id":"030405060708090a"`,
		`"service":"weftbase-test"`, `"events":[]`,
		`"gen_ai.agent.name":"researcher"`, `"weft.session.id":"s_research"`,
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("spans missing %s: %s", want, sp)
		}
	}
	golden(t, "spans-sub.golden.json", sp)

	// A failed run's span reads error with the message.
	_, _, failSp := get(t, h, "/weftbase/v1/runs/r_fail/spans")
	if !strings.Contains(failSp, `"status":"error"`) || !strings.Contains(failSp, "injected provider 500") {
		t.Errorf("failed run spans: %s", failSp)
	}

	// The trace route: everything under the fixture trace id — the
	// parent and the child's invoke_agent spans.
	code, _, trace := get(t, h, "/weftbase/v1/traces/"+fxTrace)
	if code != http.StatusOK {
		t.Fatalf("trace: %d", code)
	}
	if !strings.Contains(trace, `"name":"invoke_agent researcher"`) || !strings.Contains(trace, `"parent_span_id":"0102030405060708"`) {
		t.Errorf("trace spans: %s", trace)
	}
	golden(t, "trace.golden.json", trace)
	if code, _, _ := get(t, h, "/weftbase/v1/traces/nope"); code != http.StatusNotFound {
		t.Errorf("unknown trace: %d", code)
	}
}
