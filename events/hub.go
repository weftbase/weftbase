package events

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/weftbase/weftbase/tenant"
)

// ErrOverflow ends a subscription whose buffer filled: the subscriber
// fell behind and reconnects with its last seq (SSE Last-Event-ID).
var ErrOverflow = errors.New("events: subscriber overflow")

// Selector says which events a subscriber receives. Project and Env
// always match exactly; Tenant too unless AllTenants. Empty Types,
// ResourceKind and ResourceID match everything.
type Selector struct {
	Project, Env, Tenant string
	AllTenants           bool
	Types                []string
	ResourceKind         string
	ResourceID           string
}

// ForScope selects the events of one scope. allTenants widens it to
// every tenant of the scope's project and env (a project-level
// credential).
func ForScope(s tenant.Scope, allTenants bool) Selector {
	return Selector{Project: s.Project(), Env: s.Env(), Tenant: s.Tenant(), AllTenants: allTenants}
}

// Match reports whether sel admits e. Every replayed and live row is
// re-checked against the subscriber's scope.
func (sel Selector) Match(e Event) bool {
	if e.ProjectID != sel.Project || e.Env != sel.Env {
		return false
	}
	if !sel.AllTenants && e.TenantID != sel.Tenant {
		return false
	}
	if len(sel.Types) > 0 && !slices.Contains(sel.Types, e.Type) {
		return false
	}
	if sel.ResourceKind != "" && e.ResourceKind != sel.ResourceKind {
		return false
	}
	return sel.ResourceID == "" || e.ResourceID == sel.ResourceID
}

// Hub fans committed events out to subscribers. Run pumps it: a commit
// on this node wakes it at once, a commit elsewhere is found by the
// poll.
type Hub struct {
	log *Log

	mu        sync.Mutex
	subs      map[*Sub]struct{}
	notifiers map[chan struct{}]struct{}
}

func newHub(l *Log) *Hub {
	return &Hub{log: l, subs: map[*Sub]struct{}{}, notifiers: map[chan struct{}]struct{}{}}
}

// wake signals every waiter without blocking.
func (h *Hub) wake() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.notifiers {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) notify() chan struct{} {
	c := make(chan struct{}, 1)
	h.mu.Lock()
	h.notifiers[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) unnotify(c chan struct{}) {
	h.mu.Lock()
	delete(h.notifiers, c)
	h.mu.Unlock()
}

// Run delivers new rows to the subscribers until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	wake := h.notify()
	defer h.unnotify(wake)
	cur, err := h.log.Head(ctx)
	if err != nil {
		return err
	}
	// A subscription opened before Run finished its replay at some seq;
	// rows committed after that and before the Head read above would
	// reach it neither by replay nor live. Start at the lowest such
	// cursor: a subscriber drops what it already has (sendLocked).
	if low := h.lowestCursor(); low < cur {
		cur = low
	}
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		for {
			evs, err := h.log.After(ctx, cur, 500)
			if err != nil || len(evs) == 0 {
				break
			}
			for _, e := range evs {
				h.deliver(e)
			}
			cur = evs[len(evs)-1].Seq
		}
		select {
		case <-ctx.Done():
			h.closeAll(ctx.Err())
			return ctx.Err()
		case <-wake:
		case <-t.C:
		}
	}
}

// lowestCursor is the smallest last-sent seq among open subscriptions,
// or a very large value when there are none.
func (h *Hub) lowestCursor() int64 {
	h.mu.Lock()
	subs := make([]*Sub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	low := int64(1<<63 - 1)
	for _, s := range subs {
		if l := s.lastSeen(); l < low {
			low = l
		}
	}
	return low
}

func (h *Hub) deliver(e Event) {
	h.mu.Lock()
	subs := make([]*Sub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		if s.sel.Match(e) {
			s.push(e)
		}
	}
}

func (h *Hub) closeAll(err error) {
	h.mu.Lock()
	subs := make([]*Sub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.end(err)
	}
}

// Sub is one subscription. C closes when it ends; Err says why.
type Sub struct {
	C <-chan Event

	hub *Hub
	sel Selector
	c   chan Event

	mu        sync.Mutex
	last      int64
	replaying bool
	pending   []Event
	closed    bool
	err       error
}

// Subscribe opens a subscription receiving the events sel matches with
// seq > after: the stored ones first (a reconnect's replay), then live
// ones, each once, in seq order. buffer bounds what may queue before
// the subscription overflows (default 256).
func (h *Hub) Subscribe(ctx context.Context, sel Selector, after int64, buffer int) (*Sub, error) {
	if buffer <= 0 {
		buffer = 256
	}
	s := &Sub{hub: h, sel: sel, c: make(chan Event, buffer), last: after, replaying: true}
	s.C = s.c
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	for {
		evs, err := h.log.List(ctx, sel, s.lastSeen(), 500)
		if err != nil {
			s.Close()
			return nil, err
		}
		for _, e := range evs {
			if sel.Match(e) {
				s.pushReplay(e)
			} else {
				s.skip(e.Seq)
			}
		}
		if len(evs) < 500 {
			break
		}
	}
	s.finishReplay()
	return s, nil
}

func (s *Sub) lastSeen() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Sub) skip(seq int64) {
	s.mu.Lock()
	if seq > s.last {
		s.last = seq
	}
	s.mu.Unlock()
}

// push is a live delivery: queued while the replay runs.
func (s *Sub) push(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replaying {
		s.pending = append(s.pending, e)
		return
	}
	s.sendLocked(e)
}

func (s *Sub) pushReplay(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendLocked(e)
}

func (s *Sub) finishReplay() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replaying = false
	for _, e := range s.pending {
		s.sendLocked(e)
	}
	s.pending = nil
}

// sendLocked sends e unless already sent; a full buffer ends the
// subscription with ErrOverflow.
func (s *Sub) sendLocked(e Event) {
	if s.closed || e.Seq <= s.last {
		return
	}
	select {
	case s.c <- e:
		s.last = e.Seq
	default:
		s.endLocked(ErrOverflow)
	}
}

func (s *Sub) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endLocked(err)
}

func (s *Sub) endLocked(err error) {
	if s.closed {
		return
	}
	s.closed, s.err = true, err
	close(s.c)
	go s.hub.drop(s)
}

func (h *Hub) drop(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Err is why the subscription ended (nil while open or after Close).
func (s *Sub) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the subscription.
func (s *Sub) Close() { s.end(nil) }
