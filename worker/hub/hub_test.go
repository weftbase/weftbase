package hub_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/worker/hub"
)

// fake is a scripted executor: answers[i] answers the i-th dispatch
// (accepted when there are none left); acks go out through send.
type fake struct {
	reg     wire.Registration
	mu      sync.Mutex
	answers []func(wire.Command) (wire.Ack, error)
	got     []wire.Command
	cancels []wire.Cancel
	steers  []wire.Steer
	events  chan wire.Frame
}

func newFake(agent string) *fake {
	body := []byte(`{"weft":1,"agents":[{"name":"` + agent + `"}]}`)
	return &fake{events: make(chan wire.Frame, 64), reg: wire.Registration{Placement: wire.PlacementLocal,
		Build:  wire.Build{Digest: "sha256:f0"},
		Agents: []wire.AgentRegistration{{Name: agent, Manifest: body, ManifestHash: registry.ManifestHash(body)}}}}
}

func (f *fake) Register(context.Context) (wire.Registration, error) { return f.reg, nil }
func (f *fake) Dispatch(_ context.Context, c wire.Command) (wire.Ack, error) {
	f.mu.Lock()
	f.got = append(f.got, c)
	var ans func(wire.Command) (wire.Ack, error)
	if len(f.answers) > 0 {
		ans, f.answers = f.answers[0], f.answers[1:]
	}
	f.mu.Unlock()
	if ans != nil {
		return ans(c)
	}
	return wire.Ack{CommandID: c.CommandID, State: wire.AckAccepted}, nil
}
func (f *fake) Cancel(_ context.Context, c wire.Cancel) error {
	f.mu.Lock()
	f.cancels = append(f.cancels, c)
	f.mu.Unlock()
	return nil
}
func (f *fake) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(yield func(wire.Frame, error) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case fr := <-f.events:
				if !yield(fr, nil) {
					return
				}
			}
		}
	}
}
func (f *fake) Steer(_ context.Context, s wire.Steer) (wire.Ack, error) {
	f.mu.Lock()
	f.steers = append(f.steers, s)
	f.mu.Unlock()
	return wire.Ack{CommandID: s.CommandID, State: wire.AckAccepted, RunID: s.RunID}, nil
}
func (f *fake) Drain(context.Context, time.Time) error { return nil }
func (f *fake) send(a wire.Ack)                        { f.events <- wire.AckFrame(a) }
func (f *fake) commands() []wire.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wire.Command(nil), f.got...)
}

type env struct {
	h   *hub.Hub
	log *events.Log
	l   *sessions.Ledger
	f   *fake
	s   tenant.Scope
	db  *sqlite.DB
}

func setup(t *testing.T, ttl time.Duration, f *fake, cfg ...func(*hub.Config)) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	log := events.New(db)
	reg := registry.New(db, log)
	q := queue.New(db)
	l := sessions.New(db, log, reg, q)
	c := hub.Config{DB: db, Events: log, Queue: q, Registry: reg, Sink: l, LeaseTTL: ttl, Poll: 10 * time.Millisecond}
	for _, fn := range cfg {
		fn(&c)
	}
	h := hub.New(c)
	l.SetCanceler(h)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = h.Run(runCtx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = h.Close(ctx, time.Now())
		_ = db.Close()
	})
	if _, _, err := h.Attach(ctx, tenant.Default(), f); err != nil {
		t.Fatal(err)
	}
	return &env{h: h, log: log, l: l, f: f, s: tenant.Default(), db: db}
}

func (e *env) admit(t *testing.T) string {
	t.Helper()
	run, err := e.l.Admit(context.Background(), e.s, events.System, sessions.AdmitRequest{Agent: "a", Input: wire.Input{Prompt: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func (e *env) wait(t *testing.T, id string, cond func(sessions.Run) bool) sessions.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := e.l.Get(context.Background(), e.s, id)
		if err != nil {
			t.Fatal(err)
		}
		if cond(run) {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s stuck at %+v", id, run)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func status(s string) func(sessions.Run) bool {
	return func(r sessions.Run) bool { return r.Status == s }
}

func waitCommands(t *testing.T, f *fake, n int) []wire.Command {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.commands()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d commands, want %d", len(f.commands()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f.commands()
}

func TestAcksDriveTheLedger(t *testing.T) {
	e := setup(t, time.Second, newFake("a"))
	id := e.admit(t)
	cmd := waitCommands(t, e.f, 1)[0]
	if cmd.RunID != id || !wire.ValidCommandID(cmd.CommandID) || cmd.Validate() != nil {
		t.Fatalf("command = %+v", cmd)
	}
	e.f.send(wire.Ack{CommandID: "cmd_unknown", State: wire.AckFinished, Status: wire.StatusFailed}) // not ours: ignored
	e.f.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckRunStarted, RunID: id})
	e.wait(t, id, status(sessions.StatusRunning))
	e.f.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: id, Status: wire.StatusSucceeded})
	e.f.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: id, Status: wire.StatusFailed, Error: "dup"})
	run := e.wait(t, id, status(sessions.StatusSucceeded))
	time.Sleep(50 * time.Millisecond)
	if again, _ := e.l.Get(context.Background(), e.s, id); again.Status != sessions.StatusSucceeded || run.Error != "" {
		t.Errorf("a duplicate finished changed the run: %+v", again)
	}
	if e.h.InFlight() != 0 {
		t.Errorf("in flight = %d", e.h.InFlight())
	}
}

func TestBusyRequeuesRejectedAndErrorsEnd(t *testing.T) {
	f := newFake("a")
	f.answers = []func(wire.Command) (wire.Ack, error){
		func(c wire.Command) (wire.Ack, error) {
			return wire.Ack{CommandID: c.CommandID, State: wire.AckBusy}, nil
		},
		func(c wire.Command) (wire.Ack, error) {
			return wire.Ack{CommandID: c.CommandID, State: wire.AckRejected, Error: "not today"}, nil
		},
		func(c wire.Command) (wire.Ack, error) { return wire.Ack{}, errors.New("socket closed") },
	}
	e := setup(t, time.Second, f)
	busy := e.admit(t)
	// busy: handed back and dispatched again later (released for 1 s),
	// as a new command — never marked lost.
	cmds := waitCommands(t, f, 1)
	if cmds[0].RunID != busy {
		t.Fatal(cmds)
	}
	rejected := e.admit(t)
	if r := e.wait(t, rejected, status(sessions.StatusFailed)); !strings.Contains(r.Error, "rejected: not today") {
		t.Errorf("rejected = %+v", r)
	}
	broken := e.admit(t)
	if r := e.wait(t, broken, status(sessions.StatusFailed)); !strings.HasPrefix(r.Error, sessions.ErrWorkerLost) {
		t.Errorf("dispatch error = %+v", r)
	}
	cmds = waitCommands(t, f, 4)
	if cmds[3].RunID != busy || cmds[3].CommandID == cmds[0].CommandID {
		t.Fatalf("the busy run's second dispatch = %+v", cmds[3])
	}
	if r, _ := e.l.Get(context.Background(), e.s, busy); r.Status != sessions.StatusQueued {
		t.Errorf("busy run = %+v", r)
	}
}

// A run longer than the lease stays leased while the hub holds it: the
// item is never claimed twice.
func TestLeaseOutlivesItsTTLWhileHeld(t *testing.T) {
	e := setup(t, 60*time.Millisecond, newFake("a"))
	id := e.admit(t)
	cmd := waitCommands(t, e.f, 1)[0]
	e.f.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckRunStarted, RunID: id})
	time.Sleep(300 * time.Millisecond) // five TTLs
	if n := len(e.f.commands()); n != 1 {
		t.Fatalf("dispatched %d times", n)
	}
	if r, _ := e.l.Get(context.Background(), e.s, id); r.Status != sessions.StatusRunning {
		t.Fatalf("run = %+v", r)
	}
	// Cancel goes to the holder; its end is the worker's report.
	if _, err := e.l.Cancel(context.Background(), e.s, events.System, id, ""); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	cancels := append([]wire.Cancel(nil), e.f.cancels...)
	e.f.mu.Unlock()
	if len(cancels) != 1 || cancels[0].RunID != id || cancels[0].Cause != wire.CauseUser || !wire.ValidCommandID(cancels[0].CommandID) {
		t.Fatalf("cancels = %+v", cancels)
	}
	e.f.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: id, Status: wire.StatusCanceled, Cause: wire.CauseUser})
	e.wait(t, id, status(sessions.StatusCanceled))
}
