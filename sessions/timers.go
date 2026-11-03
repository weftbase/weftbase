package sessions

import (
	"errors"
	"fmt"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// parkTimers schedules a parked run's approval expiry and maximum pin
// age, per the Policy.
func (l *Ledger) parkTimers(tx store.Tx, s tenant.Scope, runID string) error {
	now := l.now()
	if d := l.policy.ApprovalExpiry; d > 0 {
		if _, err := l.q.At(tx, s, queue.Timer{Kind: TimerApprovalExpiry, Ref: runID, DueAt: now.Add(d)}); err != nil {
			return err
		}
	}
	if d := l.policy.MaxPinAge; d > 0 {
		if _, err := l.q.At(tx, s, queue.Timer{Kind: TimerMaxPinAge, Ref: runID, DueAt: now.Add(d)}); err != nil {
			return err
		}
	}
	return nil
}

// OnTimer fires one of the ledger's timers in tx (queue.FireFunc). A
// timer whose run moved on (dispatched, resumed, ended) does nothing:
// the timer table is never cleaned up on the happy path, the fire
// checks instead. Kinds it does not own are ignored.
func (l *Ledger) OnTimer(tx store.Tx, t queue.Timer) error {
	switch t.Kind {
	case TimerQueueTTL:
		return l.queueTTL(tx, t.Scope, t.Ref)
	case TimerApprovalExpiry:
		return l.approvalExpiry(tx, t.Scope, t.Ref)
	case TimerMaxPinAge:
		return l.maxPinAge(tx, t.Scope, t.Ref)
	}
	return nil
}

func (l *Ledger) getOpt(tx store.Tx, s tenant.Scope, id string) (Run, bool, error) {
	run, err := l.get(tx, s, id)
	if errors.Is(err, store.ErrNotFound) {
		return Run{}, false, nil
	}
	return run, err == nil, err
}

// queueTTL fails a run still queued and never dispatched: no worker
// served its version in time (P§6.3 rule 3).
func (l *Ledger) queueTTL(tx store.Tx, s tenant.Scope, runID string) error {
	run, ok, err := l.getOpt(tx, s, runID)
	if err != nil || !ok || run.Status != StatusQueued || run.commandID != "" {
		return err
	}
	why := fmt.Sprintf("%s: no worker served %s within the queue time-to-live (%s)", ErrWorkerLost, run.AgentVersion, l.policy.QueueTTL)
	if run.queueItem != "" {
		if _, err := l.q.Cancel(tx, s, run.queueItem, why); err != nil {
			return err
		}
	}
	return l.end(tx, s, events.System, &run, wire.Ack{Status: wire.StatusFailed, Error: why})
}

// approvalExpiry resumes a parked run that nobody decided in time with
// every pending call denied, and records approval.expired.
func (l *Ledger) approvalExpiry(tx store.Tx, s tenant.Scope, runID string) error {
	parked, ok, err := l.getOpt(tx, s, runID)
	if err != nil || !ok || parked.Status != StatusParked || parked.ResumedBy != "" {
		return err
	}
	decisions := make([]wire.Decision, len(parked.Pending))
	calls := make([]string, len(parked.Pending))
	for i, p := range parked.Pending {
		decisions[i] = wire.Decision{CallID: p.CallID, Decision: "deny", Reason: "approval expired"}
		calls[i] = p.CallID
	}
	res, err := l.resume(tx, s, events.System, parked, decisions)
	if err != nil {
		return err
	}
	return l.emit(tx, s, events.System, events.ApprovalExpired, parked, map[string]any{"calls": calls, "resumed_by": res.ID})
}

// maxPinAge migrates a parked run that outlived its maximum pin age to
// the agent's prod version (prod, else the newest): its resume runs
// there (resolved_via migrate), where weft re-validates every parked
// call against the new tool contract. A run already pinned to that
// version is only marked; an agent with no version left cancels it
// (cause version_retired).
func (l *Ledger) maxPinAge(tx store.Tx, s tenant.Scope, runID string) error {
	parked, ok, err := l.getOpt(tx, s, runID)
	if err != nil || !ok || parked.Status != StatusParked || parked.ResumedBy != "" {
		return err
	}
	now := l.ts()
	v, _, rerr := l.reg.Resolve(tx, s, parked.Agent, "")
	if rerr != nil {
		if !errors.Is(rerr, store.ErrNotFound) {
			return rerr
		}
		if _, err := store.Exec(tx, `UPDATE runs SET pin_expired_at = ? WHERE id = ?`, now, runID); err != nil {
			return err
		}
		parked.CancelCause = wire.CauseVersionRetired
		return l.end(tx, s, events.System, &parked, wire.Ack{Status: wire.StatusCanceled, Cause: wire.CauseVersionRetired,
			Error: "the maximum pin age passed and the agent has no version to migrate to"})
	}
	to := ""
	if v.ID != parked.AgentVersion {
		to = v.ID
	}
	if _, err := store.Exec(tx, `UPDATE runs SET pin_expired_at = ?, migrated_to = ?, updated_at = ? WHERE id = ?`, now, to, now, runID); err != nil {
		return err
	}
	if to == "" {
		return nil
	}
	return l.emit(tx, s, events.System, events.RunMigrated, parked, map[string]any{"from": parked.AgentVersion, "to": to,
		"pin_age": l.now().Sub(derefTime(parked.ParkedAt)).Round(time.Second).String()})
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
