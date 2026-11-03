// Package hub is the server side of the worker link: it attaches
// executors (registering their builds and agents, which creates the
// AgentVersions), claims queue items for the versions they serve,
// routes each to a worker by (AgentVersion, lease holder), dispatches
// it at most once, and turns the workers' acks into run ledger changes
// through a worker.RunSink.
//
// The queue is the durable state: an item stays leased while its run
// is in a worker's hands, the lease is extended while the worker beats,
// and the run's end completes the item in the same transaction as the
// ledger change. What the hub keeps in memory (the runs in flight on
// this node) is rebuilt by expiry: after a restart an in-flight item's
// lease runs out, the next claim finds its run dispatched, and the run
// is marked lost — never sent twice.
//
// A run is also lost, at once, when its worker's link ends with the
// worker (an executor's disconnected frame with lost set: a local child
// exited), and when its worker stops acking it for longer than
// max(Silence, three of the worker's heartbeats). Every link that ends
// writes worker.disconnected to the outbox.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/worker"
)

// Config is what a hub runs over.
type Config struct {
	DB       store.Runner
	Events   *events.Log
	Queue    *queue.Queue
	Registry *registry.Registry
	Sink     worker.RunSink
	// Owner names this node's claims (default: a fresh id).
	Owner string
	// LeaseTTL is a claimed item's lease (default queue.DefaultTTL).
	LeaseTTL time.Duration
	// Poll is how often the claim loop looks without a wake (default
	// 250 ms).
	Poll time.Duration
	// Silence is how long a run in a worker's hands may go without an
	// ack (run_started, heartbeat) before it is lost: the longer of it
	// and three of the worker's heartbeats (default 10 min, P§6.3).
	Silence time.Duration
	Logger  *slog.Logger
}

// DefaultSilence is Config.Silence's default.
const DefaultSilence = 10 * time.Minute

// ErrNotHeld is SteerRun's error for a run no worker attached to this
// node holds, or whose worker refused the steer.
var ErrNotHeld = errors.New("hub: the run is not in a worker's hands")

// Hub dispatches queued runs to attached executors.
type Hub struct {
	c      Config
	log    *slog.Logger
	router worker.Router

	base context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	flight map[string]*flight // by command id
	byRun  map[string]string  // run id → command id
}

type flight struct {
	lease queue.Lease // guarded by Hub.mu (extend moves it)
	// scope is the lease's scope, fixed: read without the lock.
	scope  tenant.Scope
	runID  string
	worker *worker.Worker
	// heard is when the worker last acked the run.
	heard time.Time
}

// New returns a hub. Close stops it.
func New(c Config) *Hub {
	if c.Owner == "" {
		c.Owner = "node_" + ulid.New()
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = queue.DefaultTTL
	}
	if c.Poll <= 0 {
		c.Poll = 250 * time.Millisecond
	}
	if c.Silence <= 0 {
		c.Silence = DefaultSilence
	}
	h := &Hub{c: c, log: c.Logger, flight: map[string]*flight{}, byRun: map[string]string{}}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.base, h.stop = context.WithCancel(context.Background())
	return h
}

// Router is the hub's router (its attached workers).
func (h *Hub) Router() *worker.Router { return &h.router }

// Attach registers ex in scope s — its build and agents, creating the
// AgentVersions it serves — and starts consuming its acks. It returns
// the worker with its versions.
func (h *Hub) Attach(ctx context.Context, s tenant.Scope, ex worker.Executor) (*worker.Worker, []registry.Version, error) {
	reg, err := ex.Register(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("hub: register: %w", err)
	}
	if err := reg.Validate(); err != nil {
		return nil, nil, err
	}
	specs := make([]registry.AgentSpec, len(reg.Agents))
	for i, a := range reg.Agents {
		specs[i] = registry.AgentSpec{Name: a.Name, Manifest: a.Manifest, ManifestHash: a.ManifestHash}
	}
	w := &worker.Worker{ID: reg.WorkerID, Exec: ex, Registration: reg, Scope: s}
	if w.ID == "" {
		w.ID = "wk_" + ulid.New()
	}
	var versions []registry.Version
	err = h.c.DB.InTx(ctx, func(tx store.Tx) error {
		kind := reg.Build.Kind
		if kind == "" {
			kind = wire.BuildExternal
		}
		var err error
		if versions, err = h.c.Registry.Register(tx, s, reg.Build.Digest, kind, specs); err != nil {
			return err
		}
		ids := make([]string, len(versions))
		for i, v := range versions {
			ids[i] = v.ID
		}
		_, err = h.c.Events.Emit(tx, s, events.Event{Type: events.WorkerRegistered, ResourceKind: "worker", ResourceID: w.ID},
			map[string]any{"worker": w.ID, "placement": reg.Placement, "build_digest": reg.Build.Digest, "versions": ids})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	for _, v := range versions {
		w.Versions = append(w.Versions, v.ID)
	}
	h.router.Add(w)
	h.wg.Add(1)
	go h.consume(w)
	h.c.Queue.Poke()
	return w, versions, nil
}

// Detach removes ex's workers from the router: they take no more runs.
// The runs they hold report as before.
func (h *Hub) Detach(ex worker.Executor) {
	for _, w := range h.router.Workers() {
		if w.Exec == ex {
			h.router.Remove(w.ID)
		}
	}
}

// Run claims and dispatches until ctx ends, and extends the leases of
// the runs in flight.
func (h *Hub) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-h.base.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.extendLoop(ctx)
	}()
	defer wg.Wait()
	poll := time.NewTicker(h.c.Poll)
	defer poll.Stop()
	for {
		for h.claimOne(ctx) {
		}
		select {
		case <-ctx.Done():
			return nil
		case <-h.c.Queue.Wake():
		case <-poll.C:
		}
	}
}

// claimOne claims and dispatches one item; false when there was none
// (or the hub is stopping).
func (h *Hub) claimOne(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	l, err := h.c.Queue.Claim(ctx, queue.ClaimFilter{Owner: h.c.Owner, Kinds: []string{queue.KindRun, queue.KindResume},
		Routes: h.router.Versions(), TTL: h.c.LeaseTTL})
	if errors.Is(err, queue.ErrEmpty) {
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("hub: claim failed", "err", err)
		}
		return false
	}
	h.dispatch(ctx, l)
	return true
}

func (h *Hub) dispatch(ctx context.Context, l queue.Lease) {
	w, ok := h.router.Pick(l.Route, "")
	if !ok { // the worker left between the claim and now
		h.release(ctx, l)
		return
	}
	cmdID := wire.NewCommandID()
	var cmd wire.Command
	err := h.c.DB.InTx(ctx, func(tx store.Tx) error {
		var ok bool
		var err error
		cmd, ok, err = h.c.Sink.Prepare(tx, l.Scope, l.Payload, cmdID, w.ID)
		if err != nil || ok {
			return err
		}
		return h.c.Queue.Done(tx, l)
	})
	if err != nil {
		h.log.Warn("hub: prepare failed", "item", l.ID, "err", err)
		h.fail(ctx, l, err.Error())
		return
	}
	if cmd.CommandID == "" {
		return // nothing to send: done above
	}
	f := &flight{lease: l, scope: l.Scope, runID: cmd.RunID, worker: w, heard: time.Now()}
	h.mu.Lock()
	h.flight[cmdID], h.byRun[cmd.RunID] = f, cmdID
	h.mu.Unlock()
	h.router.Hold(w, 1)

	ack, err := w.Exec.Dispatch(ctx, cmd)
	switch {
	case err != nil:
		// The worker may or may not have it: at most once, so lost.
		h.end(f, func(tx store.Tx) error { return h.c.Sink.Lost(tx, l.Scope, cmd.RunID, "dispatch failed: "+err.Error()) })
	case ack.State == wire.AckAccepted:
		// Running: its acks arrive through Events.
	case ack.State == wire.AckBusy:
		h.forget(f)
		_ = h.c.DB.InTx(ctx, func(tx store.Tx) error { return h.c.Sink.Undispatched(tx, l.Scope, cmd.RunID) })
		h.release(ctx, l)
	default: // rejected, or an answer that is not one
		why := ack.Error
		if ack.State != wire.AckRejected {
			why = "unexpected ack state " + string(ack.State)
		}
		h.end(f, func(tx store.Tx) error { return h.c.Sink.Rejected(tx, l.Scope, cmd.RunID, why) })
	}
}

func (h *Hub) release(ctx context.Context, l queue.Lease) {
	if err := h.c.Queue.Release(ctx, l, time.Second); err != nil && ctx.Err() == nil {
		h.log.Warn("hub: release failed", "item", l.ID, "err", err)
	}
}

func (h *Hub) fail(ctx context.Context, l queue.Lease, why string) {
	_ = h.c.DB.InTx(ctx, func(tx store.Tx) error {
		_, err := h.c.Queue.Fail(tx, l, why, time.Second)
		return err
	})
}

// forget drops f from the flights; false when it was gone already (an
// end raced another: the first one wins).
func (h *Hub) forget(f *flight) bool {
	h.mu.Lock()
	found := false
	for id, x := range h.flight {
		if x == f {
			delete(h.flight, id)
			found = true
		}
	}
	if h.byRun[f.runID] != "" && h.flight[h.byRun[f.runID]] == nil {
		delete(h.byRun, f.runID)
	}
	h.mu.Unlock()
	if found {
		h.router.Hold(f.worker, -1)
	}
	return found
}

// end records a run's end (change) and completes its item, in one
// transaction, and forgets the flight. When the lease was lost (the
// item was claimed again elsewhere) the end is still recorded.
func (h *Hub) end(f *flight, change func(tx store.Tx) error) {
	if !h.forget(f) {
		return
	}
	// Forgotten, f is extended no more; an extension already under way
	// may still move the fence, which the retry below absorbs.
	h.mu.Lock()
	lease := f.lease
	h.mu.Unlock()
	ctx := context.Background()
	err := h.c.DB.InTx(ctx, func(tx store.Tx) error {
		if err := change(tx); err != nil {
			return err
		}
		return h.c.Queue.Done(tx, lease)
	})
	if errors.Is(err, queue.ErrLeaseLost) {
		err = h.c.DB.InTx(ctx, change)
	}
	if err != nil {
		h.log.Warn("hub: recording a run's end failed", "run", f.runID, "err", err)
	}
}

// consume reads a worker's acks until the hub closes. A disconnected
// frame records worker.disconnected (and, when the runs went with the
// worker, loses them); an event stream that ends before the hub stops
// is a worker gone for good: it is detached and its runs are lost.
func (h *Hub) consume(w *worker.Worker) {
	defer h.wg.Done()
	for fr, err := range w.Exec.Events(h.base) {
		if err != nil {
			h.log.Warn("hub: worker stream", "worker", w.ID, "err", err)
			continue
		}
		switch fr.Event {
		case wire.EventAck:
			var ack wire.Ack
			if err := fr.Decode(&ack); err != nil {
				h.log.Warn("hub: malformed ack", "worker", w.ID, "err", err)
				continue
			}
			h.onAck(ack)
		case wire.EventDisconnected:
			var d wire.Disconnected
			_ = fr.Decode(&d)
			h.disconnected(w, d)
		}
	}
	if h.base.Err() == nil {
		h.router.Remove(w.ID)
		h.disconnected(w, wire.Disconnected{Reason: "the worker's event stream ended", Lost: true})
	}
}

// disconnected records that w's link ended (worker.disconnected) and,
// when its runs ended with it, loses them.
func (h *Hub) disconnected(w *worker.Worker, d wire.Disconnected) {
	var lost []string
	if d.Lost {
		h.mu.Lock()
		var fs []*flight
		for _, f := range h.flight {
			if f.worker == w {
				fs = append(fs, f)
				lost = append(lost, f.runID)
			}
		}
		h.mu.Unlock()
		for _, f := range fs {
			s := f.scope
			h.end(f, func(tx store.Tx) error { return h.c.Sink.Lost(tx, s, f.runID, "the worker disconnected: "+d.Reason) })
		}
	}
	err := h.c.DB.InTx(context.Background(), func(tx store.Tx) error {
		_, err := h.c.Events.Emit(tx, w.Scope, events.Event{Type: events.WorkerDisconnected, ResourceKind: "worker", ResourceID: w.ID},
			map[string]any{"worker": w.ID, "build_digest": w.Registration.Build.Digest, "reason": d.Reason, "lost_runs": lost})
		return err
	})
	if err != nil {
		h.log.Warn("hub: recording a disconnect failed", "worker", w.ID, "err", err)
	}
}

func (h *Hub) onAck(ack wire.Ack) {
	h.mu.Lock()
	f := h.flight[ack.CommandID]
	h.mu.Unlock()
	if f == nil {
		return // not ours (any more): a late ack after an end or a restart
	}
	s := f.scope
	if ack.State == wire.AckRunStarted || ack.State == wire.AckHeartbeat || ack.State == wire.AckAccepted {
		h.mu.Lock()
		f.heard = time.Now()
		h.mu.Unlock()
	}
	switch ack.State {
	case wire.AckRunStarted:
		if err := h.c.DB.InTx(context.Background(), func(tx store.Tx) error { return h.c.Sink.Started(tx, s, f.runID) }); err != nil {
			h.log.Warn("hub: run started", "run", f.runID, "err", err)
		}
	case wire.AckHeartbeat:
		h.extend(f)
	case wire.AckFinished:
		h.end(f, func(tx store.Tx) error { return h.c.Sink.Finished(tx, s, f.runID, ack) })
	case wire.AckRejected:
		h.end(f, func(tx store.Tx) error { return h.c.Sink.Rejected(tx, s, f.runID, ack.Error) })
	}
}

func (h *Hub) extend(f *flight) {
	h.mu.Lock()
	l := f.lease
	h.mu.Unlock()
	err := h.c.Queue.Extend(context.Background(), &l, h.c.LeaseTTL)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		f.lease = l
		return
	}
	if errors.Is(err, queue.ErrLeaseLost) {
		h.log.Warn("hub: lease lost while the run was in flight", "run", f.runID)
	}
}

// silence is how long f may go unheard before its run is lost.
func (h *Hub) silence(f *flight) time.Duration {
	return max(h.c.Silence, 3*time.Duration(f.worker.Registration.HeartbeatMS)*time.Millisecond)
}

// extendLoop keeps the leases of in-flight runs alive between
// heartbeats (a lease outlives this node by at most one TTL), and loses
// a run whose worker went silent on it.
func (h *Hub) extendLoop(ctx context.Context) {
	t := time.NewTicker(max(h.c.LeaseTTL/3, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.mu.Lock()
		fs := make([]*flight, 0, len(h.flight))
		var silent []*flight
		for _, f := range h.flight {
			if time.Since(f.heard) > h.silence(f) {
				silent = append(silent, f)
				continue
			}
			fs = append(fs, f)
		}
		h.mu.Unlock()
		for _, f := range fs {
			h.extend(f)
		}
		for _, f := range silent {
			s, why := f.scope, fmt.Sprintf("no ack from the worker in %s", h.silence(f))
			h.end(f, func(tx store.Tx) error { return h.c.Sink.Lost(tx, s, f.runID, why) })
		}
	}
}

// CancelRun sends a cancel to the worker holding runID (sessions'
// Canceler). A run not in flight on this node is ignored.
func (h *Hub) CancelRun(ctx context.Context, _ tenant.Scope, runID, cause string) error {
	h.mu.Lock()
	f := h.flight[h.byRun[runID]]
	h.mu.Unlock()
	if f == nil {
		return nil
	}
	return f.worker.Exec.Cancel(ctx, wire.Cancel{CommandID: wire.NewCommandID(), RunID: runID, Cause: cause})
}

// SteerRun delivers user messages to the worker holding runID
// (sessions' Steerer). A run not in a worker's hands on this node, or
// whose worker refused the steer, is ErrNotHeld.
func (h *Hub) SteerRun(ctx context.Context, _ tenant.Scope, runID string, messages []json.RawMessage) error {
	h.mu.Lock()
	f := h.flight[h.byRun[runID]]
	h.mu.Unlock()
	if f == nil {
		return fmt.Errorf("%w: %s", ErrNotHeld, runID)
	}
	ack, err := f.worker.Exec.Steer(ctx, wire.Steer{CommandID: wire.NewCommandID(), RunID: runID, Messages: messages})
	if err != nil {
		return err
	}
	if ack.State != wire.AckAccepted {
		return fmt.Errorf("%w: the worker refused the steer: %s", ErrNotHeld, ack.Error)
	}
	return nil
}

// InFlight is the number of runs in workers' hands on this node.
func (h *Hub) InFlight() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.flight)
}

// Close drains every attached worker by deadline (their last acks are
// recorded), then stops consuming. Call it after Run returned and
// before the stores close.
func (h *Hub) Close(ctx context.Context, deadline time.Time) error {
	var errs []error
	for _, w := range h.router.Workers() {
		if err := w.Exec.Drain(ctx, deadline); err != nil {
			errs = append(errs, err)
		}
	}
	// Let the consumers record the acks the drain produced; a worker
	// that never answers (gone) is given up a second past the deadline:
	// its runs' leases expire and the next claim marks them lost.
	giveUp := time.Now().Add(time.Until(deadline) + time.Second)
	for h.InFlight() > 0 && time.Now().Before(giveUp) && ctx.Err() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	h.stop()
	h.wg.Wait()
	return errors.Join(errs...)
}
