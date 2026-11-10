package worker

import (
	"slices"
	"sync"
	"time"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/tenant"
)

// Worker is one attached executor and the AgentVersions it serves.
type Worker struct {
	ID           string
	Exec         Executor
	Registration wire.Registration
	// Versions are the agv_… ids its registration resolved to.
	Versions []string
	// Scope is the scope it registered in.
	Scope tenant.Scope
	// Capacity bounds the runs of a version (agv_… id) the worker takes
	// at once: its registration's per-agent Concurrency. A version
	// absent (or 0) has no bound of its own.
	Capacity map[string]int

	inflight int
	held     map[string]int       // runs in hand per version
	paused   map[string]time.Time // a version the worker said busy for, until
}

// room reports whether w may take one more run of agv at now. r.mu is
// held.
func (w *Worker) room(agv string, now time.Time) bool {
	if now.Before(w.paused[agv]) {
		return false
	}
	c := w.Capacity[agv]
	return c <= 0 || w.held[agv] < c
}

// Router routes a run to a worker by (AgentVersion, lease holder)
// (P§6.3): only workers that registered the run's version are
// eligible; a run- or session-scoped command goes to the worker that
// holds it; a new start goes to the eligible worker with the fewest
// runs in hand. Agent name alone never routes.
type Router struct {
	mu      sync.Mutex
	workers []*Worker
}

// Add attaches w (replacing a worker of the same id).
func (r *Router) Add(w *Worker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers = slices.DeleteFunc(r.workers, func(x *Worker) bool { return x.ID == w.ID })
	r.workers = append(r.workers, w)
}

// Remove detaches a worker.
func (r *Router) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers = slices.DeleteFunc(r.workers, func(x *Worker) bool { return x.ID == id })
}

// Workers lists the attached workers.
func (r *Router) Workers() []*Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.workers)
}

// Ready is every AgentVersion some attached worker serves and has room
// for at now (under its capacity, not paused): the routes a claim may
// take. A route whose workers are all full is not claimed, so its items
// keep their place in the queue until a slot frees.
func (r *Router) Ready(now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, w := range r.workers {
		for _, v := range w.Versions {
			if w.room(v, now) && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Versions is every AgentVersion some attached worker serves.
func (r *Router) Versions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, w := range r.workers {
		for _, v := range w.Versions {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Pick chooses the worker for a command on version agv. holder, when
// set, is the worker that holds the run or session: it is chosen when
// it serves agv, and nothing else is. With no holder the eligible
// worker with room (Ready) and the fewest runs in hand is chosen. ok is
// false when no attached worker serves agv (with room, for a new
// start).
func (r *Router) Pick(agv, holder string) (*Worker, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var best *Worker
	for _, w := range r.workers {
		if !slices.Contains(w.Versions, agv) {
			continue
		}
		if holder != "" {
			if w.ID == holder {
				return w, true
			}
			continue
		}
		if !w.room(agv, now) {
			continue
		}
		if best == nil || w.inflight < best.inflight {
			best = w
		}
	}
	return best, best != nil
}

// Hold counts a run of agv in w's hands (+1) or out of them (-1). A run
// leaving w's hands ends a pause of agv: a slot is free.
func (r *Router) Hold(w *Worker, agv string, delta int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.inflight += delta
	if w.held == nil {
		w.held = map[string]int{}
	}
	w.held[agv] += delta
	if delta < 0 {
		delete(w.paused, agv)
	}
}

// Pause takes agv off w's routes until until, or until a run of agv
// leaves w's hands: w answered busy (it is full by its own count, or
// not ready).
func (r *Router) Pause(w *Worker, agv string, until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w.paused == nil {
		w.paused = map[string]time.Time{}
	}
	w.paused[agv] = until
}
