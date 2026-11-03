package clickhouse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// TestRunlessSessionRecordsFollowTheirSession (U12.2-fix): a thread
// record filed under no run, riding a run's trace and span, stays in
// otel_logs and weft_session_events alike when DeleteRun takes that run
// (it is the session's, not the run's app line), and goes from both
// when Sweep finds its session with no run left; Vacuum merges
// weft_session_events with the other tables deletions mutate.
func TestRunlessSessionRecordsFollowTheirSession(t *testing.T) {
	base := os.Getenv("WEFT_CLICKHOUSE_DSN")
	if base == "" {
		t.Skip("WEFT_CLICKHOUSE_DSN not set: needs a clickhouse server (README has the one-line container recipe)")
	}
	ctx := context.Background()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "weft_rl_" + hex.EncodeToString(suffix)
	admin := rawConn(t, base)
	if err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE `%s`", name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		_ = admin.Close()
	})
	u.Path = "/" + name
	db, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn := rawConn(t, u.String())
	defer func() { _ = conn.Close() }()

	const trace, span, sid, run = "a1a2a3a4a5a6a7a8a9aaabacadaeaf10", "1000000000000001", "s_rl", "s_rl-t1"
	t0 := time.Now().UTC().Add(-time.Minute)
	ev := func(typ string, pos int64, body string) obsdb.Record {
		return obsdb.Record{Time: t0, TraceID: trace, SpanID: span, Severity: 9, Body: body, Service: "svc",
			Attrs: map[string]any{"weft.record": "event", "weft.run.id": run, "weft.event.type": typ, "weft.event.pos": pos, "weft.session.id": sid, "weft.turn": int64(1)}}
	}
	batch := obsdb.Batch{
		Spans: []obsdb.Span{{TraceID: trace, SpanID: span, Name: "invoke_agent a", Kind: 1, Start: t0, End: t0.Add(time.Second), StatusCode: 1, Service: "svc",
			Attrs: map[string]any{"gen_ai.operation.name": "invoke_agent", "weft.run.id": run, "weft.session.id": sid, "weft.turn": int64(1)}}},
		Records: []obsdb.Record{
			ev("run_start", 0, `{"type":"run_start","id":"`+run+`"}`),
			ev("run_finish", 1, `{"type":"run_finish","run_id":"`+run+`","steps":1}`),
			{Time: t0, TraceID: trace, SpanID: span, Severity: 13, EventName: "weft.thread.approval.refused", Service: "svc",
				Body:  `{"kind":"approval.refused","session":"s_rl","entry":"e2"}`,
				Attrs: map[string]any{"weft.record": "thread", "weft.run.id": "", "weft.session.id": sid, "weft.content": "none"}},
			{Time: t0, TraceID: trace, SpanID: span, Severity: 9, Body: "an app line", Service: "svc"},
		},
	}
	if err := db.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	counts := func() (logs, events uint64) {
		t.Helper()
		if err := conn.QueryRow(ctx, `SELECT count() FROM otel_logs WHERE LogAttributes['weft.record'] = 'thread' AND LogAttributes['weft.session.id'] = ?`, sid).Scan(&logs); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT count() FROM weft_session_events FINAL WHERE SessionId = ?`, sid).Scan(&events); err != nil {
			t.Fatal(err)
		}
		return logs, events
	}
	if err := db.DeleteRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	var app uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM otel_logs WHERE Body = 'an app line'`).Scan(&app); err != nil {
		t.Fatal(err)
	}
	if l, e := counts(); l != 1 || e != 1 || app != 0 {
		t.Errorf("after DeleteRun: %d thread rows in otel_logs, %d session events, %d app lines; want 1, 1, 0 (the app line goes with its run, the session's record stays whole)", l, e, app)
	}
	if _, err := db.Sweep(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if l, e := counts(); l != 0 || e != 0 {
		t.Errorf("after Sweep: %d thread rows in otel_logs, %d session events; want none (the session has no run left)", l, e)
	}
	if err := db.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vacuumTables, "weft_session_events") {
		t.Errorf("vacuumTables = %v, want weft_session_events among them", vacuumTables)
	}
}
