// Package events is the control store's outbox: every state change
// anyone may react to writes one events row in the same transaction as
// the change (Log.Emit). Consumers read the table, never in-memory
// callbacks: the Hub fans new rows out to SSE subscribers, and Relay
// drives at-least-once consumers (webhooks, notifications) with a
// cursor committed in the same transaction as their work.
//
// Token deltas and weft run events are not here: their volume belongs
// to the trace store (obsdb) and its live hub.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// The v1 event types (plan §6.5). Audited marks the ones chained into
// the audit log.
const (
	RunQueued          = "run.queued"
	RunStarted         = "run.started"
	RunParked          = "run.parked"
	RunFinished        = "run.finished"
	RunFailed          = "run.failed"
	RunCanceled        = "run.canceled"
	RunSteered         = "run.steered"
	RunMigrated        = "run.migrated"
	SessionCreated     = "session.created"
	SessionDeleted     = "session.deleted"
	ApprovalRequested  = "approval.requested"
	ApprovalDecided    = "approval.decided"
	ApprovalDenied     = "approval.denied_attempt"
	ApprovalExpired    = "approval.expired"
	ApprovalApplied    = "approval.applied"
	LabelMoved         = "label.moved"
	BuildPushed        = "build.pushed"
	VersionDrained     = "version.drained"
	KeyMinted          = "key.minted"
	KeyRevoked         = "key.revoked"
	TenantCreated      = "tenant.created"
	TenantUpdated      = "tenant.updated"
	TenantDeleted      = "tenant.deleted"
	BudgetWarning      = "budget.warning"
	BudgetExceeded     = "budget.exceeded"
	WorkerRegistered   = "worker.registered"
	WorkerDisconnected = "worker.disconnected"
	WebhookDisabled    = "webhook.disabled"
)

// Actor is who caused an event.
type Actor struct {
	Kind       string `json:"kind"` // key | operator | worker | system
	ID         string `json:"id,omitempty"`
	Credential string `json:"credential,omitempty"` // the key id or session id it acted with
	Assurance  string `json:"assurance,omitempty"`
}

// System is the actor of the server's own work (timers, sweeps).
var System = Actor{Kind: "system"}

// Event is one outbox row.
type Event struct {
	Seq          int64           `json:"seq"`
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Env          string          `json:"env"`
	TenantID     string          `json:"tenant_id"`
	Type         string          `json:"type"`
	ResourceKind string          `json:"resource_kind"`
	ResourceID   string          `json:"resource_id"`
	Actor        Actor           `json:"actor"`
	Data         json.RawMessage `json:"data"`
	Audited      bool            `json:"audited"`
	CreatedAt    time.Time       `json:"created_at"`
}

// Log writes and reads the outbox.
type Log struct {
	db  store.Runner
	hub *Hub
	now func() time.Time
}

// Option configures a Log.
type Option func(*Log)

// Clock sets the time source (tests).
func Clock(now func() time.Time) Option { return func(l *Log) { l.now = now } }

// New returns a Log over db with its Hub.
func New(db store.Runner, opts ...Option) *Log {
	l := &Log{db: db, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	l.hub = newHub(l)
	return l
}

// Hub is the log's SSE fan-out.
func (l *Log) Hub() *Hub { return l.hub }

// Emit writes e in tx under scope s. ID, scope, Seq and CreatedAt are
// filled here; data is marshalled as JSON and must never carry tool
// arguments or secrets. The hub wakes after the commit.
func (l *Log) Emit(tx store.Tx, s tenant.Scope, e Event, data any) (Event, error) {
	if s.IsZero() {
		return Event{}, tenant.ErrZeroScope
	}
	if e.Type == "" || e.ResourceKind == "" || e.ResourceID == "" {
		return Event{}, errors.New("events: an event needs a type and a resource")
	}
	if e.Actor.Kind == "" {
		e.Actor = System
	}
	e.ID = "evt_" + ulid.New()
	e.ProjectID, e.Env, e.TenantID = s.Project(), s.Env(), s.Tenant()
	e.CreatedAt = l.now().UTC()
	if data == nil {
		data = struct{}{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal data: %w", err)
	}
	e.Data = b
	actor, _ := json.Marshal(e.Actor)
	err = store.QueryRow(tx, `INSERT INTO events
		(id, project_id, env, tenant_id, type, resource_kind, resource_id, actor, data, audited, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
		e.ID, e.ProjectID, e.Env, e.TenantID, e.Type, e.ResourceKind, e.ResourceID,
		string(actor), string(e.Data), e.Audited, e.CreatedAt.Format(time.RFC3339Nano)).Scan(&e.Seq)
	if err != nil {
		return Event{}, fmt.Errorf("events: insert: %w", err)
	}
	tx.AfterCommit(l.hub.wake)
	return e, nil
}

const selectEvents = `SELECT seq, id, project_id, env, tenant_id, type, resource_kind, resource_id,
	actor, data, audited, created_at FROM events`

func scanEvents(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]Event, error) {
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var actor, data, created string
		if err := rows.Scan(&e.Seq, &e.ID, &e.ProjectID, &e.Env, &e.TenantID, &e.Type,
			&e.ResourceKind, &e.ResourceID, &actor, &data, &e.Audited, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(actor), &e.Actor)
		e.Data = json.RawMessage(data)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// After reads up to limit events with seq > after, every scope, in
// seq order: the relay's and the hub's read.
func (l *Log) After(ctx context.Context, after int64, limit int) ([]Event, error) {
	var out []Event
	err := l.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, selectEvents+` WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
		if err != nil {
			return err
		}
		out, err = scanEvents(rows)
		return err
	})
	return out, err
}

// List reads up to limit events sel matches with seq > after, in seq
// order (a subscriber's replay, GET /v1/events).
func (l *Log) List(ctx context.Context, sel Selector, after int64, limit int) ([]Event, error) {
	if sel.Project == "" || sel.Env == "" || (!sel.AllTenants && sel.Tenant == "") {
		return nil, tenant.ErrZeroScope
	}
	q := selectEvents + ` WHERE project_id = ? AND env = ? AND seq > ?`
	args := []any{sel.Project, sel.Env, after}
	if !sel.AllTenants {
		q += ` AND tenant_id = ?`
		args = append(args, sel.Tenant)
	}
	if sel.ResourceKind != "" {
		q += ` AND resource_kind = ?`
		args = append(args, sel.ResourceKind)
	}
	if sel.ResourceID != "" {
		q += ` AND resource_id = ?`
		args = append(args, sel.ResourceID)
	}
	q += ` ORDER BY seq LIMIT ?`
	args = append(args, limit)
	var out []Event
	err := l.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, q, args...)
		if err != nil {
			return err
		}
		out, err = scanEvents(rows)
		return err
	})
	return out, err
}

// Head is the newest seq (0 for an empty log).
func (l *Log) Head(ctx context.Context) (int64, error) {
	var seq int64
	err := l.db.Read(ctx, func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	})
	return seq, err
}

// Cursor is a relay consumer's committed position (0 before its first
// batch).
func (l *Log) Cursor(ctx context.Context, consumer string) (int64, error) {
	var seq int64
	err := l.db.Read(ctx, func(tx store.Tx) error {
		err := store.QueryRow(tx, `SELECT seq FROM event_cursors WHERE consumer = ?`, consumer).Scan(&seq)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			seq, err = 0, nil
		}
		return err
	})
	return seq, err
}

// Advance moves a consumer's cursor to seq in tx: in the same
// transaction as the work the batch enqueued.
func (l *Log) Advance(tx store.Tx, consumer string, seq int64) error {
	_, err := store.Exec(tx, `INSERT INTO event_cursors (consumer, seq, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (consumer) DO UPDATE SET seq = excluded.seq, updated_at = excluded.updated_at`,
		consumer, seq, l.now().UTC().Format(time.RFC3339Nano))
	return err
}

// PollInterval is how often a relay and the hub re-read the table when
// no commit on this node woke them (a commit on another node, Stage 1).
var PollInterval = 250 * time.Millisecond

// Relay runs handle over every batch of events after consumer's cursor
// until ctx ends, advancing the cursor in handle's transaction. A
// failing handle is retried on the next wake; the error is reported to
// onErr when set.
func (l *Log) Relay(ctx context.Context, consumer string, batch int, handle func(tx store.Tx, evs []Event) error, onErr func(error)) error {
	wake := l.hub.notify()
	defer l.hub.unnotify(wake)
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		for {
			cur, err := l.Cursor(ctx, consumer)
			if err != nil {
				return err
			}
			evs, err := l.After(ctx, cur, batch)
			if err != nil || len(evs) == 0 {
				break
			}
			err = l.db.InTx(ctx, func(tx store.Tx) error {
				if err := handle(tx, evs); err != nil {
					return err
				}
				return l.Advance(tx, consumer, evs[len(evs)-1].Seq)
			})
			if err != nil {
				if onErr != nil {
					onErr(err)
				}
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-t.C:
		}
	}
}
