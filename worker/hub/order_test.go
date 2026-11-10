package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/sessions"
)

// cappedFake is a worker with a capacity of its own, as client/worker's
// Runner: it answers busy past cap runs in hand.
type cappedFake struct {
	*fake
	cap      int
	held     map[string]bool
	accepted []string // run ids, in the order they were taken
}

func (c *cappedFake) Dispatch(ctx context.Context, cmd wire.Command) (wire.Ack, error) {
	c.mu.Lock()
	c.got = append(c.got, cmd)
	if len(c.held) >= c.cap {
		c.mu.Unlock()
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckBusy}, nil
	}
	c.held[cmd.RunID] = true
	c.accepted = append(c.accepted, cmd.RunID)
	c.mu.Unlock()
	return wire.Ack{CommandID: cmd.CommandID, State: wire.AckAccepted}, nil
}

func (c *cappedFake) finish(cmd wire.Command) {
	c.mu.Lock()
	delete(c.held, cmd.RunID)
	c.mu.Unlock()
	c.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID, Status: wire.StatusSucceeded})
}

func (c *cappedFake) taken() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.accepted...)
}

// TestAtCapacityRunsStartInOrder: at a capacity of 1 with runs arriving
// while one is in hand, the runs start in the order they were admitted.
// A busy answer used to push the item one second back (run_at), so a
// run admitted later overtook it, and a steady inflow could starve it.
func TestAtCapacityRunsStartInOrder(t *testing.T) {
	e := setup(t, time.Second, newFake("z")) // the hub; agent a is served below
	base := newFake("a")
	base.reg.Build.Digest = "sha256:f1"
	base.reg.Agents[0].Concurrency = 1
	c := &cappedFake{fake: base, cap: 1, held: map[string]bool{}}
	if _, _, err := e.h.Attach(context.Background(), e.s, c); err != nil {
		t.Fatal(err)
	}
	var admitted []string
	waitTaken := func(n int) []string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(c.taken()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("%d runs taken, want %d (admitted %v)", len(c.taken()), n, admitted)
			}
			time.Sleep(2 * time.Millisecond)
		}
		return c.taken()
	}
	finishCurrent := func() {
		taken := c.taken()
		for _, x := range base.commands() {
			if x.RunID == taken[len(taken)-1] {
				c.finish(x)
				return
			}
		}
		t.Fatal("the run in hand has no command")
	}
	admitted = append(admitted, e.admit(t))
	waitTaken(1)
	// Each round two runs arrive while one is in hand (the hub may try
	// them on the full worker meanwhile), then the run in hand ends.
	for round := 0; round < 4; round++ {
		admitted = append(admitted, e.admit(t))
		time.Sleep(20 * time.Millisecond) // the hub's chance to try (and, before, bump) it
		admitted = append(admitted, e.admit(t))
		finishCurrent()
		waitTaken(round + 2)
	}
	for len(c.taken()) < len(admitted) {
		finishCurrent()
		waitTaken(len(c.taken()) + 1)
	}
	taken := c.taken()
	for i := range admitted {
		if taken[i] != admitted[i] {
			t.Fatalf("start order %v, want the admit order %v", taken, admitted)
		}
	}
	for _, id := range admitted {
		if r, _ := e.l.Get(context.Background(), e.s, id); r.Status == sessions.StatusFailed {
			t.Errorf("run %s failed: %+v", id, r)
		}
	}
	_ = events.System
}
