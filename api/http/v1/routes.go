package v1

import (
	"net/http"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/observe/ingest"
)

// The route registry: the server's surface is a list of route groups,
// and api/meta's capabilities are computed from that list — never
// hard-coded. New registers the groups below.

// routeGroup is one capability's block of routes: a name for errors
// and logs, the capability api/meta reports when the group is
// registered ("" for groups that are always present and name nothing),
// and the registration that mounts its routes on the server's mux.
type routeGroup struct {
	name       string
	capability string
	register   func(mux *http.ServeMux, s *Server)
}

// addGroup registers g's routes and records the group for
// meta.capabilities.
func (s *Server) addGroup(g routeGroup) {
	if g.register != nil {
		g.register(s.mux, s)
	}
	s.groups = append(s.groups, g)
}

// runRoute is one /api/runs/{id}/<ext> sub-route: run ids contain
// slashes, so these cannot be mux patterns of their own, and a group
// registers them through addRunRoute instead.
type runRoute struct {
	ext   string
	serve func(http.ResponseWriter, *http.Request, string)
	// item, when set, serves /api/runs/{id}/<ext>/{item} instead: one
	// item of a run's collection (a step by its ordinal).
	item func(w http.ResponseWriter, r *http.Request, id, item string)
}

// addRunRoute registers a GET /api/runs/{id}/<ext> sub-route, which
// serveRunRoutes dispatches by suffix. So a child run id whose last
// segment equals a sub-route name (events, transcript, spans, requests,
// tools, export, logs) is shadowed by that route: its run document is unreachable
// at /api/runs/{id}. A child id's last segment is the provider's tool
// call id, and providers' ids never take those shapes.
func (s *Server) addRunRoute(ext string, serve func(http.ResponseWriter, *http.Request, string)) {
	s.runRoutes = append(s.runRoutes, runRoute{ext: ext, serve: serve})
}

// addRunItemRoute registers a GET /api/runs/{id}/<ext>/{item} sub-route
// (one item of a run's collection: a step by its ordinal), which
// serveRunRoutes dispatches when the path's next-to-last segment is
// ext. The same shadowing caveat as addRunRoute's applies to a child
// run id whose last two segments read <ext>/<item>; a child id's
// segments are <step>/<callID>, so its next-to-last is a number.
func (s *Server) addRunItemRoute(ext string, serve func(w http.ResponseWriter, r *http.Request, id, item string)) {
	s.runRoutes = append(s.runRoutes, runRoute{ext: ext, item: serve})
}

// capabilityList names what the registered groups provide plus
// anything the hosting wrapper declared with Capabilities(...): the
// UI gates every deployment-specific screen on these names
// (ADR 0018 §8).
func (s *Server) capabilityList() []string {
	caps := make([]string, 0, len(s.groups)+len(s.capabilities))
	for _, g := range s.groups {
		if g.capability != "" {
			caps = append(caps, g.capability)
		}
	}
	caps = append(caps, s.capabilities...)
	return caps
}

// registerGroups mounts the route groups.
func (s *Server) registerGroups() {
	// The read API: meta, manifest, runs, run detail, events,
	// transcript, spans, traces, sessions, public-id resolution (and
	// its reverse, sessions/{id}/public_id).
	// Always present, so it names no capability.
	s.addGroup(routeGroup{
		name:     "api",
		register: registerReadAPI,
	})
	// The request record's read routes (ADR 0028 §10, A1): every
	// model-call attempt with the prompt and catalog it named, and the
	// run's catalogs. Always present now that obsdb.DB reads them; the
	// capability is what the UIs gate the Request pane on.
	s.addGroup(routeGroup{
		name:       "requests",
		capability: "requests",
		register: func(_ *http.ServeMux, s *Server) {
			s.addRunRoute("requests", s.serveRunRequests)
			s.addRunRoute("tools", s.serveRunTools)
		},
	})
	// One step assembled server-side (plan A7, A4's attempts, A10's
	// children): always present; the capability is what the UIs gate
	// the step card's single read on.
	s.addGroup(routeGroup{
		name:       "steps",
		capability: "steps",
		register: func(_ *http.ServeMux, s *Server) {
			s.addRunItemRoute("steps", s.serveRunStep)
		},
	})
	// The run export (plan A7): the whole run as one download, in
	// json, jsonl, otlp or wefttest. Always present; the capability is
	// what the UIs gate their export buttons on.
	s.addGroup(routeGroup{
		name:       "export",
		capability: "export",
		register: func(_ *http.ServeMux, s *Server) {
			s.addRunRoute("export", s.serveRunExport)
		},
	})
	// The run's app logs (plan A7): the non-weft log records attributed
	// to the run through its spans. Always present; the capability is
	// what the UIs gate their logs pane on.
	s.addGroup(routeGroup{
		name:       "logs",
		capability: "logs",
		register: func(_ *http.ServeMux, s *Server) {
			s.addRunRoute("logs", s.serveRunLogs)
		},
	})
	// The step-aligned diff of two runs (plan E3.1): every step of
	// both assembled as the step route assembles it. Always present;
	// the capability is what the UIs gate their compare views on.
	s.addGroup(routeGroup{
		name:       "diff",
		capability: "diff",
		register: func(mux *http.ServeMux, s *Server) {
			mux.HandleFunc("GET /api/diff", s.serveDiff)
		},
	})
	// The trace list (plan A7): every trace the database holds, one
	// row each. Always present; the capability is what the trace page
	// and the command palette gate their list on.
	s.addGroup(routeGroup{
		name:       "traces",
		capability: "traces",
		register: func(mux *http.ServeMux, s *Server) {
			mux.HandleFunc("GET /api/traces", s.serveTraces)
		},
	})
	// Search over runs (plan A7): present when the database can
	// search (obsdb.Searcher — both first-party backends); otherwise
	// meta.capabilities_off says why.
	if _, ok := s.db.(obsdb.Searcher); ok {
		s.addGroup(routeGroup{
			name:       "search",
			capability: "search",
			register: func(mux *http.ServeMux, s *Server) {
				mux.HandleFunc("GET /api/search", s.serveSearch)
			},
		})
	}
	// Aggregates (UX plan A4, D7): GET /api/stats, and the error_class
	// facet and filter on /api/search and /api/runs. Present when the
	// database aggregates (obsdb.Aggregator — both first-party
	// backends); otherwise meta.capabilities_off says why.
	if _, ok := s.db.(obsdb.Aggregator); ok {
		s.addGroup(statsGroupRoute())
	}
	// The manifest store (U27): GET /api/manifests/{hash}, the agent
	// configuration a run's manifest_hash names. Present when the
	// database stores manifests (obsdb.ManifestStore — both first-party
	// backends); otherwise meta.capabilities_off says why.
	if _, ok := s.db.(obsdb.ManifestStore); ok {
		s.addGroup(manifestsGroup())
	}
	// The run tree (U9.1): GET /api/runs/{id}/spans?tree=1, a run's
	// spans with every descendant run's. The route is the read API's
	// spans route; the capability says the flag is served — present
	// when the database reads run trees (obsdb.TreeReader — both
	// first-party backends), otherwise meta.capabilities_off says why
	// and the flag answers 501.
	if _, ok := s.db.(obsdb.TreeReader); ok {
		s.addGroup(routeGroup{name: "spans_tree", capability: "spans_tree"})
	}
	// The session timeline (U12.2): GET /api/sessions/{id}/timeline and
	// /entries, a session's turns beside its state log. The routes are
	// the sessions route's sub-routes (serveSessionRoutes); the
	// capability says they are served — present when the database reads
	// session events (obsdb.SessionEventReader — both first-party
	// backends), otherwise meta.capabilities_off says why and both
	// answer 501.
	if _, ok := s.db.(obsdb.SessionEventReader); ok {
		s.addGroup(routeGroup{name: "timeline", capability: "timeline"})
	}
	// The pricing table (plan A5): GET /api/settings/pricing, the
	// effective table read-only. Always present; cost itself rides the
	// rows, priced at read time.
	s.addGroup(pricingGroup())
	// Deletion (plan A7, §13.13): DELETE /api/runs/{id} and
	// /api/sessions/{id}, obsdb's tables only. Always present.
	s.addGroup(deleteGroup())
	// The live stream.
	s.addGroup(routeGroup{
		name:       "live",
		capability: "live",
		register: func(mux *http.ServeMux, s *Server) {
			mux.HandleFunc("GET /api/live", s.serveLive)
		},
	})
	// The tag filters: tag.<k>=<v> and flow= (run metadata weft.flow)
	// on /api/runs, /api/sessions, /api/search, /api/traces and
	// /api/live (a selector over run frames, alone or narrowing
	// another). The routes are the read API's and the live group's; the
	// capability says the parameters are served. Always present.
	s.addGroup(routeGroup{name: "scope_tags", capability: "scope_tags"})
	// OTLP ingest, unless NoIngest turned the receiver off.
	if !s.noIngest {
		s.addGroup(routeGroup{
			name:       "ingest",
			capability: "ingest",
			register:   registerIngest,
		})
	}
}

// serveAPINotFound answers any registered-group path the groups do
// not cover.
func (s *Server) serveAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "not_found",
		"no such api route "+r.URL.Path)
}

// registerReadAPI mounts the read routes. Run ids contain slashes
// (a subagent's child id is <parent>/<step>/<callID>), so the run
// subtree is one handler that takes everything after /api/runs/ as
// the id, with at most one trailing segment a group registered through
// addRunRoute (/events, /transcript, /spans; /requests and /tools from
// the requests group); session, trace and public ids never carry one.
func registerReadAPI(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/meta", s.serveMeta)
	mux.HandleFunc("GET /api/manifest", s.serveManifest)
	mux.HandleFunc("GET /api/runs", s.serveRuns)
	mux.HandleFunc("GET /api/runs/", s.serveRunRoutes)
	mux.HandleFunc("GET /api/sessions", s.serveSessions)
	mux.HandleFunc("GET /api/sessions/", s.serveSessionRoutes) // {id} and {id}/public_id
	mux.HandleFunc("GET /api/traces/", s.serveTrace)
	mux.HandleFunc("GET /api/public/", s.servePublic)
	s.addRunRoute("events", s.serveRunEvents)
	s.addRunRoute("transcript", s.serveRunTranscript)
	s.addRunRoute("spans", s.serveRunSpans)
}

// registerIngest mounts the OTLP/HTTP receiver (observe/ingest) at
// /otlp/v1/{traces,logs}: OTEL_EXPORTER_OTLP_ENDPOINT is <url>/otlp.
func registerIngest(mux *http.ServeMux, s *Server) {
	ok := s.ingestAuthorized
	mux.HandleFunc("POST /otlp/v1/traces", ingest.Traces(s.db, s.live, ok))
	// The logs receiver publishes through the health hub: heartbeats'
	// drop counts feed api/meta's ingest health (observe/ingest).
	mux.HandleFunc("POST /otlp/v1/logs", ingest.Logs(s.db, s.ingestHealth.Hub(s.live), ok))
}
