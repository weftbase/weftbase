package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Approvals (P§10, first slice): every call a run parks on is one
// approval record, written in the transaction that parks the run. A
// person decides a record (Decide); the run resumes once every record
// of it is decided, with those decisions. weft denies a pending call a
// resume carries no decision for ("DENIED: no decision"), so a resume
// is never sent early: one decision of two leaves the run parked.
//
// Every change of a record writes its own approval.* event
// (resource_kind approval, data.chain_run_id) in the transaction that
// makes it: approval.requested at the park, approval.decided for a
// person's decision, approval.expired when the expiry denies it,
// approval.cancelled when its run ends without resuming.

// Approval statuses.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalDenied    = "denied"
	ApprovalResolved  = "resolved"  // answered with content instead of running the tool (resume only)
	ApprovalExpired   = "expired"   // the approval expiry passed: denied by the system
	ApprovalCancelled = "cancelled" // the parked run was cancelled
)

// EventApprovalCancelled is the event a pending approval writes when its
// parked run ends without resuming (cancelled, or its version retired).
const EventApprovalCancelled = "approval.cancelled"

// Approval errors.
var (
	// ErrArgsChanged: the decision names other arguments than the
	// record holds (409 args_changed).
	ErrArgsChanged = errors.New("sessions: the approval's arguments are not the ones the decision names")
	// ErrNotPending: the approval was decided otherwise, expired or was
	// cancelled (409 not_pending).
	ErrNotPending = errors.New("sessions: the approval is not pending")
)

// Approval is one pending or decided call.
type Approval struct {
	ID           string          `json:"id"`
	Object       string          `json:"object"` // "approval"
	TenantID     string          `json:"tenant_id"`
	RunID        string          `json:"run_id"`
	CallID       string          `json:"call_id"`
	Agent        string          `json:"agent"`
	AgentVersion string          `json:"agent_version"`
	Tool         string          `json:"tool"`
	Args         json.RawMessage `json:"args"`
	// ArgsSHA256 is sha256 (hex) of the stored arguments' bytes: a
	// decision echoes it, so it binds to the arguments its decider saw.
	ArgsSHA256  string    `json:"args_sha256"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
	// Outcome is approve, deny or resolve once decided (an expired
	// approval reads deny).
	Outcome   string        `json:"outcome,omitempty"`
	Reason    string        `json:"reason,omitempty"`
	DecidedAt *time.Time    `json:"decided_at,omitempty"`
	Actor     *events.Actor `json:"actor,omitempty"`
	// ResumedBy is the run that continued the parked one.
	ResumedBy string `json:"resumed_by,omitempty"`
	// Content is a resolved approval's answer: the result the model was
	// given in place of the tool's.
	Content string `json:"content,omitempty"`
}

// ArgsSHA256 is the hash an approval binds its arguments with.
func ArgsSHA256(args json.RawMessage) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:])
}

func outcomeOf(status string) string {
	switch status {
	case ApprovalApproved:
		return "approve"
	case ApprovalDenied, ApprovalExpired:
		return "deny"
	case ApprovalResolved:
		return "resolve"
	}
	return ""
}

func statusOf(decision string) string {
	switch decision {
	case "approve":
		return ApprovalApproved
	case "resolve":
		return ApprovalResolved
	}
	return ApprovalDenied
}

const selectApproval = `SELECT id, tenant_id, run_id, call_id, agent, agent_version, tool, args, args_sha256, status, reason, content,
	actor_kind, actor_id, resumed_by, requested_at, decided_at FROM approvals`

func scanApproval(r interface{ Scan(...any) error }) (Approval, error) {
	a := Approval{Object: "approval"}
	var args, requested, kind, id string
	var decided *string
	if err := r.Scan(&a.ID, &a.TenantID, &a.RunID, &a.CallID, &a.Agent, &a.AgentVersion, &a.Tool, &args, &a.ArgsSHA256, &a.Status,
		&a.Reason, &a.Content, &kind, &id, &a.ResumedBy, &requested, &decided); err != nil {
		return Approval{}, store.NotFound(err)
	}
	a.Args = json.RawMessage(args)
	a.RequestedAt, a.DecidedAt = parseTS(requested), tsPtr(decided)
	a.Outcome = outcomeOf(a.Status)
	if kind != "" {
		a.Actor = &events.Actor{Kind: kind, ID: id}
	}
	return a, nil
}

// request writes the approval records of a run that just parked.
func (l *Ledger) request(tx store.Tx, s tenant.Scope, run Run, pending []wire.PendingCall) error {
	now := l.ts()
	for _, p := range pending {
		args := p.Args
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		a := Approval{ID: "appr_" + ulid.New(), RunID: run.ID, CallID: p.CallID, Agent: run.Agent, AgentVersion: run.AgentVersion,
			Tool: p.Tool, ArgsSHA256: ArgsSHA256(args), Status: ApprovalPending}
		if _, err := store.Exec(tx, `INSERT INTO approvals (id, project_id, env, tenant_id, run_id, call_id, agent, agent_version, tool,
			args, args_sha256, status, requested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, s.Project(), s.Env(), s.Tenant(), a.RunID, a.CallID, a.Agent, a.AgentVersion, a.Tool, string(args), a.ArgsSHA256,
			ApprovalPending, now); err != nil {
			return fmt.Errorf("sessions: insert approval: %w", err)
		}
		if err := l.emitApproval(tx, s, events.Actor{Kind: "worker", ID: run.worker}, events.ApprovalRequested, a, run.chain(), nil); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) emitApproval(tx store.Tx, s tenant.Scope, actor events.Actor, typ string, a Approval, chain string, extra map[string]any) error {
	ref, err := l.versionRef(tx, s, a.AgentVersion)
	if err != nil {
		return err
	}
	data := map[string]any{"approval_id": a.ID, "run_id": a.RunID, "chain_run_id": chain, "agent_version": ref,
		"call_id": a.CallID, "agent": a.Agent, "tool": a.Tool,
		"args_sha256": a.ArgsSHA256, "status": a.Status}
	for k, v := range extra {
		data[k] = v
	}
	_, err = l.log.Emit(tx, s, events.Event{Type: typ, ResourceKind: "approval", ResourceID: a.ID, Actor: actor,
		Audited: typ == events.ApprovalDecided}, data)
	return err
}

func (l *Ledger) approvalsOf(tx store.Tx, s tenant.Scope, runID string) ([]Approval, error) {
	where, args := exact(s)
	rows, err := store.Query(tx, selectApproval+` WHERE run_id = ? AND `+where+` ORDER BY requested_at, id`, append([]any{runID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// settle records a decision on a pending record, with its event:
// approval.decided, or approval.expired for the expiry's denial.
func (l *Ledger) settle(tx store.Tx, s tenant.Scope, actor events.Actor, a *Approval, chain, status, reason, content string) error {
	now := l.ts()
	if _, err := store.Exec(tx, `UPDATE approvals SET status = ?, reason = ?, content = ?, actor_kind = ?, actor_id = ?, decided_at = ?
		WHERE id = ? AND status = ?`, status, reason, content, actor.Kind, actor.ID, now, a.ID, ApprovalPending); err != nil {
		return err
	}
	t := parseTS(now)
	a.Status, a.Reason, a.Content, a.DecidedAt, a.Outcome = status, reason, content, &t, outcomeOf(status)
	a.Actor = &events.Actor{Kind: actor.Kind, ID: actor.ID}
	typ := events.ApprovalDecided
	if status == ApprovalExpired {
		typ = events.ApprovalExpired
	}
	return l.emitApproval(tx, s, actor, typ, *a, chain, map[string]any{"outcome": a.Outcome, "reason": reason})
}

// resumeDecided resumes a parked run with the decisions of its approval
// records: the ones already decided as recorded; the pending ones as
// given names them (recorded by actor), else as fallback (status and
// reason; ApprovalDenied "no decision" is what weft does with a call a
// resume leaves out).
func (l *Ledger) resumeDecided(tx store.Tx, s tenant.Scope, actor events.Actor, parked Run, given []wire.Decision, fallback, fallbackReason string) (Run, error) {
	if err := resumable(parked); err != nil {
		return Run{}, err
	}
	as, err := l.approvalsOf(tx, s, parked.ID)
	if err != nil {
		return Run{}, err
	}
	byCall := map[string]wire.Decision{}
	for _, d := range given {
		byCall[d.CallID] = d
	}
	var decisions []wire.Decision
	for i := range as {
		a := &as[i]
		if a.Status == ApprovalPending {
			if d, ok := byCall[a.CallID]; ok {
				err = l.settle(tx, s, actor, a, parked.chain(), statusOf(d.Decision), d.Reason, d.Content)
			} else {
				err = l.settle(tx, s, actor, a, parked.chain(), fallback, fallbackReason, "")
			}
			if err != nil {
				return Run{}, err
			}
		}
		delete(byCall, a.CallID)
		decisions = append(decisions, wire.Decision{CallID: a.CallID, Decision: a.Outcome, Reason: a.Reason, Content: a.Content})
	}
	for _, d := range given { // a call with no record (a run parked before records existed)
		if _, ok := byCall[d.CallID]; ok {
			decisions = append(decisions, d)
		}
	}
	run, err := l.resume(tx, s, actor, parked, decisions)
	if err != nil {
		return Run{}, err
	}
	_, err = store.Exec(tx, `UPDATE approvals SET resumed_by = ? WHERE run_id = ?`, run.ID, parked.ID)
	return run, err
}

// cancelApprovals closes the pending records of a parked run that ended
// without resuming (cancelled by a person, or its version retired), each
// with its approval.cancelled event; cause is the run's cancel cause.
func (l *Ledger) cancelApprovals(tx store.Tx, s tenant.Scope, actor events.Actor, run Run, cause string) error {
	as, err := l.approvalsOf(tx, s, run.ID)
	if err != nil {
		return err
	}
	now := l.ts()
	for _, a := range as {
		if a.Status != ApprovalPending {
			continue
		}
		if _, err := store.Exec(tx, `UPDATE approvals SET status = ?, reason = ?, decided_at = ? WHERE id = ? AND status = ?`,
			ApprovalCancelled, cause, now, a.ID, ApprovalPending); err != nil {
			return err
		}
		t := parseTS(now)
		a.Status, a.Reason, a.DecidedAt = ApprovalCancelled, cause, &t
		if err := l.emitApproval(tx, s, actor, EventApprovalCancelled, a, run.chain(), map[string]any{"cancel_cause": cause}); err != nil {
			return err
		}
	}
	return nil
}

// Decide records a person's decision on an approval — approve, deny,
// or resolve (answer for the tool: content is the result the model
// sees, the tool does not run) — and, when it was the run's last
// pending one, resumes the run with every record's decision. argsSHA256 must be the record's
// (ErrArgsChanged). The same decision again is a no-op that returns the
// record; another one on a decided record is ErrNotPending.
func (l *Ledger) Decide(ctx context.Context, s tenant.Scope, actor events.Actor, id, decision, reason, content, argsSHA256 string) (Approval, error) {
	if s.IsZero() {
		return Approval{}, tenant.ErrZeroScope
	}
	if decision != "approve" && decision != "deny" && decision != "resolve" {
		return Approval{}, fmt.Errorf("%w: decision is approve, deny or resolve", ErrInvalid)
	}
	if decision != "resolve" {
		content = ""
	}
	if argsSHA256 == "" {
		return Approval{}, fmt.Errorf("%w: a decision names the args_sha256 of the approval it answers", ErrInvalid)
	}
	var a Approval
	err := l.db.InTx(ctx, func(tx store.Tx) error {
		var err error
		if a, s, err = l.approvalFor(tx, s, id); err != nil {
			return err
		}
		if !strings.EqualFold(argsSHA256, a.ArgsSHA256) {
			return fmt.Errorf("%w (approval %s)", ErrArgsChanged, id)
		}
		if a.Status != ApprovalPending {
			if a.Status == statusOf(decision) {
				return nil
			}
			return fmt.Errorf("%w: approval %s is %s", ErrNotPending, id, a.Status)
		}
		parked, err := l.get(tx, s, a.RunID)
		if err != nil {
			return err
		}
		if err := l.settle(tx, s, actor, &a, parked.chain(), statusOf(decision), reason, content); err != nil {
			return err
		}
		var left int
		if err := store.QueryRow(tx, `SELECT COUNT(*) FROM approvals WHERE run_id = ? AND status = ?`, a.RunID, ApprovalPending).Scan(&left); err != nil || left > 0 {
			return err
		}
		run, err := l.resumeDecided(tx, s, actor, parked, nil, ApprovalDenied, "no decision")
		if err != nil {
			return err
		}
		a.ResumedBy = run.ID
		return nil
	})
	return a, err
}

// approvalFor reads one record s can decide — its own tenant's, or
// under a project-level scope any tenant's — with the scope the
// decision runs under: the record's own tenant (as locate does for a
// run).
func (l *Ledger) approvalFor(tx store.Tx, s tenant.Scope, id string) (Approval, tenant.Scope, error) {
	where, args := within(s, "")
	a, err := scanApproval(store.QueryRow(tx, selectApproval+` WHERE id = ? AND `+where, append([]any{id}, args...)...))
	if err != nil {
		return Approval{}, tenant.Scope{}, fmt.Errorf("approval %s: %w", id, err)
	}
	as, err := s.Within(a.TenantID)
	if err != nil {
		return Approval{}, tenant.Scope{}, fmt.Errorf("approval %s: %w", id, store.ErrNotFound)
	}
	return a, as, nil
}

// Approval reads one approval s can read (every tenant's under a
// project-level scope).
func (l *Ledger) Approval(ctx context.Context, s tenant.Scope, id string) (Approval, error) {
	if s.IsZero() {
		return Approval{}, tenant.ErrZeroScope
	}
	var a Approval
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, args := within(s, "")
		var err error
		if a, err = scanApproval(store.QueryRow(tx, selectApproval+` WHERE id = ? AND `+where, append([]any{id}, args...)...)); err != nil {
			return fmt.Errorf("approval %s: %w", id, err)
		}
		return nil
	})
	return a, err
}

// ApprovalQuery narrows Approvals.
type ApprovalQuery struct {
	// Status is "" (all), "pending", "decided" (anything else), or one
	// status.
	Status string
	Agent  string
	Tool   string
	RunID  string
	Limit  int // default 100, at most 500
	Offset int
}

// ApprovalPage is one page: pending first (oldest request first), then
// the decided ones, newest decision first.
type ApprovalPage struct {
	Approvals []Approval `json:"approvals"`
	// Total is how many match; Pending how many of the scope's wait,
	// whatever the filter.
	Total      int  `json:"total"`
	Pending    int  `json:"pending"`
	NextOffset *int `json:"next_offset"`
}

// Approvals lists the scope's approvals.
func (l *Ledger) Approvals(ctx context.Context, s tenant.Scope, q ApprovalQuery) (ApprovalPage, error) {
	if s.IsZero() {
		return ApprovalPage{}, tenant.ErrZeroScope
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	q.Limit, q.Offset = min(q.Limit, 500), max(q.Offset, 0)
	scopeWhere, args := within(s, "")
	where := ` WHERE ` + scopeWhere
	scope := len(args)
	switch q.Status {
	case "":
	case "decided":
		where += ` AND status <> ?`
		args = append(args, ApprovalPending)
	default:
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	for col, v := range map[string]string{"agent": q.Agent, "tool": q.Tool, "run_id": q.RunID} {
		if v != "" {
			where += ` AND ` + col + ` = ?`
			args = append(args, v)
		}
	}
	page := ApprovalPage{Approvals: []Approval{}}
	err := l.db.Read(ctx, func(tx store.Tx) error {
		if err := store.QueryRow(tx, `SELECT COUNT(*) FROM approvals`+where, args...).Scan(&page.Total); err != nil {
			return err
		}
		if err := store.QueryRow(tx, `SELECT COUNT(*) FROM approvals WHERE `+scopeWhere+` AND status = ?`,
			append(args[:scope:scope], ApprovalPending)...).Scan(&page.Pending); err != nil {
			return err
		}
		rows, err := store.Query(tx, selectApproval+where+` ORDER BY CASE WHEN status = 'pending' THEN 0 ELSE 1 END,
			CASE WHEN status = 'pending' THEN requested_at ELSE '' END, decided_at DESC, id LIMIT ? OFFSET ?`,
			append(args, q.Limit, q.Offset)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			a, err := scanApproval(rows)
			if err != nil {
				return err
			}
			page.Approvals = append(page.Approvals, a)
		}
		return rows.Err()
	})
	if next := q.Offset + len(page.Approvals); err == nil && next < page.Total {
		page.NextOffset = &next
	}
	return page, err
}

// RunApprovals are the approvals of the given runs s can read, by run
// id, each run's in request order: what a run page shows of who decided
// its calls. One read per 200 runs.
func (l *Ledger) RunApprovals(ctx context.Context, s tenant.Scope, runIDs ...string) (map[string][]Approval, error) {
	out := map[string][]Approval{}
	if len(runIDs) == 0 {
		return out, nil
	}
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	ids := slices.Compact(slices.Sorted(slices.Values(runIDs)))
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, scope := within(s, "")
		for len(ids) > 0 {
			n := min(len(ids), 200)
			args := append([]any{}, scope...)
			for _, id := range ids[:n] {
				args = append(args, id)
			}
			rows, err := store.Query(tx, selectApproval+` WHERE `+where+` AND run_id IN (`+placeholders(n)+`) ORDER BY requested_at, id`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				a, err := scanApproval(rows)
				if err != nil {
					_ = rows.Close()
					return err
				}
				out[a.RunID] = append(out[a.RunID], a)
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

// ApprovalDecision is when an approval left pending, and how.
type ApprovalDecision struct {
	Status string
	At     time.Time
}

// ApprovalDecisions are the approvals decided in [since, until) (agent
// "" is every agent): what a window's statistics count as expired and
// denied.
func (l *Ledger) ApprovalDecisions(ctx context.Context, s tenant.Scope, since, until time.Time, agent string) ([]ApprovalDecision, error) {
	if s.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	var out []ApprovalDecision
	err := l.db.Read(ctx, func(tx store.Tx) error {
		where, args := within(s, "")
		rows, err := store.Query(tx, `SELECT status, decided_at FROM approvals WHERE `+where+`
			AND decided_at >= ? AND decided_at < ? AND (? = '' OR agent = ?)`,
			append(args, since.UTC().Format(tsFormat), until.UTC().Format(tsFormat), agent, agent)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var d ApprovalDecision
			var at string
			if err := rows.Scan(&d.Status, &at); err != nil {
				return err
			}
			d.At = parseTS(at)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}
