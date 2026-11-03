package v1

import (
	"net/http"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/observe/ingest"
)

// Route is one operation: a ServeMux pattern (method and path), the
// authz action a caller needs, and its handler. Ids in paths are
// percent-encoded by clients (a child run id carries "/"); the mux
// decodes a {wildcard} once (r.PathValue).
type Route struct {
	Method  string
	Pattern string
	Action  authz.Action
	Handler http.HandlerFunc
}

// withID adapts a handler of one path id.
func withID(name string, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(w, r, r.PathValue(name)) }
}

// Routes is the read surface, the streams and deletion.
func (s *Server) Routes() []Route {
	run := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc { return withID("id", h) }
	return []Route{
		{"GET", "/v1/meta", authz.MetaRead, s.serveMeta},
		{"GET", "/v1/manifest", authz.AgentsRead, s.serveManifest},
		{"GET", "/v1/manifests/{hash}", authz.AgentsRead, s.serveStoredManifest},
		{"GET", "/v1/settings/pricing", authz.SettingsRead, s.servePricing},

		{"GET", "/v1/runs", authz.RunsRead, s.serveRunsOrSearch},
		{"GET", "/v1/runs/stream", authz.RunsRead, s.serveLive},
		{"GET", "/v1/runs/{id}", authz.RunsRead, run(s.serveRun)},
		{"DELETE", "/v1/runs/{id}", authz.RunsDelete, run(s.serveDeleteRun)},
		{"GET", "/v1/runs/{id}/events", authz.RunsContent, run(s.serveRunEvents)},
		{"GET", "/v1/runs/{id}/transcript", authz.RunsContent, run(s.serveRunTranscript)},
		{"GET", "/v1/runs/{id}/spans", authz.RunsRead, run(s.serveRunSpans)},
		{"GET", "/v1/runs/{id}/steps/{n}", authz.RunsContent, func(w http.ResponseWriter, r *http.Request) {
			s.serveRunStep(w, r, r.PathValue("id"), r.PathValue("n"))
		}},
		{"GET", "/v1/runs/{id}/requests", authz.RunsContent, run(s.serveRunRequests)},
		{"GET", "/v1/runs/{id}/tools", authz.RunsContent, run(s.serveRunTools)},
		{"GET", "/v1/runs/{id}/logs", authz.RunsContent, run(s.serveRunLogs)},
		{"GET", "/v1/runs/{id}/export", authz.RunsContent, run(s.serveRunExport)},
		{"GET", "/v1/runs/{id}/stream", authz.RunsContent, run(s.serveRunStream)},

		{"GET", "/v1/sessions", authz.SessionsRead, s.serveSessions},
		{"GET", "/v1/sessions/{id}", authz.SessionsRead, run(s.serveSession)},
		{"DELETE", "/v1/sessions/{id}", authz.SessionsWrite, run(s.serveDeleteSession)},
		{"GET", "/v1/sessions/{id}/public_id", authz.SessionsRead, run(s.serveSessionPublicID)},
		{"GET", "/v1/sessions/{id}/timeline", authz.SessionsRead, run(s.serveSessionTimeline)},
		{"GET", "/v1/sessions/{id}/entries", authz.SessionsRead, run(s.serveSessionEntries)},
		{"GET", "/v1/sessions/{id}/export", authz.RunsContent, run(s.serveSessionExport)},
		{"GET", "/v1/sessions/{id}/stream", authz.RunsContent, run(s.serveSessionStream)},
		{"GET", "/v1/public/{id}", authz.SessionsRead, run(s.servePublic)},

		{"GET", "/v1/traces", authz.TracesRead, s.serveTraces},
		{"GET", "/v1/traces/{id}", authz.TracesRead, run(s.serveTrace)},
		{"GET", "/v1/stats", authz.RunsRead, s.serveStats},
		{"GET", "/v1/diff", authz.RunsContent, s.serveDiff},

		{"GET", "/v1/events", authz.EventsRead, s.serveEvents},
	}
}

// IngestRoutes is the OTLP/HTTP receiver (observe/ingest) at
// /otlp/v1/{traces,logs}: OTEL_EXPORTER_OTLP_ENDPOINT is <url>/otlp.
// The router has authenticated the caller (ingest.write) before a
// batch reaches the receiver.
func (s *Server) IngestRoutes() []Route {
	authorized := func(*http.Request) bool { return true }
	return []Route{
		{"POST", "/otlp/v1/traces", authz.IngestWrite, ingest.Traces(s.db, s.live, authorized)},
		// The logs receiver publishes through the health hub: heartbeats'
		// drop counts feed meta's ingest health (observe/ingest).
		{"POST", "/otlp/v1/logs", authz.IngestWrite, ingest.Logs(s.db, s.ingestHealth.Hub(s.live), authorized)},
	}
}

// notImplemented answers a route the database cannot serve.
func notImplemented(w http.ResponseWriter, r *http.Request, msg string) {
	writeError(w, r, http.StatusNotImplemented, "unsupported", msg)
}

// serveRunsOrSearch is GET /v1/runs: the cursor-paged list, or — when
// any search parameter is present (q, tool, model, error, since,
// until, min_tokens, min_cost, sort, offset) — the search over runs
// (search.go), whose page is offset-paged.
func (s *Server) serveRunsOrSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, k := range searchParams {
		if q.Has(k) {
			if _, ok := s.db.(obsdb.Searcher); !ok {
				notImplemented(w, r, "this database cannot search")
				return
			}
			s.serveSearch(w, r)
			return
		}
	}
	s.serveRuns(w, r)
}

// searchParams are the parameters that make GET /v1/runs a search.
var searchParams = []string{"q", "tool", "model", "error", "since", "until", "min_tokens", "min_cost", "sort", "offset"}

// serveRunStream is GET /v1/runs/{id}/stream: the live stream of one
// run (the run selector of serveLive).
func (s *Server) serveRunStream(w http.ResponseWriter, r *http.Request, id string) {
	s.serveLiveFor(w, r, "run", id)
}

// serveSessionStream is GET /v1/sessions/{id}/stream: the live stream
// of one session's runs.
func (s *Server) serveSessionStream(w http.ResponseWriter, r *http.Request, id string) {
	s.serveLiveFor(w, r, "session", id)
}

// serveLiveFor pins the stream's selector to one run or session: the
// query's own selector parameters are replaced.
func (s *Server) serveLiveFor(w http.ResponseWriter, r *http.Request, key, id string) {
	q := r.URL.Query()
	for _, k := range []string{"run", "session", "public_id", "all"} {
		q.Del(k)
	}
	q.Set(key, id)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	s.serveLive(w, r2)
}

// Why a route answers 501: the database lacks the obsdb interface it
// reads (obsdb/sqlite and obsdb/clickhouse implement all of them).
const (
	offSpansTree = "the database cannot read a run tree: its obsdb.DB does not implement obsdb.TreeReader; GET /v1/runs/{id}/spans?tree=1 answers 501"
	offStats     = "the database cannot aggregate: its obsdb.DB does not implement obsdb.Aggregator; GET /v1/stats and the error_class facet and filter are off"
	offTimeline  = "the database cannot read a session's state log: its obsdb.DB does not implement obsdb.SessionEventReader; GET /v1/sessions/{id}/timeline and /entries answer 501"
)
