package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// seedAt writes one run at t: run_start, a prompt, and — when finished —
// run_finish and its invoke_agent span with an app log line under it.
// A run with no finish whose t is recent reads running.
func seedAt(t *testing.T, db obsdb.DB, id, session, parent string, at time.Time, finished bool) {
	t.Helper()
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	span := fmt.Sprintf("%016x", h.Sum64())
	trace := strings.Repeat("de", 16)
	rec := func(kind, typ string, pos int64, body string) obsdb.Record {
		a := map[string]any{"weft.record": kind, "weft.run.id": id, "gen_ai.agent.name": "acme-support"}
		if session != "" {
			a["weft.session.id"] = session
		}
		if parent != "" {
			a["weft.parent.run.id"] = parent
		}
		if kind == "event" {
			a["weft.event.type"], a["weft.event.pos"] = typ, pos
		} else {
			a["weft.messages.index"] = pos
		}
		return obsdb.Record{Time: at.Add(time.Duration(pos) * time.Millisecond), TraceID: trace, SpanID: span, Body: body, Attrs: a}
	}
	b := obsdb.Batch{Records: []obsdb.Record{
		rec("event", "run_start", 0, `{"type":"run_start","id":"`+id+`"}`),
		rec("messages", "", 0, `[{"role":"user","content":[{"type":"text","text":"hello"}]}]`),
	}}
	if finished {
		b.Records = append(b.Records, rec("event", "run_finish", 1, `{"type":"run_finish","run_id":"`+id+`","steps":1}`),
			obsdb.Record{Time: at, TraceID: trace, SpanID: span, Body: "app line"})
		b.Spans = []obsdb.Span{{TraceID: trace, SpanID: span, Name: "invoke_agent acme-support", Start: at, End: at.Add(time.Second),
			StatusCode: 1, Attrs: map[string]any{"gen_ai.operation.name": "invoke_agent", "weft.run.id": id}}}
	}
	if err := db.Write(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, ts *httptest.Server, method, path, host, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// DELETE /v1/runs/{id} takes the run and its subagent child (setup B,
// the dev token): 200 with what was deleted, then every read of either
// is 404, a second delete is 404, and the other run stays.
func TestDeleteRunRoute(t *testing.T) {
	const tok = "tok_delete"
	srv := New(Open(t.TempDir() + "/del.db"))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	now := time.Now().UTC()
	seedAt(t, srv.db, "run_p", "s_1", "", now, true)
	seedAt(t, srv.db, "run_p/0/call_1", "", "run_p", now, true)
	seedAt(t, srv.db, "run_keep", "s_2", "", now, true)

	code, body := call(t, ts, http.MethodDelete, "/v1/runs/run_p", "", tok)
	if code != http.StatusOK {
		t.Fatalf("DELETE run = %d %s", code, body)
	}
	var doc map[string]string
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["deleted"] != "run" || doc["run_id"] != "run_p" || doc["note"] != deleteRunNote {
		t.Errorf("DELETE run body = %s", body)
	}
	for _, p := range []string{"/v1/runs/run_p", "/v1/runs/run_p%2F0%2Fcall_1", "/v1/runs/run_p/events", "/v1/runs/run_p%2F0%2Fcall_1/spans"} {
		if code, _ := call(t, ts, http.MethodGet, p, "", tok); code != http.StatusNotFound {
			t.Errorf("GET %s after delete = %d, want 404", p, code)
		}
	}
	if code, _ := call(t, ts, http.MethodGet, "/v1/runs/run_keep", "", tok); code != http.StatusOK {
		t.Errorf("the other run = %d", code)
	}
	if code, _ := call(t, ts, http.MethodDelete, "/v1/runs/run_p", "", tok); code != http.StatusNotFound {
		t.Errorf("a second DELETE = %d, want 404", code)
	}
	// A child alone: its parent stays.
	seedAt(t, srv.db, "run_keep/0/c", "", "run_keep", now, true)
	if code, body := call(t, ts, http.MethodDelete, "/v1/runs/run_keep%2F0%2Fc", "", tok); code != http.StatusOK || !strings.Contains(body, `"run_id":"run_keep/0/c"`) {
		t.Errorf("DELETE child = %d %s", code, body)
	}
	if code, _ := call(t, ts, http.MethodGet, "/v1/runs/run_keep", "", tok); code != http.StatusOK {
		t.Errorf("the parent after its child's delete = %d", code)
	}
}

// DELETE /v1/sessions/{id} takes the session's observability rows and
// says the app's conversation store is untouched.
func TestDeleteSessionRoute(t *testing.T) {
	const tok = "tok_delete"
	srv := New(Open(t.TempDir() + "/del.db"))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	now := time.Now().UTC()
	seedAt(t, srv.db, "t1", "s_gone", "", now, true)
	seedAt(t, srv.db, "t2", "s_gone", "", now.Add(time.Millisecond), true)
	seedAt(t, srv.db, "t3", "s_stays", "", now, true)
	code, body := call(t, ts, http.MethodDelete, "/v1/sessions/s_gone", "", tok)
	if code != http.StatusOK || !strings.Contains(body, "the app's conversation store is untouched") || !strings.Contains(body, `"session_id":"s_gone"`) {
		t.Fatalf("DELETE session = %d %s", code, body)
	}
	for _, p := range []string{"/v1/sessions/s_gone", "/v1/runs/t1", "/v1/runs/t2"} {
		if code, _ := call(t, ts, http.MethodGet, p, "", tok); code != http.StatusNotFound {
			t.Errorf("GET %s after delete = %d, want 404", p, code)
		}
	}
	if code, _ := call(t, ts, http.MethodGet, "/v1/sessions/s_stays", "", tok); code != http.StatusOK {
		t.Errorf("the other session = %d", code)
	}
	for _, p := range []string{"/v1/sessions/s_gone", "/v1/sessions/nope", "/v1/sessions/a/b"} {
		if code, _ := call(t, ts, http.MethodDelete, p, "", tok); code != http.StatusNotFound {
			t.Errorf("DELETE %s = %d, want 404", p, code)
		}
	}
}

// api/meta's retention: null by default (meta.golden.json), the
// configured duration as a Go duration string with Retain.
func TestMetaRetention(t *testing.T) {
	srv := New(DB(fixtureDB(t)), Manifest([]byte(fixtureManifest)), Retain(720*time.Hour))
	t.Cleanup(func() { _ = srv.Close() })
	code, _, body := get(t, srv.Handler(), "/weftbase/v1/meta")
	if code != http.StatusOK {
		t.Fatalf("meta: %d", code)
	}
	if !strings.Contains(body, `"retention":"720h0m0s"`) {
		t.Errorf("meta retention: %s", body)
	}
	wefttest.Golden(t, "testdata/v1/meta-retention.golden.json",
		[]byte(metaPIDRe.ReplaceAllString(indent(t, body), `"pid": 1234`)))
	_, _, plain := get(t, Handler(DB(fixtureDB(t))), "/weftbase/v1/meta")
	if !strings.Contains(plain, `"retention":null`) {
		t.Errorf("default meta retention: %s", plain)
	}
}

func openTestDB(t *testing.T) obsdb.DB {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/retain.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
