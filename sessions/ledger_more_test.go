package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// A session's next turn is refused in the admitting transaction while
// the ledger holds one of its runs queued, running or parked (not
// resumed) — the trace store cannot know a queued turn.
func TestSessionBusyInTheAdmit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	turn := func() (sessions.Run, error) {
		return f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
	}
	first, err := turn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn(); !errors.Is(err, sessions.ErrSessionBusy) {
		t.Fatalf("a turn while the first is queued: %v, want ErrSessionBusy", err)
	}
	if _, err := f.l.Cancel(ctx, f.s, events.System, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	second, err := turn()
	if err != nil || second.Metadata[sessions.MetaTurn] != "2" {
		t.Fatalf("the next turn = %+v, %v", second.Metadata, err)
	}
}

// Concurrent admits of one session's turn: exactly one wins.
func TestSessionBusyRace(t *testing.T) {
	f := setupAt(t, filepath.Join(t.TempDir(), "control.db"), nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, busy := 0, 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, sessions.ErrSessionBusy):
				busy++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if won != 1 || busy != 7 {
		t.Errorf("won %d, busy %d; want 1 and 7", won, busy)
	}
}

// A turn's number is one more than the highest the session holds, in
// the ledger or (TraceTurn) the trace store: a deleted turn's number is
// never reused.
func TestTurnNumbersSkipDeletedTurns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var ids []string
	for range 2 {
		run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.l.Cancel(ctx, f.s, events.System, run.ID, ""); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	if _, _, err := f.l.Delete(ctx, f.s, events.System, ids[:1]); err != nil {
		t.Fatal(err)
	}
	third, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
	if err != nil || third.Metadata[sessions.MetaTurn] != "3" {
		t.Fatalf("third turn = %v, %v; want 3", third.Metadata, err)
	}
	if _, err := f.l.Cancel(ctx, f.s, events.System, third.ID, ""); err != nil {
		t.Fatal(err)
	}
	fifth, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", TraceTurn: 4, Input: wire.Input{Prompt: "hi"}})
	if err != nil || fifth.Metadata[sessions.MetaTurn] != "5" {
		t.Fatalf("after a traced turn 4 = %v, %v; want 5", fifth.Metadata, err)
	}
}

// A parked run that ends without resuming writes one approval.cancelled
// per pending approval, with the chain's first run.
func TestCancelWritesApprovalEvents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run := f.parkOn(t, refund, email)
	if _, err := f.l.Cancel(ctx, f.s, alice, run.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ts := strings.Join(f.typesOf(t, "approval"), ","); ts != "approval.requested,approval.requested,approval.cancelled,approval.cancelled" {
		t.Errorf("approval events = %s", ts)
	}
	page, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: run.ID})
	for _, a := range page.Approvals {
		if a.Status != sessions.ApprovalCancelled || a.Reason != wire.CauseUser || a.DecidedAt == nil {
			t.Errorf("approval = %+v", a)
		}
	}
}

// Two last decisions at once resume the run once; the slower one reads
// the approval decided.
func TestConcurrentLastDecisionsResumeOnce(t *testing.T) {
	for range 5 {
		f := setupAt(t, filepath.Join(t.TempDir(), "control.db"), nil)
		ctx := context.Background()
		run := f.parkOn(t, refund, email)
		page, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: run.ID})
		var wg sync.WaitGroup
		errs := make([]error, len(page.Approvals))
		for i, a := range page.Approvals {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = f.l.Decide(ctx, f.s, alice, a.ID, "approve", "", "", a.ArgsSHA256)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, _ := f.l.Get(ctx, f.s, run.ID)
		resumes, _, err := f.l.Queued(ctx, f.s, sessions.QueuedQuery{Resumes: true})
		if err != nil || got.ResumedBy == "" || len(resumes) != 1 || resumes[0].ResumeOf != run.ID {
			t.Fatalf("resumed_by %q, queued resumes %+v, %v; want one", got.ResumedBy, resumes, err)
		}
	}
}

// A decision racing a cancel: one of the two wins and the run is
// either resumed or cancelled, never both.
func TestDecideRacingCancel(t *testing.T) {
	for range 5 {
		f := setupAt(t, filepath.Join(t.TempDir(), "control.db"), nil)
		ctx := context.Background()
		run := f.parkOn(t, refund)
		page, _ := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: run.ID})
		a := page.Approvals[0]
		var wg sync.WaitGroup
		var decideErr, cancelErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, decideErr = f.l.Decide(ctx, f.s, alice, a.ID, "approve", "", "", a.ArgsSHA256)
		}()
		go func() {
			defer wg.Done()
			_, cancelErr = f.l.Cancel(ctx, f.s, alice, run.ID, "")
		}()
		wg.Wait()
		got, _ := f.l.Get(ctx, f.s, run.ID)
		switch {
		case decideErr == nil && errors.Is(cancelErr, sessions.ErrAlreadyResumed):
			if got.ResumedBy == "" || got.Status != sessions.StatusParked {
				t.Fatalf("decided first: %+v", got)
			}
		case cancelErr == nil && errors.Is(decideErr, sessions.ErrNotPending):
			if got.ResumedBy != "" || got.Status != sessions.StatusCancelled {
				t.Fatalf("cancelled first: %+v", got)
			}
		default:
			t.Fatalf("decide %v, cancel %v", decideErr, cancelErr)
		}
	}
}

// A project-level scope reads every tenant's runs and approvals (as the
// events stream does); a tenant's scope reads its own; writes stay in
// the writer's tenant.
func TestProjectScopeReadsEveryTenant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	acme, err := tenant.NewScope(tenant.DefaultProject, tenant.DefaultEnv, "ten_acme")
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.l.Admit(ctx, acme, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.Get(ctx, f.s, run.ID); err != nil {
		t.Errorf("the project scope cannot read acme's run: %v", err)
	}
	if m, err := f.l.ByID(ctx, f.s, []string{run.ID}); err != nil || m[run.ID].TenantID != "ten_acme" {
		t.Errorf("ByID = %v, %v", m, err)
	}
	if q, n, err := f.l.Queued(ctx, f.s, sessions.QueuedQuery{}); err != nil || n != 1 || len(q) != 1 {
		t.Errorf("Queued = %d %v, %v", n, q, err)
	}
	other, _ := tenant.NewScope(tenant.DefaultProject, tenant.DefaultEnv, "ten_other")
	if _, err := f.l.Get(ctx, other, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another tenant reads acme's run: %v", err)
	}
	if own, _ := f.l.Owners(ctx, other, []string{run.ID}); own[run.ID] != "ten_acme" {
		t.Errorf("Owners = %v", own)
	}
}

// Queued pages honestly: limit, a (created_at, id) cursor both ways,
// the exact count.
func TestQueuedPages(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	f := setupClock(t, c.now)
	ctx := context.Background()
	var ids []string
	for range 7 {
		run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
		c.add(time.Second)
	}
	for _, oldest := range []bool{false, true} {
		var got []string
		q := sessions.QueuedQuery{Limit: 3, Oldest: oldest}
		for {
			page, total, err := f.l.Queued(ctx, f.s, q)
			if err != nil || total != 7 {
				t.Fatalf("total %d, %v", total, err)
			}
			for _, r := range page {
				got = append(got, r.ID)
			}
			if len(page) < 3 {
				break
			}
			last := page[len(page)-1]
			q.Before, q.BeforeID = last.CreatedAt, last.ID
		}
		want := append([]string{}, ids...)
		if !oldest {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("oldest=%v pages = %v, want %v", oldest, got, want)
		}
	}
}

// DispatchedToBuild: a run (or a subagent child of it) handed to a
// worker of the build reads true; another build's, or a run not yet
// dispatched, false.
func TestDispatchedToBuild(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{run.ID, run.ID + "/0/call_1", "run_unknown"}
	if got, _ := f.l.DispatchedToBuild(ctx, f.s, "sha256:aaa", ids); got[run.ID] {
		t.Errorf("a queued run reads dispatched: %v", got)
	}
	f.claim(t)
	got, err := f.l.DispatchedToBuild(ctx, f.s, "sha256:aaa", ids)
	if err != nil || !got[run.ID] || !got[run.ID+"/0/call_1"] || got["run_unknown"] {
		t.Errorf("DispatchedToBuild(aaa) = %v, %v", got, err)
	}
	if got, _ := f.l.DispatchedToBuild(ctx, f.s, "sha256:bbb", ids); got[run.ID] {
		t.Errorf("another build's key owns the run: %v", got)
	}
}

// DeleteSession removes every ledger run of the session in one
// transaction, refusing while one is active, and audits it.
func TestDeleteSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.l.DeleteSession(ctx, f.s, alice, "s1", nil); !errors.Is(err, sessions.ErrActive) {
		t.Fatalf("delete with a queued turn: %v", err)
	}
	if _, err := f.l.Cancel(ctx, f.s, alice, run.ID, ""); err != nil {
		t.Fatal(err)
	}
	ids, runs, _, err := f.l.DeleteSession(ctx, f.s, alice, "s1", []string{"traced_only"})
	if err != nil || runs != 1 || strings.Join(ids, ",") != "traced_only,"+run.ID {
		t.Fatalf("DeleteSession = %v %d, %v", ids, runs, err)
	}
	sel := events.ForScope(f.s, false)
	sel.ResourceKind = "session"
	evs, _ := f.log.List(ctx, sel, 0, 10)
	if len(evs) != 1 || evs[0].Type != events.SessionDeleted || !evs[0].Audited {
		t.Fatalf("session events = %+v", evs)
	}
	var d struct {
		RunIDs []string `json:"run_ids"`
	}
	if json.Unmarshal(evs[0].Data, &d) != nil || len(d.RunIDs) != 2 {
		t.Errorf("session.deleted data = %s", evs[0].Data)
	}
	// A run only the trace store knows is audited too.
	if _, _, err := f.l.Delete(ctx, f.s, alice, []string{"otel-trace-only"}); err != nil {
		t.Fatal(err)
	}
	if ts := f.types(t, "otel-trace-only"); len(ts) != 1 || ts[0] != events.RunDeleted {
		t.Errorf("trace-only delete events = %v", ts)
	}
}

// A session's run events carry its session_id (a session page hears a
// queued turn end without a trace), and a cancel is run.cancelled.
func TestRunEventsNameTheSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	run, err := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", SessionID: "s1", Input: wire.Input{Prompt: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.Cancel(ctx, f.s, events.System, run.ID, ""); err != nil {
		t.Fatal(err)
	}
	sel := events.ForScope(f.s, false)
	sel.ResourceKind, sel.ResourceID = "run", run.ID
	evs, _ := f.log.List(ctx, sel, 0, 10)
	if len(evs) != 2 || evs[1].Type != "run.cancelled" {
		t.Fatalf("events = %+v", evs)
	}
	for _, e := range evs {
		var d struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.SessionID != "s1" {
			t.Errorf("%s data = %s, want session_id s1", e.Type, e.Data)
		}
	}
}
