package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weftbase/internal/httpx"
)

// serveHealthz is liveness: the process answers.
func (rt *Router) serveHealthz(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// readyTimeout bounds one readiness check.
const readyTimeout = 5 * time.Second

// serveReadyz is readiness: the stores answer (Config.Ready).
func (rt *Router) serveReadyz(w http.ResponseWriter, r *http.Request) {
	if rt.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := rt.ready(ctx); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "not ready: "+err.Error())
			return
		}
	}
	httpx.WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
}

// metrics counts the requests each route answered, by status code.
type metrics struct {
	mu       sync.Mutex
	reqs     map[[2]string]int64 // {route, code} → count
	inFlight map[string]int64    // route → requests being served (a stream counts until it ends)
}

func newMetrics() *metrics {
	return &metrics{reqs: map[[2]string]int64{}, inFlight: map[string]int64{}}
}

// begin counts a request in; the returned func counts it out.
func (m *metrics) begin(route string) func() {
	m.mu.Lock()
	m.inFlight[route]++
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.inFlight[route]--; m.inFlight[route] == 0 {
			delete(m.inFlight, route)
		}
		m.mu.Unlock()
	}
}

func (m *metrics) request(route string, status int) {
	m.mu.Lock()
	m.reqs[[2]string{route, fmt.Sprint(status)}]++
	m.mu.Unlock()
}

// serveMetrics is the Prometheus text exposition: the requests each
// route answered by status, and the OTLP batches this node accepted
// and refused (internal/reqlog). Counters reset with the process.
func (rt *Router) serveMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("# HELP weftbase_http_requests_total HTTP requests answered, by route and status code.\n")
	b.WriteString("# TYPE weftbase_http_requests_total counter\n")
	rt.metrics.mu.Lock()
	keys := make([][2]string, 0, len(rt.metrics.reqs))
	for k := range rt.metrics.reqs {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int {
		if c := strings.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return strings.Compare(a[1], b[1])
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "weftbase_http_requests_total{route=%q,code=%q} %d\n", k[0], k[1], rt.metrics.reqs[k])
	}
	// The requests being served now: an SSE stream is one for as long
	// as its client holds it, so this is where open streams are counted.
	b.WriteString("# HELP weftbase_http_requests_in_flight HTTP requests being served, by route (an open stream counts).\n")
	b.WriteString("# TYPE weftbase_http_requests_in_flight gauge\n")
	routes := make([]string, 0, len(rt.metrics.inFlight))
	for k := range rt.metrics.inFlight {
		routes = append(routes, k)
	}
	slices.Sort(routes)
	for _, k := range routes {
		fmt.Fprintf(&b, "weftbase_http_requests_in_flight{route=%q} %d\n", k, rt.metrics.inFlight[k])
	}
	rt.metrics.mu.Unlock()
	in := rt.reqs.Ingest()
	b.WriteString("# HELP weftbase_ingest_batches_total OTLP batches answered, by outcome.\n")
	b.WriteString("# TYPE weftbase_ingest_batches_total counter\n")
	fmt.Fprintf(&b, "weftbase_ingest_batches_total{outcome=\"accepted\"} %d\n", in.Accepted)
	fmt.Fprintf(&b, "weftbase_ingest_batches_total{outcome=\"refused\"} %d\n", in.Refused)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(b.String()))
}

// countingWriter remembers the status a handler answered, keeping the
// Flusher (SSE) when the underlying writer has one.
type countingWriter struct {
	http.ResponseWriter
	code int
}

func (c *countingWriter) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.ResponseWriter.Write(b)
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *countingWriter) status() int {
	if c.code == 0 {
		return http.StatusOK
	}
	return c.code
}

type flushingWriter struct{ *countingWriter }

func (f flushingWriter) Flush() {
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// writer is c as an http.ResponseWriter that flushes when the
// underlying one does.
func (c *countingWriter) writer() http.ResponseWriter {
	if _, ok := c.ResponseWriter.(http.Flusher); ok {
		return flushingWriter{c}
	}
	return c
}
