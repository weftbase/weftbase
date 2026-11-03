package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// A session row carries its title (thread's weft.session.title, the
// newest turn's) and its first turn's prompt preview — what server and
// the Console titles it with (lib/title.ts) — on the list, the document
// and a scoped token's scoped document alike; ?q= finds sessions by
// either (obsdb.SessionQuery.Q), never by a later turn's prompt; an
// untitled session whose first turn stored no content carries
// content_off, so the list can say "(content not captured)" (U6-fix).
func TestSessionTitlesAndQuery(t *testing.T) {
	const serverTok = "srv-token"
	db, err := sqlite.Open(t.TempDir() + "/titles.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedRun(t, db, "conv_t-t1", "pub_a", "conv_t", "", map[string]any{obsdb.SessionTitleKey: "Order desk"})
	seedRun(t, db, "conv_p-t1", "pub_b", "conv_p", "", nil)
	// Content-off: the run's events, no messages record.
	now := time.Now().UTC()
	var off []obsdb.Record
	for i, ev := range [][2]string{
		{"run_start", `{"type":"run_start","id":"conv_o-t1","model":{"provider":"wefttest","name":"script"},"agent":"acme-support"}`},
		{"run_finish", `{"type":"run_finish","run_id":"conv_o-t1","usage":{"input_tokens":1,"output_tokens":1},"steps":1}`},
	} {
		off = append(off, obsdb.Record{
			Time: now.Add(time.Duration(i) * time.Millisecond), EventName: "weft.event", Severity: 9, Body: ev[1], Service: "svc",
			Attrs: map[string]any{
				"weft.record": "event", "weft.run.id": "conv_o-t1", "gen_ai.agent.name": "acme-support",
				"weft.public_id": "pub_o", "weft.session.id": "conv_o", "weft.turn": "1",
				"weft.event.type": ev[0], "weft.event.pos": int64(i),
			},
			Resource: map[string]any{"service.name": "svc"},
		})
	}
	if err := db.Write(context.Background(), obsdb.Batch{Records: off}); err != nil {
		t.Fatal(err)
	}
	srv := New(DB(db), Token(serverTok))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	type row struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		FirstPrompt string `json:"first_prompt"`
		ContentOff  bool   `json:"content_off"`
	}
	list := func(path string) map[string]row {
		t.Helper()
		code, out := b4Do(t, http.MethodGet, ts.URL+path, serverTok, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, out)
		}
		var page struct {
			Sessions []row `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		got := map[string]row{}
		for _, s := range page.Sessions {
			got[s.ID] = s
		}
		return got
	}
	all := list("/api/sessions")
	if r := all["conv_t"]; r.Title != "Order desk" || r.FirstPrompt != "secret of pub_a" || r.ContentOff {
		t.Errorf("conv_t = %+v, want its title and first prompt", r)
	}
	if r := all["conv_p"]; r.Title != "" || r.FirstPrompt != "secret of pub_b" || r.ContentOff {
		t.Errorf("conv_p = %+v, want no title, its first prompt", r)
	}
	if r := all["conv_o"]; r.Title != "" || r.FirstPrompt != "" || !r.ContentOff {
		t.Errorf("conv_o = %+v, want no title, no prompt, content_off", r)
	}
	if got := list("/api/sessions?q=order+DESK"); len(got) != 1 || got["conv_t"].ID == "" {
		t.Errorf("?q=order DESK = %v, want conv_t by its title", got)
	}
	if got := list("/api/sessions?q=pub_b"); len(got) != 1 || got["conv_p"].ID == "" {
		t.Errorf("?q=pub_b = %v, want conv_p by its first prompt and public id", got)
	}
	// id_prefix (U30.2): the session ids that start with it, never one
	// that holds it mid-id or a title or prompt that does.
	if got := list("/api/sessions?id_prefix=conv_t"); len(got) != 1 || got["conv_t"].ID == "" {
		t.Errorf("?id_prefix=conv_t = %v, want conv_t", got)
	}
	if got := list("/api/sessions?id_prefix=onv_"); len(got) != 0 {
		t.Errorf("?id_prefix=onv_ = %v, want none (a prefix, not a substring)", got)
	}
}

// A session row serves obsdb's SessionRow.SummarizerUsage as
// summarizer_usage (record contract §6), apart from usage and unpriced:
// on the list, on the document and — scoped to its public id's turns —
// on a scoped token's document, where a colliding turn of another public
// id sharing the session id adds nothing; absent when nothing was
// summarized.
func TestSessionSummarizerUsage(t *testing.T) {
	const serverTok = "srv-token"
	db, err := sqlite.Open(t.TempDir() + "/summ.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedRun(t, db, "conv_s-t1", "pub_a", "conv_s", "", nil)
	seedRun(t, db, "conv_s-t2", "pub_b", "conv_s", "", nil) // collides: another app's
	seedRun(t, db, "conv_u-t1", "pub_a", "conv_u", "", nil)
	seedRun(t, db, "conv_n-t1", "pub_a", "conv_n", "", nil) // summarized nothing
	now := time.Now().UTC()
	summ := func(run, spanID string, in, out int64) obsdb.Span {
		return obsdb.Span{
			TraceID: "0102030405060708090a0b0c0d0e0f99", SpanID: spanID, Name: "chat summary-mini", Kind: 3,
			Start: now, End: now.Add(time.Millisecond), StatusCode: 1, Service: "svc",
			Attrs: map[string]any{
				"gen_ai.operation.name": "chat", obsdb.AttrChatRole: obsdb.ChatRoleSummarizer,
				"weft.run.id": run, "gen_ai.usage.input_tokens": in, "gen_ai.usage.output_tokens": out,
			},
			Resource: map[string]any{"service.name": "svc"},
		}
	}
	if err := db.Write(context.Background(), obsdb.Batch{Spans: []obsdb.Span{
		summ("conv_s-t1", "00000000000000a1", 100, 5),
		summ("conv_s-t2", "00000000000000a2", 40, 2),
		summ("conv_u-t1", "00000000000000a3", 30, 3),
	}}); err != nil {
		t.Fatal(err)
	}
	srv := New(DB(db), Token(serverTok))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	type usage struct {
		In  int `json:"input_tokens"`
		Out int `json:"output_tokens"`
	}
	type row struct {
		ID    string `json:"id"`
		Usage usage  `json:"usage"`
		Summ  *usage `json:"summarizer_usage"`
	}
	read := func(path, tok string, into any) {
		t.Helper()
		code, out := b4Do(t, http.MethodGet, ts.URL+path, tok, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, out)
		}
		if err := json.Unmarshal([]byte(out), into); err != nil {
			t.Fatal(err)
		}
	}
	var page struct {
		Sessions []row `json:"sessions"`
	}
	read("/api/sessions", serverTok, &page)
	got := map[string]row{}
	for _, r := range page.Sessions {
		got[r.ID] = r
	}
	if r := got["conv_s"]; r.Summ == nil || *r.Summ != (usage{140, 7}) || r.Usage != (usage{2, 2}) {
		t.Errorf("list conv_s = %+v (summarizer %v), want summarizer 140/7 apart from usage 2/2", r, r.Summ)
	}
	if r := got["conv_n"]; r.ID == "" || r.Summ != nil {
		t.Errorf("list conv_n = %+v, want listed with no summarizer_usage", r)
	}
	var doc row
	read("/api/sessions/conv_s", serverTok, &doc)
	if doc.Summ == nil || *doc.Summ != (usage{140, 7}) {
		t.Errorf("doc conv_s summarizer_usage = %v, want 140/7", doc.Summ)
	}
}
