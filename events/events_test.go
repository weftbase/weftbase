package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

func newLog(t *testing.T) (*Log, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), db
}

func emit(t *testing.T, l *Log, db *sqlite.DB, s tenant.Scope, typ, id string) Event {
	t.Helper()
	var e Event
	if err := db.InTx(context.Background(), func(tx store.Tx) error {
		var err error
		e, err = l.Emit(tx, s, Event{Type: typ, ResourceKind: "run", ResourceID: id}, map[string]string{"id": id})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

func recv(t *testing.T, s *Sub) Event {
	t.Helper()
	select {
	case e, ok := <-s.C:
		if !ok {
			t.Fatalf("subscription ended: %v", s.Err())
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}
	return Event{}
}

func TestEmitRollsBackWithTheChange(t *testing.T) {
	l, db := newLog(t)
	boom := errors.New("boom")
	err := db.InTx(context.Background(), func(tx store.Tx) error {
		if _, err := l.Emit(tx, tenant.Default(), Event{Type: RunStarted, ResourceKind: "run", ResourceID: "r1"}, nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if head, _ := l.Head(context.Background()); head != 0 {
		t.Errorf("head after a rollback = %d, want 0", head)
	}
	if err := db.InTx(context.Background(), func(tx store.Tx) error {
		_, err := l.Emit(tx, tenant.Scope{}, Event{Type: RunStarted, ResourceKind: "run", ResourceID: "r1"}, nil)
		return err
	}); !errors.Is(err, tenant.ErrZeroScope) {
		t.Errorf("zero scope = %v", err)
	}
}

// TestHubReplayThenLive: a subscription replays the stored events after
// its cursor, then delivers new ones once each, in seq order, matching
// its scope only.
func TestHubReplayThenLive(t *testing.T) {
	l, db := newLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Hub().Run(ctx) }()
	def := tenant.Default()
	other, _ := tenant.NewScope("prj_other", "test", tenant.ProjectTenant)
	emit(t, l, db, def, RunStarted, "r1")
	emit(t, l, db, other, RunStarted, "x1")
	emit(t, l, db, def, RunFinished, "r1")

	sub, err := l.Hub().Subscribe(ctx, ForScope(def, true), 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if e := recv(t, sub); e.Seq != 1 || e.ResourceID != "r1" {
		t.Fatalf("first = %+v", e)
	}
	if e := recv(t, sub); e.Seq != 3 {
		t.Fatalf("second = %+v, want seq 3 (seq 2 is another project's)", e)
	}
	emit(t, l, db, other, RunStarted, "x2")
	emit(t, l, db, def, RunStarted, "r2")
	if e := recv(t, sub); e.Seq != 5 || e.ResourceID != "r2" {
		t.Fatalf("live = %+v, want seq 5", e)
	}

	// A resume after seq 3 sees only 5.
	sub2, err := l.Hub().Subscribe(ctx, ForScope(def, true), 3, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Close()
	if e := recv(t, sub2); e.Seq != 5 {
		t.Fatalf("resume = %+v", e)
	}
}

func TestHubOverflow(t *testing.T) {
	l, db := newLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 5; i++ {
		emit(t, l, db, tenant.Default(), RunStarted, "r")
	}
	sub, err := l.Hub().Subscribe(ctx, ForScope(tenant.Default(), true), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range sub.C {
		n++
	}
	if n != 2 || !errors.Is(sub.Err(), ErrOverflow) {
		t.Errorf("read %d then %v, want 2 then ErrOverflow", n, sub.Err())
	}
}

// TestRelayAdvancesWithItsWork: the relay hands batches to its
// consumer and commits the cursor in the same transaction; a failing
// batch is retried and its cursor not moved.
func TestRelayAdvancesWithItsWork(t *testing.T) {
	l, db := newLog(t)
	PollInterval = 10 * time.Millisecond
	t.Cleanup(func() { PollInterval = 250 * time.Millisecond })
	emit(t, l, db, tenant.Default(), RunStarted, "r1")
	emit(t, l, db, tenant.Default(), RunFinished, "r1")
	ctx, cancel := context.WithCancel(context.Background())
	var seen, fails atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Relay(ctx, "test", 1, func(tx store.Tx, evs []Event) error {
			if fails.Add(1) == 1 {
				return errors.New("first try fails")
			}
			seen.Add(int64(len(evs)))
			return nil
		}, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for seen.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if seen.Load() != 2 {
		t.Fatalf("relayed %d events, want 2", seen.Load())
	}
	if cur, _ := l.Cursor(context.Background(), "test"); cur != 2 {
		t.Errorf("cursor = %d, want 2", cur)
	}
}
