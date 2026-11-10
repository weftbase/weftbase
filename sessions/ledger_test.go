package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

type fixture struct {
	db  *sqlite.DB
	log *events.Log
	reg *registry.Registry
	q   *queue.Queue
	l   *sessions.Ledger
	v   registry.Version
	s   tenant.Scope
}

type canceler struct{ got []string }

func (c *canceler) CancelRun(_ context.Context, _ tenant.Scope, runID, cause string) error {
	c.got = append(c.got, runID+":"+cause)
	return nil
}

func setup(t *testing.T, opts ...sessions.Option) *fixture {
	t.Helper()
	return setupClock(t, nil, opts...)
}

func setupClock(t *testing.T, now func() time.Time, opts ...sessions.Option) *fixture {
	t.Helper()
	return setupAt(t, ":memory:", now, opts...)
}

// setupAt is setupClock over a control store at path (a file: the
// concurrency tests need connections of their own).
func setupAt(t *testing.T, path string, now func() time.Time, opts ...sessions.Option) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{db: db, log: events.New(db), s: tenant.Default()}
	f.reg = registry.New(db, f.log)
	var qopts []queue.Option
	if now != nil {
		qopts = append(qopts, queue.Clock(now))
		opts = append(opts, sessions.Clock(now))
	}
	f.q = queue.New(db, qopts...)
	f.l = sessions.New(db, f.log, f.reg, f.q, opts...)
	body := []byte(`{"weft":1,"agents":[{"name":"support"}]}`)
	vs, err := f.reg.RegisterIn(ctx, f.s, "sha256:aaa", "embedded",
		[]registry.AgentSpec{{Name: "support", Manifest: body, ManifestHash: registry.ManifestHash(body)}})
	if err != nil {
		t.Fatal(err)
	}
	f.v = vs[0]
	return f
}

// claim takes the next item and prepares its command, as the hub does.
func (f *fixture) claim(t *testing.T) (queue.Lease, wire.Command, bool) {
	t.Helper()
	ctx := context.Background()
	l, err := f.q.Claim(ctx, queue.ClaimFilter{Owner: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var cmd wire.Command
	var ok bool
	if err := f.db.InTx(ctx, func(tx store.Tx) error {
		var err error
		cmd, ok, err = f.l.Prepare(tx, l.Scope, l.Payload, wire.NewCommandID(), "wk_test")
		if err != nil || ok {
			return err
		}
		return f.q.Done(tx, l)
	}); err != nil {
		t.Fatal(err)
	}
	return l, cmd, ok
}

func (f *fixture) tx(t *testing.T, fn func(tx store.Tx) error) {
	t.Helper()
	if err := f.db.InTx(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) types(t *testing.T, runID string) []string {
	t.Helper()
	sel := events.ForScope(f.s, false)
	sel.ResourceKind, sel.ResourceID = "run", runID
	evs, err := f.log.List(context.Background(), sel, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// typesOf is every event's type under one resource kind, oldest first.
func (f *fixture) typesOf(t *testing.T, kind string) []string {
	t.Helper()
	sel := events.ForScope(f.s, false)
	sel.ResourceKind = kind
	evs, err := f.log.List(context.Background(), sel, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// decide decides every pending approval of a parked run with one
// decision (the only door a parked run resumes through besides the
// timers) and returns the resume it admitted.
func (f *fixture) decide(t *testing.T, actor events.Actor, runID, decision string) sessions.Run {
	t.Helper()
	ctx := context.Background()
	page, err := f.l.Approvals(ctx, f.s, sessions.ApprovalQuery{RunID: runID, Status: sessions.ApprovalPending})
	if err != nil || len(page.Approvals) == 0 {
		t.Fatalf("pending approvals of %s = %+v, %v", runID, page, err)
	}
	var resumed string
	for _, a := range page.Approvals {
		got, err := f.l.Decide(ctx, f.s, actor, a.ID, decision, "", "", a.ArgsSHA256)
		if err != nil {
			t.Fatal(err)
		}
		resumed = got.ResumedBy
	}
	res, err := f.l.Get(ctx, f.s, resumed)
	if err != nil {
		t.Fatalf("the resume %q: %v", resumed, err)
	}
	return res
}

func TestAdmitRunFinish(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	actor := events.Actor{Kind: "key", ID: "k1"}
	if _, err := f.l.Admit(ctx, f.s, actor, sessions.AdmitRequest{Agent: "support"}); !errors.Is(err, sessions.ErrInvalid) {
		t.Errorf("no input = %v", err)
	}
	if _, err := f.l.Admit(ctx, f.s, actor, sessions.AdmitRequest{Agent: "nobody", Input: wire.Input{Prompt: "hi"}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown agent = %v", err)
	}
	run, err := f.l.Admit(ctx, f.s, actor, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}, Metadata: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.AgentVersion != f.v.ID || run.ResolvedVia != registry.ViaLatest || run.Status != sessions.StatusQueued || run.TenantID != tenant.ProjectTenant {
		t.Fatalf("admitted = %+v", run)
	}
	l, cmd, ok := f.claim(t)
	if !ok || l.Route != f.v.ID || cmd.Kind != wire.KindStart || cmd.RunID != run.ID || cmd.AgentVersion != f.v.ID ||
		cmd.BuildDigest != "sha256:aaa" || cmd.ManifestHash != f.v.ManifestHash || cmd.Input.Prompt != "hi" || cmd.Metadata["k"] != "v" || cmd.Validate() != nil {
		t.Fatalf("command = %+v ok %v", cmd, ok)
	}
	f.tx(t, func(tx store.Tx) error { return f.l.Started(tx, l.Scope, run.ID) })
	f.tx(t, func(tx store.Tx) error {
		if err := f.l.Finished(tx, l.Scope, run.ID, wire.Ack{State: wire.AckFinished, Status: wire.StatusSucceeded, Usage: &wire.Usage{InputTokens: 10, OutputTokens: 5}}); err != nil {
			return err
		}
		return f.q.Done(tx, l)
	})
	// A second end is ignored: the first wins.
	f.tx(t, func(tx store.Tx) error { return f.l.Lost(tx, l.Scope, run.ID, "late") })
	got, err := f.l.Get(ctx, f.s, run.ID)
	if err != nil || got.Status != sessions.StatusSucceeded || got.Usage.OutputTokens != 5 || got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("finished run = %+v, %v", got, err)
	}
	if ts := strings.Join(f.types(t, run.ID), ","); ts != "run.queued,run.started,run.finished" {
		t.Errorf("events = %s", ts)
	}
	if _, err := f.l.Cancel(ctx, f.s, actor, run.ID, ""); !errors.Is(err, sessions.ErrFinished) {
		t.Errorf("cancel of a finished run = %v", err)
	}
	other, _ := tenant.NewScope(tenant.DefaultProject, "test", "ten_other")
	if _, err := f.l.Get(ctx, other, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another tenant's run = %v", err)
	}
	if runs, err := f.l.List(ctx, f.s, 10); err != nil || len(runs) != 1 {
		t.Errorf("List = %d, %v", len(runs), err)
	}
}

func TestCancelRules(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	c := &canceler{}
	f.l.SetCanceler(c)
	in := sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "hi"}}

	// Queued: canceled at once; the claim then dispatches nothing.
	queued, _ := f.l.Admit(ctx, f.s, events.System, in)
	r, err := f.l.Cancel(ctx, f.s, events.System, queued.ID, "changed my mind")
	if err != nil || r.Status != sessions.StatusCancelled || r.CancelCause != wire.CauseUser {
		t.Fatalf("cancel queued = %+v, %v", r, err)
	}
	if _, err := f.q.Claim(ctx, queue.ClaimFilter{Owner: "x"}); !errors.Is(err, queue.ErrEmpty) {
		t.Errorf("a canceled run's item is still claimable: %v", err)
	}

	// Running: marked, delivered to the canceler, ended by the worker.
	running, _ := f.l.Admit(ctx, f.s, events.System, in)
	l, _, _ := f.claim(t)
	f.tx(t, func(tx store.Tx) error { return f.l.Started(tx, l.Scope, running.ID) })
	r, err = f.l.Cancel(ctx, f.s, events.System, running.ID, "")
	if err != nil || r.Status != sessions.StatusRunning || !r.CancelRequested || len(c.got) != 1 || c.got[0] != running.ID+":user" {
		t.Fatalf("cancel running = %+v, %v, delivered %v", r, err, c.got)
	}
	f.tx(t, func(tx store.Tx) error {
		return f.l.Finished(tx, l.Scope, running.ID, wire.Ack{Status: wire.StatusCanceled, Cause: wire.CauseUser})
	})
	if got, _ := f.l.Get(ctx, f.s, running.ID); got.Status != sessions.StatusCancelled {
		t.Errorf("after the worker's end = %s", got.Status)
	}
	if ts := strings.Join(f.types(t, running.ID), ","); ts != "run.queued,run.started,run.cancelled" {
		t.Errorf("events = %s", ts)
	}
}

func TestParkResumeAndLost(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	run, _ := f.l.Admit(ctx, f.s, events.System, sessions.AdmitRequest{Agent: "support", Input: wire.Input{Prompt: "refund 42"}})
	l, _, _ := f.claim(t)
	f.tx(t, func(tx store.Tx) error { return f.l.Started(tx, l.Scope, run.ID) })
	checkpoint := json.RawMessage(`{"messages":[1]}`)
	f.tx(t, func(tx store.Tx) error {
		return f.l.Finished(tx, l.Scope, run.ID, wire.Ack{Status: wire.StatusParked, Checkpoint: checkpoint,
			Pending: []wire.PendingCall{{CallID: "call_1", Tool: "refund"}}})
	})
	parked, _ := f.l.Get(ctx, f.s, run.ID)
	if parked.Status != sessions.StatusParked || len(parked.Pending) != 1 || parked.FinishedAt != nil {
		t.Fatalf("parked = %+v", parked)
	}
	if run.ChainID != run.ID {
		t.Errorf("a first run's chain = %q", run.ChainID)
	}
	// A new version is labelled prod; the resume still pins the old one.
	body := []byte(`{"weft":1,"agents":[{"name":"support","v":2}]}`)
	vs, err := f.reg.RegisterIn(ctx, f.s, "sha256:bbb", "embedded", []registry.AgentSpec{{Name: "support", Manifest: body, ManifestHash: registry.ManifestHash(body)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reg.PromoteIn(ctx, f.s, registry.PromoteRequest{Agent: "support", Label: "prod", Version: vs[0].ID}); err != nil {
		t.Fatal(err)
	}
	res := f.decide(t, events.System, run.ID, "approve")
	if res.AgentVersion != f.v.ID || res.ResolvedVia != registry.ViaPin || res.ResumeOf != run.ID || res.ChainID != run.ID {
		t.Fatalf("resume = %+v", res)
	}
	if _, err := f.l.Cancel(ctx, f.s, events.System, run.ID, ""); !errors.Is(err, sessions.ErrAlreadyResumed) {
		t.Errorf("cancel of a resumed parked run = %v", err)
	}
	l2, cmd, ok := f.claim(t)
	if !ok || l2.Kind != queue.KindResume || cmd.Kind != wire.KindResume || cmd.AgentVersion != f.v.ID || cmd.BuildDigest != "sha256:aaa" ||
		cmd.Resume == nil || string(cmd.Resume.Checkpoint) != string(checkpoint) || cmd.Resume.Decisions[0].Decision != "approve" || cmd.Validate() != nil {
		t.Fatalf("resume command = %+v", cmd)
	}
	// Prepared again (a restart before any ack): lost, never re-sent.
	f.tx(t, func(tx store.Tx) error {
		_, ok, err := f.l.Prepare(tx, l2.Scope, l2.Payload, wire.NewCommandID(), "wk_2")
		if ok {
			t.Error("a dispatched run was dispatched again")
		}
		return err
	})
	got, _ := f.l.Get(ctx, f.s, res.ID)
	if got.Status != sessions.StatusFailed || !strings.HasPrefix(got.Error, sessions.ErrWorkerLost) {
		t.Errorf("re-prepared run = %+v", got)
	}
	if err := f.l.Steer(ctx, f.s, events.System, res.ID, nil); !errors.Is(err, sessions.ErrInvalidSteer) {
		t.Errorf("steer without messages = %v", err)
	}
}
