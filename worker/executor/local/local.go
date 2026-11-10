// Package local is the local placement (P§5.7, M§4.3), the default
// for builds: a supervised child process per build, talking the worker
// protocol over a Unix socket. The supervisor starts the build with
// WEFTBASE_SOCKET, WEFTBASE_WORKER_TOKEN (a per-child secret for the
// link), WEFTBASE_BUILD_DIGEST and WEFTBASE_PLACEMENT=local; the build
// (client/worker.Serve) dials back and registers. Telemetry is the
// node's to supply (Config.Telemetry): WEFTBASE_URL and WEFTBASE_TOKEN,
// an ingest key, so the build's exporter.Install() reports to the node
// that supervises it.
//
// One Executor is one build. It implements worker.Executor: the hub
// dispatches to it like to any placement. The child is started on
// demand (lazily: an executor made from a known registration starts
// nothing until a command arrives), restarted with backoff when it
// crashes, and drained on Retire — a newer build of the same agents
// took over — after which it starts again only when a run pinned to
// this build needs it. A crash ends the runs the child held: Events
// yields a disconnected frame with lost set, and the hub marks them
// worker_lost.
//
// A child is not a security boundary: it runs as the node's user,
// with its filesystem and network.
package local

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	mrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	buildworker "github.com/weftbase/weftbase/client/worker"
	"github.com/weftbase/weftbase/worker"
)

// Defaults (P§6.1).
const (
	DefaultPing            = 15 * time.Second
	DefaultAckTimeout      = 30 * time.Second
	DefaultRegisterTimeout = 15 * time.Second
	// healthy: a child that stayed up this long resets the restart
	// backoff.
	healthy = 10 * time.Second
	// killGrace: past a drain's deadline, how long the child gets to
	// exit on its own before it is killed.
	killGrace = 5 * time.Second
	// dropGrace: a link that drops while the process lives is reported
	// (lost false) only if the process is still up after this.
	dropGrace = 300 * time.Millisecond
)

// ErrClosed is the error of an executor after Close.
var ErrClosed = errors.New("local: executor closed")

// Config is one build to supervise.
type Config struct {
	// Path is the build's executable. The kernel passes the copy in its
	// build store, so the bytes cannot change under a digest.
	Path string
	// Args are extra arguments.
	Args []string
	// Env are extra environment variables (KEY=VALUE) for the child,
	// after the node's own and before the WEFTBASE_* ones.
	Env []string
	// Telemetry, when set, is asked at every start of the child for
	// where its traces and logs go: the node's base URL and an API key
	// that may ingest. Both non-empty: the child gets WEFTBASE_URL and
	// WEFTBASE_TOKEN. The node's own values of these (and of every
	// WEFTBASE_* the supervisor sets) are never inherited.
	Telemetry func() (url, key string)
	// Digest is the build's digest; default sha256 of Path.
	Digest string
	// SocketDir is where the socket is made (default os.TempDir()). A
	// Unix socket path is limited to about 100 bytes: a longer one
	// falls back to the default.
	SocketDir string
	// Known is the build's registration as the registry has it. With
	// it the executor registers without starting the child; the child
	// starts on the first command and must register the same agents.
	Known *wire.Registration
	// Ping, AckTimeout and RegisterTimeout are the link's bounds
	// (default 15 s, 30 s, 15 s).
	Ping, AckTimeout, RegisterTimeout time.Duration
	// Output takes the child's stdout and stderr (default os.Stderr).
	Output io.Writer
	Logger *slog.Logger
}

// Executor supervises one build's child.
type Executor struct {
	c        Config
	token    string
	sock     string
	ln       net.Listener
	workerID string

	events chan wire.Frame
	base   context.Context
	stop   context.CancelFunc

	mu         sync.Mutex
	reg        *wire.Registration
	link       *link
	linkUp     chan struct{} // closed when a link registers; replaced after
	proc       *exec.Cmd
	procDone   chan struct{}
	exitErr    error
	restarts   int
	restartAt  time.Time
	draining   bool // shutting down: never restart
	retired    bool // drained for a newer build: restart on demand only
	waiters    map[string]chan wire.Ack
	expectExit bool
	wg         sync.WaitGroup
}

// link is one registered connection from the child.
type link struct {
	conn net.Conn
	wmu  sync.Mutex
	done chan struct{}
	// reads counts the frames read; handing is set while the reader
	// waits for the hub to take one (awaitReader).
	reads   atomic.Int64
	handing atomic.Bool
}

func (l *link) write(f wire.Frame) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	_ = l.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return wire.WriteFrame(l.conn, f)
}

var _ worker.Executor = (*Executor)(nil)

// New makes the executor and its socket. Nothing is started until
// Register (no Known registration) or the first command.
func New(c Config) (*Executor, error) {
	if c.Path == "" {
		return nil, errors.New("local: a build needs its executable's path")
	}
	if c.Digest == "" {
		d, err := buildworker.FileDigest(c.Path)
		if err != nil {
			return nil, fmt.Errorf("local: digest of %s: %w", c.Path, err)
		}
		c.Digest = d
	}
	if c.Ping <= 0 {
		c.Ping = DefaultPing
	}
	if c.AckTimeout <= 0 {
		c.AckTimeout = DefaultAckTimeout
	}
	if c.RegisterTimeout <= 0 {
		c.RegisterTimeout = DefaultRegisterTimeout
	}
	if c.Output == nil {
		c.Output = os.Stderr
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	e := &Executor{c: c, events: make(chan wire.Frame, 1024), waiters: map[string]chan wire.Ack{}, linkUp: make(chan struct{})}
	if c.Known != nil {
		k := *c.Known
		if k.Build.Digest != c.Digest {
			return nil, fmt.Errorf("local: the known registration is build %s, the executable %s", k.Build.Digest, c.Digest)
		}
		k.Placement = wire.PlacementLocal
		e.reg = &k
	}
	e.token = "wbt_" + randHex(24)
	e.workerID = "wk_" + randHex(13)
	sock, err := socketPath(c.SocketDir, c.Digest)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("local: socket: %w", err)
	}
	_ = os.Chmod(sock, 0o600)
	e.sock, e.ln = sock, ln
	e.base, e.stop = context.WithCancel(context.Background())
	e.wg.Add(1)
	go e.accept()
	return e, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// socketPath is a fresh, short socket path for the build.
func socketPath(dir, digest string) (string, error) {
	hexd := strings.TrimPrefix(digest, "sha256:")
	if len(hexd) > 12 {
		hexd = hexd[:12]
	}
	name := fmt.Sprintf("wb-%s-%s.sock", hexd, randHex(3))
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("local: socket dir: %w", err)
		}
		if p := filepath.Join(dir, name); len(p) < 100 {
			return p, nil
		}
	}
	return filepath.Join(os.TempDir(), name), nil
}

// Digest is the build's digest.
func (e *Executor) Digest() string { return e.c.Digest }

// Running reports whether the child process is up.
func (e *Executor) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proc != nil
}

// Register returns the build's registration: the known one, or the one
// the child registers (starting it).
func (e *Executor) Register(ctx context.Context) (wire.Registration, error) {
	e.mu.Lock()
	reg := e.reg
	e.mu.Unlock()
	if reg != nil {
		r := *reg
		r.WorkerID = ""
		return r, nil
	}
	if _, err := e.ensure(ctx); err != nil {
		return wire.Registration{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reg == nil {
		return wire.Registration{}, errors.New("local: the child registered nothing")
	}
	r := *e.reg
	r.WorkerID = ""
	return r, nil
}

// ensure returns the live link, starting the child when none is up,
// and waits up to RegisterTimeout for it to register.
func (e *Executor) ensure(ctx context.Context) (*link, error) {
	e.mu.Lock()
	if e.base.Err() != nil {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	if e.link != nil {
		l := e.link
		e.mu.Unlock()
		return l, nil
	}
	if e.draining {
		e.mu.Unlock()
		return nil, errors.New("local: the build is shutting down")
	}
	if e.proc == nil {
		if wait := time.Until(e.restartAt); wait > 0 {
			e.mu.Unlock()
			return nil, fmt.Errorf("local: the build is restarting (backoff %s)", wait.Round(time.Millisecond))
		}
		if err := e.startLocked(); err != nil {
			e.mu.Unlock()
			return nil, err
		}
	}
	up, done := e.linkUp, e.procDone
	e.mu.Unlock()
	t := time.NewTimer(e.c.RegisterTimeout)
	defer t.Stop()
	select {
	case <-up:
	case <-done:
		e.mu.Lock()
		err := e.exitErr
		e.mu.Unlock()
		return nil, fmt.Errorf("local: the build exited before it registered: %v", err)
	case <-t.C:
		return nil, fmt.Errorf("local: the build did not register within %s", e.c.RegisterTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.base.Done():
		return nil, ErrClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.link == nil {
		return nil, errors.New("local: the build's link dropped")
	}
	return e.link, nil
}

// supervisorEnv are the variables the supervisor sets for a child. The
// node's own values of them are never inherited: a node that itself
// runs with WEFTBASE_TOKEN (a key of another node, or an operator's)
// must not hand that credential to its builds; with no Telemetry the
// child gets no WEFTBASE_URL/WEFTBASE_TOKEN at all.
var supervisorEnv = []string{
	buildworker.EnvURL, buildworker.EnvToken, buildworker.EnvSocket,
	buildworker.EnvWorkerToken, buildworker.EnvBuildDigest, buildworker.EnvPlacement,
}

// nodeEnv is the node's environment without supervisorEnv.
func nodeEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(supervisorEnv, k) {
			out = append(out, kv)
		}
	}
	return out
}

// startLocked starts the child. e.mu is held.
func (e *Executor) startLocked() error {
	cmd := exec.Command(e.c.Path, e.c.Args...)
	cmd.Env = append(nodeEnv(), e.c.Env...)
	if e.c.Telemetry != nil {
		if url, key := e.c.Telemetry(); url != "" && key != "" {
			cmd.Env = append(cmd.Env, buildworker.EnvURL+"="+url, buildworker.EnvToken+"="+key)
		}
	}
	cmd.Env = append(cmd.Env,
		buildworker.EnvSocket+"="+e.sock,
		buildworker.EnvWorkerToken+"="+e.token,
		buildworker.EnvBuildDigest+"="+e.c.Digest,
		buildworker.EnvPlacement+"="+wire.PlacementLocal,
	)
	cmd.Stdout, cmd.Stderr = e.c.Output, e.c.Output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("local: start %s: %w", e.c.Path, err)
	}
	done := make(chan struct{})
	e.proc, e.procDone, e.exitErr, e.expectExit = cmd, done, nil, false
	started := time.Now()
	e.c.Logger.Debug("local: build started", "digest", e.c.Digest, "pid", cmd.Process.Pid)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		err := cmd.Wait()
		e.exited(cmd, done, err, time.Since(started))
	}()
	return nil
}

// exited records the child's end: the runs it held are lost; a crash
// of a current build restarts it after a backoff. The link's reader is
// drained to EOF first: frames the child wrote just before it exited (a
// finished ack) are still in the socket, and they must reach the hub
// before the disconnected frame that loses the runs still held.
func (e *Executor) exited(cmd *exec.Cmd, done chan struct{}, err error, upFor time.Duration) {
	e.mu.Lock()
	if e.proc != cmd {
		e.mu.Unlock()
		return
	}
	l := e.link
	e.mu.Unlock()
	if l != nil {
		e.awaitReader(l)
	}
	e.mu.Lock()
	e.proc, e.exitErr = nil, err
	close(done)
	expected := e.expectExit || e.draining || e.base.Err() != nil
	if upFor >= healthy {
		e.restarts = 0
	}
	restart := !expected && !e.retired
	if !expected {
		d := backoff(e.restarts)
		e.restarts++
		e.restartAt = time.Now().Add(d)
	}
	l = e.link
	e.link = nil
	e.failWaitersLocked()
	e.mu.Unlock()
	if l != nil {
		_ = l.conn.Close()
	}
	reason := "the build exited"
	if err != nil {
		reason += ": " + err.Error()
	}
	if expected {
		reason = "the build drained and exited"
	}
	e.emit(wire.EventDisconnected, wire.Disconnected{Reason: reason, Lost: true})
	if restart {
		e.c.Logger.Warn("local: the build exited; restarting", "digest", e.c.Digest, "err", err, "after", time.Until(e.restartAt).Round(time.Millisecond))
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			select {
			case <-time.After(time.Until(e.restartAt)):
			case <-e.base.Done():
				return
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.proc == nil && !e.draining && !e.retired && e.base.Err() == nil {
				if err := e.startLocked(); err != nil {
					e.c.Logger.Warn("local: restart failed", "digest", e.c.Digest, "err", err)
				}
			}
		}()
	}
}

// backoff is 0.5 s doubling to 30 s, jittered over its upper half.
func backoff(n int) time.Duration {
	d := 30 * time.Second
	if n < 6 {
		d = 500 * time.Millisecond * (1 << n)
	}
	return d/2 + mrand.N(d/2+1)
}

// exitDrain is how long an exited child's link may go without the
// reader making progress before it is closed (a descendant that
// inherited the socket could hold it open past the exit).
const exitDrain = 2 * time.Second

// awaitReader waits for l's reader to read what the exited child left
// in the socket and end (dropped closes l.done). A reader waiting on
// the hub to take a frame is progress, however long; a reader that read
// nothing for exitDrain while not handing a frame over is waiting on a
// socket no exited process will write, and the connection is closed.
func (e *Executor) awaitReader(l *link) {
	t := time.NewTicker(exitDrain)
	defer t.Stop()
	last := l.reads.Load()
	for {
		select {
		case <-l.done:
			return
		case <-e.base.Done():
			_ = l.conn.Close()
			return
		case <-t.C:
			if n := l.reads.Load(); n != last || l.handing.Load() {
				last = n
				continue
			}
			e.c.Logger.Warn("local: an exited build's link stayed open; closing it", "digest", e.c.Digest)
			_ = l.conn.Close()
			return
		}
	}
}

func (e *Executor) failWaitersLocked() {
	for id, ch := range e.waiters {
		close(ch)
		delete(e.waiters, id)
	}
}

func (e *Executor) emit(event string, v any) {
	f, err := wire.NewFrame(event, "", v)
	if err != nil {
		return
	}
	select {
	case e.events <- f:
	case <-e.base.Done():
	}
}

// accept takes the child's connections: a register frame with the
// child's token and this build's digest (and, when the registration
// was known, the same agents) is welcomed; anything else is refused.
func (e *Executor) accept() {
	defer e.wg.Done()
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			return
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.serve(conn)
		}()
	}
}

func (e *Executor) refuse(conn net.Conn, why string) {
	f, _ := wire.NewFrame(wire.EventWelcome, "", wire.Welcome{Error: why})
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = wire.WriteFrame(conn, f)
	_ = conn.Close()
	e.c.Logger.Warn("local: a registration was refused", "digest", e.c.Digest, "why", why)
}

func (e *Executor) serve(conn net.Conn) {
	fr := wire.NewFrameReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(e.c.RegisterTimeout))
	f, err := fr.Read()
	var hello wire.Hello
	if err != nil || f.Event != wire.EventRegister || f.Decode(&hello) != nil {
		_ = conn.Close()
		return
	}
	if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(e.token)) != 1 {
		e.refuse(conn, "token refused")
		return
	}
	reg := hello.Registration
	reg.Placement = wire.PlacementLocal
	for i, a := range reg.Agents {
		reg.Agents[i].Manifest = wire.RestoreManifest(a.Manifest, a.ManifestHash)
	}
	if err := reg.Validate(); err != nil {
		e.refuse(conn, err.Error())
		return
	}
	if reg.Build.Digest != e.c.Digest {
		e.refuse(conn, fmt.Sprintf("this socket serves build %s, not %s", e.c.Digest, reg.Build.Digest))
		return
	}
	e.mu.Lock()
	if e.reg != nil && !sameAgents(*e.reg, reg) {
		e.mu.Unlock()
		e.refuse(conn, "the build registered other agents than its recorded ones")
		return
	}
	if e.reg == nil {
		r := reg
		e.reg = &r
	}
	e.mu.Unlock()
	// The welcome is the link's first frame: it is written before the
	// link is published, so no dispatch can put a run frame ahead of it
	// (the child drops a link whose first frame is not the welcome).
	l := &link{conn: conn, done: make(chan struct{})}
	if beforeWelcome != nil {
		beforeWelcome(e)
	}
	w, _ := wire.NewFrame(wire.EventWelcome, "", wire.Welcome{WorkerID: e.workerID, PingMS: e.c.Ping.Milliseconds()})
	if err := l.write(w); err != nil {
		close(l.done)
		_ = conn.Close()
		return
	}
	e.mu.Lock()
	old := e.link
	e.link = l
	close(e.linkUp)
	e.linkUp = make(chan struct{})
	e.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	go e.ping(l)
	_ = conn.SetReadDeadline(time.Time{})
	for {
		f, err := fr.Read()
		if err != nil {
			e.dropped(l, err)
			return
		}
		l.reads.Add(1)
		if f.Event != wire.EventAck {
			continue
		}
		var a wire.Ack
		if f.Decode(&a) != nil {
			continue
		}
		e.mu.Lock()
		ch := e.waiters[a.CommandID]
		if ch != nil {
			delete(e.waiters, a.CommandID)
		}
		e.mu.Unlock()
		if ch != nil {
			ch <- a
			continue
		}
		l.handing.Store(true)
		select {
		case e.events <- wire.AckFrame(a):
			l.handing.Store(false)
		case <-e.base.Done():
			return
		}
	}
}

// beforeWelcome, when set (tests only), runs just before a link's
// welcome is written.
var beforeWelcome func(*Executor)

func sameAgents(a, b wire.Registration) bool {
	if len(a.Agents) != len(b.Agents) {
		return false
	}
	want := map[string]string{}
	for _, x := range a.Agents {
		want[x.Name] = x.ManifestHash
	}
	for _, x := range b.Agents {
		if want[x.Name] != x.ManifestHash {
			return false
		}
	}
	return true
}

func (e *Executor) ping(l *link) {
	t := time.NewTicker(e.c.Ping)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-e.base.Done():
			return
		case <-t.C:
			if l.write(wire.Frame{Event: wire.EventPing}) != nil {
				return
			}
		}
	}
}

// dropped ends a link. When the process lives on (it will redial),
// worker.disconnected is reported without losing its runs; when it
// exited, exited reports.
func (e *Executor) dropped(l *link, err error) {
	close(l.done)
	_ = l.conn.Close()
	e.mu.Lock()
	if e.link != l {
		e.mu.Unlock()
		return
	}
	e.link = nil
	e.failWaitersLocked()
	done := e.procDone
	e.mu.Unlock()
	go func() {
		select {
		case <-done:
			return // the exit reports, lost
		case <-time.After(dropGrace):
		case <-e.base.Done():
			return
		}
		e.emit(wire.EventDisconnected, wire.Disconnected{Reason: "the build's link dropped: " + err.Error()})
	}()
}

// roundTrip writes a frame that is answered by an ack with its command
// id, and waits for that answer. sent reports whether the frame left.
func (e *Executor) roundTrip(ctx context.Context, l *link, f wire.Frame, id string) (a wire.Ack, sent bool, err error) {
	ch := make(chan wire.Ack, 1)
	e.mu.Lock()
	e.waiters[id] = ch
	e.mu.Unlock()
	if err := l.write(f); err != nil {
		e.mu.Lock()
		delete(e.waiters, id)
		e.mu.Unlock()
		return wire.Ack{}, false, err
	}
	t := time.NewTimer(e.c.AckTimeout)
	defer t.Stop()
	select {
	case a, ok := <-ch:
		if !ok {
			return wire.Ack{}, true, errors.New("local: the build's link dropped before it answered")
		}
		return a, true, nil
	case <-t.C:
		err = fmt.Errorf("local: no answer within %s", e.c.AckTimeout)
	case <-ctx.Done():
		err = ctx.Err()
	case <-e.base.Done():
		err = ErrClosed
	}
	e.mu.Lock()
	delete(e.waiters, id)
	e.mu.Unlock()
	return wire.Ack{}, true, err
}

// Dispatch sends cmd to the child, starting it when it is not up, and
// returns its answer. A command that could not be sent (the child is
// not up and will not start now) is answered busy: it is requeued, not
// lost. A command sent and not answered is an error: the hub marks it
// lost (at most once).
func (e *Executor) Dispatch(ctx context.Context, cmd wire.Command) (wire.Ack, error) {
	busy := wire.Ack{CommandID: cmd.CommandID, State: wire.AckBusy}
	l, err := e.ensure(ctx)
	if errors.Is(err, ErrClosed) {
		return wire.Ack{}, err
	}
	if err != nil {
		e.c.Logger.Debug("local: dispatch deferred", "digest", e.c.Digest, "err", err)
		return busy, nil
	}
	f, err := wire.NewFrame(wire.EventRun, cmd.CommandID, cmd)
	if err != nil {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckRejected, Error: err.Error()}, nil
	}
	a, sent, err := e.roundTrip(ctx, l, f, cmd.CommandID)
	if !sent {
		return busy, nil
	}
	return a, err
}

// Cancel sends a cancel to the child; with no child up there is
// nothing to cancel.
func (e *Executor) Cancel(_ context.Context, c wire.Cancel) error {
	e.mu.Lock()
	l := e.link
	e.mu.Unlock()
	if l == nil {
		return nil
	}
	f, err := wire.NewFrame(wire.EventCancel, "", c)
	if err != nil {
		return err
	}
	return l.write(f)
}

// Steer sends a steer to the child and returns its answer.
func (e *Executor) Steer(ctx context.Context, s wire.Steer) (wire.Ack, error) {
	e.mu.Lock()
	l := e.link
	e.mu.Unlock()
	if l == nil {
		return wire.Ack{CommandID: s.CommandID, State: wire.AckRejected, RunID: s.RunID, Error: "the build is not running"}, nil
	}
	f, err := wire.NewFrame(wire.EventSteer, s.CommandID, s)
	if err != nil {
		return wire.Ack{}, err
	}
	a, _, err := e.roundTrip(ctx, l, f, s.CommandID)
	return a, err
}

// Events yields the child's acks and the executor's disconnected
// frames until ctx ends or the executor is closed.
func (e *Executor) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(yield func(wire.Frame, error) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.base.Done():
				return
			case f := <-e.events:
				if !yield(f, nil) {
					return
				}
			}
		}
	}
}

// Drain asks the child to finish what it holds by deadline and exit,
// and waits for it (killing it past the deadline and a grace). The
// executor then starts nothing more: the node is shutting down.
func (e *Executor) Drain(ctx context.Context, deadline time.Time) error {
	e.mu.Lock()
	e.draining = true
	e.mu.Unlock()
	return e.drainChild(ctx, deadline)
}

// Retire drains the child as Drain does, for a newer build of the
// same agents; the executor stays attached and starts the child again
// when a run pinned to this build arrives.
func (e *Executor) Retire(ctx context.Context, deadline time.Time) error {
	e.mu.Lock()
	e.retired = true
	e.mu.Unlock()
	return e.drainChild(ctx, deadline)
}

// Retired reports whether a newer build took over.
func (e *Executor) Retired() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.retired
}

func (e *Executor) drainChild(ctx context.Context, deadline time.Time) error {
	e.mu.Lock()
	l, proc, done := e.link, e.proc, e.procDone
	if proc != nil {
		e.expectExit = true
	}
	e.mu.Unlock()
	if proc == nil {
		return nil
	}
	if l != nil {
		f, _ := wire.NewFrame(wire.EventDrain, "", wire.Drain{DeadlineMS: max(time.Until(deadline).Milliseconds(), 1)})
		if l.write(f) != nil {
			_ = proc.Process.Kill()
		}
	} else {
		_ = proc.Process.Signal(os.Interrupt)
	}
	t := time.NewTimer(time.Until(deadline) + killGrace)
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-t.C:
	case <-ctx.Done():
	}
	_ = proc.Process.Kill()
	select {
	case <-done:
	case <-time.After(killGrace):
	}
	return ctx.Err()
}

// Close kills the child, removes the socket and ends Events. Call it
// after Drain.
func (e *Executor) Close() error {
	e.mu.Lock()
	e.draining = true
	proc := e.proc
	e.mu.Unlock()
	if proc != nil {
		_ = proc.Process.Kill()
	}
	e.stop()
	_ = e.ln.Close()
	_ = os.Remove(e.sock)
	e.mu.Lock()
	if e.link != nil {
		_ = e.link.conn.Close()
	}
	e.mu.Unlock()
	e.wg.Wait()
	return nil
}
