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

	hub  *Hub
	sel  Selector
	c    chan Event
	done chan struct{} // closed when the subscription ends

	mu        sync.Mutex
	last      int64
	replaying bool
	missed    bool // a live row arrived during the replay: list again before going live
	closed    bool
	err       error
}

// Subscribe opens a subscription receiving the events sel matches with
// seq > after: the stored ones first (a reconnect's replay), then live
// ones, each once, in seq order. It returns at once; the replay streams
// in the background and waits for the reader, however long it is, so
// it never overflows. buffer bounds what live rows may queue for a
// reader that falls behind once the replay is done (default 256): a
// full buffer ends the subscription with ErrOverflow. A replay that
// fails (or whose ctx ends) ends the subscription with that error.
func (h *Hub) Subscribe(ctx context.Context, sel Selector, after int64, buffer int) (*Sub, error) {
	if buffer <= 0 {
		buffer = 256
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &Sub{hub: h, sel: sel, c: make(chan Event, buffer), done: make(chan struct{}), last: after, replaying: true}
	s.C = s.c
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	go s.replay(ctx)
	return s, nil
}

// replayPage is how many stored rows one replay read returns.
const replayPage = 500

// replay sends the stored rows after the cursor, blocking on the reader,
// then hands the subscription to the live pump. Live rows that arrive
// meanwhile are not queued (they are in the store): they only make the
// replay read once more before it goes live, so nothing is held in
// memory however far behind the replay is. While replaying, this
// goroutine is the only writer of s.c and the one that closes it.
func (s *Sub) replay(ctx context.Context) {
	for {
		evs, err := s.hub.log.List(ctx, s.sel, s.lastSeen(), replayPage)
		if err != nil {
			s.stopReplay(err)
			return
		}
		for _, e := range evs {
			if !s.sel.Match(e) {
				s.skip(e.Seq)
				continue
			}
			select { // an ended subscription sends nothing more
			case <-s.done:
				s.stopReplay(nil)
				return
			default:
			}
			select {
			case s.c <- e:
				s.skip(e.Seq)
			case <-s.done:
				s.stopReplay(nil)
				return
			case <-ctx.Done():
				s.stopReplay(ctx.Err())
				return
			}
		}
		if len(evs) == replayPage {
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			s.stopReplay(nil)
			return
		}
		if s.missed {
			// Rows committed after the read above: read them too.
			s.missed = false
			s.mu.Unlock()
			continue
		}
		s.replaying = false
		s.mu.Unlock()
		return
	}
}

// stopReplay ends a subscription from its replay goroutine: it records
// err (unless already ended) and closes s.c, which only it may do while
// replaying.
func (s *Sub) stopReplay(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed, s.err = true, err
		close(s.done)
		go s.hub.drop(s)
	}
	s.replaying = false
	close(s.c)
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

// push is a live delivery. During the replay it only marks that the
// replay must read again (the row is stored).
func (s *Sub) push(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replaying {
		if e.Seq > s.last {
			s.missed = true
		}
		return
	}
	s.sendLocked(e)
}

// sendLocked sends a live e unless already sent; a full buffer ends the
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

// endLocked marks the subscription ended. s.c is closed here unless the
// replay goroutine still writes it; that goroutine sees done and closes
// it itself.
func (s *Sub) endLocked(err error) {
	if s.closed {
		return
	}
	s.closed, s.err = true, err
	close(s.done)
	if !s.replaying {
		close(s.c)
	}
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
