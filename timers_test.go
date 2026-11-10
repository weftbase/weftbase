package weftbase

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/tenant"
)

// The scheduler's timers and the run-scoped verbs end to end: the
// queue time-to-live fails a run nobody serves, a silent worker loses
// its run (and writes worker.disconnected only when its link ends), a
// parked run nobody decides is denied at its approval expiry, and a
// steer reaches a running embedded run.

func TestQueueTTLEndToEnd(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), QueueTTL(200*time.Millisecond), TimerPoll(20*time.Millisecond))
	agent := core.New(wefttest.Script(wefttest.Say("never")), core.Name("unserved"))
	m, err := core.Manifest(agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Registry().RegisterIn(context.Background(), tenant.Default(), digestB, wire.BuildBinary,
		[]registry.AgentSpec{{Name: "unserved", Manifest: m, ManifestHash: registry.ManifestHash(m)}}); err != nil {
		t.Fatal(err)
	}
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "unserved", "input": map[string]any{"prompt": "x"}})
	lost := waitRun(t, app, run["id"].(string), sessions.StatusFailed)
	if !strings.HasPrefix(lost.Error, sessions.ErrWorkerLost) || !strings.Contains(lost.Error, "time-to-live") {
		t.Fatalf("run = %+v", lost)
	}
}

func TestSilentWorkerLosesItsRun(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), silence(300*time.Millisecond), leaseTTL(100*time.Millisecond))
	if _, err := app.Executor(newStub("mute")); err != nil {
		t.Fatal(err)
	}
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "mute", "input": map[string]any{"prompt": "x"}})
	lost := waitRun(t, app, run["id"].(string), sessions.StatusFailed)
	if !strings.Contains(lost.Error, "no ack from the worker") {
		t.Fatalf("run = %+v", lost)
	}
}

func TestApprovalExpiryEndToEnd(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), BuildDigest(digestA), ApprovalExpiry(300*time.Millisecond), TimerPoll(20*time.Millisecond))
	var refunded atomic.Int32
	model := wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "refund", Args: `{"order":42}`}), wefttest.Say("not refunded"))
	if err := app.Agent(refunds(model, &refunded)); err != nil {
		t.Fatal(err)
	}
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "refund 42"}})
	id := run["id"].(string)
	parked := waitRun(t, app, id, sessions.StatusParked)
	deadline := time.Now().Add(10 * time.Second)
	for parked.ResumedBy == "" {
		if time.Now().After(deadline) {
			t.Fatal("the approval never expired")
		}
		time.Sleep(20 * time.Millisecond)
		parked, _ = app.Ledger().Get(context.Background(), tenant.Default(), id)
	}
	res := waitRun(t, app, parked.ResumedBy, sessions.StatusSucceeded, sessions.StatusFailed)
	if res.Status != sessions.StatusSucceeded || refunded.Load() != 0 {
		t.Fatalf("the expiry's resume = %+v (refunded %d)", res, refunded.Load())
	}
	if got := strings.Join(runEvents(t, app, id), ","); got != "run.queued,run.started,run.parked,approval.expired" {
		t.Errorf("events = %s", got)
	}
}

func TestSteerEndToEnd(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), BuildDigest(digestA))
	started, gate := make(chan struct{}, 1), make(chan struct{})
	wait := core.Tool("wait", "Wait.", func(context.Context, struct{}) (string, error) {
		started <- struct{}{}
		<-gate
		return "ok", nil
	})
	model := wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("done"))
	if err := app.Agent(core.New(model, core.Name("slow"), wait)); err != nil {
		t.Fatal(err)
	}
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "slow", "input": map[string]any{"prompt": "go"}})
	id := run["id"].(string)
	<-started
	waitRun(t, app, id, sessions.StatusRunning)
	msg := map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "only EU orders"}}}
	if code, body := postJSON(t, ts.URL+"/v1/runs/"+id+"/steer", map[string]any{"messages": []any{msg}}); code != 202 || body["steered"] != float64(1) {
		t.Fatalf("steer: %d %v", code, body)
	}
	bad := map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "x"}}}
	if code, body := postJSON(t, ts.URL+"/v1/runs/"+id+"/steer", map[string]any{"messages": []any{bad}}); code != 400 ||
		body["error"].(map[string]any)["code"] != "invalid_steer" {
		t.Errorf("an assistant steer: %d %v", code, body)
	}
	close(gate)
	waitRun(t, app, id, sessions.StatusSucceeded)
	if got := model.LastRequest().LastText(); got != "only EU orders" {
		t.Errorf("the model's last user text = %q", got)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs/"+id+"/steer", map[string]any{"messages": []any{msg}}); code != 409 {
		t.Errorf("steer of a finished run: %d, want 409", code)
	}
	if got := strings.Join(runEvents(t, app, id), ","); got != "run.queued,run.started,run.steered,run.finished" {
		t.Errorf("events = %s", got)
	}
}
