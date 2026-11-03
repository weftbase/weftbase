package worker

import (
	"slices"
	"sync"

	"github.com/weftbase/weftbase/client/wire"
)

// Worker is one attached executor and the AgentVersions it serves.
type Worker struct {
	ID           string
	Exec         Executor
	Registration wire.Registration
	// Versions are the agv_… ids its registration resolved to.
	Versions []string

	inflight int
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

// Versions is every AgentVersion some attached worker serves: the
// routes a claim may take.
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
// worker with the fewest runs in hand is chosen. ok is false when no
// attached worker serves agv.
func (r *Router) Pick(agv, holder string) (*Worker, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
		if best == nil || w.inflight < best.inflight {
			best = w
		}
	}
	return best, best != nil
}

// Hold counts a run in w's hands (+1) or out of them (-1).
func (r *Router) Hold(w *Worker, delta int) {
	r.mu.Lock()
	w.inflight += delta
	r.mu.Unlock()
}
