package hub_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/worker/hub"
)

// The hub's side of the legacy link hardening tests
// (the legacy branch's server-side hardening_test.go), re-targeted
// at the worker protocol: a link that ends loses the runs that ended
// with it and records worker.disconnected; a held command whose worker
// stalls is lost after the silence bound (the old finish watch), and a
// duplicate or unknown ack does not disarm that; run-scoped commands
// (cancel, steer) go to the holder.

func (e *env) outbox(t *testing.T, typ string) []events.Event {
	t.Helper()
	sel := events.ForScope(e.s, false)
	evs, err := e.log.List(context.Background(), sel, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// running admits a run and has the worker start it. The command count
// is taken before the admit: the hub can dispatch before Admit returns.
func running(t *testing.T, e *env) (string, wire.Command) {
	t.Helper()
	n := len(e.f.commands())
	id := e.admit(t)
	cmd := waitCommands(t, e.f, n+1)
	c := cmd[n]
	if c.RunID != id {
		t.Fatalf("command %d is for %s, want %s", n, c.RunID, id)
	}
	e.f.send(wire.Ack{CommandID: c.CommandID, State: wire.AckRunStarted, RunID: id})
	e.wait(t, id, status(sessions.StatusRunning))
	return id, c
}

func disconnect(f *fake, d wire.Disconnected) {
	fr, _ := wire.NewFrame(wire.EventDisconnected, "", d)
	f.events <- fr
}

func TestDisconnectLosesTheRunsThatWentWithIt(t *testing.T) {
	e := setup(t, time.Second, newFake("a"))
	id, _ := running(t, e)
	// The socket dropped, the process lives: the run goes on.
	disconnect(e.f, wire.Disconnected{Reason: "socket closed"})
	time.Sleep(50 * time.Millisecond)
	if r, _ := e.l.Get(context.Background(), e.s, id); r.Status != sessions.StatusRunning {
		t.Fatalf("a link drop ended the run: %+v", r)
	}
	// The process exited: its runs are lost at once.
	disconnect(e.f, wire.Disconnected{Reason: "the child exited", Lost: true})
	r := e.wait(t, id, status(sessions.StatusFailed))
	if !strings.HasPrefix(r.Error, sessions.ErrWorkerLost) || !strings.Contains(r.Error, "the child exited") {
		t.Errorf("lost run = %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.outbox(t, events.WorkerDisconnected)) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	evs := e.outbox(t, events.WorkerDisconnected)
	if len(evs) != 2 {
		t.Fatalf("worker.disconnected events = %d", len(evs))
	}
	var data struct {
		Reason   string   `json:"reason"`
		LostRuns []string `json:"lost_runs"`
	}
	_ = json.Unmarshal(evs[1].Data, &data)
	if data.Reason != "the child exited" || len(data.LostRuns) != 1 || data.LostRuns[0] != id {
		t.Errorf("event = %s", evs[1].Data)
	}
	if e.h.InFlight() != 0 {
		t.Errorf("in flight = %d", e.h.InFlight())
	}
}

// TestSilentWorkerLosesTheRun: a worker that accepted a command and
// then stalled for good used to leave the row accepted forever. A held
// run unheard for the silence bound is lost; a beating one is not, and
// a duplicate or unknown ack is not a beat that rescues a stalled one.
func TestSilentWorkerLosesTheRun(t *testing.T) {
	// The lease (5 s, kept alive by the hub) outlasts the silence bound
	// (1 s), which outlasts the beat (50 ms) twentyfold: the stalled run
	// is lost by the silence bound alone, the beating one never.
	e := setup(t, 5*time.Second, newFake("a"), func(c *hub.Config) { c.Silence = time.Second })
	beating, bc := running(t, e)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				e.f.send(wire.Ack{CommandID: bc.CommandID, State: wire.AckHeartbeat, RunID: beating})
			}
		}
	}()
	stalled, sc := running(t, e)
	e.f.send(wire.Ack{CommandID: sc.CommandID, State: "bogus", RunID: stalled})
	r := e.wait(t, stalled, status(sessions.StatusFailed))
	if !strings.Contains(r.Error, "no ack from the worker") {
		t.Errorf("stalled = %+v", r)
	}
	if r, _ := e.l.Get(context.Background(), e.s, beating); r.Status != sessions.StatusRunning {
		t.Errorf("the beating run = %+v", r)
	}
	// A late finished ack of the lost run changes nothing.
	e.f.send(wire.Ack{CommandID: sc.CommandID, State: wire.AckFinished, RunID: stalled, Status: wire.StatusSucceeded})
	time.Sleep(50 * time.Millisecond)
	if r, _ := e.l.Get(context.Background(), e.s, stalled); r.Status != sessions.StatusFailed {
		t.Errorf("a late ack revived the lost run: %+v", r)
	}
}

func TestSteerGoesToTheHolder(t *testing.T) {
	e := setup(t, time.Second, newFake("a"))
	id, _ := running(t, e)
	msg := []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`)}
	if err := e.h.SteerRun(context.Background(), e.s, id, msg); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	steers := append([]wire.Steer(nil), e.f.steers...)
	e.f.mu.Unlock()
	if len(steers) != 1 || steers[0].RunID != id || !wire.ValidCommandID(steers[0].CommandID) || len(steers[0].Messages) != 1 {
		t.Fatalf("steers = %+v", steers)
	}
	if err := e.h.SteerRun(context.Background(), e.s, "run_elsewhere", msg); !errors.Is(err, hub.ErrNotHeld) {
		t.Errorf("steer of a run not held = %v", err)
	}
}

// goneExec is a worker whose event stream ends: it went away.
type goneExec struct {
	*fake
	gone chan struct{}
}

func (g *goneExec) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(func(wire.Frame, error) bool) {
		select {
		case <-g.gone:
		case <-ctx.Done():
		}
	}
}

func TestEndedStreamDetachesTheWorker(t *testing.T) {
	e := setup(t, time.Second, newFake("a"))
	g := &goneExec{fake: newFake("b"), gone: make(chan struct{})}
	w, _, err := e.h.Attach(context.Background(), e.s, g)
	if err != nil {
		t.Fatal(err)
	}
	close(g.gone)
	deadline := time.Now().Add(5 * time.Second)
	for {
		routed := false
		for _, x := range e.h.Router().Workers() {
			routed = routed || x.ID == w.ID
		}
		if !routed && len(e.outbox(t, events.WorkerDisconnected)) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the gone worker is still routed to, or no worker.disconnected")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
