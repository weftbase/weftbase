// Package queue is the control store's durable work queue: items
// (a run to start, a run to resume) with priorities, a run_at, a
// concurrency key and a route, claimed under a lease with a fencing
// token; and the timer table.
//
// The store is authoritative: a claim, an extension, a completion and
// a retry are each one write transaction, and the queue keeps no state
// in memory that a restart or another node could not rebuild. A lease
// that is not extended expires; the next claim takes the item again
// with a higher fence, and every write of the old holder then fails
// with ErrLeaseLost. On SQLite (one writer) the claim's read and update
// share one BEGIN IMMEDIATE transaction; Postgres will add SKIP LOCKED.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Item kinds.
const (
	KindRun    = "run"
	KindResume = "resume"
)

// Item statuses.
const (
	StatusReady  = "ready"
	StatusLeased = "leased"
	StatusDone   = "done"
	StatusFailed = "failed"
)

// Errors.
var (
	// ErrEmpty: nothing to claim now.
	ErrEmpty = errors.New("queue: nothing to claim")
	// ErrLeaseLost: the lease expired and was claimed again (its fence
	// moved on), or the item is no longer leased.
	ErrLeaseLost = errors.New("queue: lease lost")
)

// Item is one unit of work.
type Item struct {
	ID    string       `json:"id"`
	Scope tenant.Scope `json:"-"`
	Kind  string       `json:"kind"`
	// Route is what a claimer must serve to take the item: the
	// AgentVersion of a run.
	Route    string `json:"route"`
	Priority int    `json:"priority"`
	// RunAt is the earliest claim time (zero: now).
	RunAt time.Time `json:"run_at"`
	// ConcurrencyKey: at most one item of a key is leased at a time.
	ConcurrencyKey string          `json:"concurrency_key,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	// MaxAttempts bounds Fail's retries (0: 3).
	MaxAttempts int    `json:"max_attempts"`
	Attempts    int    `json:"attempts"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
}

// Lease is a claimed item.
type Lease struct {
	Item
	Owner string
	Fence int64
	Until time.Time
}

// ClaimFilter says what a claimer takes.
type ClaimFilter struct {
	// Owner names the claimer (a node and loop).
	Owner string
	// Kinds limits the item kinds (nil: any).
	Kinds []string
	// Routes limits the routes (nil: any; empty: none).
	Routes []string
	// TTL is the lease's length (0: 30 s).
	TTL time.Duration
}

// DefaultTTL is a lease's length when the filter names none.
const DefaultTTL = 30 * time.Second

// tsFormat is fixed-width, so stored times compare as strings.
const tsFormat = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsFormat) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsFormat, s)
	return t
}

// Queue is the queue service.
type Queue struct {
	db        store.Runner
	now       func() time.Time
	wake      chan struct{}
	timerWake chan struct{}
}

// Option configures a Queue.
type Option func(*Queue)

// Clock sets the time source (tests).
func Clock(now func() time.Time) Option { return func(q *Queue) { q.now = now } }

// New returns the queue over db.
func New(db store.Runner, opts ...Option) *Queue {
	q := &Queue{db: db, now: time.Now, wake: make(chan struct{}, 1), timerWake: make(chan struct{}, 1)}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Now is the queue's clock.
func (q *Queue) Now() time.Time { return q.now() }

// Wake is signalled after a commit that enqueued an item: the claim
// loop's cue (it also polls, for another node's commits).
func (q *Queue) Wake() <-chan struct{} { return q.wake }

// Poke signals Wake.
func (q *Queue) Poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Enqueue adds it in tx under s and returns its id.
func (q *Queue) Enqueue(tx store.Tx, s tenant.Scope, it Item) (string, error) {
	if s.IsZero() {
		return "", tenant.ErrZeroScope
	}
	if it.Kind == "" {
		return "", errors.New("queue: an item needs a kind")
	}
	if it.MaxAttempts <= 0 {
		it.MaxAttempts = 3
	}
	if len(it.Payload) == 0 {
		it.Payload = json.RawMessage(`{}`)
	}
	now := q.now()
	if it.RunAt.IsZero() {
		it.RunAt = now
	}
	it.ID = "qi_" + ulid.New()
	_, err := store.Exec(tx, `INSERT INTO queue_items (id, project_id, env, tenant_id, kind, route, priority, run_at, concurrency_key,
		payload, status, attempts, max_attempts, fence, lease_owner, lease_until, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 0, '', '', '', ?, ?)`,
		it.ID, s.Project(), s.Env(), s.Tenant(), it.Kind, it.Route, it.Priority, ts(it.RunAt), it.ConcurrencyKey,
		string(it.Payload), StatusReady, it.MaxAttempts, ts(now), ts(now))
	if err != nil {
		return "", fmt.Errorf("queue: enqueue: %w", err)
	}
	tx.AfterCommit(q.Poke)
	return it.ID, nil
}

const selectItem = `SELECT id, project_id, env, tenant_id, kind, route, priority, run_at, concurrency_key, payload,
	status, attempts, max_attempts, fence, lease_owner, lease_until, last_error FROM queue_items`

type row struct {
	Item
	project, env, tenant string
	fence                int64
	owner, until         string
}

func scanRow(r interface{ Scan(...any) error }) (row, error) {
	var x row
	var runAt, payload string
	err := r.Scan(&x.ID, &x.project, &x.env, &x.tenant, &x.Kind, &x.Route, &x.Priority, &runAt, &x.ConcurrencyKey, &payload,
		&x.Status, &x.Attempts, &x.MaxAttempts, &x.fence, &x.owner, &x.until, &x.LastError)
	if err != nil {
		return row{}, store.NotFound(err)
	}
	x.RunAt, x.Payload = parseTS(runAt), json.RawMessage(payload)
	// The row was written under this scope (Enqueue, At): rebuilding it
	// here is the queue's one NewScope (scripts/layering allows it).
	x.Scope, err = tenant.NewScope(x.project, x.env, x.tenant)
	return x, err
}

func in(col string, vals []string, args *[]any) string {
	ph := make([]string, len(vals))
	for i, v := range vals {
		ph[i] = "?"
		*args = append(*args, v)
	}
	return " AND " + col + " IN (" + strings.Join(ph, ", ") + ")"
}

// Claim leases the most urgent claimable item f admits: a ready item
// due now, or a leased one whose lease expired, whose concurrency key
// no live lease holds. Highest priority first, then oldest run_at. It
// returns ErrEmpty when there is none.
func (q *Queue) Claim(ctx context.Context, f ClaimFilter) (Lease, error) {
	if f.Routes != nil && len(f.Routes) == 0 {
		return Lease{}, ErrEmpty
	}
	if f.Owner == "" {
		return Lease{}, errors.New("queue: a claim needs an owner")
	}
	ttl := f.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	var out Lease
	err := q.db.InTx(ctx, func(tx store.Tx) error {
		now := ts(q.now())
		args := []any{StatusReady, now, StatusLeased, now}
		where := ` WHERE ((status = ? AND run_at <= ?) OR (status = ? AND lease_until <= ?))`
		if f.Kinds != nil {
			where += in("kind", f.Kinds, &args)
		}
		if f.Routes != nil {
			where += in("route", f.Routes, &args)
		}
		rows, err := store.Query(tx, selectItem+where+` ORDER BY priority DESC, run_at, seq LIMIT 32`, args...)
		if err != nil {
			return err
		}
		var cands []row
		for rows.Next() {
			x, err := scanRow(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			cands = append(cands, x)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range cands {
			if x.ConcurrencyKey != "" {
				var n int
				if err := store.QueryRow(tx, `SELECT COUNT(*) FROM queue_items WHERE concurrency_key = ? AND status = ? AND lease_until > ? AND id <> ?`,
					x.ConcurrencyKey, StatusLeased, now, x.ID).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					continue
				}
			}
			until := q.now().Add(ttl)
			res, err := store.Exec(tx, `UPDATE queue_items SET status = ?, attempts = attempts + 1, fence = fence + 1,
				lease_owner = ?, lease_until = ?, updated_at = ? WHERE id = ? AND fence = ?`,
				StatusLeased, f.Owner, ts(until), now, x.ID, x.fence)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				continue
			}
			x.Status = StatusLeased
			x.Attempts++
			out = Lease{Item: x.Item, Owner: f.Owner, Fence: x.fence + 1, Until: until}
			return nil
		}
		return ErrEmpty
	})
	return out, err
}

// fenced runs a write that holds only while l is the item's live lease.
func (q *Queue) fenced(tx store.Tx, l Lease, set string, args ...any) error {
	args = append(args, ts(q.now()), l.ID, l.Fence, l.Owner, StatusLeased)
	res, err := store.Exec(tx, `UPDATE queue_items SET `+set+`, updated_at = ? WHERE id = ? AND fence = ? AND lease_owner = ? AND status = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Extend moves l's expiry to now + ttl. It fails with ErrLeaseLost when
// the item was claimed again.
func (q *Queue) Extend(ctx context.Context, l *Lease, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	until := q.now().Add(ttl)
	err := q.db.InTx(ctx, func(tx store.Tx) error { return q.fenced(tx, *l, `lease_until = ?`, ts(until)) })
	if err == nil {
		l.Until = until
	}
	return err
}

// Done completes l's item in tx: in the same transaction as the state
// change the work made.
func (q *Queue) Done(tx store.Tx, l Lease) error {
	return q.fenced(tx, l, `status = ?, lease_until = ''`, StatusDone)
}

// Fail records cause on l's item in tx. Under its attempt bound the
// item is ready again after retryIn; at the bound it is failed for
// good (final is true).
func (q *Queue) Fail(tx store.Tx, l Lease, cause string, retryIn time.Duration) (final bool, err error) {
	if l.Attempts >= l.MaxAttempts {
		return true, q.fenced(tx, l, `status = ?, last_error = ?, lease_until = ''`, StatusFailed, cause)
	}
	return false, q.fenced(tx, l, `status = ?, last_error = ?, run_at = ?, lease_until = ''`,
		StatusReady, cause, ts(q.now().Add(retryIn)))
}

// Release hands l's item back unworked after delay, without counting
// the attempt (a busy worker, no eligible worker).
func (q *Queue) Release(ctx context.Context, l Lease, delay time.Duration) error {
	return q.db.InTx(ctx, func(tx store.Tx) error {
		return q.fenced(tx, l, `status = ?, attempts = attempts - 1, run_at = ?, lease_until = ''`,
			StatusReady, ts(q.now().Add(delay)))
	})
}

// Return hands l's item back unworked in tx, keeping its place: run_at
// is left as it was and the attempt is not counted (a worker that
// answered busy). The caller keeps it from being claimed again at once
// (the hub pauses the route on that worker).
func (q *Queue) Return(tx store.Tx, l Lease) error {
	return q.fenced(tx, l, `status = ?, attempts = attempts - 1, lease_until = ''`, StatusReady)
}

// Cancel completes a ready item in tx without running it (the work it
// stood for ended another way: a canceled run, a run whose queue time
// ran out). A leased item is left to its holder; an item already done
// is left alone. It reports whether it completed the item.
func (q *Queue) Cancel(tx store.Tx, s tenant.Scope, id, why string) (bool, error) {
	res, err := store.Exec(tx, `UPDATE queue_items SET status = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND env = ? AND tenant_id = ? AND status = ?`,
		StatusDone, why, ts(q.now()), id, s.Project(), s.Env(), s.Tenant(), StatusReady)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Get reads one item in s.
func (q *Queue) Get(ctx context.Context, s tenant.Scope, id string) (Item, error) {
	var it Item
	err := q.db.Read(ctx, func(tx store.Tx) error {
		x, err := scanRow(store.QueryRow(tx, selectItem+` WHERE id = ? AND project_id = ? AND env = ? AND tenant_id = ?`,
			id, s.Project(), s.Env(), s.Tenant()))
		it = x.Item
		return err
	})
	return it, err
}

// Depth counts the ready and leased items of a route (P§6.3: queue
// depth per version).
func (q *Queue) Depth(ctx context.Context, route string) (int, error) {
	var n int
	err := q.db.Read(ctx, func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM queue_items WHERE route = ? AND status IN (?, ?)`,
			route, StatusReady, StatusLeased).Scan(&n)
	})
	return n, err
}

// Timer is one row of the timer table: an expiry, a sleep, a pin age.
type Timer struct {
	ID      string          `json:"id"`
	Scope   tenant.Scope    `json:"-"`
	Kind    string          `json:"kind"`
	Ref     string          `json:"ref"`
	DueAt   time.Time       `json:"due_at"`
	Payload json.RawMessage `json:"payload"`
}

// At schedules t in tx under s and returns its id.
func (q *Queue) At(tx store.Tx, s tenant.Scope, t Timer) (string, error) {
	if s.IsZero() {
		return "", tenant.ErrZeroScope
	}
	if t.Kind == "" || t.DueAt.IsZero() {
		return "", errors.New("queue: a timer needs a kind and a due time")
	}
	if len(t.Payload) == 0 {
		t.Payload = json.RawMessage(`{}`)
	}
	t.ID = "tmr_" + ulid.New()
	tx.AfterCommit(q.pokeTimers)
	_, err := store.Exec(tx, `INSERT INTO timers (id, project_id, env, tenant_id, kind, ref, due_at, payload, fired_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		t.ID, s.Project(), s.Env(), s.Tenant(), t.Kind, t.Ref, ts(t.DueAt), string(t.Payload), ts(q.now()))
	if err != nil {
		return "", fmt.Errorf("queue: timer: %w", err)
	}
	return t.ID, nil
}

func (q *Queue) pokeTimers() {
	select {
	case q.timerWake <- struct{}{}:
	default:
	}
}

// FireFunc fires one due timer, in the transaction that marks it fired.
type FireFunc func(tx store.Tx, t Timer) error

// RunTimers fires due timers until ctx ends: overdue ones at once (boot
// recovery), the rest as they fall due, looking every poll and after
// every commit that scheduled one. Each timer fires in one transaction
// with its fired mark, so it fires once (another node, a replay: Fire
// reports false and nothing runs). A fire that fails is logged and the
// timer is marked fired anyway: a timer that cannot fire must not
// spin.
func (q *Queue) RunTimers(ctx context.Context, poll time.Duration, fire FireFunc, log *slog.Logger) {
	if poll <= 0 {
		poll = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		for {
			due, err := q.Due(ctx, 64)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("queue: reading due timers failed", "err", err)
				}
				break
			}
			for _, tm := range due {
				err := q.db.InTx(ctx, func(tx store.Tx) error {
					ok, err := q.Fire(tx, tm.ID)
					if err != nil || !ok {
						return err
					}
					return fire(tx, tm)
				})
				if err != nil && ctx.Err() == nil {
					log.Warn("queue: a timer failed to fire", "timer", tm.ID, "kind", tm.Kind, "ref", tm.Ref, "err", err)
					_ = q.db.InTx(ctx, func(tx store.Tx) error { _, err := q.Fire(tx, tm.ID); return err })
				}
			}
			if len(due) < 64 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-q.timerWake:
		}
	}
}

// Due reads up to limit unfired timers due by now, earliest first.
func (q *Queue) Due(ctx context.Context, limit int) ([]Timer, error) {
	var out []Timer
	err := q.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, `SELECT id, project_id, env, tenant_id, kind, ref, due_at, payload FROM timers
			WHERE fired_at IS NULL AND due_at <= ? ORDER BY due_at LIMIT ?`, ts(q.now()), limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t Timer
			var p, e, tn, due, payload string
			if err := rows.Scan(&t.ID, &p, &e, &tn, &t.Kind, &t.Ref, &due, &payload); err != nil {
				return err
			}
			if t.Scope, err = tenant.NewScope(p, e, tn); err != nil {
				return err
			}
			t.DueAt, t.Payload = parseTS(due), json.RawMessage(payload)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// Fire marks a timer fired in tx. It reports false when it had already
// fired (another node, a replay): the caller then does nothing.
func (q *Queue) Fire(tx store.Tx, id string) (bool, error) {
	res, err := store.Exec(tx, `UPDATE timers SET fired_at = ? WHERE id = ? AND fired_at IS NULL`, ts(q.now()), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelTimer deletes an unfired timer in s.
func (q *Queue) CancelTimer(tx store.Tx, s tenant.Scope, id string) error {
	_, err := store.Exec(tx, `DELETE FROM timers WHERE id = ? AND project_id = ? AND env = ? AND tenant_id = ? AND fired_at IS NULL`,
		id, s.Project(), s.Env(), s.Tenant())
	return err
}
