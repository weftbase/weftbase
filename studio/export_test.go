package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/obsdb"
)

// exportGet GETs one export until it answers 200 (ingest lands
// asynchronously) and cond holds, returning the response and body.
func exportGet(t *testing.T, ts *httptest.Server, path string, cond func(string) bool) (*http.Response, []byte) {
	t.Helper()
	fetchJSON(t, ts, strings.SplitN(path, "/export", 2)[0], cond) // the run row has settled
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, resp.StatusCode, b)
	}
	return resp, b
}

// exportShape flattens a JSON document into the set of its field paths
// and leaf types (golden_shape_test.go's rule): arrays collapse to [],
// an object with a "type" names its fields per type, and the maps whose
// keys are data — attributes, metadata, the prompts and catalogs keyed
// by hash — collapse to one <key>.
func exportShape(prefix string, v any, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		out[prefix+"{}"] = true
		for k, x := range v {
			p := prefix + "." + k
			for _, m := range []string{".attrs", ".meta", ".prompts", ".catalogs"} {
				if strings.HasSuffix(prefix, m) {
					p = prefix + ".<key>"
				}
			}
			if typ, ok := v["type"].(string); ok && k != "type" {
				p = prefix + "[" + typ + "]." + k
			}
			exportShape(p, x, out)
		}
	case []any:
		out[prefix+"[]"] = true
		for _, x := range v {
			exportShape(prefix+"[]", x, out)
		}
	default:
		out[prefix+"="+fmt.Sprintf("%T", v)] = true
	}
}

func shapeGolden(t *testing.T, name string, docs ...[]byte) {
	t.Helper()
	paths := map[string]bool{}
	for _, b := range docs {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		prefix := ""
		if m, ok := v.(map[string]any); ok {
			if k, ok := m["record"].(string); ok {
				prefix = "<" + k + ">"
			}
		}
		exportShape(prefix, v, paths)
	}
	list := make([]string, 0, len(paths))
	for p := range paths {
		list = append(list, p)
	}
	sort.Strings(list)
	wefttest.Golden(t, "testdata/api/"+name, []byte(strings.Join(list, "\n")+"\n"))
}

// exportDocT is the json export as a client decodes it.
type exportDocT struct {
	Format string `json:"format"`
	Run    struct {
		ID         string            `json:"id"`
		EventCount int64             `json:"event_count"`
		Children   []json.RawMessage `json:"children"`
	} `json:"run"`
	Events struct {
		Events []json.RawMessage `json:"events"`
		Gaps   []int64           `json:"gaps"`
		Badge  string            `json:"badge"`
		Reason string            `json:"reason"`
	} `json:"events"`
	Transcript struct {
		Batches []struct {
			Index    int64           `json:"index"`
			Step     int             `json:"step"`
			Input    bool            `json:"input"`
			Messages json.RawMessage `json:"messages"`
		} `json:"batches"`
		Badge string `json:"badge"`
	} `json:"transcript"`
	Compactions []struct {
		Scope   string `json:"scope"`
		Step    int    `json:"step"`
		FromSeq int64  `json:"from_seq"`
		ToSeq   int64  `json:"to_seq"`
	} `json:"compactions"`
	Requests struct {
		Requests []struct {
			Index   int64  `json:"index"`
			Content string `json:"content"`
		} `json:"requests"`
		Prompts  map[string]json.RawMessage `json:"prompts"`
		Catalogs map[string]json.RawMessage `json:"catalogs"`
		Badge    string                     `json:"badge"`
		Reason   string                     `json:"reason"`
		Fix      string                     `json:"fix"`
	} `json:"requests"`
	Spans []json.RawMessage `json:"spans"`
	Holes []struct {
		Hole   string `json:"hole"`
		Reason string `json:"reason"`
	} `json:"holes"`
}

func (d exportDocT) holes() string {
	var out []string
	for _, h := range d.Holes {
		out = append(out, h.Hole)
	}
	return strings.Join(out, ",")
}

// settled is the condition a recorded steps run's row meets once every
// record and span has landed.
func settled(id string) func(string) bool {
	return func(b string) bool {
		return strings.Contains(b, `"request_count":6`) && strings.Contains(b, `"id":"`+id+`/1/c_sub"`) && strings.Contains(b, `"status":"succeeded"`)
	}
}

// TestExportJSON pins format=json over the A7 steps run (retries, a
// subagent, a compaction view, a parked call): every block present and
// its golden shape, the attachment headers, the prompts and catalogs by
// hash; 400 for an unknown format, 404 for an unknown run.
func TestExportJSON(t *testing.T) {
	ts, _ := requestsServer(t)
	recordStepsRun(t, ts.URL, "r_exp", nil)
	resp, body := exportGet(t, ts, "/api/runs/r_exp/export?format=json", settled("r_exp"))
	if ct, cd := resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"); !strings.HasPrefix(ct, "application/json") || cd != `attachment; filename="r_exp.json"` {
		t.Errorf("headers = %q %q, want json and an attachment named r_exp.json", ct, cd)
	}
	shapeGolden(t, "export-json.shape.golden", body)
	var d exportDocT
	decode(t, string(body), &d)
	if d.Format != "weft.run.export/1" || d.Run.ID != "r_exp" || len(d.Run.Children) != 1 {
		t.Errorf("export run = %+v, want r_exp with its child", d.Run)
	}
	if int64(len(d.Events.Events)) != d.Run.EventCount || d.Events.Badge != "" || len(d.Events.Gaps) != 0 {
		t.Errorf("events = %d (badge %q), want the run's %d", len(d.Events.Events), d.Events.Badge, d.Run.EventCount)
	}
	if len(d.Transcript.Batches) == 0 || !d.Transcript.Batches[0].Input || d.Transcript.Badge != "" {
		t.Errorf("transcript = %+v, want the batches from the input record", d.Transcript)
	}
	if len(d.Compactions) != 1 || d.Compactions[0].Scope != "run" || d.Compactions[0].Step != 2 {
		t.Errorf("compactions = %+v, want step 2's run-scope view", d.Compactions)
	}
	if len(d.Requests.Requests) != 6 || len(d.Requests.Prompts) != 1 || len(d.Requests.Catalogs) != 1 || d.Requests.Badge != "" {
		t.Errorf("requests = %d prompts %d catalogs %d badge %q, want 6, 1, 1, none", len(d.Requests.Requests), len(d.Requests.Prompts), len(d.Requests.Catalogs), d.Requests.Badge)
	}
	for _, p := range d.Requests.Prompts {
		if !strings.Contains(string(p), "You are a support agent.") {
			t.Errorf("prompt = %s, want the system text", p)
		}
	}
	if len(d.Spans) == 0 || d.holes() != "compacted" {
		t.Errorf("spans = %d holes = %s, want spans and the compacted hole", len(d.Spans), d.holes())
	}

	for path, want := range map[string]int{
		"/api/runs/r_exp/export?format=xml":          http.StatusBadRequest,
		"/api/runs/r_exp/export":                     http.StatusBadRequest,
		"/api/runs/nope/export?format=json":          http.StatusNotFound,
		"/api/runs/nope/export?format=otlp":          http.StatusNotFound,
		"/api/runs/r_exp/1/c_sub/export?format=json": http.StatusOK,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != want || (want != http.StatusOK && !strings.Contains(string(b), `"error":{"code":`)) {
			t.Errorf("%s = %d %s, want %d in the error shape", path, resp.StatusCode, b, want)
		}
	}
	// A child's id spells its slashes "_" in the file name.
	resp, _ = exportGet(t, ts, "/api/runs/r_exp/1/c_sub/export?format=jsonl", nil)
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="r_exp_1_c_sub.jsonl"` {
		t.Errorf("child Content-Disposition = %q", cd)
	}
	// Setup A: the loopback Host only.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/runs/r_exp/export?format=json", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("export with a foreign Host = %d, want 403", resp.StatusCode)
	}
}

// TestExportJSONL pins format=jsonl: one record per line in the stable
// order (the run, then events by pos, messages by index, compactions,
// requests by index, prompts, catalogs, spans), the same records the
// json document holds, application/x-ndjson.
func TestExportJSONL(t *testing.T) {
	ts, _ := requestsServer(t)
	recordStepsRun(t, ts.URL, "r_jsonl", nil)
	_, doc := exportGet(t, ts, "/api/runs/r_jsonl/export?format=json", settled("r_jsonl"))
	resp, body := exportGet(t, ts, "/api/runs/r_jsonl/export?format=jsonl", nil)
	if ct, cd := resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"); ct != "application/x-ndjson" || cd != `attachment; filename="r_jsonl.jsonl"` {
		t.Errorf("headers = %q %q", ct, cd)
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n"))
	shapeGolden(t, "export-jsonl.shape.golden", lines...)
	order := []string{"run", "badge", "event", "gap", "messages", "compaction", "request", "prompt", "tools", "span"}
	rank := map[string]int{}
	for i, k := range order {
		rank[k] = i
	}
	counts := map[string]int{}
	last, lastPos := -1, int64(-1)
	for i, l := range lines {
		var h struct {
			Kind  string `json:"record"`
			Pos   int64  `json:"pos"`
			Index int64  `json:"index"`
		}
		decode(t, string(l), &h)
		r, ok := rank[h.Kind]
		if !ok || r < last {
			t.Fatalf("line %d kind %q out of order", i, h.Kind)
		}
		if r != last {
			lastPos = -1
		}
		key := h.Pos
		if h.Kind == "messages" || h.Kind == "request" {
			key = h.Index
		}
		if (h.Kind == "event" || h.Kind == "messages" || h.Kind == "request") && key <= lastPos {
			t.Errorf("line %d: %s %d after %d", i, h.Kind, key, lastPos)
		}
		last, lastPos = r, key
		counts[h.Kind]++
	}
	var d exportDocT
	decode(t, string(doc), &d)
	if counts["run"] != 1 || counts["event"] != len(d.Events.Events) || counts["messages"] != len(d.Transcript.Batches) ||
		counts["compaction"] != len(d.Compactions) || counts["request"] != len(d.Requests.Requests) ||
		counts["prompt"] != len(d.Requests.Prompts) || counts["tools"] != len(d.Requests.Catalogs) || counts["span"] != len(d.Spans) {
		t.Errorf("jsonl counts = %v, want the json document's records", counts)
	}
}

// TestExportOTLPRoundTrip is the otlp format's proof: the export, POSTed
// to a fresh Studio's own OTLP receiver (/v1/logs and /v1/traces),
// re-ingests as the same run — the json export of the copy equals the
// source's, run row, events, transcript, compactions, request records,
// prompts, catalogs, spans and holes, but for the subagent child (an
// export is one run; the child exports on its own). Content-off too.
func TestExportOTLPRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		dest []otel.DestOption
	}{{"content-on", nil}, {"content-off", []otel.DestOption{otel.NoContent()}}} {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := requestsServer(t)
			recordStepsRun(t, ts.URL, "r_otlp", nil, tc.dest...)
			_, want := exportGet(t, ts, "/api/runs/r_otlp/export?format=json", settled("r_otlp"))
			resp, body := exportGet(t, ts, "/api/runs/r_otlp/export?format=otlp", nil)
			if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="r_otlp.otlp.json"` {
				t.Errorf("Content-Disposition = %q", cd)
			}
			if tc.name == "content-on" {
				shapeGolden(t, "export-otlp.shape.golden", body)
			}
			var parts struct {
				Logs   json.RawMessage `json:"logs"`
				Traces json.RawMessage `json:"traces"`
			}
			decode(t, string(body), &parts)
			// OTLP/JSON spells ids as hex, never the protobuf mapping's
			// base64; and an export is the same bytes every time.
			var src struct {
				Run struct {
					TraceID string `json:"trace_id"`
				} `json:"run"`
			}
			decode(t, string(want), &src)
			if src.Run.TraceID == "" || !strings.Contains(string(body), `"traceId":"`+src.Run.TraceID+`"`) {
				t.Errorf("the export does not spell trace id %q as hex", src.Run.TraceID)
			}
			if _, again := exportGet(t, ts, "/api/runs/r_otlp/export?format=otlp", nil); !bytes.Equal(again, body) {
				t.Error("two otlp exports of one run differ")
			}

			fresh, _ := requestsServer(t)
			for path, part := range map[string]json.RawMessage{"/v1/logs": parts.Logs, "/v1/traces": parts.Traces} {
				resp, err := http.Post(fresh.URL+path, "application/json", bytes.NewReader(part))
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("POST %s = %d %s", path, resp.StatusCode, b)
				}
			}
			_, got := exportGet(t, fresh, "/api/runs/r_otlp/export?format=json", nil)
			// The chain's content marks ride the rebuilt event records
			// (weft.content), so the copy badges the same events.
			if tc.name == "content-off" {
				for _, doc := range [][]byte{want, got} {
					if !strings.Contains(string(doc), `"attrs":{"weft.content":"`) || !strings.Contains(string(doc), `"hole":"stripped"`) {
						t.Errorf("content-off export lacks the events' weft.content attrs or the run's stripped hole:\n%s", doc)
					}
				}
			} else if strings.Contains(string(got), `"attrs":{"weft.content":`) {
				t.Error("the content-on copy badges events the source did not")
			}
			if a, b := withoutChildren(t, want), withoutChildren(t, got); a != b {
				t.Errorf("re-ingested export differs from the source:\n got %s\nwant %s", b, a)
			}
		})
	}
}

// withoutChildren is a json export with the run's child rows (and the cost split they decide) dropped,
// re-indented for the diff.
func withoutChildren(t *testing.T, body []byte) string {
	t.Helper()
	var doc map[string]any
	decode(t, string(body), &doc)
	run, _ := doc["run"].(map[string]any)
	delete(run, "children")
	delete(run, "children_count") // H9: the children it counts are not in the copy
	delete(run, "children_failed")
	// The run's cost split reads its children (plan A5: own usage is its
	// usage minus theirs, delegated is theirs), which the copy lacks.
	for _, k := range []string{"cost_usd", "cost_known", "cost_badge", "cost_reason", "cache",
		"delegated_cost_usd", "delegated_cost_known", "delegated_cost_reason", "own_usage", "delegated_usage"} {
		delete(run, k)
	}
	b, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExportContentOff: a content-off run's json export keeps every
// block it has and badges the rest — the request rows stripped, their
// prompts and catalogs {hash, badge: stripped}, the transcript
// stripped.
func TestExportContentOff(t *testing.T) {
	ts, _ := requestsServer(t)
	recordStepsRun(t, ts.URL, "r_xoff", nil, otel.NoContent())
	_, body := exportGet(t, ts, "/api/runs/r_xoff/export?format=json", settled("r_xoff"))
	shapeGolden(t, "export-json-stripped.shape.golden", body)
	var d exportDocT
	decode(t, string(body), &d)
	if len(d.Requests.Requests) != 6 || d.Requests.Requests[0].Content != "stripped" || d.Requests.Badge != "" {
		t.Errorf("content-off requests = %+v, want 6 stripped rows", d.Requests)
	}
	for _, refs := range []map[string]json.RawMessage{d.Requests.Prompts, d.Requests.Catalogs} {
		if len(refs) != 1 {
			t.Errorf("content-off refs = %v, want one", refs)
		}
		for h, v := range refs {
			if string(v) != `{"hash":"`+h+`","badge":"stripped"}` {
				t.Errorf("content-off ref = %s, want {hash, badge: stripped}", v)
			}
		}
	}
	if len(d.Transcript.Batches) != 0 || d.Transcript.Badge != "stripped" || len(d.Events.Events) == 0 {
		t.Errorf("content-off transcript = %+v events %d, want none, badged stripped, events kept", d.Transcript, len(d.Events.Events))
	}
	if d.holes() != "stripped" {
		t.Errorf("content-off holes = %s, want stripped", d.holes())
	}
}

// TestExportOTLPKeepsTruncatedAndDerived: a hand-written record set
// whose prompt was cut by a cap (truncated), whose tools record and
// second prompt did not parse (derived) — both badged in the export's
// top-level holes — re-ingests from its OTLP export with both holes
// intact — never read back as a gap.
func TestExportOTLPKeepsTruncatedAndDerived(t *testing.T) {
	ts, srv := requestsServer(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := func(i int, kind, body string, attrs map[string]any) obsdb.Record {
		a := map[string]any{"weft.record": kind, "weft.run.id": "r_hand", "gen_ai.agent.name": "a"}
		for k, v := range attrs {
			a[k] = v
		}
		return obsdb.Record{Time: now.Add(time.Duration(i) * time.Millisecond), EventName: "weft." + kind, Severity: 9,
			Body: body, Service: "svc", Attrs: a, Resource: map[string]any{"service.name": "svc"}}
	}
	if err := srv.db.Write(context.Background(), obsdb.Batch{Records: []obsdb.Record{
		rec(0, "event", `{"type":"run_start","id":"r_hand","model":{"provider":"p","name":"m"},"agent":"a"}`,
			map[string]any{"weft.event.type": "run_start", "weft.event.pos": int64(0), "weft.instructions.hash": "ih"}),
		rec(1, "messages", `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`,
			map[string]any{"weft.messages.index": int64(0), "weft.step.index": int64(0), "weft.messages.input": true}),
		rec(2, "request", `{"step":0,"attempt":1,"system_hash":"sh","messages_ref":{"index":0,"count":1},"tools":{"catalog_hash":"ch","names":["x"]}}`,
			map[string]any{"weft.request.index": int64(0), "weft.step.index": int64(0), "weft.system.hash": "sh", "weft.catalog.hash": "ch", "weft.content": "full"}),
		rec(3, "prompt", `{"hash":"sh","text":"You are cu"}`,
			map[string]any{"weft.prompt.index": int64(0), "weft.system.hash": "sh", "weft.content.truncated_bytes": int64(40)}),
		rec(4, "tools", `not json`, map[string]any{"weft.tools.index": int64(0), "weft.catalog.hash": "ch"}),
		// A second attempt under another system text, whose prompt
		// record did not parse: a derived prompt.
		rec(5, "request", `{"step":0,"attempt":2,"system_hash":"sh2","messages_ref":{"index":0,"count":1},"tools":{"catalog_hash":"ch","names":["x"]}}`,
			map[string]any{"weft.request.index": int64(1), "weft.step.index": int64(0), "weft.system.hash": "sh2", "weft.catalog.hash": "ch", "weft.content": "full"}),
		rec(6, "prompt", `not json`, map[string]any{"weft.prompt.index": int64(1), "weft.system.hash": "sh2"}),
		rec(7, "event", `{"type":"run_finish","run_id":"r_hand","usage":{"input_tokens":1,"output_tokens":1},"steps":1}`,
			map[string]any{"weft.event.type": "run_finish", "weft.event.pos": int64(1)}),
	}}); err != nil {
		t.Fatal(err)
	}
	_, want := exportGet(t, ts, "/api/runs/r_hand/export?format=json", nil)
	for _, w := range []string{`"content":"truncated"`, `"content":"derived"`, `"hole":"truncated"`} {
		if !strings.Contains(string(want), w) {
			t.Fatalf("source export lacks %s: %s", w, want)
		}
	}
	_, body := exportGet(t, ts, "/api/runs/r_hand/export?format=otlp", nil)
	var parts struct {
		Logs json.RawMessage `json:"logs"`
	}
	decode(t, string(body), &parts)
	fresh, _ := requestsServer(t)
	resp, err := http.Post(fresh.URL+"/v1/logs", "application/json", bytes.NewReader(parts.Logs))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_, got := exportGet(t, fresh, "/api/runs/r_hand/export?format=json", nil)
	for name, doc := range map[string][]byte{"source": want, "copy": got} {
		var d exportDocT
		decode(t, string(doc), &d)
		if !strings.Contains(string(d.Requests.Prompts["sh2"]), `"content":"derived"`) || !strings.Contains(","+d.holes()+",", ",derived,") ||
			!strings.Contains(","+d.holes()+",", ",truncated,") {
			t.Errorf("%s: prompt sh2 %s, holes %s; want the derived prompt and both truncated and derived among the holes", name, d.Requests.Prompts["sh2"], d.holes())
		}
	}
	if a, b := withoutChildren(t, want), withoutChildren(t, got); a != b {
		t.Errorf("re-ingested export differs:\n got %s\nwant %s", b, a)
	}
}

// TestExportOTLPInputDerived: a messages batch whose input flag the
// backend inferred is exported without weft.messages.input, so the
// copy infers it again (derived) instead of reading it as recorded.
func TestExportOTLPInputDerived(t *testing.T) {
	x := &runExport{batches: []obsdb.TranscriptBatch{
		{Index: 0, Step: 0, Input: true, InputDerived: true, Messages: json.RawMessage(`[]`)},
		{Index: 1, Step: 0, Input: true, Messages: json.RawMessage(`[]`)},
	}}
	recs := x.otlpLogs().ResourceLogs[0].ScopeLogs[0].LogRecords
	has := func(i int) bool {
		for _, kv := range recs[i].Attributes {
			if kv.Key == "weft.messages.input" {
				return true
			}
		}
		return false
	}
	if has(0) || !has(1) {
		t.Errorf("weft.messages.input on (derived, recorded) = (%v, %v), want (false, true)", has(0), has(1))
	}
}

// TestExportDownloadHeaders: the attachment name is plain ASCII in
// filename (slash, quote, backslash, control and non-ASCII characters
// spelled "_") with filename* carrying a non-ASCII id; HEAD answers the
// headers without the body.
func TestExportDownloadHeaders(t *testing.T) {
	f := exportFormats["json"]
	for id, want := range map[string]string{
		"r/1/c":   `attachment; filename="r_1_c.json"`,
		"a\"b\\c": `attachment; filename="a_b_c.json"`,
		"line\nx": `attachment; filename="line_x.json"`,
		"run-é/漢": `attachment; filename="run-___.json"; filename*=UTF-8''run-%C3%A9_%E6%BC%A2.json`,
	} {
		w := httptest.NewRecorder()
		writeDownload(w, httptest.NewRequest(http.MethodGet, "/", nil), id, f, []byte("{}"))
		if got := w.Header().Get("Content-Disposition"); got != want {
			t.Errorf("%q: Content-Disposition = %s, want %s", id, got, want)
		}
	}
	ts, _ := requestsServer(t)
	recordStepsRun(t, ts.URL, "r_head", nil)
	_, body := exportGet(t, ts, "/api/runs/r_head/export?format=json", settled("r_head"))
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/api/runs/r_head/export?format=json", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(b) != 0 || resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="r_head.json"` {
		t.Errorf("HEAD = %d, %d body bytes, length %q, disposition %q", resp.StatusCode, len(b), resp.Header.Get("Content-Length"), resp.Header.Get("Content-Disposition"))
	}
}

// TestExportHolesFromEvents: the export's holes name what any event
// says, not only the run's first or last — a tool result a
// destination's cap cut (truncated), a later event a content-off chain
// stripped, a step before the last that finished on the output token
// limit (max_tokens).
func TestExportHolesFromEvents(t *testing.T) {
	ts, srv := requestsServer(t)
	writeHand(t, srv.db, "r_evh", time.Now().UTC().Add(-time.Hour), map[string]any{}, []handRec{
		ev(0, `{"type":"run_start","id":"r_evh","model":{"provider":"p","name":"m"},"agent":"hand"}`),
		ev(1, `{"type":"step_start","run_id":"r_evh","index":0}`),
		{kind: "event", pos: 2, body: `{"type":"step_finish","run_id":"r_evh","index":0,"reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":1}}`},
		ev(3, `{"type":"step_start","run_id":"r_evh","index":1}`),
		{kind: "event", pos: 4, body: `{"type":"tool_start","run_id":"r_evh","seq":1,"call_id":"c1","name":"t","args":null}`,
			extra: map[string]any{"weft.content": "stripped"}},
		{kind: "event", pos: 5, body: `{"type":"tool_finish","run_id":"r_evh","seq":2,"call_id":"c1","name":"t","content":"xx","is_error":false}`,
			extra: map[string]any{"weft.content.truncated_bytes": int64(12)}},
		ev(6, `{"type":"step_finish","run_id":"r_evh","index":1,"reason":"stop","usage":{"input_tokens":1,"output_tokens":1}}`),
		ev(7, `{"type":"run_finish","run_id":"r_evh","usage":{"input_tokens":2,"output_tokens":2},"steps":2}`),
	})
	_, body := exportGet(t, ts, "/api/runs/r_evh/export?format=json", nil)
	var d exportDocT
	decode(t, string(body), &d)
	for _, want := range []string{"truncated", "stripped", "max_tokens"} {
		if !slices.Contains(strings.Split(d.holes(), ","), want) {
			t.Errorf("export holes = %s, want %s", d.holes(), want)
		}
	}
	_, lines := exportGet(t, ts, "/api/runs/r_evh/export?format=jsonl", nil)
	for _, want := range []string{`"hole":"truncated"`, `"hole":"stripped"`, `"hole":"max_tokens"`} {
		if !strings.Contains(strings.SplitN(string(lines), "\n", 2)[0], want) {
			t.Errorf("jsonl run line lacks %s", want)
		}
	}
}

// failEventsDB is an obsdb.DB whose Events read of one run fails.
type failEventsDB struct {
	obsdb.DB
	run string
}

func (d failEventsDB) Events(ctx context.Context, id string, after int64, limit int) (obsdb.EventPage, error) {
	if id == d.run {
		return obsdb.EventPage{}, errors.New("unreadable")
	}
	return d.DB.Events(ctx, id, after, limit)
}

// TestChildHoles: the run page and the export carry each child's holes
// (one helper, childRows), and a child whose holes cannot be read goes
// out without them — its parent's page and export still answer 200.
func TestChildHoles(t *testing.T) {
	_, base := requestsServer(t)
	at := time.Now().UTC().Add(-time.Hour)
	writeHand(t, base.db, "r_kids", at, map[string]any{}, []handRec{
		ev(0, `{"type":"run_start","id":"r_kids","model":{"provider":"p","name":"m"},"agent":"hand"}`),
		ev(1, `{"type":"run_finish","run_id":"r_kids","usage":{"input_tokens":1,"output_tokens":1},"steps":1}`),
	})
	for _, kid := range []string{"r_kids/0/ok", "r_kids/0/bad"} {
		// No run_finish, an hour quiet: interrupted, a hole to carry.
		writeHand(t, base.db, kid, at, map[string]any{"weft.parent.run.id": "r_kids", "weft.parent.call.id": kid[len("r_kids/0/"):]}, []handRec{
			ev(0, `{"type":"run_start","id":"`+kid+`","model":{"provider":"p","name":"m"},"agent":"hand"}`),
		})
	}
	srv := New(DB(failEventsDB{DB: base.db, run: "r_kids/0/bad"}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	type kidT struct {
		ID    string `json:"id"`
		Holes []struct {
			Hole string `json:"hole"`
		} `json:"holes"`
	}
	check := func(where string, kids []kidT) {
		t.Helper()
		if len(kids) != 2 {
			t.Fatalf("%s children = %+v, want 2", where, kids)
		}
		for _, k := range kids {
			switch k.ID {
			case "r_kids/0/ok":
				if len(k.Holes) == 0 || k.Holes[0].Hole != "interrupted" {
					t.Errorf("%s: child ok holes = %+v, want interrupted", where, k.Holes)
				}
			case "r_kids/0/bad":
				if len(k.Holes) != 0 {
					t.Errorf("%s: unreadable child holes = %+v, want none", where, k.Holes)
				}
			}
		}
	}
	var page struct {
		Children []kidT `json:"children"`
	}
	decode(t, fetchJSON(t, ts, "/api/runs/r_kids", nil), &page)
	check("run page", page.Children)
	var x struct {
		Run struct {
			Children []kidT `json:"children"`
		} `json:"run"`
	}
	_, body := exportGet(t, ts, "/api/runs/r_kids/export?format=json", nil)
	decode(t, string(body), &x)
	check("export", x.Run.Children)
}

// TestLostEventsOneRule: the run document's holes and the export's
// events block read a run with no stored event by one rule
// (lostEvents): request records but no event is a gap, with the same
// reason on both; spans alone are not_recorded in no_logger's words (the
// app exported spans only — never derived, a stock-span run's badge);
// neither — a row whose only record is a messages batch — is nothing.
func TestLostEventsOneRule(t *testing.T) {
	ts, srv := requestsServer(t)
	old := time.Now().UTC().Add(-time.Hour)
	writeHand(t, srv.db, "r_reqonly", old, map[string]any{}, []handRec{
		{kind: "request", pos: 0, body: reqBody(0, `{"count":1}`), extra: map[string]any{"weft.step.index": int64(0)}},
	})
	writeHand(t, srv.db, "r_spanonly", old, map[string]any{}, nil, obsdb.Span{SpanID: "d0000000000000a1", Name: "invoke_agent hand", Kind: 1,
		StatusCode: 1, Attrs: map[string]any{"gen_ai.operation.name": "invoke_agent"}})
	if err := srv.db.Write(context.Background(), obsdb.Batch{Records: []obsdb.Record{{
		Time: old, EventName: "weft.messages", Severity: 9, Body: `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`, Service: "svc",
		Attrs: map[string]any{"weft.record": "messages", "weft.run.id": "r_msgonly", "gen_ai.agent.name": "hand",
			"weft.messages.index": int64(0), "weft.messages.count": int64(1)},
		Resource: map[string]any{"service.name": "svc"},
	}}}); err != nil {
		t.Fatal(err)
	}
	type holeT struct{ Hole, Reason, Fix string }
	for _, c := range []struct {
		run, want string // the lost-events badge, "" for none
	}{{"r_reqonly", "gap"}, {"r_spanonly", "not_recorded"}, {"r_msgonly", ""}} {
		var doc struct {
			Holes []holeT `json:"holes"`
		}
		decode(t, fetchJSON(t, ts, "/api/runs/"+c.run, nil), &doc)
		var x struct {
			Events struct {
				Badge, Reason, Fix string
			} `json:"events"`
		}
		_, body := exportGet(t, ts, "/api/runs/"+c.run+"/export?format=json", nil)
		decode(t, string(body), &x)
		var runHole *holeT
		for i := range doc.Holes {
			// not_recorded in no_logger's words only: RequestsHole's own
			// (a row with no request record) is not the lost-events rule.
			noLogger, _ := obsdb.HoleNoteFor(obsdb.HoleNotRecorded, obsdb.CauseNoLogger)
			if h := doc.Holes[i].Hole; h == "gap" || h == "derived" || (h == "not_recorded" && doc.Holes[i].Reason == noLogger) {
				runHole = &doc.Holes[i]
			}
		}
		switch {
		case c.want == "":
			if runHole != nil || x.Events.Badge != "" {
				t.Errorf("%s: run hole %+v, events badge %q, want neither", c.run, runHole, x.Events.Badge)
			}
		case runHole == nil || runHole.Hole != c.want || x.Events.Badge != c.want ||
			runHole.Reason != x.Events.Reason || runHole.Fix != x.Events.Fix:
			t.Errorf("%s: run hole %+v, events %+v, want %s on both, worded alike", c.run, runHole, x.Events, c.want)
		}
	}
}
