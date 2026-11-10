// Package storetest is the control store's conformance suite: every
// backend (store/sqlite now, store/postgres later) runs it. The
// repositories are written once, in the domains, as portable SQL over
// store.Tx; what a backend owns — its transactions, its error mapping,
// its migrations — is what the suite pins, through the domains' own
// services: the transaction contract, the outbox, the registry, the
// queue and its timers, the run ledger, and a cross-tenant group (a
// row written under one scope is never read, claimed, changed or
// deleted under another).
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) store.Runner { … a fresh, migrated store … })
//	}
package storetest

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

// Opener opens a fresh, migrated, empty control store for one test;
// the opener closes it when the test ends.
type Opener func(t *testing.T) store.Runner

// Run runs every group against stores open makes.
func Run(t *testing.T, open Opener) {
	t.Run("Tx", func(t *testing.T) { testTx(t, open(t)) })
	t.Run("Outbox", func(t *testing.T) { testOutbox(t, open(t)) })
	t.Run("Registry", func(t *testing.T) { testRegistry(t, open(t)) })
	t.Run("Queue", func(t *testing.T) { testQueue(t, open(t)) })
	t.Run("Timers", func(t *testing.T) { testTimers(t, open(t)) })
	t.Run("Ledger", func(t *testing.T) { testLedger(t, open(t)) })
	t.Run("CrossTenant", func(t *testing.T) { testCrossTenant(t, open(t)) })
}

// clock is a settable clock shared by the services under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)} }

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

func scope(t *testing.T, project, env, ten string) tenant.Scope {
	t.Helper()
	s, err := tenant.NewScope(project, env, ten)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func inTx(t *testing.T, db store.Runner, fn func(tx store.Tx) error) {
	t.Helper()
	if err := db.InTx(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func manifest(name, extra string) registry.AgentSpec {
	body := []byte(`{"weft":1,"agents":[{"name":"` + name + `"` + extra + `}]}`)
	return registry.AgentSpec{Name: name, Manifest: body, ManifestHash: registry.ManifestHash(body)}
}

// testTx: commit and rollback, AfterCommit on commit only, a read
// view, and sql.ErrNoRows mapped to store.ErrNotFound.
func testTx(t *testing.T, db store.Runner) {
	ctx := context.Background()
	log := events.New(db)
	s := tenant.Default()
	var after []string
	boom := errors.New("boom")
	err := db.InTx(ctx, func(tx store.Tx) error {
		tx.AfterCommit(func() { after = append(after, "rolled back") })
		if _, err := log.Emit(tx, s, events.Event{Type: events.RunQueued, ResourceKind: "run", ResourceID: "r_gone"}, nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx returned %v, want fn's error", err)
	}
	inTx(t, db, func(tx store.Tx) error {
		tx.AfterCommit(func() { after = append(after, "committed") })
		_, err := log.Emit(tx, s, events.Event{Type: events.RunQueued, ResourceKind: "run", ResourceID: "r_kept"}, nil)
		return err
	})
	if strings.Join(after, ",") != "committed" {
		t.Errorf("AfterCommit ran for %v", after)
	}
	evs, err := log.List(ctx, events.ForScope(s, false), 0, 10)
	if err != nil || len(evs) != 1 || evs[0].ResourceID != "r_kept" {
		t.Fatalf("after a rollback and a commit: %+v, %v", evs, err)
	}
	err = db.Read(ctx, func(tx store.Tx) error {
		var n int
		return store.NotFound(store.QueryRow(tx, `SELECT seq FROM events WHERE resource_id = ?`, "r_none").Scan(&n))
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a missing row = %v, want store.ErrNotFound", err)
	}
}

// testOutbox: seq follows commit order; the hub-facing reads (After,
// Head) and the consumer cursors advance only with their transaction.
func testOutbox(t *testing.T, db store.Runner) {
	ctx := context.Background()
	log := events.New(db)
	s := tenant.Default()
	for _, id := range []string{"a", "b", "c"} {
		inTx(t, db, func(tx store.Tx) error {
			_, err := log.Emit(tx, s, events.Event{Type: events.RunQueued, ResourceKind: "run", ResourceID: id}, map[string]any{"id": id})
			return err
		})
	}
	all, err := log.After(ctx, 0, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("After = %d, %v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq || all[i].ResourceID <= all[i-1].ResourceID {
			t.Errorf("seq order: %d %s after %d %s", all[i].Seq, all[i].ResourceID, all[i-1].Seq, all[i-1].ResourceID)
		}
	}
	if head, _ := log.Head(ctx); head != all[2].Seq {
		t.Errorf("Head = %d, want %d", head, all[2].Seq)
	}
	_ = db.InTx(ctx, func(tx store.Tx) error {
		if err := log.Advance(tx, "webhooks", all[2].Seq); err != nil {
			t.Fatal(err)
		}
		return errors.New("rollback")
	})
	if c, _ := log.Cursor(ctx, "webhooks"); c != 0 {
		t.Errorf("a rolled-back advance moved the cursor to %d", c)
	}
	inTx(t, db, func(tx store.Tx) error { return log.Advance(tx, "webhooks", all[1].Seq) })
	if c, _ := log.Cursor(ctx, "webhooks"); c != all[1].Seq {
		t.Errorf("cursor = %d, want %d", c, all[1].Seq)
	}
}

// testRegistry: builds and versions are idempotent by digest and
// manifest; numbers count per agent; labels move with a compare;
// Resolve takes prod, else the newest, or a pin.
func testRegistry(t *testing.T, db store.Runner) {
	ctx := context.Background()
	reg := registry.New(db, events.New(db))
	s := tenant.Default()
	a1, err := reg.RegisterIn(ctx, s, "sha256:aaa", wire.BuildBinary, []registry.AgentSpec{manifest("support", "")})
	if err != nil || len(a1) != 1 || a1[0].Number != 1 {
		t.Fatalf("first build = %+v, %v", a1, err)
	}
	again, err := reg.RegisterIn(ctx, s, "sha256:aaa", wire.BuildBinary, []registry.AgentSpec{manifest("support", "")})
	if err != nil || again[0].ID != a1[0].ID {
		t.Fatalf("the same build again = %+v, %v", again, err)
	}
	b1, err := reg.RegisterIn(ctx, s, "sha256:bbb", wire.BuildBinary, []registry.AgentSpec{manifest("support", "")})
	if err != nil || b1[0].Number != 2 || b1[0].ID == a1[0].ID {
		t.Fatalf("a second build = %+v, %v", b1, err)
	}
	bad := manifest("support", "")
	bad.ManifestHash = "beef"
	if _, err := reg.RegisterIn(ctx, s, "sha256:ccc", wire.BuildBinary, []registry.AgentSpec{bad}); !errors.Is(err, registry.ErrHashMismatch) {
		t.Errorf("a wrong manifest hash = %v", err)
	}
	var v registry.Version
	var via string
	inTx(t, db, func(tx store.Tx) error { v, via, err = reg.Resolve(tx, s, "support", ""); return err })
	if v.ID != b1[0].ID || via != registry.ViaLatest {
		t.Errorf("no prod label: %s via %s", v.ID, via)
	}
	if err := reg.PromoteIn(ctx, s, registry.PromoteRequest{Agent: "support", Label: "prod", Version: a1[0].ID, Actor: events.System}); err != nil {
		t.Fatal(err)
	}
	if err := reg.PromoteIn(ctx, s, registry.PromoteRequest{Agent: "support", Label: "prod", Version: b1[0].ID, Expected: "agv_stale", Actor: events.System}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale expected = %v, want store.ErrConflict", err)
	}
	inTx(t, db, func(tx store.Tx) error { v, via, err = reg.Resolve(tx, s, "support", ""); return err })
	if v.ID != a1[0].ID || via != "label:prod" {
		t.Errorf("prod: %s via %s", v.ID, via)
	}
	inTx(t, db, func(tx store.Tx) error { v, via, err = reg.Resolve(tx, s, "support", b1[0].ID); return err })
	if v.ID != b1[0].ID || via != registry.ViaPin {
		t.Errorf("pin: %s via %s", v.ID, via)
	}
	kind, agents, err := reg.BuildAgents(ctx, s, "sha256:bbb")
	if err != nil || kind != wire.BuildBinary || len(agents) != 1 || agents[0].ManifestHash != b1[0].ManifestHash {
		t.Errorf("BuildAgents = %s %+v, %v", kind, agents, err)
	}
	if m, err := reg.Manifest(ctx, s, a1[0].ManifestHash); err != nil || m.Agent != "support" {
		t.Errorf("Manifest = %+v, %v", m, err)
	}
}

// testQueue: claim order, routes, kinds, concurrency keys, fencing,
// extension, completion, retries, release and cancel.
func testQueue(t *testing.T, db store.Runner) {
	ctx := context.Background()
	c := newClock()
	q := queue.New(db, queue.Clock(c.now))
	s := tenant.Default()
	enqueue := func(it queue.Item) string {
		var id string
		inTx(t, db, func(tx store.Tx) error { var err error; id, err = q.Enqueue(tx, s, it); return err })
		return id
	}
	low := enqueue(queue.Item{Kind: queue.KindRun, Route: "agv_a", Priority: 0})
	high := enqueue(queue.Item{Kind: queue.KindRun, Route: "agv_a", Priority: 5})
	later := enqueue(queue.Item{Kind: queue.KindRun, Route: "agv_a", Priority: 9, RunAt: c.now().Add(time.Hour)})
	other := enqueue(queue.Item{Kind: queue.KindResume, Route: "agv_b"})
	claim := func(f queue.ClaimFilter) (queue.Lease, error) {
		if f.Owner == "" {
			f.Owner = "node_1"
		}
		return q.Claim(ctx, f)
	}
	l, err := claim(queue.ClaimFilter{Routes: []string{"agv_a"}})
	if err != nil || l.ID != high || l.Fence != 1 || l.Attempts != 1 {
		t.Fatalf("first claim = %+v, %v; want the highest priority due item", l, err)
	}
	if _, err := claim(queue.ClaimFilter{Routes: []string{}}); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("no routes = %v", err)
	}
	if o, err := claim(queue.ClaimFilter{Kinds: []string{queue.KindResume}}); err != nil || o.ID != other {
		t.Errorf("by kind = %+v, %v", o, err)
	}
	if err := q.Extend(ctx, &l, time.Minute); err != nil || !l.Until.Equal(c.now().Add(time.Minute)) {
		t.Errorf("Extend = %v (until %v)", err, l.Until)
	}
	inTx(t, db, func(tx store.Tx) error { return q.Done(tx, l) })
	if err := q.Extend(ctx, &l, time.Minute); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("Extend after Done = %v", err)
	}
	l2, err := claim(queue.ClaimFilter{Routes: []string{"agv_a"}})
	if err != nil || l2.ID != low {
		t.Fatalf("second claim = %+v, %v (the delayed item is not due)", l2, err)
	}
	var final bool
	inTx(t, db, func(tx store.Tx) error { final, err = q.Fail(tx, l2, "flaky", time.Second); return err })
	if final {
		t.Error("a first failure under max_attempts 3 was final")
	}
	c.add(2 * time.Second)
	l3, err := claim(queue.ClaimFilter{Routes: []string{"agv_a"}})
	if err != nil || l3.ID != low || l3.Attempts != 2 || l3.Fence <= l2.Fence {
		t.Fatalf("the retry = %+v, %v", l3, err)
	}
	if err := q.Release(ctx, l3, 0); err != nil {
		t.Fatal(err)
	}
	if it, err := q.Get(ctx, s, low); err != nil || it.Status != queue.StatusReady || it.Attempts != 1 {
		t.Errorf("after Release = %+v, %v (a release is not an attempt)", it, err)
	}
	// Lease expiry: the next claim takes the item with a higher fence;
	// the old holder's writes fail.
	l4, _ := claim(queue.ClaimFilter{Routes: []string{"agv_a"}, TTL: time.Second})
	c.add(2 * time.Second)
	l5, err := claim(queue.ClaimFilter{Owner: "node_2", Routes: []string{"agv_a"}})
	if err != nil || l5.ID != l4.ID || l5.Fence != l4.Fence+1 {
		t.Fatalf("reclaim after expiry = %+v, %v", l5, err)
	}
	if err := db.InTx(ctx, func(tx store.Tx) error { return q.Done(tx, l4) }); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("the old holder's Done = %v", err)
	}
	inTx(t, db, func(tx store.Tx) error { return q.Done(tx, l5) })
	// Concurrency keys: one live lease per key.
	k1 := enqueue(queue.Item{Kind: queue.KindRun, Route: "agv_k", ConcurrencyKey: "order-1"})
	enqueue(queue.Item{Kind: queue.KindRun, Route: "agv_k", ConcurrencyKey: "order-1"})
	if lk, err := claim(queue.ClaimFilter{Routes: []string{"agv_k"}}); err != nil || lk.ID != k1 {
		t.Fatalf("key claim = %+v, %v", lk, err)
	}
	if _, err := claim(queue.ClaimFilter{Routes: []string{"agv_k"}}); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("a second item of a held key was claimed: %v", err)
	}
	if n, err := q.Depth(ctx, "agv_k"); err != nil || n != 2 {
		t.Errorf("Depth = %d, %v", n, err)
	}
	// Cancel completes a ready item only.
	var ok bool
	inTx(t, db, func(tx store.Tx) error { ok, err = q.Cancel(tx, s, later, "gone"); return err })
	if !ok {
		t.Error("Cancel of a ready item did nothing")
	}
	inTx(t, db, func(tx store.Tx) error { ok, err = q.Cancel(tx, s, k1, "held"); return err })
	if ok {
		t.Error("Cancel completed a leased item")
	}
}

// testTimers: due order, fire once, cancel.
func testTimers(t *testing.T, db store.Runner) {
	ctx := context.Background()
	c := newClock()
	q := queue.New(db, queue.Clock(c.now))
	s := tenant.Default()
	var ids []string
	for _, d := range []time.Duration{2 * time.Hour, time.Hour, 3 * time.Hour} {
		inTx(t, db, func(tx store.Tx) error {
			id, err := q.At(tx, s, queue.Timer{Kind: "k", Ref: d.String(), DueAt: c.now().Add(d)})
			ids = append(ids, id)
			return err
		})
	}
	if due, _ := q.Due(ctx, 10); len(due) != 0 {
		t.Fatalf("%d timers due early", len(due))
	}
	c.add(150 * time.Minute)
	due, err := q.Due(ctx, 10)
	if err != nil || len(due) != 2 || due[0].Ref != "1h0m0s" || due[1].Ref != "2h0m0s" || due[0].Scope != s {
		t.Fatalf("due = %+v, %v", due, err)
	}
	var first, second bool
	inTx(t, db, func(tx store.Tx) error { first, err = q.Fire(tx, due[0].ID); return err })
	inTx(t, db, func(tx store.Tx) error { second, err = q.Fire(tx, due[0].ID); return err })
	if !first || second {
		t.Errorf("Fire = %v then %v, want true then false", first, second)
	}
	inTx(t, db, func(tx store.Tx) error { return q.CancelTimer(tx, s, ids[2]) })
	c.add(10 * time.Hour)
	due, _ = q.Due(ctx, 10)
	if len(due) != 1 || due[0].ID != ids[0] {
		t.Errorf("after a fire and a cancel, due = %+v", due)
	}
}

// testLedger: admission writes the run, its queue item, its timer and
// run.queued together; the hub-facing transitions; a resume on the pin.
func testLedger(t *testing.T, db store.Runner) {
	ctx := context.Background()
	c := newClock()
	log := events.New(db)
	reg := registry.New(db, log)
	q := queue.New(db, queue.Clock(c.now))
	l := sessions.New(db, log, reg, q, sessions.Clock(c.now), sessions.WithPolicy(sessions.Policy{QueueTTL: time.Hour, MaxPinAge: -1}))
	s := tenant.Default()
	vs, err := reg.RegisterIn(ctx, s, "sha256:aaa", wire.BuildBinary, []registry.AgentSpec{manifest("support", "")})
	if err != nil {
		t.Fatal(err)
	}
	run, err := l.Admit(ctx, s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund 42"}})
	if err != nil || run.AgentVersion != vs[0].ID || run.ResolvedVia != registry.ViaLatest || run.Status != sessions.StatusQueued {
		t.Fatalf("Admit = %+v, %v", run, err)
	}
	lease, err := q.Claim(ctx, queue.ClaimFilter{Owner: "node_1"})
	if err != nil || lease.Route != vs[0].ID {
		t.Fatalf("the run's item = %+v, %v", lease, err)
	}
	var cmd wire.Command
	inTx(t, db, func(tx store.Tx) error {
		var ok bool
		cmd, ok, err = l.Prepare(tx, s, lease.Payload, wire.NewCommandID(), "wk_1")
		if !ok && err == nil {
			t.Error("Prepare sent nothing")
		}
		return err
	})
	if cmd.AgentVersion != vs[0].ID || cmd.BuildDigest != "sha256:aaa" || cmd.Validate() != nil {
		t.Fatalf("command = %+v", cmd)
	}
	inTx(t, db, func(tx store.Tx) error { return l.Started(tx, s, run.ID) })
	inTx(t, db, func(tx store.Tx) error {
		if err := l.Finished(tx, s, run.ID, wire.Ack{Status: wire.StatusParked, Checkpoint: json.RawMessage(`{"messages":[]}`),
			Pending: []wire.PendingCall{{CallID: "c1", Tool: "refund"}}}); err != nil {
			return err
		}
		return q.Done(tx, lease)
	})
	parked, err := l.Get(ctx, s, run.ID)
	if err != nil || parked.Status != sessions.StatusParked || parked.ParkedAt == nil || len(parked.Pending) != 1 {
		t.Fatalf("parked = %+v, %v", parked, err)
	}
	apps, err := l.Approvals(ctx, s, sessions.ApprovalQuery{RunID: run.ID})
	if err != nil || len(apps.Approvals) != 1 {
		t.Fatalf("the parked run's approvals = %+v, %v", apps, err)
	}
	decided, err := l.Decide(ctx, s, events.System, apps.Approvals[0].ID, "approve", "", "", apps.Approvals[0].ArgsSHA256)
	if err != nil || decided.ResumedBy == "" {
		t.Fatalf("Decide = %+v, %v", decided, err)
	}
	res, err := l.Get(ctx, s, decided.ResumedBy)
	if err != nil || res.AgentVersion != vs[0].ID || res.ResolvedVia != registry.ViaPin || res.ResumeOf != run.ID || res.ChainID != run.ID {
		t.Fatalf("the resume = %+v, %v", res, err)
	}
	if _, err := l.Cancel(ctx, s, events.System, run.ID, ""); !errors.Is(err, sessions.ErrAlreadyResumed) {
		t.Errorf("a cancel of the resumed parked run = %v", err)
	}
	// The queue time-to-live fails a run nobody dispatched.
	c.add(2 * time.Hour)
	due, err := q.Due(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range due {
		inTx(t, db, func(tx store.Tx) error {
			if ok, err := q.Fire(tx, tm.ID); err != nil || !ok {
				return err
			}
			return l.OnTimer(tx, tm)
		})
	}
	if got, _ := l.Get(ctx, s, res.ID); got.Status != sessions.StatusFailed || !strings.HasPrefix(got.Error, sessions.ErrWorkerLost) {
		t.Errorf("the undispatched resume after its TTL = %+v", got)
	}
	if got, _ := l.Get(ctx, s, run.ID); got.Status != sessions.StatusParked {
		t.Errorf("the dispatched run's TTL changed it: %+v", got)
	}
	types := map[string]int{}
	evs, _ := log.List(ctx, events.ForScope(s, false), 0, 100)
	for _, e := range evs {
		types[e.Type]++
	}
	if types[events.RunQueued] != 2 || types[events.RunStarted] != 1 || types[events.RunParked] != 1 || types[events.RunFailed] != 1 {
		t.Errorf("outbox = %v", types)
	}
}

// testCrossTenant: what one scope wrote, another scope — another
// tenant, env or project — cannot read, list, claim through, change or
// delete. Every miss is store.ErrNotFound (never a distinct error).
func testCrossTenant(t *testing.T, db store.Runner) {
	ctx := context.Background()
	log := events.New(db)
	reg := registry.New(db, log)
	q := queue.New(db)
	l := sessions.New(db, log, reg, q)
	a := scope(t, tenant.DefaultProject, "test", "ten_a")
	others := []tenant.Scope{
		scope(t, tenant.DefaultProject, "test", "ten_b"),
		scope(t, tenant.DefaultProject, "live", "ten_a"),
		scope(t, "prj_other", "test", "ten_a"),
	}
	// The registry is project-level: register under the project tenant
	// of a's project and env; a's runs resolve it.
	if _, err := reg.RegisterIn(ctx, tenant.Default(), "sha256:aaa", wire.BuildBinary, []registry.AgentSpec{manifest("support", "")}); err != nil {
		t.Fatal(err)
	}
	// A tenant-scoped ledger row needs its scope's registry; the
	// tenant's own rows carry ten_a.
	run, err := l.Admit(ctx, a, events.Actor{Kind: "key", ID: "k_a"}, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "x"}})
	if err != nil {
		t.Fatalf("Admit under ten_a = %v", err)
	}
	if run.TenantID != "ten_a" {
		t.Errorf("tenant_id = %s", run.TenantID)
	}
	var item string
	inTx(t, db, func(tx store.Tx) error {
		var err error
		item, err = q.Enqueue(tx, a, queue.Item{Kind: queue.KindRun, Route: "agv_x"})
		return err
	})
	var timer string
	inTx(t, db, func(tx store.Tx) error {
		var err error
		timer, err = q.At(tx, a, queue.Timer{Kind: "k", Ref: "r", DueAt: time.Now().Add(time.Hour)})
		return err
	})
	for _, o := range others {
		if _, err := l.Get(ctx, o, run.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s reads %s's run: %v", o, a, err)
		}
		if runs, err := l.List(ctx, o, 100); err != nil || len(runs) != 0 {
			t.Errorf("%s lists %d of %s's runs (%v)", o, len(runs), a, err)
		}
		if _, err := l.Cancel(ctx, o, events.System, run.ID, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s cancels %s's run: %v", o, a, err)
		}
		if _, err := q.Get(ctx, o, item); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s reads %s's queue item: %v", o, a, err)
		}
		var ok bool
		inTx(t, db, func(tx store.Tx) error { ok, err = q.Cancel(tx, o, item, "x"); return err })
		if ok {
			t.Errorf("%s canceled %s's queue item", o, a)
		}
		inTx(t, db, func(tx store.Tx) error { return q.CancelTimer(tx, o, timer) })
		if evs, err := log.List(ctx, events.ForScope(o, false), 0, 100); err != nil || len(evs) != 0 {
			t.Errorf("%s sees %d of %s's events (%v)", o, len(evs), a, err)
		}
		// A delete names nothing of a's (its audit event is o's own).
		if n, _, err := l.Delete(ctx, o, events.System, []string{run.ID}); err != nil || n != 0 {
			t.Errorf("%s deletes %d of %s's runs: %v", o, n, a, err)
		}
		if _, err := l.Get(ctx, a, run.ID); err != nil {
			t.Errorf("%s's delete removed %s's run: %v", o, a, err)
		}
	}
	if got, err := l.Get(ctx, a, run.ID); err != nil || got.Status != sessions.StatusQueued {
		t.Errorf("the owner's run after the others tried = %+v, %v", got, err)
	}
	if it, err := q.Get(ctx, a, item); err != nil || it.Status != queue.StatusReady {
		t.Errorf("the owner's item = %+v, %v", it, err)
	}
	var pending int
	_ = db.Read(ctx, func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM timers WHERE id = ? AND fired_at IS NULL`, timer).Scan(&pending)
	})
	if pending != 1 {
		t.Error("another scope deleted the owner's timer")
	}
	// The registry is per project and env: another project sees no agent.
	if agents, err := reg.Agents(ctx, others[2]); err != nil || len(agents) != 0 {
		t.Errorf("another project lists %d agents (%v)", len(agents), err)
	}
}
