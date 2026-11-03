// Package worker is the server side of the worker protocol (P§6): the
// Executor every placement implements, the RunSink the hub reports
// runs to, and the Router that picks a worker for a run by its
// AgentVersion and lease holder. The hub (worker/hub) claims queue
// items and dispatches them; the executors (worker/executor) run them:
// embedded in-process over Go calls, local in a child process.
package worker

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Executor is one connected worker, whatever its placement (P§6.1).
// Register announces the build and its agents (again after a
// reconnect). Dispatch hands a command over at most once: the answer
// is accepted (it will run), rejected or busy (it will not); nothing
// runs on an unconfirmed ack. Cancel asks the worker to cancel a run
// it holds. Steer queues user messages for a run it holds (accepted,
// or rejected when the run is not there). Events yields the worker's
// later acks (run_started, heartbeat, finished) as frames, and a
// disconnected frame (wire.Disconnected) when the worker's link ended,
// until ctx ends. Drain finishes what the worker holds by deadline,
// cancelling the rest, and takes nothing more.
type Executor interface {
	Register(ctx context.Context) (wire.Registration, error)
	Dispatch(ctx context.Context, cmd wire.Command) (wire.Ack, error)
	Cancel(ctx context.Context, c wire.Cancel) error
	Steer(ctx context.Context, s wire.Steer) (wire.Ack, error)
	Events(ctx context.Context) iter.Seq2[wire.Frame, error]
	Drain(ctx context.Context, deadline time.Time) error
}

// RunSink is what the hub reports runs to: the run ledger (sessions),
// wired by the kernel — worker does not import sessions. Every method
// runs in the transaction that also completes or keeps the queue item.
type RunSink interface {
	// Prepare returns the command for a claimed item's payload and
	// records its dispatch, or ok false when nothing is to be sent.
	Prepare(tx store.Tx, s tenant.Scope, payload json.RawMessage, commandID, workerID string) (cmd wire.Command, ok bool, err error)
	// Undispatched clears a dispatch the worker handed back (busy).
	Undispatched(tx store.Tx, s tenant.Scope, runID string) error
	Started(tx store.Tx, s tenant.Scope, runID string) error
	Finished(tx store.Tx, s tenant.Scope, runID string, ack wire.Ack) error
	Rejected(tx store.Tx, s tenant.Scope, runID, reason string) error
	Lost(tx store.Tx, s tenant.Scope, runID, why string) error
}
