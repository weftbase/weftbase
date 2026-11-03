// Package executor holds the placements' executors (P§5.7, P§6.1):
// Embedded runs weft agents in the Weftbase process over Go calls; the
// local placement (a supervised child over a socket) lands later.
// Every one implements worker.Executor, so the hub has one code path.
package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/worker"
)

// SeenSize bounds the set of command ids an executor remembers: a
// command it has seen is never run twice (at-most-once).
const SeenSize = 4096

// DefaultHeartbeat is how often a held command is re-acked.
const DefaultHeartbeat = 60 * time.Second

// ErrClosed is Dispatch's error after Drain.
var ErrClosed = errors.New("executor: drained")

// Embedded is the embedded placement: the agents run in this process,
// on the build that is this process (its digest is the host binary's).
// One version per agent per Embedded value; a parked run of another
// build resumes only where that build is served.
type Embedded struct {
	build     wire.Build
	agents    map[string]embeddedAgent
	order     []string
	heartbeat time.Duration

	events chan wire.Frame
	base   context.Context
	stop   context.CancelFunc

	mu       sync.Mutex
	seen     map[string]bool
	seenFIFO []string
	runs     map[string]context.CancelCauseFunc
	draining bool
	wg       sync.WaitGroup
}

type embeddedAgent struct {
	agent    *core.Agent
	manifest []byte
	hash     string
}

var _ worker.Executor = (*Embedded)(nil)

// EmbeddedOption configures NewEmbedded.
type EmbeddedOption func(*Embedded)

// Heartbeat sets how often a running command is re-acked (default 60 s).
func Heartbeat(d time.Duration) EmbeddedOption {
	return func(e *Embedded) {
		if d > 0 {
			e.heartbeat = d
		}
	}
}

// NewEmbedded serves agents in-process as the build with digest
// ("sha256:<hex>"; ExecutableDigest gives the host binary's). Every
// agent needs a name, unique here.
func NewEmbedded(digest string, agents []*core.Agent, opts ...EmbeddedOption) (*Embedded, error) {
	e := &Embedded{
		build:     wire.Build{Digest: digest, Kind: wire.BuildEmbedded, WeftVersion: weftVersion()},
		agents:    map[string]embeddedAgent{},
		heartbeat: DefaultHeartbeat,
		events:    make(chan wire.Frame, 1024),
		seen:      map[string]bool{},
		runs:      map[string]context.CancelCauseFunc{},
	}
	for _, o := range opts {
		o(e)
	}
	if len(agents) == 0 {
		return nil, errors.New("executor: an embedded build needs at least one agent")
	}
	for _, a := range agents {
		if a == nil || a.Name() == "" {
			return nil, errors.New("executor: an embedded agent needs a name (weft.Name)")
		}
		if _, dup := e.agents[a.Name()]; dup {
			return nil, fmt.Errorf("executor: agent %q given twice", a.Name())
		}
		m, err := core.Manifest(a)
		if err != nil {
			return nil, fmt.Errorf("executor: manifest of %s: %w", a.Name(), err)
		}
		sum := sha256.Sum256(m)
		e.agents[a.Name()] = embeddedAgent{agent: a, manifest: m, hash: hex.EncodeToString(sum[:])}
		e.order = append(e.order, a.Name())
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) <= len("sha256:") {
		return nil, fmt.Errorf("executor: build digest %q is not sha256:<hex>", digest)
	}
	e.base, e.stop = context.WithCancel(context.Background())
	return e, nil
}

func weftVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/weftbase/weft/core" {
				return d.Version
			}
		}
	}
	return ""
}

var (
	exeOnce   sync.Once
	exeDigest string
	exeErr    error
)

// ExecutableDigest is sha256 of the running executable, as a build
// digest ("sha256:<hex>"), computed once.
func ExecutableDigest() (string, error) {
	exeOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			exeErr = err
			return
		}
		f, err := os.Open(path)
		if err != nil {
			exeErr = err
			return
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			exeErr = err
			return
		}
		exeDigest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	})
	return exeDigest, exeErr
}

// Register announces the build and its agents.
func (e *Embedded) Register(context.Context) (wire.Registration, error) {
	reg := wire.Registration{
		Placement: wire.PlacementEmbedded, Build: e.build, PID: os.Getpid(),
		Capabilities: []string{wire.EventRun, wire.EventCancel}, HeartbeatMS: e.heartbeat.Milliseconds(),
	}
	if h, err := os.Hostname(); err == nil {
		reg.Host = h
	}
	for _, n := range e.order {
		a := e.agents[n]
		reg.Agents = append(reg.Agents, wire.AgentRegistration{Name: n, Manifest: a.manifest, ManifestHash: a.hash})
	}
	return reg, reg.Validate()
}

// markSeen records id; it reports false when id was seen before.
func (e *Embedded) markSeen(id string) bool {
	if e.seen[id] {
		return false
	}
	e.seen[id] = true
	e.seenFIFO = append(e.seenFIFO, id)
	if len(e.seenFIFO) > SeenSize {
		delete(e.seen, e.seenFIFO[0])
		e.seenFIFO = e.seenFIFO[1:]
	}
	return true
}

// Dispatch accepts a command for a version this build serves and runs
// it in a goroutine; anything else is rejected before it runs. A
// command id seen before is rejected (never run twice); after Drain
// every command is busy.
func (e *Embedded) Dispatch(_ context.Context, cmd wire.Command) (wire.Ack, error) {
	reject := func(why string) (wire.Ack, error) {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckRejected, Error: why}, nil
	}
	if err := cmd.Validate(); err != nil {
		return reject(err.Error())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.draining {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckBusy}, nil
	}
	if !e.markSeen(cmd.CommandID) {
		return reject("command " + cmd.CommandID + " was seen before")
	}
	a, ok := e.agents[cmd.Agent]
	if !ok || cmd.BuildDigest != e.build.Digest || cmd.ManifestHash != a.hash {
		return reject(fmt.Sprintf("agent_version %s (%s, build %s, manifest %s) is not served here", cmd.AgentVersion, cmd.Agent, cmd.BuildDigest, cmd.ManifestHash))
	}
	if _, running := e.runs[cmd.RunID]; running {
		return reject("run " + cmd.RunID + " is already running here")
	}
	ctx, cancel := context.WithCancelCause(e.base)
	e.runs[cmd.RunID] = cancel
	e.wg.Add(1)
	go e.run(ctx, cancel, a.agent, cmd)
	return wire.Ack{CommandID: cmd.CommandID, State: wire.AckAccepted}, nil
}

// checkpoint is a parked run's state, opaque to the server.
type checkpoint struct {
	Messages []core.Message `json:"messages"`
	Park     *core.ParkRule `json:"park,omitempty"`
}

func (e *Embedded) send(f wire.Frame) {
	select {
	case e.events <- f:
	case <-e.base.Done():
	}
}

func (e *Embedded) run(ctx context.Context, cancel context.CancelCauseFunc, agent *core.Agent, cmd wire.Command) {
	defer e.wg.Done()
	defer func() {
		e.mu.Lock()
		delete(e.runs, cmd.RunID)
		e.mu.Unlock()
		cancel(nil)
	}()
	ack := wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID}
	func() {
		defer func() {
			if p := recover(); p != nil {
				ack.Status, ack.Error = wire.StatusFailed, fmt.Sprintf("panic: %v", p)
			}
		}()
		opts, err := runOptions(cmd)
		if err != nil {
			ack.Status, ack.Error = wire.StatusFailed, err.Error()
			return
		}
		e.send(wire.AckFrame(wire.Ack{CommandID: cmd.CommandID, State: wire.AckRunStarted, RunID: cmd.RunID}))
		beat := time.NewTicker(e.heartbeat)
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-beat.C:
					e.send(wire.AckFrame(wire.Ack{CommandID: cmd.CommandID, State: wire.AckHeartbeat, RunID: cmd.RunID}))
				}
			}
		}()
		res, err := agent.Generate(ctx, opts...)
		beat.Stop()
		close(done)
		if res != nil {
			ack.Usage = &wire.Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens}
		}
		switch {
		case err != nil && ctx.Err() != nil:
			ack.Status, ack.Cause = wire.StatusCanceled, causeOf(ctx)
		case err != nil:
			ack.Status, ack.Error = wire.StatusFailed, err.Error()
		case len(res.Pending) > 0:
			ack.Status = wire.StatusParked
			for _, p := range res.Pending {
				ack.Pending = append(ack.Pending, wire.PendingCall{CallID: p.ID, Tool: p.Name, Args: p.Args})
			}
			ack.Checkpoint, _ = json.Marshal(checkpoint{Messages: res.Messages, Park: res.Park})
		default:
			ack.Status = wire.StatusSucceeded
		}
	}()
	e.send(wire.AckFrame(ack))
}

func causeOf(ctx context.Context) string {
	var c core.CancelCause
	if errors.As(context.Cause(ctx), &c) {
		return string(c)
	}
	return wire.CauseInterrupt
}

// runOptions turns a command into weft run options.
func runOptions(cmd wire.Command) ([]core.RunOption, error) {
	opts := []core.RunOption{core.RunID(cmd.RunID)}
	if len(cmd.Metadata) > 0 {
		opts = append(opts, core.Metadata(cmd.Metadata))
	}
	switch cmd.Kind {
	case wire.KindResume:
		var cp checkpoint
		if len(cmd.Resume.Checkpoint) == 0 {
			return nil, errors.New("executor: a resume command carries no checkpoint")
		}
		if err := json.Unmarshal(cmd.Resume.Checkpoint, &cp); err != nil {
			return nil, fmt.Errorf("executor: checkpoint: %w", err)
		}
		opts = append(opts, core.Messages(cp.Messages...), core.ResumeOf(cmd.Resume.ResumeOf))
		opts = append(opts, cp.Park.Options()...)
		for _, d := range cmd.Resume.Decisions {
			switch d.Decision {
			case "approve":
				opts = append(opts, core.Approve(d.CallID))
			case "deny":
				opts = append(opts, core.Deny(d.CallID, d.Reason))
			case "resolve":
				opts = append(opts, core.Resolve(d.CallID, d.Content))
			default:
				return nil, fmt.Errorf("executor: unknown decision %q", d.Decision)
			}
		}
	default:
		if cmd.Input == nil {
			return nil, errors.New("executor: a start command carries no input")
		}
		if len(cmd.Input.Messages) > 0 {
			msgs := make([]core.Message, len(cmd.Input.Messages))
			for i, raw := range cmd.Input.Messages {
				if err := json.Unmarshal(raw, &msgs[i]); err != nil {
					return nil, fmt.Errorf("executor: input message %d: %w", i, err)
				}
			}
			opts = append(opts, core.Messages(msgs...))
		}
		if cmd.Input.Prompt != "" {
			opts = append(opts, core.Prompt(cmd.Input.Prompt))
		}
	}
	return opts, nil
}

// Cancel cancels a run this executor holds with c's cause; a run it
// does not hold is ignored (it ended, or it was never here).
func (e *Embedded) Cancel(_ context.Context, c wire.Cancel) error {
	e.mu.Lock()
	cancel := e.runs[c.RunID]
	e.mu.Unlock()
	if cancel != nil {
		cause := c.Cause
		if cause == "" {
			cause = wire.CauseUser
		}
		cancel(core.CancelCause(cause))
	}
	return nil
}

// Events yields the executor's acks until ctx ends or it is drained.
func (e *Embedded) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(yield func(wire.Frame, error) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-e.events:
				if !yield(f, nil) {
					return
				}
			case <-e.base.Done():
				// Drained: hand over what is still buffered, then end.
				for {
					select {
					case f := <-e.events:
						if !yield(f, nil) {
							return
						}
					default:
						return
					}
				}
			}
		}
	}
}

// Drain takes no more commands, lets running ones finish until
// deadline, then cancels the rest (cause shutdown) and waits for them
// (bounded by ctx). Their finished acks are still yielded by Events.
func (e *Embedded) Drain(ctx context.Context, deadline time.Time) error {
	e.mu.Lock()
	e.draining = true
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		e.mu.Lock()
		for _, cancel := range e.runs {
			cancel(core.CancelCause(wire.CauseShutdown))
		}
		e.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Close ends Events once the hub has read the last acks.
func (e *Embedded) Close() { e.stop() }
