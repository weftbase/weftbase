package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	buildworker "github.com/weftbase/weftbase/client/worker"
)

// The fixture build (testdata/refunds), built twice: A and B differ
// only in -X main.build, so they are two digests of one manifest.
var binA, binB string

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeChildEnv); mode != "" {
		fakeChild(mode) // this binary, started by an executor as its build
		return
	}
	dir, err := os.MkdirTemp("", "wb-local")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binA, binB = filepath.Join(dir, "refunds-a"), filepath.Join(dir, "refunds-b")
	for bin, tag := range map[string]string{binA: "A", binB: "B"} {
		if err := BuildFixture(bin, tag, "./testdata/refunds"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// BuildFixture builds the fixture package pkg to out with main.build
// set to tag.
func BuildFixture(out, tag, pkg string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("the go tool builds the fixture: %w", err)
	}
	cmd := exec.Command(gobin, "build", "-o", out, "-ldflags", "-X main.build="+tag, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building the fixture: %v\n%s", err, b)
	}
	return nil
}

func newExec(t *testing.T, c Config) *Executor {
	t.Helper()
	if c.Output == nil {
		c.Output = io.Discard
	}
	c.SocketDir = t.TempDir()
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.Drain(context.Background(), time.Now().Add(time.Second))
		_ = e.Close()
	})
	return e
}

func command(reg wire.Registration, prompt string) wire.Command {
	a := reg.Agents[0]
	id := wire.NewCommandID()
	return wire.Command{CommandID: id, Kind: wire.KindStart, RunID: "run_" + id[4:], Agent: a.Name, AgentVersion: "agv_1",
		BuildDigest: reg.Build.Digest, ManifestHash: a.ManifestHash, TenantID: "ten_project", Input: &wire.Input{Prompt: prompt}}
}

// next reads frames from the executor until one matches.
func next(t *testing.T, e *Executor, match func(wire.Frame) bool) wire.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for f, err := range e.Events(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if match(f) {
			return f
		}
	}
	t.Fatal("no such frame")
	return wire.Frame{}
}

func finished(t *testing.T, e *Executor, cmdID string) wire.Ack {
	t.Helper()
	var a wire.Ack
	next(t, e, func(f wire.Frame) bool {
		if f.Event != wire.EventAck || f.Decode(&a) != nil {
			return false
		}
		return a.CommandID == cmdID && a.State == wire.AckFinished
	})
	return a
}

func TestRunOnAChild(t *testing.T) {
	e := newExec(t, Config{Path: binA})
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := buildworker.FileDigest(binA)
	if reg.Placement != wire.PlacementLocal || reg.Build.Digest != digest || reg.Build.Kind != wire.BuildBinary ||
		len(reg.Agents) != 1 || reg.Agents[0].Name != "refunds" || !e.Running() {
		t.Fatalf("registration = %+v", reg)
	}
	cmd := command(reg, "hello")
	ack, err := e.Dispatch(context.Background(), cmd)
	if err != nil || ack.State != wire.AckAccepted {
		t.Fatalf("dispatch = %+v, %v", ack, err)
	}
	if fin := finished(t, e, cmd.CommandID); fin.Status != wire.StatusSucceeded {
		t.Fatalf("finished = %+v", fin)
	}
	// A version the build does not serve is rejected by the child.
	other := command(reg, "hello")
	other.ManifestHash = "beef"
	if ack, err := e.Dispatch(context.Background(), other); err != nil || ack.State != wire.AckRejected {
		t.Errorf("a foreign version = %+v, %v", ack, err)
	}
}

func TestParkCancelAndSteer(t *testing.T) {
	e := newExec(t, Config{Path: binA})
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	park := command(reg, "refund 42")
	if ack, _ := e.Dispatch(context.Background(), park); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	fin := finished(t, e, park.CommandID)
	if fin.Status != wire.StatusParked || len(fin.Pending) != 1 || fin.Pending[0].Tool != "refund" || len(fin.Checkpoint) == 0 {
		t.Fatalf("parked = %+v", fin)
	}

	wait := command(reg, "wait")
	if ack, _ := e.Dispatch(context.Background(), wait); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	next(t, e, func(f wire.Frame) bool {
		var a wire.Ack
		return f.Decode(&a) == nil && a.CommandID == wait.CommandID && a.State == wire.AckRunStarted
	})
	steer := wire.Steer{CommandID: wire.NewCommandID(), RunID: wait.RunID,
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"only EU"}]}`)}}
	if ack, err := e.Steer(context.Background(), steer); err != nil || ack.State != wire.AckAccepted {
		t.Fatalf("steer = %+v, %v", ack, err)
	}
	if fin := finished(t, e, wait.CommandID); fin.Status != wire.StatusSucceeded {
		t.Fatalf("the steered run = %+v", fin)
	}

	slow := command(reg, "wait")
	if ack, _ := e.Dispatch(context.Background(), slow); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	if err := e.Cancel(context.Background(), wire.Cancel{CommandID: wire.NewCommandID(), RunID: slow.RunID, Cause: wire.CauseUser}); err != nil {
		t.Fatal(err)
	}
	if fin := finished(t, e, slow.CommandID); fin.Status != wire.StatusCanceled || fin.Cause != wire.CauseUser {
		// The 300 ms wait may finish first on a slow machine.
		if fin.Status != wire.StatusSucceeded {
			t.Fatalf("the canceled run = %+v", fin)
		}
	}
}

// TestCrashLosesTheRunsAndRestarts: a child that dies ends the runs it
// held (a disconnected frame, lost) and is started again after its
// backoff.
func TestCrashLosesTheRunsAndRestarts(t *testing.T) {
	e := newExec(t, Config{Path: binA})
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	crash := command(reg, "crash")
	if ack, _ := e.Dispatch(context.Background(), crash); ack.State != wire.AckAccepted {
		t.Fatal(ack)
	}
	// The link's EOF can be reported first (lost false: the process may
	// live on and redial) when the exit is slow to be reaped; the exit's
	// own frame, lost, always follows.
	var d wire.Disconnected
	f := next(t, e, func(f wire.Frame) bool {
		return f.Event == wire.EventDisconnected && f.Decode(&d) == nil && d.Lost
	})
	if !strings.Contains(d.Reason, "exit status 3") {
		t.Fatalf("disconnected = %s", f.Data)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !e.Running() {
		if time.Now().After(deadline) {
			t.Fatal("the crashed build was not restarted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var ack wire.Ack
	for {
		cmd := command(reg, "hello")
		ack, err = e.Dispatch(context.Background(), cmd)
		if err != nil {
			t.Fatal(err)
		}
		if ack.State == wire.AckAccepted {
			finished(t, e, cmd.CommandID)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restarted build answers %+v", ack)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestKnownRegistrationIsLazy: an executor made from the registry's
// registration starts nothing until a command arrives, and refuses a
// child that registers other agents.
func TestKnownRegistrationIsLazy(t *testing.T) {
	probe := newExec(t, Config{Path: binA})
	reg, err := probe.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e := newExec(t, Config{Path: binA, Known: &reg})
	if got, err := e.Register(context.Background()); err != nil || got.Build.Digest != reg.Build.Digest || e.Running() {
		t.Fatalf("lazy register = %+v, %v; running %v", got, err, e.Running())
	}
	cmd := command(reg, "hello")
	if ack, err := e.Dispatch(context.Background(), cmd); err != nil || ack.State != wire.AckAccepted || !e.Running() {
		t.Fatalf("the first dispatch = %+v, %v", ack, err)
	}
	finished(t, e, cmd.CommandID)

	wrong := reg
	wrong.Agents = []wire.AgentRegistration{{Name: "refunds", Manifest: json.RawMessage(`{}`), ManifestHash: "other"}}
	bad := newExec(t, Config{Path: binA, Known: &wrong, RegisterTimeout: 2 * time.Second})
	if ack, err := bad.Dispatch(context.Background(), command(wrong, "hello")); err != nil || ack.State != wire.AckBusy {
		t.Errorf("a child that registers other agents: %+v, %v (want busy: never sent)", ack, err)
	}
	if _, err := New(Config{Path: binB, Known: &reg}); err == nil {
		t.Error("a known registration of another digest was accepted")
	}
}

func TestATokenIsRequired(t *testing.T) {
	e := newExec(t, Config{Path: binA})
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	f, _ := wire.NewFrame(wire.EventRegister, "", wire.Hello{Token: "wbt_guess", Registration: wire.Registration{
		Build: wire.Build{Digest: e.Digest()}, Agents: []wire.AgentRegistration{{Name: "x", Manifest: json.RawMessage(`{}`), ManifestHash: "h"}}}})
	if err := wire.WriteFrame(conn, f); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := wire.NewFrameReader(conn).Read()
	var w wire.Welcome
	if err != nil || got.Decode(&w) != nil || w.Error != "token refused" {
		t.Fatalf("welcome = %+v (%v)", w, err)
	}
}

// TestRetireThenStartOnDemand: a retired build drains and exits; a run
// pinned to it starts it again. A drained (shutting down) build starts
// nothing: commands are answered busy.
func TestRetireThenStartOnDemand(t *testing.T) {
	e := newExec(t, Config{Path: binA})
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Retire(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if e.Running() || !e.Retired() {
		t.Fatalf("after Retire: running %v, retired %v", e.Running(), e.Retired())
	}
	next(t, e, func(f wire.Frame) bool { return f.Event == wire.EventDisconnected })
	time.Sleep(700 * time.Millisecond) // a retired build is not restarted on its own
	if e.Running() {
		t.Fatal("a retired build restarted with no work")
	}
	cmd := command(reg, "hello")
	if ack, err := e.Dispatch(context.Background(), cmd); err != nil || ack.State != wire.AckAccepted {
		t.Fatalf("a pinned run's dispatch = %+v, %v", ack, err)
	}
	finished(t, e, cmd.CommandID)

	if err := e.Drain(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if ack, err := e.Dispatch(context.Background(), command(reg, "hello")); err != nil || ack.State != wire.AckBusy || e.Running() {
		t.Errorf("after Drain = %+v, %v; running %v", ack, err, e.Running())
	}
}
