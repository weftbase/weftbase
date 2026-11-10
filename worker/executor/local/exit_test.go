package local

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	buildworker "github.com/weftbase/weftbase/client/worker"
)

// fakeChildEnv starts the test binary as a raw worker-protocol child
// (TestMain): the value is its mode.
const fakeChildEnv = "WB_LOCAL_FAKE_CHILD"

// fakeChild registers over WEFTBASE_SOCKET, answers the first run
// command, and exits. Modes:
//   - "finish-exit": accepted, many heartbeats, finished, then exit at
//     once (frames still in the socket when the process is gone);
//   - "env": finished with Error = the WEFTBASE_TOKEN|WEFTBASE_URL it
//     was started with, then waits for the next command.
func fakeChild(mode string) {
	conn, err := net.Dial("unix", os.Getenv(buildworker.EnvSocket))
	if err != nil {
		os.Exit(10)
	}
	hello := wire.Hello{Token: os.Getenv(buildworker.EnvWorkerToken), Registration: wire.Registration{
		Build:  wire.Build{Digest: os.Getenv(buildworker.EnvBuildDigest)},
		Agents: []wire.AgentRegistration{{Name: "fake", Manifest: json.RawMessage(`{"name":"fake"}`), ManifestHash: "h1"}},
	}}
	f, _ := wire.NewFrame(wire.EventRegister, "", hello)
	if wire.WriteFrame(conn, f) != nil {
		os.Exit(11)
	}
	fr := wire.NewFrameReader(conn)
	ack := func(a wire.Ack) {
		if wire.WriteFrame(conn, wire.AckFrame(a)) != nil {
			os.Exit(12)
		}
	}
	for {
		f, err := fr.Read()
		if err != nil {
			os.Exit(13)
		}
		if f.Event == wire.EventDrain {
			os.Exit(0)
		}
		if f.Event != wire.EventRun {
			continue
		}
		var cmd wire.Command
		if f.Decode(&cmd) != nil {
			os.Exit(14)
		}
		ack(wire.Ack{CommandID: cmd.CommandID, State: wire.AckAccepted, RunID: cmd.RunID})
		switch mode {
		case "finish-exit":
			// More than fits the executor's event queue (1024), less
			// than would fill the socket: the child exits with frames
			// unread in the socket.
			for range 1100 {
				ack(wire.Ack{CommandID: cmd.CommandID, State: wire.AckHeartbeat, RunID: cmd.RunID})
			}
			ack(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID, Status: wire.StatusSucceeded})
			os.Exit(0)
		case "env":
			ack(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID, Status: wire.StatusSucceeded,
				Error: os.Getenv(buildworker.EnvToken) + "|" + os.Getenv(buildworker.EnvURL)})
		}
	}
}

func fakeExec(t *testing.T, mode string, c Config) (*Executor, wire.Registration) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c.Path = exe
	c.Env = append(c.Env, fakeChildEnv+"="+mode)
	e := newExec(t, c)
	reg, err := e.Register(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return e, reg
}

// TestExitDrainsTheLink: a child that writes its finished ack and exits
// at once still has the ack reach the hub, before the disconnected
// frame (lost) that ends the runs it held. The exit used to close the
// link before its reader had read what was in the socket, so a finished
// run was reported worker_lost.
func TestExitDrainsTheLink(t *testing.T) {
	e, reg := fakeExec(t, "finish-exit", Config{})
	cmd := command(reg, "hello")
	if ack, err := e.Dispatch(context.Background(), cmd); err != nil || ack.State != wire.AckAccepted {
		t.Fatalf("dispatch = %+v, %v", ack, err)
	}
	// Let the child exit with its frames still unread: the hub reads
	// nothing until the process is gone.
	e.mu.Lock()
	pid := e.proc.Process.Pid
	e.mu.Unlock()
	waitUntil(t, func() bool { return syscall.Kill(pid, 0) != nil }) // exited and reaped
	var gotFinished bool
	f := next(t, e, func(f wire.Frame) bool {
		var a wire.Ack
		if f.Event == wire.EventAck && f.Decode(&a) == nil && a.CommandID == cmd.CommandID && a.State == wire.AckFinished {
			gotFinished = true
		}
		return f.Event == wire.EventDisconnected
	})
	if !gotFinished {
		t.Fatalf("the disconnected frame (%s) came before the finished ack the child wrote", f.Data)
	}
}

// TestChildDoesNotInheritTheNodesCredential: WEFTBASE_TOKEN and
// WEFTBASE_URL in the node's own environment never reach a build; with
// Telemetry the child gets the node's pair, without it none.
func TestChildDoesNotInheritTheNodesCredential(t *testing.T) {
	t.Setenv(buildworker.EnvToken, "wb_sk_test_operators")
	t.Setenv(buildworker.EnvURL, "https://elsewhere.example")
	for _, c := range []struct {
		name string
		tel  func() (string, string)
		want string
	}{
		{"no telemetry", nil, "|"},
		{"telemetry", func() (string, string) { return "http://127.0.0.1:7331", "wb_wk_test_node" }, "wb_wk_test_node|http://127.0.0.1:7331"},
	} {
		e, reg := fakeExec(t, "env", Config{Telemetry: c.tel})
		cmd := command(reg, "hello")
		if ack, err := e.Dispatch(context.Background(), cmd); err != nil || ack.State != wire.AckAccepted {
			t.Fatalf("%s: dispatch = %+v, %v", c.name, ack, err)
		}
		a := finished(t, e, cmd.CommandID)
		if a.Error != c.want || strings.Contains(a.Error, "operators") {
			t.Errorf("%s: the child saw %q, want %q", c.name, a.Error, c.want)
		}
	}
}

// waitUntil polls cond (a state the test cannot be told about) for up
// to 15 s.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 15s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
