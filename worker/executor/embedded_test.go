package executor

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/wire"
)

const digest = "sha256:e1"

func newExec(t *testing.T, opts []EmbeddedOption, agents ...*core.Agent) (*Embedded, wire.Registration) {
	t.Helper()
	e, err := NewEmbedded(digest, agents, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return e, reg
}

func start(reg wire.Registration, agent, runID, prompt string) wire.Command {
	for _, a := range reg.Agents {
		if a.Name == agent {
			return wire.Command{CommandID: wire.NewCommandID(), Kind: wire.KindStart, RunID: runID, Agent: agent, AgentVersion: "agv_x",
				BuildDigest: reg.Build.Digest, ManifestHash: a.ManifestHash, Input: &wire.Input{Prompt: prompt}}
		}
	}
	panic("no agent " + agent)
}

// next reads acks until one for cmd in state want (others of cmd are
// collected in seen).
func next(t *testing.T, e *Embedded, want wire.AckState) wire.Ack {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for f, err := range e.Events(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		var a wire.Ack
		if err := f.Decode(&a); err != nil {
			t.Fatal(err)
		}
		if a.State == want {
			return a
		}
	}
	t.Fatalf("no %s ack", want)
	return wire.Ack{}
}

func TestRegisterAndRun(t *testing.T) {
	a := core.New(wefttest.Script(wefttest.Say("hi")), core.Name("support"))
	e, reg := newExec(t, nil, a)
	m, _ := core.Manifest(a)
	if reg.Placement != wire.PlacementEmbedded || reg.Build.Digest != digest || len(reg.Agents) != 1 ||
		string(reg.Agents[0].Manifest) != string(m) || reg.Validate() != nil {
		t.Fatalf("registration = %+v", reg)
	}
	cmd := start(reg, "support", "run_1", "hello")
	ack, err := e.Dispatch(context.Background(), cmd)
	if err != nil || ack.State != wire.AckAccepted {
		t.Fatalf("dispatch = %+v, %v", ack, err)
	}
	if a := next(t, e, wire.AckRunStarted); a.RunID != "run_1" {
		t.Errorf("run_started = %+v", a)
	}
	fin := next(t, e, wire.AckFinished)
	if fin.Status != wire.StatusSucceeded || fin.RunID != "run_1" || fin.Usage == nil || fin.Usage.OutputTokens != 5 {
		t.Errorf("finished = %+v", fin)
	}
}

// At most once: a command id seen before is refused, and so is a
// version, build or agent this executor does not serve, or a
// malformed command; nothing of them runs.
func TestRejectsWhatItDoesNotServe(t *testing.T) {
	model := wefttest.Script(wefttest.Say("once"))
	e, reg := newExec(t, nil, core.New(model, core.Name("support")))
	cmd := start(reg, "support", "run_1", "x")
	if ack, _ := e.Dispatch(context.Background(), cmd); ack.State != wire.AckAccepted {
		t.Fatalf("first = %+v", ack)
	}
	next(t, e, wire.AckFinished)
	if ack, _ := e.Dispatch(context.Background(), cmd); ack.State != wire.AckRejected || !strings.Contains(ack.Error, "seen before") {
		t.Errorf("repeat = %+v", ack)
	}
	bad := []func(c *wire.Command){
		func(c *wire.Command) { c.BuildDigest = "sha256:other" },
		func(c *wire.Command) { c.ManifestHash = "other" },
		func(c *wire.Command) { c.Agent = "nobody" },
		func(c *wire.Command) { c.CommandID = "cmd_1\nevent: run" },
		func(c *wire.Command) { c.AgentVersion = "" },
	}
	for i, mut := range bad {
		c := start(reg, "support", fmt.Sprintf("run_b%d", i), "x")
		mut(&c)
		if ack, _ := e.Dispatch(context.Background(), c); ack.State != wire.AckRejected {
			t.Errorf("bad[%d] = %+v", i, ack)
		}
	}
	if n := len(model.Requests()); n != 1 {
		t.Errorf("model calls = %d, want only the accepted run's", n)
	}
}

func TestPanicIsContained(t *testing.T) {
	boom := core.Tool("boom", "Boom.", func(context.Context, struct{}) (string, error) { panic("handler") })
	panicky := &panicModel{}
	e, reg := newExec(t, nil, core.New(panicky, core.Name("p"), boom))
	if ack, _ := e.Dispatch(context.Background(), start(reg, "p", "run_p", "x")); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	fin := next(t, e, wire.AckFinished)
	if fin.Status != wire.StatusFailed || fin.Error == "" {
		t.Errorf("finished = %+v", fin)
	}
}

// panicModel panics in Stream: the loop's own failure, contained by
// the executor or by weft, never the process.
type panicModel struct{}

func (*panicModel) Stream(context.Context, core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	panic("model")
}

func TestParkAndResume(t *testing.T) {
	var ran int
	refund := core.Tool("refund", "Refund.", func(_ context.Context, in struct {
		Order int `json:"order"`
	}) (string, error) {
		ran++
		return fmt.Sprint("refunded ", in.Order), nil
	}, core.RequireApproval())
	model := wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "refund", Args: `{"order":42}`}), wefttest.Say("done"))
	e, reg := newExec(t, nil, core.New(model, core.Name("refunds"), refund))
	if ack, _ := e.Dispatch(context.Background(), start(reg, "refunds", "run_1", "refund 42")); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	parked := next(t, e, wire.AckFinished)
	if parked.Status != wire.StatusParked || len(parked.Pending) != 1 || parked.Pending[0].Tool != "refund" || len(parked.Checkpoint) == 0 || ran != 0 {
		t.Fatalf("parked = %+v", parked)
	}
	res := start(reg, "refunds", "run_2", "")
	res.Kind, res.Input = wire.KindResume, nil
	res.Resume = &wire.Resume{ResumeOf: "run_1", Checkpoint: parked.Checkpoint,
		Decisions: []wire.Decision{{CallID: parked.Pending[0].CallID, Decision: "approve"}}}
	if ack, _ := e.Dispatch(context.Background(), res); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	if fin := next(t, e, wire.AckFinished); fin.Status != wire.StatusSucceeded || ran != 1 {
		t.Fatalf("resumed = %+v, ran %d", fin, ran)
	}
	// A resume without a checkpoint fails, it does not start afresh.
	bad := res
	bad.CommandID, bad.RunID = wire.NewCommandID(), "run_3"
	bad.Resume = &wire.Resume{ResumeOf: "run_1"}
	if ack, _ := e.Dispatch(context.Background(), bad); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	if fin := next(t, e, wire.AckFinished); fin.Status != wire.StatusFailed || !strings.Contains(fin.Error, "checkpoint") {
		t.Errorf("no checkpoint = %+v", fin)
	}
}

func blocking(started chan<- struct{}) *core.Agent {
	wait := core.Tool("wait", "Wait.", func(ctx context.Context, _ struct{}) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	return core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("x")), core.Name("slow"), wait)
}

func TestCancelHeartbeatAndDrain(t *testing.T) {
	started := make(chan struct{}, 2)
	e, reg := newExec(t, []EmbeddedOption{Heartbeat(20 * time.Millisecond)}, blocking(started))
	if ack, _ := e.Dispatch(context.Background(), start(reg, "slow", "run_c", "x")); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	<-started
	if hb := next(t, e, wire.AckHeartbeat); hb.RunID != "run_c" {
		t.Errorf("heartbeat = %+v", hb)
	}
	if err := e.Cancel(context.Background(), wire.Cancel{CommandID: wire.NewCommandID(), RunID: "run_c", Cause: wire.CauseUser}); err != nil {
		t.Fatal(err)
	}
	if fin := next(t, e, wire.AckFinished); fin.Status != wire.StatusCanceled || fin.Cause != wire.CauseUser {
		t.Errorf("canceled = %+v", fin)
	}
	// Cancel of a run it does not hold is a no-op.
	if err := e.Cancel(context.Background(), wire.Cancel{RunID: "nope"}); err != nil {
		t.Error(err)
	}

	// Drain: the running run gets the deadline, then ends with cause
	// shutdown; new commands are busy.
	e2, reg2 := newExec(t, nil, blocking(started))
	if ack, _ := e2.Dispatch(context.Background(), start(reg2, "slow", "run_d", "x")); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	<-started
	if err := e2.Drain(context.Background(), time.Now().Add(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if fin := next(t, e2, wire.AckFinished); fin.Status != wire.StatusCanceled || fin.Cause != wire.CauseShutdown {
		t.Errorf("drained = %+v", fin)
	}
	if ack, _ := e2.Dispatch(context.Background(), start(reg2, "slow", "run_e", "x")); ack.State != wire.AckBusy {
		t.Errorf("after drain = %+v", ack)
	}
}

func TestNewEmbeddedRefuses(t *testing.T) {
	m := wefttest.Script()
	for i, c := range []struct {
		digest string
		agents []*core.Agent
	}{
		{digest, nil},
		{digest, []*core.Agent{core.New(m)}},
		{digest, []*core.Agent{core.New(m, core.Name("a")), core.New(m, core.Name("a"))}},
		{"md5:x", []*core.Agent{core.New(m, core.Name("a"))}},
	} {
		if _, err := NewEmbedded(c.digest, c.agents); err == nil {
			t.Errorf("case %d built", i)
		}
	}
	if d, err := ExecutableDigest(); err != nil || !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Errorf("ExecutableDigest = %q, %v", d, err)
	}
}
