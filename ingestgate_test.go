package weftbase

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// otlpLog is an OTLP/JSON logs batch with one weft record of runID.
func otlpLog(runID string) []byte {
	return []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{
	  "timeUnixNano":"1790000000000000000","severityNumber":9,
	  "eventName":"weft.event","body":{"stringValue":"{\"type\":\"run_start\",\"id\":\"` + runID + `\"}"},
	  "attributes":[
	    {"key":"weft.record","value":{"stringValue":"event"}},
	    {"key":"weft.run.id","value":{"stringValue":"` + runID + `"}},
	    {"key":"weft.event.type","value":{"stringValue":"run_start"}},
	    {"key":"weft.event.pos","value":{"intValue":"0"}}]}]}]}]}`)
}

// TestBuildKeyIngestsItsOwnRunsOnly: each build gets its own ingest key
// (named by its digest); with it, OTLP ingest takes the records of the
// runs dispatched to that build and of their subagent children, and
// refuses a batch naming any other run (the single shared key used to
// let any build write any run's record). A worker key minted for no
// build writes no run's record. A build's key is revoked when the node
// shuts down (and when the build is retired).
func TestBuildKeyIngestsItsOwnRunsOnly(t *testing.T) {
	dir := t.TempDir()
	app, ts, stop := startApp(t, dir, BuildDigest(digestA))
	app.Serving(ts.URL)
	if err := app.Agent(core.New(wefttest.Script(wefttest.Say("hello")), core.Name("support"))); err != nil {
		t.Fatal(err)
	}
	_, key := app.buildTelemetry(digestA)
	if !strings.HasPrefix(key, "wb_wk_") {
		t.Fatalf("build key = %q", key)
	}
	_, other := app.buildTelemetry("sha256:" + strings.Repeat("b", 64))
	code, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "support", "input": map[string]any{"prompt": "hi"}})
	if code != 202 {
		t.Fatalf("POST /v1/runs: %d %v", code, run)
	}
	id := run["id"].(string)
	waitRun(t, app, id, sessions.StatusSucceeded)

	send := func(key, runID string) (int, string) {
		return call(t, "POST", ts.URL+"/otlp/v1/logs", key, "application/json", otlpLog(runID))
	}
	if code, body := send(key, id); code != 200 {
		t.Errorf("its own run: %d %s", code, body)
	}
	if code, body := send(key, id+"/1/call_1"); code != 200 {
		t.Errorf("its run's subagent child: %d %s", code, body)
	}
	if code, body := send(key, "run_forged"); code != 403 || !strings.Contains(body, "dispatched to its build") {
		t.Errorf("a run it was never sent: %d %s", code, body)
	}
	if code, body := send(other, id); code != 403 {
		t.Errorf("another build's key on this build's run: %d %s", code, body)
	}
	_, wk, err := app.Keys().Mint(context.Background(), tenant.Default(), keys.Worker, "remote", events.System)
	if err != nil {
		t.Fatal(err)
	}
	if code, body := send(wk, id); code != 403 {
		t.Errorf("a worker key of no build: %d %s", code, body)
	}
	// A secret key is not gated.
	if code, body := send(bootstrap, "run_anything"); code != 200 {
		t.Errorf("a secret key: %d %s", code, body)
	}
	// Shutdown revokes the build keys.
	stop()
	db, err := sqlite.Open(context.Background(), filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var live int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM api_keys WHERE name LIKE 'build:%' AND revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Errorf("%d build keys outlived Shutdown", live)
	}
}
