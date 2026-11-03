package v1

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/events"
)

// GET /v1/events: the control store's outbox as a resource stream
// (plan §6.4). Each SSE frame is one event: `id:` is its seq, `event:`
// its type, `data:` the event as JSON. A reconnect sends Last-Event-ID
// (or ?after=) and gets every stored event after it, then the live
// ones, each once, in seq order; every replayed and live row is matched
// against the caller's scope (events.Selector), never against what
// the query asks. ?type= (repeatable or comma-separated),
// ?resource_kind= and ?resource_id= narrow the stream. A subscriber
// that falls behind gets `event: overflow` and the stream ends: it
// reconnects with its last id.

// eventsBuffer bounds what may queue for one subscriber.
const eventsBuffer = 256

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		notImplemented(w, r, "this server has no control store outbox")
		return
	}
	p, sc, ok := authz.From(r.Context())
	if !ok || sc.IsZero() {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	sel := events.ForScope(sc, p.AllTenants)
	q := r.URL.Query()
	for _, v := range q["type"] {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				sel.Types = append(sel.Types, t)
			}
		}
	}
	sel.ResourceKind, sel.ResourceID = q.Get("resource_kind"), q.Get("resource_id")
	var after int64
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = q.Get("after")
	}
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, r, "Last-Event-ID (or after) must be an event seq (a non-negative integer)")
			return
		}
		after = n
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, r, http.StatusInternalServerError, "internal", "streaming is not supported by this connection")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	sub, err := s.events.Hub().Subscribe(ctx, sel, after, eventsBuffer)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal", "subscribe: "+err.Error())
		return
	}
	defer sub.Close()
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w)}
	sw.flush()
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for sw.err == nil {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sw.frame("event: ping\ndata: {}\n\n")
			sw.flush()
		case e, open := <-sub.C:
			if !open {
				if sub.Err() != nil && ctx.Err() == nil {
					sw.frame("event: overflow\ndata: {}\n\n")
					sw.flush()
				}
				return
			}
			// The hub matched the row against sel already; the scope
			// is checked once more here, on the row as delivered.
			if !sel.Match(e) {
				continue
			}
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			sw.frame("id: %d\nevent: %s\ndata: %s\n\n", e.Seq, sseField(e.Type), b)
			sw.flush()
		}
	}
}

// sseField keeps a value on one SSE line.
func sseField(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}
