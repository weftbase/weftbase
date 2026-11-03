package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/wire"
)

// The link tests are the legacy runtime link's (runtime/link_test.go
// on the legacy branch), re-targeted at the worker protocol over a
// socket: register on connect and after a reconnect, ack before run,
// at-most-once, ping and cancel, stop cancels runs, the bounded seen
// set, a panic contained, the backoff, heartbeats, and nothing run on
// an undelivered accepted ack.

const digest = "sha256:0123"

// fakeServer is the supervisor's end of the socket.
type fakeServer struct {
	t      *testing.T
	path   string
	ln     net.Listener
	conns  chan *fakeConn
	pingMS int64
	refuse string
	n      atomic.Int32
}

type fakeConn struct {
	t     *testing.T
	conn  net.Conn
	fr    *wire.FrameReader
	hello wire.Hello
	wmu   sync.Mutex
}

func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "wbw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	s := &fakeServer{t: t, path: socketPath(t), conns: make(chan *fakeConn, 8), pingMS: 60000}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fc := &fakeConn{t: t, conn: c, fr: wire.NewFrameReader(c)}
			f, err := fc.fr.Read()
			if err != nil || f.Event != wire.EventRegister || f.Decode(&fc.hello) != nil {
				_ = c.Close()
				continue
			}
			n := s.n.Add(1)
			fc.send(wire.EventWelcome, "", wire.Welcome{WorkerID: fmt.Sprintf("wk_%d", n), PingMS: s.pingMS, Error: s.refuse})
			t.Cleanup(func() { _ = c.Close() })
			s.conns <- fc
		}
	}()
	return s
}

func (s *fakeServer) accept() *fakeConn {
	s.t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(10 * time.Second):
		s.t.Fatal("the worker never connected")
		return nil
	}
}

func (c *fakeConn) send(event, id string, v any) {
	f, err := wire.NewFrame(event, id, v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = wire.WriteFrame(c.conn, f)
}

func (c *fakeConn) run(cmd wire.Command) { c.send(wire.EventRun, cmd.CommandID, cmd) }

// ack reads the next frame, an ack.
func (c *fakeConn) ack() wire.Ack {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	f, err := c.fr.Read()
	if err != nil {
		c.t.Fatalf("reading an ack: %v", err)
	}
	var a wire.Ack
	if f.Event != wire.EventAck || f.Decode(&a) != nil {
		c.t.Fatalf("frame %s, want an ack", f.Event)
	}
	return a
}

// until reads acks until one in state want (for command id).
func (c *fakeConn) until(id string, want wire.AckState) wire.Ack {
	c.t.Helper()
	for {
		a := c.ack()
		if a.CommandID == id && a.State == want {
			return a
		}
	}
}

func agentOf(t *testing.T, a *core.Agent) (name, hash string) {
	t.Helper()
	r, err := NewRunner(wire.Build{Digest: digest}, []AgentSpec{{Agent: a}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	return a.Name(), r.Registration("local").Agents[0].ManifestHash
}

func start(t *testing.T, a *core.Agent, id, prompt string) wire.Command {
	name, hash := agentOf(t, a)
	return wire.Command{CommandID: id, Kind: wire.KindStart, RunID: "run_" + id[4:], Agent: name, AgentVersion: "agv_1",
		BuildDigest: digest, ManifestHash: hash, TenantID: "ten_project", Input: &wire.Input{Prompt: prompt}}
}

// serve runs ServeContext in the background until the test ends.
func serve(t *testing.T, s *fakeServer, opts ...Option) (cancel func(), done chan error) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	done = make(chan error, 1)
	opts = append([]Option{Socket(s.path), Token("tok"), BuildDigest(digest), Drain(200 * time.Millisecond)}, opts...)
	finished := make(chan struct{})
	go func() { done <- ServeContext(ctx, opts...); close(finished) }()
	t.Cleanup(func() {
		cancelCtx()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return cancelCtx, done
}

// modelCalls counts model calls ahead of a scripted model.
type modelCalls struct {
	core.Model
	n atomic.Int32
}

func (m *modelCalls) Stream(ctx context.Context, req core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	m.n.Add(1)
	return m.Model.Stream(ctx, req)
}

func TestRegisterAckBeforeRun(t *testing.T) {
	s := newFakeServer(t)
	model := &modelCalls{Model: wefttest.Script(wefttest.Say("hello"))}
	a := core.New(model, core.Name("support"))
	serve(t, s, Agent(a, Concurrency(3)))
	c := s.accept()
	reg := c.hello.Registration
	if c.hello.Token != "tok" || reg.Build.Digest != digest || reg.Placement != wire.PlacementLocal || len(reg.Agents) != 1 ||
		reg.Agents[0].Name != "support" || reg.Agents[0].Concurrency != 3 || reg.WorkerID != "" {
		t.Fatalf("hello = %+v", c.hello)
	}
	cmd := start(t, a, wire.NewCommandID(), "hi")
	c.run(cmd)
	acc := c.ack()
	if acc.State != wire.AckAccepted || acc.CommandID != cmd.CommandID {
		t.Fatalf("first ack = %+v", acc)
	}
	if st := c.ack(); st.State != wire.AckRunStarted || st.RunID != cmd.RunID {
		t.Fatalf("second ack = %+v", st)
	}
	fin := c.ack()
	if fin.State != wire.AckFinished || fin.Status != wire.StatusSucceeded || fin.Usage == nil || fin.Usage.OutputTokens == 0 {
		t.Fatalf("finished = %+v", fin)
	}
	if model.n.Load() != 1 {
		t.Errorf("model calls = %d", model.n.Load())
	}
}

func TestAtMostOnce(t *testing.T) {
	s := newFakeServer(t)
	model := &modelCalls{Model: wefttest.Script(wefttest.Say("once"), wefttest.Say("twice"))}
	a := core.New(model, core.Name("support"))
	serve(t, s, Agent(a))
	c := s.accept()
	cmd := start(t, a, wire.NewCommandID(), "hi")
	c.run(cmd)
	c.until(cmd.CommandID, wire.AckFinished)
	c.run(cmd) // replayed
	if again := c.ack(); again.State != wire.AckRejected || !strings.Contains(again.Error, "seen before") {
		t.Fatalf("replayed command = %+v", again)
	}
	if model.n.Load() != 1 {
		t.Errorf("model calls = %d, want 1", model.n.Load())
	}
	// A version the build does not serve is rejected before it runs.
	other := start(t, a, wire.NewCommandID(), "x")
	other.ManifestHash = "beef"
	c.run(other)
	if r := c.ack(); r.State != wire.AckRejected || !strings.Contains(r.Error, "not served here") {
		t.Fatalf("foreign version = %+v", r)
	}
	// An undecodable run frame is answered rejected, once.
	id := wire.NewCommandID()
	c.send(wire.EventRun, id, map[string]any{"kind": 7})
	if r := c.ack(); r.State != wire.AckRejected || r.CommandID != id {
		t.Fatalf("undecodable = %+v", r)
	}
}

func TestReconnectRegistersAgain(t *testing.T) {
	s := newFakeServer(t)
	a := core.New(wefttest.Script(wefttest.Say("a")), core.Name("support"))
	serve(t, s, Agent(a))
	first := s.accept()
	_ = first.conn.Close()
	second := s.accept()
	if second.hello.Registration.WorkerID != "wk_1" {
		t.Fatalf("the reconnect registered as %q, want the id it was given (wk_1)", second.hello.Registration.WorkerID)
	}
}

// TestIdleWatchdog: a server that stops pinging is a dead link: the
// worker drops it after three ping intervals and redials.
func TestIdleWatchdog(t *testing.T) {
	s := newFakeServer(t)
	s.pingMS = 30
	a := core.New(wefttest.Script(wefttest.Say("a")), core.Name("support"))
	serve(t, s, Agent(a))
	first := s.accept()
	_ = first.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.fr.Read(); err == nil {
		t.Fatal("the silent link was not dropped")
	}
	s.accept() // redialled
}

func TestPingAndCancel(t *testing.T) {
	s := newFakeServer(t)
	started := make(chan struct{}, 1)
	wait := core.Tool("wait", "Wait.", func(ctx context.Context, _ struct{}) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	a := core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("never")), core.Name("slow"), wait)
	serve(t, s, Agent(a))
	c := s.accept()
	c.send(wire.EventPing, "", nil)
	cmd := start(t, a, wire.NewCommandID(), "go")
	c.run(cmd)
	c.until(cmd.CommandID, wire.AckRunStarted)
	<-started
	c.send(wire.EventCancel, "", wire.Cancel{CommandID: wire.NewCommandID(), RunID: cmd.RunID, Cause: wire.CauseUser})
	fin := c.until(cmd.CommandID, wire.AckFinished)
	if fin.Status != wire.StatusCanceled || fin.Cause != wire.CauseUser {
		t.Fatalf("finished = %+v", fin)
	}
}

// TestStopDrainsAndCancels: the build's stop (SIGTERM, here its ctx)
// takes no more commands, lets a running run reach the drain deadline,
// cancels it (cause shutdown), writes its finished ack and returns.
func TestStopDrainsAndCancels(t *testing.T) {
	s := newFakeServer(t)
	started := make(chan struct{}, 1)
	wait := core.Tool("wait", "Wait.", func(ctx context.Context, _ struct{}) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	a := core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("never")), core.Name("slow"), wait)
	stop, done := serve(t, s, Agent(a))
	c := s.accept()
	cmd := start(t, a, wire.NewCommandID(), "go")
	c.run(cmd)
	c.until(cmd.CommandID, wire.AckRunStarted)
	<-started
	stop()
	busy := start(t, a, wire.NewCommandID(), "late")
	c.run(busy)
	var fin wire.Ack
	for fin.State != wire.AckFinished {
		a := c.ack()
		if a.CommandID == busy.CommandID && a.State != wire.AckBusy {
			t.Errorf("a command during the drain = %+v, want busy", a)
		}
		if a.CommandID == cmd.CommandID {
			fin = a
		}
	}
	if fin.Status != wire.StatusCanceled || fin.Cause != wire.CauseShutdown {
		t.Fatalf("finished = %+v", fin)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the drain")
	}
}

// TestDrainFrame: a drain frame (a new version, a retired build) lets
// the run finish and ends Serve with nil.
func TestDrainFrame(t *testing.T) {
	s := newFakeServer(t)
	a := core.New(wefttest.Script(wefttest.Say("done")), core.Name("support"))
	_, done := serve(t, s, Agent(a))
	c := s.accept()
	cmd := start(t, a, wire.NewCommandID(), "hi")
	c.run(cmd)
	c.until(cmd.CommandID, wire.AckFinished)
	c.send(wire.EventDrain, "", wire.Drain{DeadlineMS: 1000})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after a drain frame")
	}
}

func TestRefusedRegistrationStops(t *testing.T) {
	s := newFakeServer(t)
	s.refuse = "token refused"
	a := core.New(wefttest.Script(wefttest.Say("a")), core.Name("support"))
	_, done := serve(t, s, Agent(a))
	s.accept()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "token refused") {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve kept going after a refusal")
	}
}

func TestOrphanedBuildExits(t *testing.T) {
	a := core.New(wefttest.Script(wefttest.Say("a")), core.Name("support"))
	done := make(chan error, 1)
	go func() {
		done <- ServeContext(context.Background(), Socket(socketPath(t)), BuildDigest(digest), Agent(a), MaxDisconnected(time.Millisecond))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOrphaned) {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an orphaned build kept redialling")
	}
}

func TestFromEnv(t *testing.T) {
	a := core.New(wefttest.Script(wefttest.Say("a")), core.Name("support"))
	t.Setenv(EnvSocket, "")
	t.Setenv(EnvURL, "")
	if err := ServeContext(context.Background(), FromEnv(), Agent(a)); err == nil || !strings.Contains(err.Error(), "WEFTBASE_SOCKET") {
		t.Errorf("no socket: %v", err)
	}
	t.Setenv(EnvURL, "http://x")
	if err := ServeContext(context.Background(), FromEnv(), Agent(a)); err == nil || !strings.Contains(err.Error(), "remote worker link") {
		t.Errorf("a URL only: %v", err)
	}
	t.Setenv(EnvSocket, "/nowhere.sock")
	t.Setenv(EnvToken, "tk")
	t.Setenv(EnvBuildDigest, digest)
	c := newConfig([]Option{FromEnv()})
	if c.err != nil || c.socket != "/nowhere.sock" || c.token != "tk" || c.digest != digest || c.placement != wire.PlacementLocal {
		t.Errorf("config = %+v", c)
	}
}

func TestSteerReachesTheRun(t *testing.T) {
	s := newFakeServer(t)
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	wait := core.Tool("wait", "Wait.", func(ctx context.Context, _ struct{}) (string, error) {
		started <- struct{}{}
		<-gate
		return "ok", nil
	})
	model := wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("steered"))
	a := core.New(model, core.Name("slow"), wait)
	serve(t, s, Agent(a))
	c := s.accept()
	cmd := start(t, a, wire.NewCommandID(), "go")
	c.run(cmd)
	c.until(cmd.CommandID, wire.AckRunStarted)
	<-started
	steer := wire.Steer{CommandID: wire.NewCommandID(), RunID: cmd.RunID,
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"only EU orders"}]}`)}}
	c.send(wire.EventSteer, steer.CommandID, steer)
	if a := c.until(steer.CommandID, wire.AckAccepted); a.RunID != cmd.RunID {
		t.Fatalf("steer ack = %+v", a)
	}
	bad := wire.Steer{CommandID: wire.NewCommandID(), RunID: "run_elsewhere", Messages: steer.Messages}
	c.send(wire.EventSteer, bad.CommandID, bad)
	if a := c.until(bad.CommandID, wire.AckRejected); !strings.Contains(a.Error, "not running here") {
		t.Fatalf("steer of a run not here = %+v", a)
	}
	close(gate)
	if fin := c.until(cmd.CommandID, wire.AckFinished); fin.Status != wire.StatusSucceeded {
		t.Fatalf("finished = %+v", fin)
	}
	if got := model.LastRequest().LastText(); got != "only EU orders" {
		t.Errorf("the model's last user text = %q, want the steer", got)
	}
}

func newRunner(t *testing.T, opts []RunnerOption, specs ...AgentSpec) *Runner {
	t.Helper()
	r, err := NewRunner(wire.Build{Digest: digest}, specs, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func nextAck(t *testing.T, r *Runner) wire.Ack {
	t.Helper()
	select {
	case a := <-r.Acks():
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("no ack")
		return wire.Ack{}
	}
}

func TestSeenIsBounded(t *testing.T) {
	r := newRunner(t, nil, Spec(core.New(wefttest.Script(), core.Name("a"))))
	ids := make([]string, SeenSize+1)
	r.mu.Lock()
	for i := range ids {
		ids[i] = wire.NewCommandID()
		r.markSeen(ids[i])
	}
	if len(r.seen) != SeenSize || r.seen[ids[0]] || !r.seen[ids[SeenSize]] {
		t.Errorf("seen holds %d ids; oldest kept %v", len(r.seen), r.seen[ids[0]])
	}
	if r.markSeen(ids[SeenSize]) {
		t.Error("a repeated id inside the window was admitted")
	}
	r.mu.Unlock()
}

// panicModel panics in Stream: outside any tool, so weft does not turn
// it into a tool error.
type panicModel struct{ core.Model }

func (panicModel) Stream(context.Context, core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	panic("kaboom")
}

func TestPanicIsTheCommandsFailure(t *testing.T) {
	a := core.New(panicModel{wefttest.Script()}, core.Name("p"))
	r := newRunner(t, nil, Spec(a))
	cmd := start(t, a, wire.NewCommandID(), "go")
	if ack := r.Dispatch(cmd); ack.State != wire.AckAccepted {
		t.Fatalf("dispatch = %+v", ack)
	}
	for {
		a := nextAck(t, r)
		if a.State == wire.AckFinished {
			if a.Status != wire.StatusFailed || !strings.Contains(a.Error, "kaboom") {
				t.Fatalf("a panicking model = %+v", a)
			}
			return
		}
	}
}

func TestUndeliveredAcceptedAckRunsNothing(t *testing.T) {
	model := &modelCalls{Model: wefttest.Script(wefttest.Say("x"), wefttest.Say("y"))}
	a := core.New(model, core.Name("a"))
	r := newRunner(t, nil, Spec(a, Concurrency(1)))
	cmd := start(t, a, wire.NewCommandID(), "go")
	ack, run := r.Admit(cmd)
	if ack.State != wire.AckAccepted || run == nil {
		t.Fatalf("admit = %+v", ack)
	}
	run(false)
	fin := nextAck(t, r)
	if fin.State != wire.AckFinished || fin.Status != wire.StatusFailed || !strings.Contains(fin.Error, "not run") {
		t.Fatalf("settle = %+v", fin)
	}
	if model.n.Load() != 0 || r.InFlight() != 0 {
		t.Errorf("model calls %d, in flight %d", model.n.Load(), r.InFlight())
	}
	// The slot it reserved is free again: under a concurrency of one
	// the next command runs.
	next := start(t, a, wire.NewCommandID(), "go")
	if ack := r.Dispatch(next); ack.State != wire.AckAccepted {
		t.Fatalf("next = %+v", ack)
	}
}

func TestBusyAtConcurrency(t *testing.T) {
	gate := make(chan struct{})
	wait := core.Tool("wait", "Wait.", func(context.Context, struct{}) (string, error) { <-gate; return "ok", nil })
	a := core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("x")), core.Name("a"), wait)
	r := newRunner(t, nil, Spec(a, Concurrency(1)))
	if ack := r.Dispatch(start(t, a, wire.NewCommandID(), "1")); ack.State != wire.AckAccepted {
		t.Fatalf("first = %+v", ack)
	}
	if ack := r.Dispatch(start(t, a, wire.NewCommandID(), "2")); ack.State != wire.AckBusy {
		t.Fatalf("second = %+v, want busy", ack)
	}
	close(gate)
}

func TestHeartbeats(t *testing.T) {
	gate := make(chan struct{})
	wait := core.Tool("wait", "Wait.", func(context.Context, struct{}) (string, error) { <-gate; return "ok", nil })
	a := core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("x")), core.Name("a"), wait)
	r := newRunner(t, []RunnerOption{WithHeartbeat(10 * time.Millisecond)}, Spec(a))
	cmd := start(t, a, wire.NewCommandID(), "go")
	r.Dispatch(cmd)
	beats := 0
	for beats < 2 {
		if a := nextAck(t, r); a.State == wire.AckHeartbeat && a.RunID == cmd.RunID {
			beats++
		}
	}
	close(gate)
	for nextAck(t, r).State != wire.AckFinished {
	}
	if reg := r.Registration("local"); reg.HeartbeatMS != 10 {
		t.Errorf("heartbeat_ms = %d", reg.HeartbeatMS)
	}
}

func TestBackoff(t *testing.T) {
	var b backoff
	for i, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		d := b.next()
		if d < want/2 || d > want {
			t.Errorf("wait %d = %v, want within [%v, %v]", i, d, want/2, want)
		}
	}
	b.reset()
	if d := b.next(); d > 500*time.Millisecond {
		t.Errorf("after reset = %v", d)
	}
}

func TestNewRunnerRefuses(t *testing.T) {
	a := core.New(wefttest.Script(), core.Name("a"))
	for name, err := range map[string]error{
		"no digest": func() error { _, err := NewRunner(wire.Build{Digest: "md5:x"}, []AgentSpec{Spec(a)}); return err }(),
		"no agent":  func() error { _, err := NewRunner(wire.Build{Digest: digest}, nil); return err }(),
		"no name": func() error {
			_, err := NewRunner(wire.Build{Digest: digest}, []AgentSpec{Spec(core.New(wefttest.Script()))})
			return err
		}(),
		"duplicates": func() error {
			_, err := NewRunner(wire.Build{Digest: digest}, []AgentSpec{Spec(a), Spec(a)})
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
