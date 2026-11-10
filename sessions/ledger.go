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
// past MaxPinAge is migrated to the agent's prod version (or cancelled
// version_retired when there is none).
//
// Reads (Get, List, ByID, Queued, the approval reads) under a
// project-level scope see every tenant of its project and env, as the
// events stream does for a project-level principal; under a tenant's
// scope they see that tenant only. Writes act within the caller's own
// tenant.
package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	StatusCancelled = "cancelled"
)

// Terminal reports whether a status is an end.
func Terminal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
}

// Errors.
var (
	// ErrFinished: the run already ended (cancel → 409
	// already_finished).
	ErrFinished = errors.New("sessions: the run has finished")
	// ErrNotParked: a resume of a run that is not parked (409
	// not_parked).
	ErrNotParked = errors.New("sessions: the run is not parked")
	// ErrAlreadyResumed: a parked run that was resumed already: a cancel
	// or a resume goes to its resume (409 already_resumed).
	ErrAlreadyResumed = errors.New("sessions: the parked run was resumed already")
	// ErrNotRunning: steer of a run that is not running (409
	// not_running).
	ErrNotRunning = errors.New("sessions: the run is not running")
	// ErrSessionBusy: a turn of a session whose previous turn is queued,
	// running or waiting on a person (409 session_busy).
	ErrSessionBusy = errors.New("sessions: the session has a turn in progress")
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
	// AgentApprovalExpiry overrides ApprovalExpiry for the runs of the
	// named agents.
	AgentApprovalExpiry map[string]time.Duration
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
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	// ChainID is the first run of the park-and-resume chain the run
	// belongs to (its own id when it resumes nothing).
	ChainID         string             `json:"chain_id"`
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

	sessionID  string
	turn       int
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
	// SessionID makes the run the next turn of that session: the
	// ledger stamps weft.session.id and the turn number on the run's
	// metadata. The input is the caller's: the ledger keeps no
	// transcript. A session whose previous turn the ledger holds queued,
	// running or parked (not resumed) is ErrSessionBusy, checked in the
	// admitting transaction.
	SessionID string `json:"session_id,omitempty"`
	// TraceTurn is the highest turn of the session the trace store
	// holds (the caller reads it; turns an app ran itself are only
	// there). The new turn is one more than the highest of it and the
	// ledger's own, so a deleted turn's number is never reused.
	TraceTurn int `json:"-"`
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
		turn, sessionID := 0, req.SessionID
		if sessionID == "" {
			// A caller that names the session in the metadata itself
			// (weft.session.id, and its own weft.turn) keeps its numbers;
			// the ledger records the session so its runs are the
			// session's (listed, deleted with it).
			sessionID = req.Metadata[MetaSessionID]
			turn, _ = strconv.Atoi(req.Metadata[MetaTurn])
		}
		if req.SessionID != "" {
			if err := l.sessionIdle(tx, s, req.SessionID); err != nil {
				return err
			}
			last, err := l.lastTurn(tx, s, req.SessionID)
			if err != nil {
				return err
			}
			turn = max(last, req.TraceTurn) + 1
			md := map[string]string{}
			for k, val := range req.Metadata {
				md[k] = val
			}
			md[MetaSessionID], md[MetaTurn] = req.SessionID, strconv.Itoa(turn)
			req.Metadata = md
		}
		id := "run_" + ulid.New()
		run = Run{ID: id, ChainID: id, TenantID: s.Tenant(), Agent: v.Agent, AgentVersion: v.ID, ResolvedVia: via,
			Status: StatusQueued, Input: &in, Metadata: req.Metadata, sessionID: sessionID, turn: turn}
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
		input_tokens, output_tokens, created_at, updated_at, queue_item, chain_id, session_id, turn)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', '', '', '', '', '', 0, 0, 0, ?, ?, ?, ?, ?, ?)`,
		run.ID, s.Project(), s.Env(), s.Tenant(), run.Agent, run.AgentVersion, run.ResolvedVia, run.Status,
		string(input), string(meta), run.ResumeOf, string(dec), now, now, item, run.ChainID, run.sessionID, run.turn)
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

// VersionRef is how a document names an AgentVersion: its id, its
// number and the labels it holds (an event's, when it was written).
type VersionRef struct {
	ID     string   `json:"id"`
	Number int      `json:"number"`
	Labels []string `json:"labels"`
}

// versionRef names a version in tx (its id alone when the registry no
// longer has it).
func (l *Ledger) versionRef(tx store.Tx, s tenant.Scope, id string) (VersionRef, error) {
	ref := VersionRef{ID: id, Labels: []string{}}
	v, err := l.reg.LookupLabeled(tx, s, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ref, nil
	case err != nil:
		return ref, err
	}
	ref.Number = v.Number
	if v.Labels != nil {
		ref.Labels = v.Labels
	}
	return ref, nil
}

func (l *Ledger) emit(tx store.Tx, s tenant.Scope, actor events.Actor, typ string, run Run, extra map[string]any) error {
	ref, err := l.versionRef(tx, s, run.AgentVersion)
	if err != nil {
		return err
	}
	data := map[string]any{"run_id": run.ID, "chain_run_id": run.chain(), "agent": run.Agent,
		"agent_version": ref, "resolved_via": run.ResolvedVia, "status": run.Status}
	if run.ResumeOf != "" {
		data["resume_of"] = run.ResumeOf
	}
	if run.sessionID != "" {
		// A session page hears its queued turn end (cancelled, failed by
		// the queue time-to-live) without a trace to tell it.
		data["session_id"] = run.sessionID
	}
	for k, v := range extra {
		data[k] = v
	}
	_, err = l.log.Emit(tx, s, events.Event{Type: typ, ResourceKind: "run", ResourceID: run.ID, Actor: actor}, data)
	return err
}

// chain is the first run of the chain the run belongs to: the run a
// park-and-resume chain started with (the run itself when it resumes
// nothing). A page that shows the logical run matches events by it.
func (r Run) chain() string {
	if r.ChainID != "" {
		return r.ChainID
	}
	return r.ID
}

// within is the WHERE fragment (with its arguments) that holds a read
// to s: its project and env, and its tenant unless s is project-level
// — a project-level principal reads every tenant, as the events stream
// lets it. col prefixes the columns ("" or "r.").
func within(s tenant.Scope, col string) (string, []any) {
	if s.ProjectLevel() {
		return col + `project_id = ? AND ` + col + `env = ?`, []any{s.Project(), s.Env()}
	}
	return col + `project_id = ? AND ` + col + `env = ? AND ` + col + `tenant_id = ?`, []any{s.Project(), s.Env(), s.Tenant()}
}

// exact is the WHERE fragment that holds a write to s's own tenant.
func exact(s tenant.Scope) (string, []any) {
	return `project_id = ? AND env = ? AND tenant_id = ?`, []any{s.Project(), s.Env(), s.Tenant()}
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
	created_at, started_at, finished_at, queue_item, parked_at, migrated_to, pin_expired_at, chain_id, session_id, turn FROM runs`

func scanRun(r interface{ Scan(...any) error }) (Run, error) {
	var run Run
	var input, meta, dec, checkpoint, pending, created string
	var started, finished, parked, pinExpired *string
	err := r.Scan(&run.ID, &run.TenantID, &run.Agent, &run.AgentVersion, &run.ResolvedVia, &run.Status, &input, &meta,
		&run.ResumeOf, &run.ResumedBy, &dec, &checkpoint, &pending, &run.commandID, &run.worker, &run.Error, &run.CancelCause,
		&run.CancelRequested, &run.Usage.InputTokens, &run.Usage.OutputTokens, &created, &started, &finished,
		&run.queueItem, &parked, &run.MigratedTo, &pinExpired, &run.ChainID, &run.sessionID, &run.turn)
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

// get reads one run of s's own tenant (what a write acts on).
func (l *Ledger) get(tx store.Tx, s tenant.Scope, id string) (Run, error) {
	where, args := exact(s)
	run, err := scanRun(store.QueryRow(tx, selectRun+` WHERE id = ? AND `+where, append([]any{id}, args...)...))
	if err != nil {
		return Run{}, fmt.Errorf("run %s: %w", id, err)
	}
	return run, nil
}

// Get reads one run s can read (every tenant's under a project-level
// scope); another tenant's run is store.ErrNotFound.
func (l *Ledger) Get(ctx context.Context, s tenant.Scope, id string) (Run, error) {
	if s.IsZero() {
		return Run{}, tenant.ErrZeroScope
	}
	var run Run
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, args := within(s, "")
		var err error
		if run, err = scanRun(store.QueryRow(tx, selectRun+` WHERE id = ? AND `+where, append([]any{id}, args...)...)); err != nil {
			return fmt.Errorf("run %s: %w", id, err)
		}
		return nil
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
		where, args := within(s, "")
		rows, err := store.Query(tx, selectRun+` WHERE `+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit)...)
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

// resumable refuses a resume (or a cancel) of a run that is not an open
// park.
func resumable(parked Run) error {
	switch {
	case parked.Status != StatusParked:
		return fmt.Errorf("%w: run %s is %s", ErrNotParked, parked.ID, parked.Status)
	case parked.ResumedBy != "":
		return fmt.Errorf("%w: run %s was resumed by %s", ErrAlreadyResumed, parked.ID, parked.ResumedBy)
	}
	return nil
}

// resume admits the run that continues parked: on its pin, or on the
// version it was migrated to. A parked run resumes once its approvals
// are decided (Decide), or by the ledger's own timers; there is no
// other door.
func (l *Ledger) resume(tx store.Tx, s tenant.Scope, actor events.Actor, parked Run, decisions []wire.Decision) (Run, error) {
	if err := resumable(parked); err != nil {
		return Run{}, err
	}
	version, via := parked.AgentVersion, registry.ViaPin
	if parked.MigratedTo != "" {
		version, via = parked.MigratedTo, ViaMigrate
	}
	run := Run{ID: "run_" + ulid.New(), ChainID: parked.chain(), TenantID: s.Tenant(), Agent: parked.Agent, AgentVersion: version,
		ResolvedVia: via, Status: StatusQueued, Metadata: parked.Metadata, ResumeOf: parked.ID, decisions: decisions,
		sessionID: parked.sessionID, turn: parked.turn}
	if _, err := store.Exec(tx, `UPDATE runs SET resumed_by = ?, updated_at = ? WHERE id = ?`, run.ID, l.ts(), parked.ID); err != nil {
		return Run{}, err
	}
	return run, l.insert(tx, s, actor, &run, queue.KindResume, 0)
}

// Cancel cancels a run with cause "user". A queued or parked run ends
// cancelled at once (run.cancelled); a running run is marked
// cancel_requested and the cancel goes to its worker after the commit,
// which reports the end. A finished run is ErrFinished; a parked run
// that was resumed is ErrAlreadyResumed (cancel its resume).
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
			return fmt.Errorf("%w: run %s was resumed by %s; cancel that run", ErrAlreadyResumed, runID, run.ResumedBy)
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
			if _, err := l.q.Cancel(tx, s, run.queueItem, StatusCancelled); err != nil {
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
// (cancelled while queued), or it was dispatched before and its worker
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
// succeeded (run.finished), failed (run.failed), cancelled
// (run.cancelled), parked (run.parked, with the pending calls and the
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
		typ, status = events.RunCancelled, StatusCancelled
	case wire.StatusParked:
		typ, status = events.RunParked, StatusParked
	}
	cause := run.CancelCause
	if status == StatusCancelled && ack.Cause != "" {
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
		input_tokens = ?, output_tokens = ?, finished_at = ?, parked_at = COALESCE(?, parked_at), updated_at = ? WHERE id = ?`,
		status, ack.Error, cause, string(pending), string(ack.Checkpoint), u.InputTokens, u.OutputTokens, finished, parked, now, run.ID); err != nil {
		return err
	}
	if status == StatusParked {
		if err := l.parkTimers(tx, s, run.ID, run.Agent); err != nil {
			return err
		}
		if err := l.request(tx, s, *run, ack.Pending); err != nil {
			return err
		}
	} else if run.Status == StatusParked {
		// A parked run that ends (cancelled) takes its open approvals
		// with it.
		if err := l.cancelApprovals(tx, s, actor, *run, cause); err != nil {
			return err
		}
	}
	run.Status, run.Error, run.CancelCause, run.Pending, run.Usage = status, ack.Error, cause, ack.Pending, u
	extra := map[string]any{}
	if ack.Error != "" {
		extra["error"] = ack.Error
	}
	if status == StatusCancelled {
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

// scanRuns scans every row of a selectRun query.
func scanRuns(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}, add func(Run)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return err
		}
		add(run)
	}
	return rows.Err()
}

// placeholders is "?, ?, …" for n values.
func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

// ByID reads the runs among ids s can read, and every run of their
// chains (a parked run, its resumes), by id: the ledger's side of a page
// of runs the trace store listed, its chains included, in one read. Ids
// it does not hold are absent.
func (l *Ledger) ByID(ctx context.Context, s tenant.Scope, ids []string) (map[string]Run, error) {
	out := map[string]Run{}
	if len(ids) == 0 {
		return out, nil
	}
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, scope := within(s, "")
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := append([]any{}, scope...)
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			args = append(args, scope...)
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			q := selectRun + ` WHERE ` + where + ` AND (id IN (` + placeholders(n) + `) OR chain_id IN
				(SELECT chain_id FROM runs WHERE ` + where + ` AND id IN (` + placeholders(n) + `) AND chain_id <> ''))`
			rows, err := store.Query(tx, q, args...)
			if err != nil {
				return err
			}
			if err := scanRuns(rows, func(r Run) { out[r.ID] = r }); err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	})
	return out, err
}

// QueuedQuery pages the runs still waiting for a worker (Queued).
type QueuedQuery struct {
	Agent     string // "" every agent
	SessionID string // "" every run
	// Resumes keeps the queued resumes too; without it only the runs
	// that resume nothing (a resume's chain is listed by its first run).
	Resumes bool
	// Before / BeforeID is the cursor (created_at, id): the runs after
	// that one in the page order. Oldest turns the order around (oldest
	// first; the cursor then reads the runs after it).
	Before   time.Time
	BeforeID string
	Oldest   bool
	// Since / Until bound created_at (zero: none), [Since, Until).
	Since, Until time.Time
	Limit        int // default 100, at most 500
}

// Queued pages the runs s can read that wait for a worker (queued),
// newest first, and counts every one the query matches, the cursor
// aside.
func (l *Ledger) Queued(ctx context.Context, s tenant.Scope, q QueuedQuery) (runs []Run, total int, err error) {
	if s.IsZero() {
		return nil, 0, tenant.ErrZeroScope
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	q.Limit = min(q.Limit, 500)
	where, args := within(s, "")
	where += ` AND status = ?`
	args = append(args, StatusQueued)
	if q.Agent != "" {
		where += ` AND agent = ?`
		args = append(args, q.Agent)
	}
	if q.SessionID != "" {
		where += ` AND session_id = ?`
		args = append(args, q.SessionID)
	}
	if !q.Resumes {
		where += ` AND resume_of = ''`
	}
	if !q.Since.IsZero() {
		where += ` AND created_at >= ?`
		args = append(args, q.Since.UTC().Format(tsFormat))
	}
	if !q.Until.IsZero() {
		where += ` AND created_at < ?`
		args = append(args, q.Until.UTC().Format(tsFormat))
	}
	page, pargs := where, append([]any{}, args...)
	order := ` ORDER BY created_at DESC, id DESC`
	if q.Oldest {
		order = ` ORDER BY created_at, id`
	}
	if !q.Before.IsZero() {
		at := q.Before.UTC().Format(tsFormat)
		cmp := "<"
		if q.Oldest {
			cmp = ">"
		}
		if q.BeforeID != "" {
			page += ` AND (created_at ` + cmp + ` ? OR (created_at = ? AND id ` + cmp + ` ?))`
			pargs = append(pargs, at, at, q.BeforeID)
		} else {
			page += ` AND created_at ` + cmp + ` ?`
			pargs = append(pargs, at)
		}
	}
	err = l.db.Read(ctx, func(tx store.Tx) error {
		if err := store.QueryRow(tx, `SELECT COUNT(*) FROM runs WHERE `+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := store.Query(tx, selectRun+` WHERE `+page+order+` LIMIT ?`, append(pargs, q.Limit)...)
		if err != nil {
			return err
		}
		return scanRuns(rows, func(r Run) { runs = append(runs, r) })
	})
	return runs, total, err
}

// ErrActive: a delete of a run that is queued, running or waiting on a
// person (409 run_active): cancel it first.
var ErrActive = errors.New("sessions: the run is still active")

// active reports whether the platform still has the run in hand.
func active(r Run) bool {
	return r.Status == StatusQueued || r.Status == StatusRunning || (r.Status == StatusParked && r.ResumedBy == "")
}

// Owners reads which tenant holds each of ids in s's project and env,
// whatever s's tenant: what lets a caller refuse to touch a run another
// tenant owns where the trace store cannot tell (it is not
// tenant-scoped yet). Ids the ledger does not hold are absent.
func (l *Ledger) Owners(ctx context.Context, s tenant.Scope, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := []any{s.Project(), s.Env()}
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			rows, err := store.Query(tx, `SELECT id, tenant_id FROM runs WHERE project_id = ? AND env = ? AND id IN (`+placeholders(n)+`)`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, t string
				if err := rows.Scan(&id, &t); err != nil {
					_ = rows.Close()
					return err
				}
				out[id] = t
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

// Delete is the authoritative half of deleting a logical run: in one
// transaction it refuses (ErrActive, nothing deleted) when one of ids is
// a run the ledger still has in hand — queued, running, or parked and
// not resumed — else removes the ones it holds with their approval
// records and writes one audited run.deleted naming every id (the
// trace store's own runs included: a delete of a run only the trace
// store knows is audited too). The caller deletes the trace store's
// rows after it; a retry of a half-done delete finds nothing here and
// is harmless. It returns how many ledger runs and approvals went.
func (l *Ledger) Delete(ctx context.Context, s tenant.Scope, actor events.Actor, ids []string) (runs, approvals int, err error) {
	if s.IsZero() {
		return 0, 0, tenant.ErrZeroScope
	}
	if len(ids) == 0 {
		return 0, 0, nil
	}
	err = l.db.InTx(ctx, func(tx store.Tx) error {
		runs, approvals = 0, 0
		var held []Run
		for _, id := range ids {
			run, ok, err := l.getOpt(tx, s, id)
			if err != nil {
				return err
			}
			if ok {
				held = append(held, run)
			}
		}
		var err error
		if runs, approvals, err = l.deleteRuns(tx, s, held); err != nil {
			return err
		}
		data := map[string]any{"run_ids": ids, "ledger_runs": runs, "approvals": approvals}
		for _, h := range held {
			if h.sessionID != "" {
				data["session_id"] = h.sessionID
			}
		}
		_, err = l.log.Emit(tx, s, events.Event{Type: events.RunDeleted, ResourceKind: "run", ResourceID: ids[0], Actor: actor, Audited: true}, data)
		return err
	})
	return runs, approvals, err
}

// deleteRuns refuses when one of runs is active, else deletes them with
// their approvals.
func (l *Ledger) deleteRuns(tx store.Tx, s tenant.Scope, runs []Run) (n, approvals int, err error) {
	for _, run := range runs {
		if active(run) {
			return 0, 0, fmt.Errorf("%w: run %s is %s", ErrActive, run.ID, run.Status)
		}
	}
	where, args := exact(s)
	for _, run := range runs {
		res, err := store.Exec(tx, `DELETE FROM approvals WHERE run_id = ? AND `+where, append([]any{run.ID}, args...)...)
		if err != nil {
			return 0, 0, err
		}
		k, _ := res.RowsAffected()
		approvals += int(k)
		if _, err := store.Exec(tx, `DELETE FROM runs WHERE id = ? AND `+where, append([]any{run.ID}, args...)...); err != nil {
			return 0, 0, err
		}
	}
	return len(runs), approvals, nil
}

// DeleteSession is the authoritative half of deleting a session: in one
// transaction it refuses (ErrActive) while a run of the session is in
// the platform's hands, else removes every ledger run of the session
// (the queued ones no trace names included) with their approvals, and
// writes one audited session.deleted naming them and traced (the runs
// the trace store holds of it). A turn admitted after it commits is a
// new turn of a new conversation. It returns every run id deleted.
func (l *Ledger) DeleteSession(ctx context.Context, s tenant.Scope, actor events.Actor, sessionID string, traced []string) (ids []string, runs, approvals int, err error) {
	if s.IsZero() {
		return nil, 0, 0, tenant.ErrZeroScope
	}
	err = l.db.InTx(ctx, func(tx store.Tx) error {
		ids = append([]string{}, traced...)
		seen := map[string]bool{}
		for _, id := range ids {
			seen[id] = true
		}
		where, args := exact(s)
		rows, err := store.Query(tx, selectRun+` WHERE `+where+` AND session_id = ? ORDER BY created_at, id`, append(args, sessionID)...)
		if err != nil {
			return err
		}
		var held []Run
		if err := scanRuns(rows, func(r Run) { held = append(held, r) }); err != nil {
			return err
		}
		for _, r := range held {
			if !seen[r.ID] {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if runs, approvals, err = l.deleteRuns(tx, s, held); err != nil {
			return err
		}
		_, err = l.log.Emit(tx, s, events.Event{Type: events.SessionDeleted, ResourceKind: "session", ResourceID: sessionID, Actor: actor, Audited: true},
			map[string]any{"session_id": sessionID, "run_ids": ids, "ledger_runs": runs, "approvals": approvals})
		return err
	})
	return ids, runs, approvals, err
}

// The run metadata keys that make a run a session's turn (weft's own).
const (
	MetaSessionID = "weft.session.id"
	MetaTurn      = "weft.turn"
)

// sessionIdle refuses (ErrSessionBusy) a turn of a session the ledger
// holds a run of that is queued, running, or parked and not resumed.
func (l *Ledger) sessionIdle(tx store.Tx, s tenant.Scope, sessionID string) error {
	where, args := exact(s)
	var id, status string
	err := store.QueryRow(tx, `SELECT id, status FROM runs WHERE `+where+` AND session_id = ?
		AND (status IN (?, ?) OR (status = ? AND resumed_by = '')) ORDER BY created_at DESC LIMIT 1`,
		append(args, sessionID, StatusQueued, StatusRunning, StatusParked)...).Scan(&id, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	word := status
	if status == StatusParked {
		word = "waiting on a person"
	}
	return fmt.Errorf("%w: session %s: its run %s is %s; a new turn starts when it has ended", ErrSessionBusy, sessionID, id, word)
}

// lastTurn is the highest turn the ledger holds of the session (0 for
// none).
func (l *Ledger) lastTurn(tx store.Tx, s tenant.Scope, sessionID string) (int, error) {
	where, args := exact(s)
	var n int
	err := store.QueryRow(tx, `SELECT COALESCE(MAX(turn), 0) FROM runs WHERE `+where+` AND session_id = ?`, append(args, sessionID)...).Scan(&n)
	return n, err
}

// SessionLast is the session's newest run the ledger holds (s's own
// tenant), ok false when it holds none: what the next turn's history
// waits on.
func (l *Ledger) SessionLast(ctx context.Context, s tenant.Scope, sessionID string) (run Run, ok bool, err error) {
	if s.IsZero() {
		return Run{}, false, tenant.ErrZeroScope
	}
	err = l.db.Read(ctx, func(tx store.Tx) error {
		where, args := exact(s)
		run, err = scanRun(store.QueryRow(tx, selectRun+` WHERE `+where+` AND session_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
			append(args, sessionID)...))
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		ok = err == nil
		return err
	})
	return run, ok, err
}

// SessionRuns are the ids of the session's runs the ledger holds (s's
// own tenant), oldest first: every one, a queued run no trace names
// yet included.
func (l *Ledger) SessionRuns(ctx context.Context, s tenant.Scope, sessionID string) ([]string, error) {
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	var out []string
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, args := exact(s)
		rows, err := store.Query(tx, `SELECT id FROM runs WHERE `+where+` AND session_id = ? ORDER BY created_at, id`, append(args, sessionID)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// DispatchedToBuild reports, for each of runIDs, whether the run — or,
// for a subagent child (an id "<parent>/<step>/<call>"), the root run
// it descends from — is a run the ledger dispatched to a worker of the
// build with that digest: its AgentVersion's build is digest, and it
// was handed to a worker (a command was sent, or it started). What
// OTLP ingest checks a worker key's records against: a build's key
// writes its own runs' records only. One read for the batch; s's
// project and env, every tenant (a worker serves them all).
func (l *Ledger) DispatchedToBuild(ctx context.Context, s tenant.Scope, digest string, runIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(runIDs))
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	roots := map[string]bool{}
	for _, id := range runIDs {
		out[id] = false
		roots[RootRunID(id)] = true
	}
	if len(roots) == 0 || digest == "" {
		return out, nil
	}
	ids := make([]string, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	ok := map[string]bool{}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := []any{s.Project(), s.Env(), digest}
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			rows, err := store.Query(tx, `SELECT r.id FROM runs AS r JOIN agent_versions AS v ON v.id = r.agent_version
				WHERE r.project_id = ? AND r.env = ? AND v.build_digest = ? AND (r.command_id <> '' OR r.started_at IS NOT NULL)
				AND r.id IN (`+placeholders(n)+`)`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				ok[id] = true
			}
			if err := rows.Close(); err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range runIDs {
		out[id] = ok[RootRunID(id)]
	}
	return out, nil
}

// RootRunID is the run a run id descends from: weft names a subagent
// child "<parent>/<step>/<call>" (and a child's resume
// "<parent>/resume/<call>"), so the root is the id up to its first '/'.
func RootRunID(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return id
}

// SessionsLast is each session's newest run the ledger holds (s can
// read), by session id, in one read: what a list of sessions shows of
// a turn the trace store does not know yet (a queued turn, one waiting
// on a person). Sessions it holds no run of are absent.
func (l *Ledger) SessionsLast(ctx context.Context, s tenant.Scope, sessionIDs []string) (map[string]Run, error) {
	out := map[string]Run{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, scope := within(s, "")
		ids := sessionIDs
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := append([]any{}, scope...)
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			rows, err := store.Query(tx, selectRun+` WHERE `+where+` AND session_id IN (`+placeholders(n)+`) ORDER BY created_at, id`, args...)
			if err != nil {
				return err
			}
			if err := scanRuns(rows, func(r Run) { out[r.sessionID] = r }); err != nil {
				return err
			}
			ids = ids[n:]
		}
		return nil
	})
	return out, err
}
