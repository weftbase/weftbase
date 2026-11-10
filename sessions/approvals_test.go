package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
)

// park admits a run and parks it on the given calls.
func (f *fixture) parkOn(t *testing.T, calls ...wire.PendingCall) sessions.Run {
	t.Helper()
	ctx := context.Background()
	run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.claim(t); !ok {
		t.Fatal("nothing to dispatch")
	}
	f.tx(t, func(tx store.Tx) error {
		if err := f.l.Started(tx, f.s, run.ID); err != nil {
			return err
		}
		return f.l.Finished(tx, f.s, run.ID, wire.Ack{Status: wire.StatusParked, Pending: calls, Checkpoint: json.RawMessage(`{}`)})
	})
	return run
}

var (
	alice  = events.Actor{Kind: "operator", ID: "alice"}
	refund = wire.PendingCall{CallID: "c1", Tool: "refund", Args: json.RawMessage(`{"order":42}`)}
	email  = wire.PendingCall{CallID: "c2", Tool: "email", Args: json.RawMessage(`{"to":"a@b.c"}`)}
)

// One pending call is one approval; the run resumes when the last one
// is decided, with every decision and its decider recorded.
func TestApprovalsDecide(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run := f.parkOn(t, refund, email)
	page, err := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{Status: "pending"})
	if err != nil || len(page.Approvals) != 2 || page.Pending != 2 || page.Total != 2 {
		t.Fatalf("pending = %+v, %v", page, err)
	}
	a1, a2 := page.Approvals[0], page.Approvals[1]
	if a1.RunID != run.ID || a1.Tool != "refund" || a1.ArgsSHA256 != sessions.ArgsSHA256(refund.Args) || a1.AgentVersion != f.v.ID || a1.Status != "pending" {
		t.Errorf("approval = %+v", a1)
	}

	// A decision names the arguments it was shown.
	if _, err := f.l.Decide(ctx, f.s, alice, a1.ID, "approve", "", "", "deadbeef"); !errors.Is(err, sessions.ErrArgsChanged) {
		t.Errorf("another hash: %v, want ErrArgsChanged", err)
	}
	if _, err := f.l.Decide(ctx, f.s, alice, a1.ID, "approve", "", "", ""); !errors.Is(err, sessions.ErrInvalid) {
		t.Errorf("no hash: %v, want ErrInvalid", err)
	}

	// The first of two decisions leaves the run parked.
	got, err := f.l.Decide(ctx, f.s, alice, a1.ID, "approve", "", "", a1.ArgsSHA256)
	if err != nil || got.Status != "approved" || got.Outcome != "approve" || got.ResumedBy != "" || got.Actor == nil || got.Actor.ID != "alice" {
		t.Fatalf("first decision = %+v, %v", got, err)
	}
	if r, _ := f.l.Get(ctx, f.s, run.ID); r.Status != sessions.StatusParked || r.ResumedBy != "" {
		t.Fatalf("after one of two decisions the run = %+v", r)
	}
	// The same decision again changes nothing; another one is refused.
	if again, err := f.l.Decide(ctx, f.s, alice, a1.ID, "approve", "", "", a1.ArgsSHA256); err != nil || again.DecidedAt == nil || !again.DecidedAt.Equal(*got.DecidedAt) {
		t.Errorf("the same decision again = %+v, %v", again, err)
	}
	if _, err := f.l.Decide(ctx, f.s, alice, a1.ID, "deny", "", "", a1.ArgsSHA256); !errors.Is(err, sessions.ErrNotPending) {
		t.Errorf("another decision on a decided approval: %v, want ErrNotPending", err)
	}

	// The last decision resumes the run with both.
	bob := events.Actor{Kind: "key", ID: "key_bob"}
	last, err := f.l.Decide(ctx, f.s, bob, a2.ID, "deny", "not this address", "", a2.ArgsSHA256)
	if err != nil || last.Status != "denied" || last.ResumedBy == "" {
		t.Fatalf("last decision = %+v, %v", last, err)
	}
	_, cmd, ok := f.claim(t)
	if !ok || cmd.Kind != wire.KindResume || cmd.RunID != last.ResumedBy || len(cmd.Resume.Decisions) != 2 {
		t.Fatalf("resume command = %+v (%v)", cmd, ok)
	}
	want := []wire.Decision{{CallID: "c1", Decision: "approve"}, {CallID: "c2", Decision: "deny", Reason: "not this address"}}
	for i, d := range cmd.Resume.Decisions {
		if d != want[i] {
			t.Errorf("decision %d = %+v, want %+v", i, d, want[i])
		}
	}
	decided, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{Status: "decided", RunID: run.ID})
	if len(decided.Approvals) != 2 || decided.Pending != 0 {
		t.Fatalf("decided = %+v", decided)
	}
	for _, a := range decided.Approvals {
		if a.ResumedBy != last.ResumedBy || a.Actor == nil {
			t.Errorf("decided approval = %+v", a)
		}
	}
	if byRun, _ := f.l.RunApprovals(ctx, f.s, run.ID); len(byRun[run.ID]) != 2 {
		t.Errorf("RunApprovals = %+v", byRun)
	}
}

// POST /runs/{id}/resume still resumes at once: its decisions are
// recorded by the caller, and a call it leaves out is denied, as weft
// denies it.
func TestResumeRecordsApprovals(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run := f.parkOn(t, refund, email)
	res, err := f.l.Resume(ctx, f.s, alice, run.ID, []wire.Decision{{CallID: "c1", Decision: "resolve", Content: "refunded by hand"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: run.ID})
	if len(page.Approvals) != 2 || page.Pending != 0 {
		t.Fatalf("approvals = %+v", page)
	}
	by := map[string]sessions.Approval{}
	for _, a := range page.Approvals {
		by[a.CallID] = a
		if a.ResumedBy != res.ID || a.Actor == nil || a.Actor.ID != "alice" {
			t.Errorf("approval = %+v", a)
		}
	}
	if a := by["c1"]; a.Status != "resolved" || a.Content != "refunded by hand" {
		t.Errorf("c1 = %+v", a)
	}
	if a := by["c2"]; a.Status != "denied" || a.Reason != "no decision" {
		t.Errorf("c2 = %+v", a)
	}
}

// A parked run that is cancelled takes its approvals with it.
func TestCancelClosesApprovals(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run := f.parkOn(t, refund)
	if _, err := f.l.Cancel(ctx, f.s, alice, run.ID, "never mind"); err != nil {
		t.Fatal(err)
	}
	page, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{})
	if len(page.Approvals) != 1 || page.Approvals[0].Status != "cancelled" || page.Pending != 0 {
		t.Fatalf("approvals = %+v", page)
	}
	if _, err := f.l.Decide(ctx, f.s, alice, page.Approvals[0].ID, "approve", "", "", page.Approvals[0].ArgsSHA256); !errors.Is(err, sessions.ErrNotPending) {
		t.Errorf("deciding a cancelled approval: %v, want ErrNotPending", err)
	}
}
