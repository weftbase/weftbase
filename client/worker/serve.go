// Package worker is an agent build's side of the Weftbase worker
// protocol (P§6): a build's main hands its weft agents to Serve, which
// dials the Weftbase supervisor that started it (the local placement:
// a Unix socket named by WEFTBASE_SOCKET), registers the build and its
// agents, and runs the commands it is sent.
//
//	func main() {
//		support := weft.New(model, weft.Name("support"), lookup, refund)
//		if err := worker.Serve(worker.FromEnv(), worker.Agent(support)); err != nil {
//			log.Fatal(err)
//		}
//	}
//
// The rules of the link: register on connect and after every
// reconnect; answer every command (accepted, rejected or busy) before
// anything runs, and run nothing whose accepted ack was not delivered;
// never run a command id twice (the seen set); re-ack a running command
// every heartbeat; treat a link with no frame for three ping intervals
// as dead (the idle watchdog); redial with backoff (0.5 s doubling to
// 30 s, jittered), starting over once a link held. A drain frame, or
// SIGINT/SIGTERM, finishes the runs in hand and returns.
//
// `<build> --manifest` prints the build's manifests and exits, so a
// pipeline can register a build without running it.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/client/wire"
)

// The environment a supervisor passes a build it starts.
const (
	// EnvSocket is the Unix socket the build dials.
	EnvSocket = "WEFTBASE_SOCKET"
	// EnvToken is the per-child token the build registers with.
	EnvToken = "WEFTBASE_TOKEN"
	// EnvBuildDigest is the build's digest as the supervisor computed
	// it (the build then need not hash itself).
	EnvBuildDigest = "WEFTBASE_BUILD_DIGEST"
	// EnvPlacement is the placement the build runs in (local).
	EnvPlacement = "WEFTBASE_PLACEMENT"
	// EnvURL is the Weftbase server's base URL (the remote worker link,
	// v2; and where the build's OTel exporter sends).
	EnvURL = "WEFTBASE_URL"
)

// Link bounds (P§6.1).
const (
	// DefaultPing is the server's ping cadence when its welcome names
	// none.
	DefaultPing = 15 * time.Second
	// registerTimeout bounds the wait for the server's welcome.
	registerTimeout = 15 * time.Second
	// healthyLink: a link that held this long resets the backoff.
	healthyLink = 10 * time.Second
	// DefaultDrain is how long a drain (SIGTERM, or a drain frame
	// without a deadline) lets running runs finish.
	DefaultDrain = 10 * time.Second
	// DefaultMaxDisconnected: a build that could not reach its
	// supervisor for this long, holding no run, exits (its supervisor
	// is gone).
	DefaultMaxDisconnected = 2 * time.Minute
)

// ErrRefused is Serve's error when the server refused the
// registration (a bad token, a digest it did not start).
var ErrRefused = errors.New("worker: the server refused the registration")

// ErrOrphaned is Serve's error when the supervisor stayed unreachable
// past MaxDisconnected with no run in hand.
var ErrOrphaned = errors.New("worker: the supervisor is unreachable")

// Option configures Serve.
type Option func(*config)

type config struct {
	socket          string
	token           string
	digest          string
	placement       string
	agents          []AgentSpec
	heartbeat       time.Duration
	maxDisconnected time.Duration
	drain           time.Duration
	log             *slog.Logger
	err             error
	backoff         *backoff
}

// FromEnv reads the link from the environment a supervisor sets:
// WEFTBASE_SOCKET, WEFTBASE_TOKEN, WEFTBASE_BUILD_DIGEST and
// WEFTBASE_PLACEMENT. Without WEFTBASE_SOCKET, Serve fails (the remote
// worker link over WEFTBASE_URL lands with the remote placement).
func FromEnv() Option {
	return func(c *config) {
		c.socket = os.Getenv(EnvSocket)
		c.token = os.Getenv(EnvToken)
		if d := os.Getenv(EnvBuildDigest); d != "" {
			c.digest = d
		}
		if p := os.Getenv(EnvPlacement); p != "" {
			c.placement = p
		}
		if c.socket == "" {
			if os.Getenv(EnvURL) != "" {
				c.err = errors.New("worker: WEFTBASE_URL without WEFTBASE_SOCKET: the remote worker link is not built yet; run the build under a Weftbase supervisor (weftbase serve --build)")
			} else {
				c.err = errors.New("worker: WEFTBASE_SOCKET is not set: run the build under a Weftbase supervisor (weftbase serve --build)")
			}
		}
	}
}

// Socket names the Unix socket to dial.
func Socket(path string) Option {
	return func(c *config) {
		c.socket = path
		c.err = nil
	}
}

// Token is the token the build registers with.
func Token(t string) Option { return func(c *config) { c.token = t } }

// BuildDigest names the build's digest instead of the executable's
// sha256.
func BuildDigest(d string) Option { return func(c *config) { c.digest = d } }

// Agent serves a weft agent.
func Agent(a *core.Agent, opts ...AgentOption) Option {
	return func(c *config) { c.agents = append(c.agents, Spec(a, opts...)) }
}

// Heartbeat sets how often a running command is re-acked (default
// 60 s).
func Heartbeat(d time.Duration) Option { return func(c *config) { c.heartbeat = d } }

// MaxDisconnected is how long a build with no run in hand keeps
// redialling an unreachable supervisor before Serve returns
// ErrOrphaned (default 2 min; negative: forever).
func MaxDisconnected(d time.Duration) Option { return func(c *config) { c.maxDisconnected = d } }

// Drain is how long a SIGTERM lets running runs finish before they are
// cancelled (default 10 s).
func Drain(d time.Duration) Option { return func(c *config) { c.drain = d } }

// Logger takes the link's log lines (default slog.Default()).
func Logger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// Serve runs the build until SIGINT or SIGTERM (a drain), a drain
// frame, or a refused registration. With --manifest as the first
// argument it prints the manifests and returns.
func Serve(opts ...Option) error {
	if len(os.Args) > 1 && os.Args[1] == "--manifest" {
		return PrintManifest(opts...)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ServeContext(ctx, opts...)
}

// PrintManifest writes the build's manifests to stdout as one JSON
// object: {"agents": {"<name>": <manifest>, …}}.
func PrintManifest(opts ...Option) error {
	c := newConfig(opts)
	r, err := c.runner()
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := json.MarshalIndent(map[string]any{"agents": r.Manifest()}, "", "  ")
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(b, '\n'))
	return err
}

func newConfig(opts []Option) *config {
	c := &config{placement: wire.PlacementLocal, maxDisconnected: DefaultMaxDisconnected, drain: DefaultDrain, backoff: &backoff{}}
	for _, o := range opts {
		if o != nil {
			o(c)
		}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	return c
}

func (c *config) runner() (*Runner, error) {
	digest := c.digest
	if digest == "" {
		d, err := ExecutableDigest()
		if err != nil {
			return nil, fmt.Errorf("worker: the build's digest: %w", err)
		}
		digest = d
	}
	return NewRunner(wire.Build{Digest: digest, Kind: wire.BuildBinary, VCS: vcs()}, c.agents, WithHeartbeat(c.heartbeat))
}

// ServeContext is Serve under ctx: ctx's end drains the build.
func ServeContext(ctx context.Context, opts ...Option) error {
	c := newConfig(opts)
	if c.err != nil {
		return c.err
	}
	if c.socket == "" {
		return errors.New("worker: no socket to dial (FromEnv or Socket)")
	}
	r, err := c.runner()
	if err != nil {
		return err
	}
	l := &link{c: c, r: r, drained: make(chan struct{})}
	return l.loop(ctx)
}

// link is one build's connection to its supervisor, across redials.
type link struct {
	c *config
	r *Runner

	workerID string
	// pending are acks taken from the Runner that no link delivered
	// yet: written first on the next link.
	pmu     sync.Mutex
	pending []wire.Ack

	drainOnce sync.Once
	drained   chan struct{} // closed when a drain finished
}

func (l *link) loop(ctx context.Context) error {
	defer l.r.Close()
	go func() {
		<-ctx.Done()
		l.drain(l.c.drain)
	}()
	var downSince time.Time
	for {
		select {
		case <-l.drained:
			return l.flushLast(ctx)
		default:
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("unix", l.c.socket)
		if err == nil {
			opened := time.Now()
			err = l.session(conn)
			if time.Since(opened) >= healthyLink {
				l.c.backoff.reset()
			}
			if errors.Is(err, ErrRefused) {
				return err
			}
			downSince = time.Time{}
		}
		if downSince.IsZero() {
			downSince = time.Now()
		}
		select {
		case <-l.drained:
			return nil
		default:
		}
		if l.c.maxDisconnected >= 0 && time.Since(downSince) > l.c.maxDisconnected && l.r.InFlight() == 0 {
			return fmt.Errorf("%w: %v", ErrOrphaned, err)
		}
		l.c.log.Debug("worker: link down; redialling", "socket", l.c.socket, "err", err)
		select {
		case <-time.After(l.c.backoff.next()):
		case <-l.drained:
			return nil
		}
	}
}

// drain finishes the runs in hand (the Runner answers new commands
// busy), then marks the link drained.
func (l *link) drain(d time.Duration) {
	l.drainOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), d+5*time.Second)
			defer cancel()
			_ = l.r.Drain(ctx, time.Now().Add(d))
			close(l.drained)
		}()
	})
}

// flushLast is reached when a drain finished with no link up: there is
// no one to give the last acks to.
func (l *link) flushLast(context.Context) error { return nil }

// session runs one connection: register, then read frames until the
// link breaks or a drain finished and every ack was written.
func (l *link) session(conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	var wmu sync.Mutex
	write := func(f wire.Frame) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return wire.WriteFrame(conn, f)
	}
	reg := l.r.Registration(l.c.placement)
	reg.WorkerID = l.workerID
	hello, err := wire.NewFrame(wire.EventRegister, "", wire.Hello{Token: l.c.token, Registration: reg})
	if err != nil {
		return err
	}
	if err := write(hello); err != nil {
		return err
	}
	fr := wire.NewFrameReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(registerTimeout))
	f, err := fr.Read()
	if err != nil {
		return fmt.Errorf("worker: waiting for the welcome: %w", err)
	}
	var w wire.Welcome
	if f.Event != wire.EventWelcome || f.Decode(&w) != nil {
		return fmt.Errorf("worker: expected a welcome, got %q", f.Event)
	}
	if w.Error != "" {
		return fmt.Errorf("%w: %s", ErrRefused, w.Error)
	}
	l.workerID = w.WorkerID
	idle := 3 * DefaultPing
	if w.PingMS > 0 {
		idle = 3 * time.Duration(w.PingMS) * time.Millisecond
	}

	// The writer: pending acks first, then the Runner's, until the link
	// breaks. An ack it could not write goes back to pending.
	broken := make(chan struct{})
	var breakOnce sync.Once
	brk := func() { breakOnce.Do(func() { close(broken); _ = conn.Close() }) }
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			l.pmu.Lock()
			var next *wire.Ack
			if len(l.pending) > 0 {
				a := l.pending[0]
				l.pending = l.pending[1:]
				next = &a
			}
			l.pmu.Unlock()
			if next == nil {
				select {
				case a := <-l.r.Acks():
					next = &a
				case <-broken:
					return
				case <-l.drained:
					// Drained: everything the Runner sent is buffered;
					// write it, then end the session.
					select {
					case a := <-l.r.Acks():
						next = &a
					default:
						brk()
						return
					}
				}
			}
			if err := write(wire.AckFrame(*next)); err != nil {
				l.pmu.Lock()
				l.pending = append([]wire.Ack{*next}, l.pending...)
				l.pmu.Unlock()
				brk()
				return
			}
		}
	}()
	defer func() { brk(); <-writerDone }()

	for {
		// The idle watchdog: the server pings every PingMS; three
		// silent intervals and the link is dead.
		_ = conn.SetReadDeadline(time.Now().Add(idle))
		f, err := fr.Read()
		if err != nil {
			select {
			case <-broken:
				select {
				case <-l.drained:
					return nil
				default:
				}
			default:
			}
			return err
		}
		switch f.Event {
		case wire.EventPing:
		case wire.EventRun:
			var cmd wire.Command
			if err := f.Decode(&cmd); err != nil {
				if wire.ValidCommandID(f.ID) {
					_ = write(wire.AckFrame(wire.Ack{CommandID: f.ID, State: wire.AckRejected, Error: "undecodable command: " + err.Error()}))
				}
				continue
			}
			ack, start := l.r.Admit(cmd)
			werr := write(wire.AckFrame(ack))
			if start != nil {
				// Ack before run: nothing runs on an ack the server
				// never got.
				start(werr == nil)
			}
			if werr != nil {
				return werr
			}
		case wire.EventCancel:
			var c wire.Cancel
			if f.Decode(&c) == nil {
				l.r.Cancel(c)
			}
		case wire.EventSteer:
			var s wire.Steer
			if err := f.Decode(&s); err != nil {
				if wire.ValidCommandID(f.ID) {
					_ = write(wire.AckFrame(wire.Ack{CommandID: f.ID, State: wire.AckRejected, Error: "undecodable steer: " + err.Error()}))
				}
				continue
			}
			if err := write(wire.AckFrame(l.r.Steer(s))); err != nil {
				return err
			}
		case wire.EventDrain:
			var d wire.Drain
			_ = f.Decode(&d)
			dl := l.c.drain
			if d.DeadlineMS > 0 {
				dl = time.Duration(d.DeadlineMS) * time.Millisecond
			}
			l.drain(dl)
		default:
			l.c.log.Debug("worker: unknown frame", "event", f.Event)
		}
	}
}

// backoff is the redial schedule: half a second doubling to a 30 s
// cap, each wait jittered over its upper half (a fleet behind one
// restarted server does not redial in step), starting over once a link
// held.
type backoff struct {
	mu sync.Mutex
	n  int
}

func (b *backoff) next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := 30 * time.Second
	if b.n < 6 {
		d = 500 * time.Millisecond * (1 << b.n)
		b.n++
	}
	return d/2 + rand.N(d/2+1)
}

func (b *backoff) reset() {
	b.mu.Lock()
	b.n = 0
	b.mu.Unlock()
}
