// Package executor holds the placements' executors (P§5.7, P§6.1):
// Embedded runs weft agents in the Weftbase process over Go calls;
// executor/local runs a build as a supervised child over a socket.
// Both drive the same command engine, the agent build's own
// (client/worker.Runner): the at-most-once gate, capacity, cancel,
// steer, heartbeats and drain behave the same in every placement, and
// every executor implements worker.Executor, so the hub has one code
// path.
package executor

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/client/wire"
	buildworker "github.com/weftbase/weftbase/client/worker"
	"github.com/weftbase/weftbase/worker"
)

// DefaultHeartbeat is how often a held command is re-acked.
const DefaultHeartbeat = buildworker.DefaultHeartbeat

// ErrClosed is Dispatch's error after Close.
var ErrClosed = errors.New("executor: closed")

// Embedded is the embedded placement: the agents run in this process,
// on the build that is this process (its digest is the host binary's).
// One version per agent per Embedded value; a parked run of another
// build resumes only where that build is served.
type Embedded struct {
	r           *buildworker.Runner
	heartbeat   time.Duration
	concurrency map[string]int
}

var _ worker.Executor = (*Embedded)(nil)

// EmbeddedOption configures NewEmbedded.
type EmbeddedOption func(*Embedded)

// Heartbeat sets how often a running command is re-acked (default 60 s).
func Heartbeat(d time.Duration) EmbeddedOption {
	return func(e *Embedded) { e.heartbeat = d }
}

// Concurrency bounds how many runs of the named agent execute at once
// here; one past it waits in the queue (it is handed back busy and
// claimed again). 0 sets no bound.
func Concurrency(agent string, n int) EmbeddedOption {
	return func(e *Embedded) {
		if e.concurrency == nil {
			e.concurrency = map[string]int{}
		}
		e.concurrency[agent] = n
	}
}

// NewEmbedded serves agents in-process as the build with digest
// ("sha256:<hex>"; ExecutableDigest gives the host binary's). Every
// agent needs a name, unique here.
func NewEmbedded(digest string, agents []*core.Agent, opts ...EmbeddedOption) (*Embedded, error) {
	e := &Embedded{}
	for _, o := range opts {
		o(e)
	}
	specs := make([]buildworker.AgentSpec, len(agents))
	for i, a := range agents {
		specs[i] = buildworker.Spec(a)
		if a != nil {
			specs[i].Concurrency = max(e.concurrency[a.Name()], 0)
		}
	}
	r, err := buildworker.NewRunner(wire.Build{Digest: digest, Kind: wire.BuildEmbedded}, specs, buildworker.WithHeartbeat(e.heartbeat))
	if err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}
	e.r = r
	return e, nil
}

// ExecutableDigest is sha256 of the running executable, as a build
// digest ("sha256:<hex>"), computed once.
func ExecutableDigest() (string, error) { return buildworker.ExecutableDigest() }

// Register announces the build and its agents.
func (e *Embedded) Register(context.Context) (wire.Registration, error) {
	reg := e.r.Registration(wire.PlacementEmbedded)
	return reg, reg.Validate()
}

// Dispatch accepts a command for a version this build serves and runs
// it; anything else is answered rejected (or busy) before it runs.
func (e *Embedded) Dispatch(_ context.Context, cmd wire.Command) (wire.Ack, error) {
	select {
	case <-e.r.Done():
		return wire.Ack{}, ErrClosed
	default:
	}
	return e.r.Dispatch(cmd), nil
}

// Cancel cancels a run this executor holds with c's cause; a run it
// does not hold is ignored.
func (e *Embedded) Cancel(_ context.Context, c wire.Cancel) error {
	e.r.Cancel(c)
	return nil
}

// Steer queues user messages for a run this executor holds.
func (e *Embedded) Steer(_ context.Context, s wire.Steer) (wire.Ack, error) { return e.r.Steer(s), nil }

// Events yields the executor's acks until ctx ends or it is closed
// (what is still buffered then is handed over first).
func (e *Embedded) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(yield func(wire.Frame, error) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case a := <-e.r.Acks():
				if !yield(wire.AckFrame(a), nil) {
					return
				}
			case <-e.r.Done():
				for {
					select {
					case a := <-e.r.Acks():
						if !yield(wire.AckFrame(a), nil) {
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

// Drain takes no more commands (busy), lets running ones finish until
// deadline, then cancels the rest (cause shutdown) and waits for them
// (bounded by ctx). Their finished acks are still yielded by Events.
func (e *Embedded) Drain(ctx context.Context, deadline time.Time) error {
	return e.r.Drain(ctx, deadline)
}

// Close ends Events once the hub has read the last acks.
func (e *Embedded) Close() { e.r.Close() }
