package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// clock is a test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fire fires every due timer, as the scheduler's timer loop does.
func (f *fixture) fire(t *testing.T) int {
	t.Helper()
	due, err := f.q.Due(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range due {
		f.tx(t, func(tx store.Tx) error {
			ok, err := f.q.Fire(tx, tm.ID)
			if err != nil || !ok {
				return err
			}
			return f.l.OnTimer(tx, tm)
		})
	}
	return len(due)
}

func (f *fixture) park(t *testing.T, ctx context.Context) sessions.Run {
	t.Helper()
	run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund 42"}})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, ok := f.claim(t)
	if !ok {
		t.Fatal("nothing to dispatch")
	}
	f.tx(t, func(tx store.Tx) error { // the end and the item's completion, as the hub records them
		if err := f.l.Finished(tx, f.s, run.ID, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{"messages":[]}`),
			Pending: []wire.PendingCall{{CallID: "call_1", Tool: "refund"}}}); err != nil {
			return err
		}
		return f.q.Done(tx, lease)
	})
	got, _ := f.l.Get(ctx, f.s, run.ID)
	if got.Status != sessions.StatusParked || got.ParkedAt == nil {
		t.Fatalf("parked = %+v", got)
	}
	return got
}

func TestQueueTTLFailsAnUnservedRun(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f := setupClock(t, c.now, sessions.WithPolicy(sessions.Policy{QueueTTL: time.Hour, MaxPinAge: -1}))
	unserved, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.fire(t); n != 0 {
		t.Fatalf("%d timers fired before their time", n)
	}
	c.add(2 * time.Hour)
	f.fire(t)
	got, _ := f.l.Get(ctx, f.s, unserved.ID)
	if got.Status != sessions.StatusFailed || !strings.HasPrefix(got.Error, sessions.ErrWorkerLost) || !strings.Contains(got.Error, "time-to-live") {
		t.Fatalf("after the queue TTL = %+v", got)
	}
	if _, err := f.q.Claim(ctx, queue.ClaimFilter{Owner: "x"}); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("its queue item is still claimable: %v", err)
	}
	if ts := strings.Join(f.types(t, unserved.ID), ","); ts != "run.queued,run.failed" {
		t.Errorf("events = %s", ts)
	}

	// A run dispatched before its time-to-live is left to its worker.
	held, _ := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "b"}})
	f.claim(t)
	c.add(2 * time.Hour)
	f.fire(t)
	if got, _ := f.l.Get(ctx, f.s, held.ID); got.Status != sessions.StatusQueued {
		t.Errorf("a dispatched run after the TTL = %+v", got)
	}
}

func TestApprovalExpiryDenies(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f := setupClock(t, c.now, sessions.WithPolicy(sessions.Policy{ApprovalExpiry: time.Hour, MaxPinAge: -1, QueueTTL: -1}))
	parked := f.park(t, ctx)
	c.add(30 * time.Minute)
	f.fire(t)
	if got, _ := f.l.Get(ctx, f.s, parked.ID); got.ResumedBy != "" {
		t.Fatal("resumed before the expiry")
	}
	c.add(time.Hour)
	f.fire(t)
	got, _ := f.l.Get(ctx, f.s, parked.ID)
	if got.ResumedBy == "" {
		t.Fatalf("not resumed after the expiry: %+v", got)
	}
	_, cmd, ok := f.claim(t)
	if !ok || cmd.Kind != wire.KindResume || len(cmd.Resume.Decisions) != 1 || cmd.Resume.Decisions[0].Decision != "deny" ||
		cmd.Resume.Decisions[0].Reason != "approval expired" || cmd.AgentVersion != parked.AgentVersion {
		t.Fatalf("the expiry's resume = %+v", cmd)
	}
	if ts := strings.Join(f.types(t, parked.ID), ","); ts != "run.queued,run.started,run.parked,approval.expired" &&
		ts != "run.queued,run.parked,approval.expired" {
		t.Errorf("events = %s", ts)
	}
	// A run decided before its expiry is left alone.
	decided := f.park(t, ctx)
	if _, err := f.l.Resume(ctx, f.s, events.System, decided.ID, []wire.Decision{{CallID: "call_1", Decision: "approve"}}); err != nil {
		t.Fatal(err)
	}
	c.add(2 * time.Hour)
	f.fire(t)
	if ts := f.types(t, decided.ID); ts[len(ts)-1] == events.ApprovalExpired {
		t.Error("a decided run expired")
	}
}

func TestMaxPinAgeMigrates(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f := setupClock(t, c.now, sessions.WithPolicy(sessions.Policy{MaxPinAge: 24 * time.Hour, QueueTTL: -1}))
	stays := f.park(t, ctx)
	c.add(25 * time.Hour)
	f.fire(t)
	got, _ := f.l.Get(ctx, f.s, stays.ID)
	if got.PinExpiredAt == nil || got.MigratedTo != "" {
		t.Fatalf("a run on the current version = %+v", got)
	}

	parked := f.park(t, ctx)
	body := []byte(`{"weft":1,"agents":[{"name":"support","instructions":"v2"}]}`)
	vs, err := f.reg.RegisterIn(ctx, f.s, "sha256:bbb", "binary", []registry.AgentSpec{{Name: "support", Manifest: body, ManifestHash: registry.ManifestHash(body)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reg.PromoteIn(ctx, f.s, registry.PromoteRequest{Agent: "support", Label: "prod", Version: vs[0].ID, Actor: events.System}); err != nil {
		t.Fatal(err)
	}
	c.add(25 * time.Hour)
	f.fire(t)
	got, _ = f.l.Get(ctx, f.s, parked.ID)
	if got.MigratedTo != vs[0].ID || got.PinExpiredAt == nil || got.Status != sessions.StatusParked {
		t.Fatalf("after the maximum pin age = %+v", got)
	}
	res, err := f.l.Resume(ctx, f.s, events.System, parked.ID, []wire.Decision{{CallID: "call_1", Decision: "approve"}})
	if err != nil || res.AgentVersion != vs[0].ID || res.ResolvedVia != sessions.ViaMigrate {
		t.Fatalf("the migrated run's resume = %+v, %v", res, err)
	}
	if ts := f.types(t, parked.ID); ts[len(ts)-1] != events.RunMigrated {
		t.Errorf("events = %v", ts)
	}
}

type fakeSteerer struct {
	err error
	got []string
}

func (s *fakeSteerer) SteerRun(_ context.Context, _ tenant.Scope, runID string, _ []json.RawMessage) error {
	s.got = append(s.got, runID)
	return s.err
}

func TestSteer(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	st := &fakeSteerer{}
	f.l.SetSteerer(st)
	user := []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"only EU"}]}`)}
	run, _ := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "a"}})
	if err := f.l.Steer(ctx, f.s, events.System, run.ID, user); !errors.Is(err, sessions.ErrNotRunning) {
		t.Errorf("steer of a queued run = %v", err)
	}
	f.claim(t)
	f.tx(t, func(tx store.Tx) error { return f.l.Started(tx, f.s, run.ID) })
	if err := f.l.Steer(ctx, f.s, events.System, run.ID, []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)}); !errors.Is(err, sessions.ErrInvalidSteer) {
		t.Errorf("an assistant steer = %v", err)
	}
	if err := f.l.Steer(ctx, f.s, events.Actor{Kind: "key", ID: "k1"}, run.ID, user); err != nil {
		t.Fatalf("steer = %v", err)
	}
	if len(st.got) != 1 || st.got[0] != run.ID {
		t.Errorf("delivered = %v", st.got)
	}
	if ts := f.types(t, run.ID); ts[len(ts)-1] != events.RunSteered {
		t.Errorf("events = %v", ts)
	}
	st.err = errors.New("not held")
	if err := f.l.Steer(ctx, f.s, events.System, run.ID, user); !errors.Is(err, sessions.ErrNotRunning) {
		t.Errorf("a steer its worker refused = %v", err)
	}
	if err := f.l.Steer(ctx, f.s, events.System, "run_nope", user); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("steer of an unknown run = %v", err)
	}
}
