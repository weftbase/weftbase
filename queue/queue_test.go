package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)} }

func openAt(t *testing.T, path string, c *clock) (*queue.Queue, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return queue.New(db, queue.Clock(c.now)), db
}

func enqueue(t *testing.T, db *sqlite.DB, q *queue.Queue, s tenant.Scope, it queue.Item) string {
	t.Helper()
	var id string
	if err := db.InTx(context.Background(), func(tx store.Tx) error {
		var err error
		id, err = q.Enqueue(tx, s, it)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClaimOrderRoutesAndKeys(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	q, db := openAt(t, ":memory:", c)
	defer func() { _ = db.Close() }()
	s := tenant.Default()

	low := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "agv_a", Payload: json.RawMessage(`{"n":1}`)})
	high := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "agv_a", Priority: 5})
	later := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "agv_a", Priority: 9, RunAt: c.now().Add(time.Minute)})
	other := enqueue(t, db, q, s, queue.Item{Kind: queue.KindResume, Route: "agv_b"})
	select {
	case <-q.Wake():
	default:
		t.Error("Enqueue did not wake the claimer")
	}

	f := queue.ClaimFilter{Owner: "n1", Routes: []string{"agv_a"}, TTL: 10 * time.Second}
	l1, err := q.Claim(ctx, f)
	if err != nil || l1.ID != high || l1.Fence != 1 || l1.Attempts != 1 || l1.Scope != s {
		t.Fatalf("first claim = %+v, %v (want the high-priority item)", l1, err)
	}
	l2, err := q.Claim(ctx, f)
	if err != nil || l2.ID != low || string(l2.Payload) != `{"n":1}` {
		t.Fatalf("second claim = %+v, %v", l2, err)
	}
	if _, err := q.Claim(ctx, f); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("third claim = %v, want ErrEmpty (the later item is not due, agv_b is not served)", err)
	}
	if _, err := q.Claim(ctx, queue.ClaimFilter{Owner: "n1", Routes: []string{}}); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("no routes = %v", err)
	}
	c.add(time.Minute)
	if l, err := q.Claim(ctx, f); err != nil || l.ID != later {
		t.Errorf("due item = %+v, %v", l, err)
	}
	if l, err := q.Claim(ctx, queue.ClaimFilter{Owner: "n1", Kinds: []string{queue.KindResume}}); err != nil || l.ID != other {
		t.Errorf("by kind = %+v, %v", l, err)
	}

	// Concurrency keys: one live lease per key.
	k1 := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "agv_k", ConcurrencyKey: "session:1"})
	k2 := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "agv_k", ConcurrencyKey: "session:1"})
	fk := queue.ClaimFilter{Owner: "n1", Routes: []string{"agv_k"}}
	lk, err := q.Claim(ctx, fk)
	if err != nil || lk.ID != k1 {
		t.Fatalf("key claim = %+v, %v", lk, err)
	}
	if _, err := q.Claim(ctx, fk); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("second of a key while the first is leased = %v", err)
	}
	if err := db.InTx(ctx, func(tx store.Tx) error { return q.Done(tx, lk) }); err != nil {
		t.Fatal(err)
	}
	if l, err := q.Claim(ctx, fk); err != nil || l.ID != k2 {
		t.Errorf("key after done = %+v, %v", l, err)
	}
	if it, err := q.Get(ctx, s, k1); err != nil || it.Status != queue.StatusDone {
		t.Errorf("done item = %+v, %v", it, err)
	}
	other2, _ := tenant.NewScope("prj_x", "test", tenant.ProjectTenant)
	if _, err := q.Get(ctx, other2, k1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another scope's item = %v", err)
	}
}

func TestLeaseExpiryFencesTheOldHolder(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	q, db := openAt(t, ":memory:", c)
	defer func() { _ = db.Close() }()
	s := tenant.Default()
	id := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "r"})

	f := queue.ClaimFilter{Owner: "n1", TTL: 10 * time.Second}
	l1, err := q.Claim(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	c.add(5 * time.Second)
	if err := q.Extend(ctx, &l1, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	c.add(9 * time.Second) // 14 s: alive only thanks to the extension
	if _, err := q.Claim(ctx, queue.ClaimFilter{Owner: "n2"}); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("claim of a live lease = %v", err)
	}
	c.add(2 * time.Second) // past the extension
	l2, err := q.Claim(ctx, queue.ClaimFilter{Owner: "n2"})
	if err != nil || l2.ID != id || l2.Fence != 2 || l2.Attempts != 2 {
		t.Fatalf("reclaim = %+v, %v", l2, err)
	}
	if err := q.Extend(ctx, &l1, time.Second); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("old holder's Extend = %v", err)
	}
	if err := db.InTx(ctx, func(tx store.Tx) error { return q.Done(tx, l1) }); !errors.Is(err, queue.ErrLeaseLost) {
		t.Errorf("old holder's Done = %v", err)
	}
	// Fail retries under the bound, fails for good at it.
	var final bool
	if err := db.InTx(ctx, func(tx store.Tx) error {
		var err error
		final, err = q.Fail(tx, l2, "boom", time.Second)
		return err
	}); err != nil || final {
		t.Fatalf("Fail = %v, final %v", err, final)
	}
	if _, err := q.Claim(ctx, f); !errors.Is(err, queue.ErrEmpty) {
		t.Error("a retried item was claimable before its delay")
	}
	c.add(time.Second)
	l3, err := q.Claim(ctx, f)
	if err != nil || l3.Attempts != 3 || l3.LastError != "boom" {
		t.Fatalf("retry claim = %+v, %v", l3, err)
	}
	// Release does not count the attempt.
	if err := q.Release(ctx, l3, 0); err != nil {
		t.Fatal(err)
	}
	l4, err := q.Claim(ctx, f)
	if err != nil || l4.Attempts != 3 {
		t.Fatalf("after release = %+v, %v", l4, err)
	}
	if err := db.InTx(ctx, func(tx store.Tx) error {
		var err error
		final, err = q.Fail(tx, l4, "boom again", time.Second)
		return err
	}); err != nil || !final {
		t.Fatalf("Fail at the bound = %v, final %v", err, final)
	}
	if it, _ := q.Get(ctx, s, id); it.Status != queue.StatusFailed {
		t.Errorf("status = %s, want failed", it.Status)
	}
}

// TestRestart: the store is authoritative. A queued item and a leased
// one survive closing the database; after reopening, the queued item is
// claimed at once and the leased one only once its lease expired.
func TestRestart(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	path := filepath.Join(t.TempDir(), "control.db")
	q, db := openAt(t, path, c)
	s := tenant.Default()
	leased := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "r", Priority: 1})
	queued := enqueue(t, db, q, s, queue.Item{Kind: queue.KindRun, Route: "r"})
	tid := ""
	if err := db.InTx(ctx, func(tx store.Tx) error {
		var err error
		tid, err = q.At(tx, s, queue.Timer{Kind: "expiry", Ref: "run_1", DueAt: c.now().Add(time.Minute)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if l, err := q.Claim(ctx, queue.ClaimFilter{Owner: "before", TTL: 30 * time.Second}); err != nil || l.ID != leased {
		t.Fatalf("claim = %+v, %v", l, err)
	}
	_ = db.Close()

	q, db = openAt(t, path, c)
	defer func() { _ = db.Close() }()
	f := queue.ClaimFilter{Owner: "after", TTL: 30 * time.Second}
	if l, err := q.Claim(ctx, f); err != nil || l.ID != queued {
		t.Fatalf("after restart = %+v, %v (want the queued item)", l, err)
	}
	if _, err := q.Claim(ctx, f); !errors.Is(err, queue.ErrEmpty) {
		t.Fatal("the leased item was claimable before its lease expired")
	}
	c.add(31 * time.Second)
	if l, err := q.Claim(ctx, f); err != nil || l.ID != leased || l.Fence != 2 {
		t.Fatalf("expired lease = %+v, %v", l, err)
	}
	if due, err := q.Due(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("Due before the time = %+v, %v", due, err)
	}
	c.add(time.Minute)
	due, err := q.Due(ctx, 10)
	if err != nil || len(due) != 1 || due[0].ID != tid || due[0].Scope != s || due[0].Ref != "run_1" {
		t.Fatalf("Due = %+v, %v", due, err)
	}
	for i, want := range []bool{true, false} {
		var fired bool
		if err := db.InTx(ctx, func(tx store.Tx) error {
			var err error
			fired, err = q.Fire(tx, tid)
			return err
		}); err != nil || fired != want {
			t.Errorf("Fire #%d = %v, %v", i, fired, err)
		}
	}
	if due, _ := q.Due(ctx, 10); len(due) != 0 {
		t.Error("a fired timer is still due")
	}
}
