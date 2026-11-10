// Package sessions is the system of record for runs (and, later,
// sessions over weft's thread store): the run ledger.
//
// Every run carries the AgentVersion it was admitted on and how that
// version was chosen (resolved_via: label:<l>, latest, pin), and the
// tenant of the credential that started it. Admit resolves the version,
// writes the ledger row, enqueues the start item and emits run.queued
// in one transaction. The worker hub reports what happens next through
// the methods a worker.RunSink needs (Prepare, Started, Finished,
// Rejected, Lost), each in the transaction that also completes the
// queue item; every status change writes its event in that transaction.
//
// The ledger's timers (OnTimer, fired by the scheduler role from the
// queue's timer table) enforce its Policy: a run still queued with no
// worker after QueueTTL fails worker_lost; a parked run whose approval
// expiry passes is resumed with its pending calls denied; a parked run
// past MaxPinAge is migrated to the agent's prod version (or canceled
// version_retired when there is none).
package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Run statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusParked    = "parked"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Terminal reports whether a status is an end.
func Terminal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCanceled
}

// Errors.
var (
	// ErrFinished: the run already ended (cancel → 409).
	ErrFinished = errors.New("sessions: the run has finished")
	// ErrNotParked: resume of a run that is not parked, or was resumed
	// already (409).
	ErrNotParked = errors.New("sessions: the run is not parked")
	// ErrNotRunning: steer of a run that is not running (409).
	ErrNotRunning = errors.New("sessions: the run is not running")
	// ErrNotSupported: a verb that has not landed (steer).
	ErrNotSupported = errors.New("sessions: not supported yet")
	// ErrInvalid: a malformed request (400).
	ErrInvalid = errors.New("sessions: invalid request")
	// ErrInvalidSteer: a steer that is not user messages (400
	// invalid_steer).
	ErrInvalidSteer = errors.New("sessions: invalid steer")
)

// Timer kinds the ledger schedules (queue.Timer.Kind; Ref is the run).
const (
	TimerQueueTTL       = "run.queue_ttl"
	TimerApprovalExpiry = "run.approval_expiry"
	TimerMaxPinAge      = "run.max_pin_age"
)

// ViaMigrate is the resolved_via of a resume that runs on the version a
// parked run was migrated to (its maximum pin age passed).
const ViaMigrate = "migrate"

// Policy is the ledger's time limits.
type Policy struct {
	// QueueTTL: a run still queued, never dispatched, this long after
	// admission fails worker_lost (P§6.3 rule 3). 0: DefaultQueueTTL;
	// negative: never.
	QueueTTL time.Duration
	// ApprovalExpiry: a parked run not resumed this long after it
	// parked is resumed with every pending call denied ("approval
	// expired"). 0: never.
	ApprovalExpiry time.Duration
	// MaxPinAge: a parked run not resumed this long after it parked is
	// migrated to the agent's prod version (prod, else the newest), or
	// canceled version_retired when it has none (P§5.6). 0:
	// DefaultMaxPinAge; negative: never.
	MaxPinAge time.Duration
}

// Policy defaults.
const (
	DefaultQueueTTL  = 24 * time.Hour
	DefaultMaxPinAge = 30 * 24 * time.Hour
)

func (p Policy) withDefaults() Policy {
	if p.QueueTTL == 0 {
		p.QueueTTL = DefaultQueueTTL
	}
	if p.MaxPinAge == 0 {
		p.MaxPinAge = DefaultMaxPinAge
	}
	return p
}

// ErrWorkerLost is the error a run gets when its worker stopped
// answering before it finished.
const ErrWorkerLost = "worker_lost"

// Usage is a run's token usage.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Run is one ledger row.
type Run struct {
	ID              string             `json:"id"`
	TenantID        string             `json:"tenant_id"`
	Agent           string             `json:"agent"`
	AgentVersion    string             `json:"agent_version"`
	ResolvedVia     string             `json:"resolved_via"`
	Status          string             `json:"status"`
	Input           *wire.Input        `json:"input,omitempty"`
	Metadata        map[string]string  `json:"metadata,omitempty"`
	ResumeOf        string             `json:"resume_of,omitempty"`
	ResumedBy       string             `json:"resumed_by,omitempty"`
	MigratedTo      string             `json:"migrated_to,omitempty"`
	Pending         []wire.PendingCall `json:"pending,omitempty"`
	Error           string             `json:"error,omitempty"`
	CancelCause     string             `json:"cancel_cause,omitempty"`
	CancelRequested bool               `json:"cancel_requested,omitempty"`
	Usage           Usage              `json:"usage"`
	CreatedAt       time.Time          `json:"created_at"`
	StartedAt       *time.Time         `json:"started_at,omitempty"`
	ParkedAt        *time.Time         `json:"parked_at,omitempty"`
	PinExpiredAt    *time.Time         `json:"pin_expired_at,omitempty"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`

	queueItem  string
	commandID  string
	worker     string
	checkpoint json.RawMessage
	decisions  []wire.Decision
}

// Canceler delivers a cancel to whoever holds a running run (the
// worker hub; the kernel wires it).
type Canceler interface {
	CancelRun(ctx context.Context, s tenant.Scope, runID, cause string) error
}

// Steerer delivers a steer to whoever holds a running run (the worker
// hub; the kernel wires it).
type Steerer interface {
	SteerRun(ctx context.Context, s tenant.Scope, runID string, messages []json.RawMessage) error
}

// Ledger is the run ledger.
type Ledger struct {
	db       store.Runner
	log      *events.Log
	reg      *registry.Registry
	q        *queue.Queue
	now      func() time.Time
	policy   Policy
	canceler Canceler
	steerer  Steerer
}

// Option configures a Ledger.
type Option func(*Ledger)

// Clock sets the time source (tests).
func Clock(now func() time.Time) Option { return func(l *Ledger) { l.now = now } }

// WithPolicy sets the ledger's time limits.
func WithPolicy(p Policy) Option { return func(l *Ledger) { l.policy = p } }

// New returns the ledger.
func New(db store.Runner, log *events.Log, reg *registry.Registry, q *queue.Queue, opts ...Option) *Ledger {
	l := &Ledger{db: db, log: log, reg: reg, q: q, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	l.policy = l.policy.withDefaults()
	return l
}

// SetCanceler wires the cancel path to running runs (before serving).
func (l *Ledger) SetCanceler(c Canceler) { l.canceler = c }

// SetSteerer wires the steer path to running runs (before serving).
func (l *Ledger) SetSteerer(st Steerer) { l.steerer = st }

// Policy is the ledger's time limits, defaults applied.
func (l *Ledger) Policy() Policy { return l.policy }

const tsFormat = "2006-01-02T15:04:05.000000000Z"

func (l *Ledger) ts() string { return l.now().UTC().Format(tsFormat) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(tsFormat, s)
	return t
}

// AdmitRequest starts a run.
type AdmitRequest struct {
	Agent string `json:"agent"`
	// Version selects the version: "label:<l>", "agv_…", or "" (the
	// prod label, else the newest version).
	Version  string            `json:"version,omitempty"`
	Input    wire.Input        `json:"input"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// Priority orders the start among queued runs.
	Priority int `json:"priority,omitempty"`
}

// itemPayload is a queue item's payload.
type itemPayload struct {
	RunID string `json:"run_id"`
}

// Admit resolves the version, writes the run (queued), enqueues its
// start and emits run.queued, in one transaction.
func (l *Ledger) Admit(ctx context.Context, s tenant.Scope, actor events.Actor, req AdmitRequest) (Run, error) {
	if s.IsZero() {
		return Run{}, tenant.ErrZeroScope
	}
	if req.Agent == "" {
		return Run{}, fmt.Errorf("%w: a run needs an agent", ErrInvalid)
	}
	if req.Input.Prompt == "" && len(req.Input.Messages) == 0 {
		return Run{}, fmt.Errorf("%w: a run needs input.prompt or input.messages", ErrInvalid)
	}
	var run Run
	err := l.db.InTx(ctx, func(tx store.Tx) error {
		v, via, err := l.reg.Resolve(tx, s, req.Agent, req.Version)
		if err != nil {
			return err
		}
		in := req.Input
		run = Run{ID: "run_" + ulid.New(), TenantID: s.Tenant(), Agent: v.Agent, AgentVersion: v.ID, ResolvedVia: via,
			Status: StatusQueued, Input: &in, Metadata: req.Metadata}
		return l.insert(tx, s, actor, &run, queue.KindRun, req.Priority)
	})
	return run, err
}

func (l *Ledger) insert(tx store.Tx, s tenant.Scope, actor events.Actor, run *Run, kind string, priority int) error {
	now := l.ts()
	run.CreatedAt = parseTS(now)
	input, _ := json.Marshal(run.Input)
	meta, _ := json.Marshal(run.Metadata)
	dec, _ := json.Marshal(run.decisions)
	payload, _ := json.Marshal(itemPayload{RunID: run.ID})
	item, err := l.q.Enqueue(tx, s, queue.Item{Kind: kind, Route: run.AgentVersion, Priority: priority, Payload: payload, MaxAttempts: 1})
	if err != nil {
		return err
	}
	run.queueItem = item
	_, err = store.Exec(tx, `INSERT INTO runs (id, project_id, env, tenant_id, agent, agent_version, resolved_via, status, input, metadata,
		resume_of, resumed_by, decisions, checkpoint, pending, command_id, worker, error, cancel_cause, cancel_requested,
		input_tokens, output_tokens, created_at, updated_at, queue_item)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', '', '', '', '', '', 0, 0, 0, ?, ?, ?)`,
		run.ID, s.Project(), s.Env(), s.Tenant(), run.Agent, run.AgentVersion, run.ResolvedVia, run.Status,
		string(input), string(meta), run.ResumeOf, string(dec), now, now, item)
	if err != nil {
		return fmt.Errorf("sessions: insert run: %w", err)
	}
	if l.policy.QueueTTL > 0 {
		if _, err := l.q.At(tx, s, queue.Timer{Kind: TimerQueueTTL, Ref: run.ID, DueAt: l.now().Add(l.policy.QueueTTL)}); err != nil {
			return err
		}
	}
	return l.emit(tx, s, actor, events.RunQueued, *run, nil)
}

func (l *Ledger) emit(tx store.Tx, s tenant.Scope, actor events.Actor, typ string, run Run, extra map[string]any) error {
	data := map[string]any{"run_id": run.ID, "agent": run.Agent, "agent_version": run.AgentVersion,
		"resolved_via": run.ResolvedVia, "status": run.Status}
	if run.ResumeOf != "" {
		data["resume_of"] = run.ResumeOf
	}
	for k, v := range extra {
		data[k] = v
	}
	_, err := l.log.Emit(tx, s, events.Event{Type: typ, ResourceKind: "run", ResourceID: run.ID, Actor: actor}, data)
	return err
}

// ItemRunID reads the run id of a queue item's payload.
func ItemRunID(payload json.RawMessage) (string, error) {
	var p itemPayload
	if err := json.Unmarshal(payload, &p); err != nil || p.RunID == "" {
		return "", fmt.Errorf("sessions: queue item payload %s names no run", payload)
	}
	return p.RunID, nil
}

const selectRun = `SELECT id, tenant_id, agent, agent_version, resolved_via, status, input, metadata, resume_of, resumed_by,
	decisions, checkpoint, pending, command_id, worker, error, cancel_cause, cancel_requested, input_tokens, output_tokens,
	created_at, started_at, finished_at, queue_item, parked_at, migrated_to, pin_expired_at FROM runs`

func scanRun(r interface{ Scan(...any) error }) (Run, error) {
	var run Run
	var input, meta, dec, checkpoint, pending, created string
	var started, finished, parked, pinExpired *string
	err := r.Scan(&run.ID, &run.TenantID, &run.Agent, &run.AgentVersion, &run.ResolvedVia, &run.Status, &input, &meta,
		&run.ResumeOf, &run.ResumedBy, &dec, &checkpoint, &pending, &run.commandID, &run.worker, &run.Error, &run.CancelCause,
		&run.CancelRequested, &run.Usage.InputTokens, &run.Usage.OutputTokens, &created, &started, &finished,
		&run.queueItem, &parked, &run.MigratedTo, &pinExpired)
	if err != nil {
		return Run{}, store.NotFound(err)
	}
	_ = json.Unmarshal([]byte(input), &run.Input)
	_ = json.Unmarshal([]byte(meta), &run.Metadata)
	_ = json.Unmarshal([]byte(dec), &run.decisions)
	_ = json.Unmarshal([]byte(pending), &run.Pending)
	if checkpoint != "" {
		run.checkpoint = json.RawMessage(checkpoint)
	}
	run.CreatedAt = parseTS(created)
	run.StartedAt, run.FinishedAt, run.ParkedAt, run.PinExpiredAt = tsPtr(started), tsPtr(finished), tsPtr(parked), tsPtr(pinExpired)
	return run, nil
}

func tsPtr(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t := parseTS(*s)
	return &t
}

func (l *Ledger) get(tx store.Tx, s tenant.Scope, id string) (Run, error) {
	run, err := scanRun(store.QueryRow(tx, selectRun+` WHERE id = ? AND project_id = ? AND env = ? AND tenant_id = ?`,
		id, s.Project(), s.Env(), s.Tenant()))
	if err != nil {
		return Run{}, fmt.Errorf("run %s: %w", id, err)
	}
	return run, nil
}

// Get reads one run in s; another tenant's run is store.ErrNotFound.
func (l *Ledger) Get(ctx context.Context, s tenant.Scope, id string) (Run, error) {
	if s.IsZero() {
		return Run{}, tenant.ErrZeroScope
	}
	var run Run
	err := l.db.Read(ctx, func(tx store.Tx) error {
		var err error
		run, err = l.get(tx, s, id)
		return err
	})
	return run, err
}

// List reads the scope's newest runs.
func (l *Ledger) List(ctx context.Context, s tenant.Scope, limit int) ([]Run, error) {
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []Run
	err := l.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, selectRun+` WHERE project_id = ? AND env = ? AND tenant_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
			s.Project(), s.Env(), s.Tenant(), limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

// Resume admits a new run that continues a parked one on the parked
// run's own AgentVersion (resolved_via pin), with the decisions on its
// pending calls. A run that is not parked, or was resumed already, is
// ErrNotParked.
func (l *Ledger) Resume(ctx context.Context, s tenant.Scope, actor events.Actor, runID string, decisions []wire.Decision) (Run, error) {
	if s.IsZero() {
		return Run{}, tenant.ErrZeroScope
	}
	for _, d := range decisions {
		if d.CallID == "" || (d.Decision != "approve" && d.Decision != "deny" && d.Decision != "resolve") {
			return Run{}, fmt.Errorf("%w: a decision needs a call_id and approve, deny or resolve", ErrInvalid)
		}
	}
	var run Run
	err := l.db.InTx(ctx, func(tx store.Tx) error {
		parked, err := l.get(tx, s, runID)
		if err != nil {
			return err
		}
		run, err = l.resumeDecided(tx, s, actor, parked, decisions, ApprovalDenied, "no decision")
		return err
	})
	return run, err
}

// resume admits the run that continues parked: on its pin, or on the
// version it was migrated to.
func (l *Ledger) resume(tx store.Tx, s tenant.Scope, actor events.Actor, parked Run, decisions []wire.Decision) (Run, error) {
	if parked.Status != StatusParked || parked.ResumedBy != "" {
		return Run{}, fmt.Errorf("%w: run %s is %s", ErrNotParked, parked.ID, parked.Status)
	}
	version, via := parked.AgentVersion, registry.ViaPin
	if parked.MigratedTo != "" {
		version, via = parked.MigratedTo, ViaMigrate
	}
	run := Run{ID: "run_" + ulid.New(), TenantID: s.Tenant(), Agent: parked.Agent, AgentVersion: version,
		ResolvedVia: via, Status: StatusQueued, Metadata: parked.Metadata, ResumeOf: parked.ID, decisions: decisions}
	if _, err := store.Exec(tx, `UPDATE runs SET resumed_by = ?, updated_at = ? WHERE id = ?`, run.ID, l.ts(), parked.ID); err != nil {
		return Run{}, err
	}
	return run, l.insert(tx, s, actor, &run, queue.KindResume, 0)
}

// Cancel cancels a run with cause "user". A queued or parked run ends
// canceled at once (run.canceled); a running run is marked
// cancel_requested and the cancel goes to its worker after the commit,
// which reports the end. A finished run is ErrFinished; a parked run
// that was resumed is ErrNotParked (cancel its resume).
func (l *Ledger) Cancel(ctx context.Context, s tenant.Scope, actor events.Actor, runID, reason string) (Run, error) {
	if s.IsZero() {
		return Run{}, tenant.ErrZeroScope
	}
	var run Run
	var deliver bool
	err := l.db.InTx(ctx, func(tx store.Tx) error {
		var err error
		if run, err = l.get(tx, s, runID); err != nil {
			return err
		}
		switch {
		case Terminal(run.Status):
			return fmt.Errorf("%w: run %s is %s", ErrFinished, runID, run.Status)
		case run.Status == StatusParked && run.ResumedBy != "":
			return fmt.Errorf("%w: run %s was resumed by %s; cancel that run", ErrNotParked, runID, run.ResumedBy)
		case run.Status == StatusRunning:
			if run.CancelRequested {
				return nil
			}
			run.CancelRequested, run.CancelCause, deliver = true, wire.CauseUser, true
			_, err := store.Exec(tx, `UPDATE runs SET cancel_requested = 1, cancel_cause = ?, updated_at = ? WHERE id = ?`,
				wire.CauseUser, l.ts(), runID)
			return err
		}
		// A queued run already handed to a worker (accepted, not yet
		// started) is ended here and the cancel still goes to it.
		deliver = run.Status == StatusQueued && run.commandID != ""
		if run.Status == StatusQueued && run.queueItem != "" {
			if _, err := l.q.Cancel(tx, s, run.queueItem, "canceled"); err != nil {
				return err
			}
		}
		return l.end(tx, s, actor, &run, wire.Ack{Status: wire.StatusCanceled, Cause: wire.CauseUser, Error: reason})
	})
	if err == nil && deliver && l.canceler != nil {
		if cerr := l.canceler.CancelRun(ctx, s, runID, wire.CauseUser); cerr != nil {
			return run, fmt.Errorf("sessions: cancel recorded, delivery failed: %w", cerr)
		}
	}
	return run, err
}

// Steer delivers user messages into a running run (P§7.5): they are
// drained at its next drain point. Messages that are not user messages
// are ErrInvalidSteer; a run that is not running (queued, parked,
// finished, or no longer in its worker's hands) is ErrNotRunning. A
// delivered steer writes run.steered.
func (l *Ledger) Steer(ctx context.Context, s tenant.Scope, actor events.Actor, runID string, messages []json.RawMessage) error {
	if s.IsZero() {
		return tenant.ErrZeroScope
	}
	if err := (wire.Steer{CommandID: wire.NewCommandID(), RunID: runID, Messages: messages}).Validate(); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidSteer, strings.TrimPrefix(err.Error(), "wire: "))
	}
	run, err := l.Get(ctx, s, runID)
	if err != nil {
		return err
	}
	if run.Status != StatusRunning || run.CancelRequested {
		return fmt.Errorf("%w: run %s is %s", ErrNotRunning, runID, run.Status)
	}
	if l.steerer == nil {
		return ErrNotSupported
	}
	if err := l.steerer.SteerRun(ctx, s, runID, messages); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	return l.db.InTx(ctx, func(tx store.Tx) error {
		return l.emit(tx, s, actor, events.RunSteered, run, map[string]any{"messages": len(messages)})
	})
}

// Prepare is the hub's first step for a claimed item (its payload), in the
// transaction that records the dispatch: it returns the command for
// the run, or ok false when nothing is to be dispatched — the run ended
// (canceled while queued), or it was dispatched before and its worker
// is gone (a restart, an expired lease): that run is lost, never
// re-sent (at-most-once).
func (l *Ledger) Prepare(tx store.Tx, s tenant.Scope, payload json.RawMessage, commandID, worker string) (wire.Command, bool, error) {
	runID, err := ItemRunID(payload)
	if err != nil {
		return wire.Command{}, false, err
	}
	run, err := l.get(tx, s, runID)
	if err != nil {
		return wire.Command{}, false, err
	}
	if run.Status != StatusQueued {
		if run.Status == StatusRunning {
			return wire.Command{}, false, l.Lost(tx, s, runID, "the worker stopped answering")
		}
		return wire.Command{}, false, nil
	}
	if run.commandID != "" {
		return wire.Command{}, false, l.Lost(tx, s, runID, "dispatched before a restart; its outcome is unknown")
	}
	v, err := l.reg.Lookup(tx, s, run.AgentVersion)
	if err != nil {
		return wire.Command{}, false, err
	}
	if _, err := store.Exec(tx, `UPDATE runs SET command_id = ?, worker = ?, updated_at = ? WHERE id = ?`,
		commandID, worker, l.ts(), runID); err != nil {
		return wire.Command{}, false, err
	}
	cmd := wire.Command{CommandID: commandID, Kind: wire.KindStart, RunID: run.ID, Agent: run.Agent, AgentVersion: run.AgentVersion,
		BuildDigest: v.BuildDigest, ManifestHash: v.ManifestHash, TenantID: run.TenantID, Input: run.Input, Metadata: run.Metadata}
	if run.ResumeOf != "" {
		parked, err := l.get(tx, s, run.ResumeOf)
		if err != nil {
			return wire.Command{}, false, err
		}
		cmd.Kind, cmd.Input = wire.KindResume, nil
		cmd.Resume = &wire.Resume{ResumeOf: parked.ID, Checkpoint: parked.checkpoint, Decisions: run.decisions}
	}
	return cmd, true, nil
}

// Undispatched clears a run's dispatch when its worker handed the
// command back unrun (busy): the next claim dispatches it afresh.
func (l *Ledger) Undispatched(tx store.Tx, s tenant.Scope, runID string) error {
	_, err := store.Exec(tx, `UPDATE runs SET command_id = '', worker = '', updated_at = ? WHERE id = ? AND project_id = ? AND env = ? AND tenant_id = ? AND status = ?`,
		l.ts(), runID, s.Project(), s.Env(), s.Tenant(), StatusQueued)
	return err
}

// Started marks a run running (run.started). A run already past
// queued is left alone.
func (l *Ledger) Started(tx store.Tx, s tenant.Scope, runID string) error {
	run, err := l.get(tx, s, runID)
	if err != nil || run.Status != StatusQueued {
		return err
	}
	now := l.ts()
	if _, err := store.Exec(tx, `UPDATE runs SET status = ?, started_at = ?, updated_at = ? WHERE id = ?`,
		StatusRunning, now, now, runID); err != nil {
		return err
	}
	run.Status = StatusRunning
	return l.emit(tx, s, events.Actor{Kind: "worker", ID: run.worker}, events.RunStarted, run, nil)
}

// Finished records a run's end from its worker's finished ack:
// succeeded (run.finished), failed (run.failed), canceled
// (run.canceled), parked (run.parked, with the pending calls and the
// checkpoint its resume is sent). A run that ended already is left
// alone: the first end wins.
func (l *Ledger) Finished(tx store.Tx, s tenant.Scope, runID string, ack wire.Ack) error {
	run, err := l.get(tx, s, runID)
	if err != nil || Terminal(run.Status) || run.Status == StatusParked {
		return err
	}
	return l.end(tx, s, events.Actor{Kind: "worker", ID: run.worker}, &run, ack)
}

// Rejected fails a run its worker refused before running it.
func (l *Ledger) Rejected(tx store.Tx, s tenant.Scope, runID, reason string) error {
	return l.Finished(tx, s, runID, wire.Ack{Status: wire.StatusFailed, Error: "rejected: " + reason})
}

// Lost fails a run whose worker stopped answering (error worker_lost).
func (l *Ledger) Lost(tx store.Tx, s tenant.Scope, runID, why string) error {
	run, err := l.get(tx, s, runID)
	if err != nil || Terminal(run.Status) || run.Status == StatusParked {
		return err
	}
	return l.end(tx, s, events.System, &run, wire.Ack{Status: wire.StatusFailed, Error: ErrWorkerLost + ": " + why})
}

func (l *Ledger) end(tx store.Tx, s tenant.Scope, actor events.Actor, run *Run, ack wire.Ack) error {
	typ, status := events.RunFailed, StatusFailed
	switch ack.Status {
	case wire.StatusSucceeded:
		typ, status = events.RunFinished, StatusSucceeded
	case wire.StatusCanceled:
		typ, status = events.RunCanceled, StatusCanceled
	case wire.StatusParked:
		typ, status = events.RunParked, StatusParked
	}
	cause := run.CancelCause
	if status == StatusCanceled && ack.Cause != "" {
		cause = ack.Cause
	}
	pending, _ := json.Marshal(ack.Pending)
	if len(ack.Pending) == 0 {
		pending = nil
	}
	var u Usage
	if ack.Usage != nil {
		u = Usage{InputTokens: ack.Usage.InputTokens, OutputTokens: ack.Usage.OutputTokens}
	}
	now := l.ts()
	var finished, parked any = now, nil
	if status == StatusParked {
		finished, parked = nil, now
	}
	if _, err := store.Exec(tx, `UPDATE runs SET status = ?, error = ?, cancel_cause = ?, pending = ?, checkpoint = ?,
		input_tokens = ?, output_tokens = ?, finished_at = ?, parked_at = ?, updated_at = ? WHERE id = ?`,
		status, ack.Error, cause, string(pending), string(ack.Checkpoint), u.InputTokens, u.OutputTokens, finished, parked, now, run.ID); err != nil {
		return err
	}
	if status == StatusParked {
		if err := l.parkTimers(tx, s, run.ID); err != nil {
			return err
		}
		if err := l.request(tx, s, *run, ack.Pending); err != nil {
			return err
		}
	} else if run.Status == StatusParked {
		// A parked run that ends (cancelled) takes its open approvals
		// with it.
		if err := l.cancelApprovals(tx, run.ID); err != nil {
			return err
		}
	}
	run.Status, run.Error, run.CancelCause, run.Pending, run.Usage = status, ack.Error, cause, ack.Pending, u
	extra := map[string]any{}
	if ack.Error != "" {
		extra["error"] = ack.Error
	}
	if status == StatusCanceled {
		extra["cancel_cause"] = cause
	}
	if status == StatusParked {
		ids := make([]string, len(ack.Pending))
		for i, p := range ack.Pending {
			ids[i] = p.CallID
		}
		extra["pending"] = ids
	}
	return l.emit(tx, s, actor, typ, *run, extra)
}

// ByID reads the scope's runs among ids, by id: the ledger's side of a
// page of runs the trace store listed. Ids it does not hold are absent.
func (l *Ledger) ByID(ctx context.Context, s tenant.Scope, ids []string) (map[string]Run, error) {
	out := map[string]Run{}
	if s.IsZero() || len(ids) == 0 {
		return out, nil
	}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := []any{s.Project(), s.Env(), s.Tenant()}
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			rows, err := store.Query(tx, selectRun+` WHERE project_id = ? AND env = ? AND tenant_id = ? AND id IN (?`+strings.Repeat(", ?", n-1)+`)`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				run, err := scanRun(rows)
				if err != nil {
					_ = rows.Close()
					return err
				}
				out[run.ID] = run
			}
			if err := rows.Close(); err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	})
	return out, err
}

// Queued lists the scope's runs still waiting for a worker, newest
// first (at most limit; agent "" is every agent): the runs no trace
// names yet.
func (l *Ledger) Queued(ctx context.Context, s tenant.Scope, agent string, limit int) ([]Run, error) {
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []Run
	err := l.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, selectRun+` WHERE project_id = ? AND env = ? AND tenant_id = ? AND status = ? AND (? = '' OR agent = ?)
			ORDER BY created_at DESC, id DESC LIMIT ?`, s.Project(), s.Env(), s.Tenant(), StatusQueued, agent, agent, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}
