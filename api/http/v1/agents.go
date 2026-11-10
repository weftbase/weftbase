package v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
)

// The Phase 1 write surface: agents and their versions (registry),
// labels (promote), and the run ledger's admit, cancel and resume
// (sessions). Without the registry or the ledger (a read-only server)
// these routes answer 501.

// Registry serves the agent routes from reg.
func Registry(reg *registry.Registry) Option { return func(c *config) { c.registry = reg } }

// Ledger serves the run admission routes from l.
func Ledger(l *sessions.Ledger) Option { return func(c *config) { c.ledger = l } }

// maxBody bounds a write request's JSON body.
const maxBody = 1 << 20

// controlRoutes are the agent and run-admission operations.
func (s *Server) controlRoutes() []Route {
	return []Route{
		{"GET", "/v1/agents", authz.AgentsRead, s.serveAgents},
		{"GET", "/v1/agents/{name}/versions", authz.AgentsRead, withID("name", s.serveAgentVersions)},
		{"POST", "/v1/agents/{name}/labels/{label}", authz.AgentsWrite, s.servePromote},
		{"POST", "/v1/runs", authz.RunsCreate, s.serveAdmit},
		{"GET", "/v1/runs/{id}/status", authz.RunsRead, withID("id", s.serveRunStatus)},
		{"POST", "/v1/runs/{id}/cancel", authz.RunsCancel, withID("id", s.serveCancel)},
		{"POST", "/v1/runs/{id}/steer", authz.RunsSteer, withID("id", s.serveSteer)},
		{"POST", "/v1/runs/{id}/resume", authz.RunsCreate, withID("id", s.serveResume)},
	}
}

// actor names the caller for an event.
func actor(r *http.Request) events.Actor {
	p, _, _ := authz.From(r.Context())
	return events.Actor{Kind: string(p.Kind), ID: p.ID, Credential: p.Credential, Assurance: p.Assurance}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		badRequest(w, r, "the body is unreadable or over 1 MiB")
		return false
	}
	if len(b) == 0 {
		b = []byte("{}")
	}
	if err := json.Unmarshal(b, v); err != nil {
		badRequest(w, r, "the body is not the expected JSON: "+err.Error())
		return false
	}
	return true
}

// controlError maps a domain error onto the API's error codes.
func controlError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, r, err.Error())
	case errors.Is(err, store.ErrConflict), errors.Is(err, sessions.ErrFinished), errors.Is(err, sessions.ErrNotParked),
		errors.Is(err, sessions.ErrNotRunning):
		writeError(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, sessions.ErrInvalidSteer):
		writeError(w, r, http.StatusBadRequest, "invalid_steer", err.Error())
	case errors.Is(err, sessions.ErrInvalid), errors.Is(err, registry.ErrInvalid):
		badRequest(w, r, err.Error())
	case errors.Is(err, sessions.ErrNotSupported):
		writeError(w, r, http.StatusNotImplemented, "not_implemented", err.Error())
	default:
		writeError(w, r, http.StatusInternalServerError, "internal", err.Error())
	}
}

func (s *Server) needRegistry(w http.ResponseWriter, r *http.Request) bool {
	if s.registry == nil {
		notImplemented(w, r, "this server has no agent registry")
		return false
	}
	return true
}

func (s *Server) needLedger(w http.ResponseWriter, r *http.Request) bool {
	if s.ledger == nil {
		notImplemented(w, r, "this server runs no agents")
		return false
	}
	return true
}

// serveAgents is GET /v1/agents.
func (s *Server) serveAgents(w http.ResponseWriter, r *http.Request) {
	if !s.needRegistry(w, r) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	agents, err := s.registry.Agents(r.Context(), sc)
	if err != nil {
		controlError(w, r, err)
		return
	}
	if agents == nil {
		agents = []registry.Agent{}
	}
	switch r.URL.Query().Get("include") {
	case "":
	case "summary":
		s.serveAgentsSummary(w, r, agents)
		return
	default:
		badRequest(w, r, "include is summary")
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"agents": agents})
}

// serveAgentVersions is GET /v1/agents/{name}/versions.
func (s *Server) serveAgentVersions(w http.ResponseWriter, r *http.Request, name string) {
	if !s.needRegistry(w, r) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	vs, err := s.registry.Versions(r.Context(), sc, name)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"agent": name, "versions": vs})
}

// servePromote is POST /v1/agents/{name}/labels/{label}: move a label
// (P§5.4). expected must name what the label holds ("" for a new
// label); a mismatch is 409.
func (s *Server) servePromote(w http.ResponseWriter, r *http.Request) {
	if !s.needRegistry(w, r) {
		return
	}
	var body struct {
		Version  string `json:"version"`
		Expected string `json:"expected"`
		Reason   string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	p := registry.PromoteRequest{Agent: r.PathValue("name"), Label: r.PathValue("label"), Version: body.Version,
		Expected: body.Expected, Reason: body.Reason, Actor: actor(r)}
	if err := s.registry.PromoteIn(r.Context(), sc, p); err != nil {
		controlError(w, r, err)
		return
	}
	v, err := s.registry.Version(r.Context(), sc, body.Version)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, v)
}

// serveAdmit is POST /v1/runs: admit a run (202, queued).
func (s *Server) serveAdmit(w http.ResponseWriter, r *http.Request) {
	if !s.needLedger(w, r) {
		return
	}
	var req sessions.AdmitRequest
	if !decodeBody(w, r, &req) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	run, err := s.ledger.Admit(r.Context(), sc, actor(r), req)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, run)
}

// serveRunStatus is GET /v1/runs/{id}/status: the run's ledger row.
func (s *Server) serveRunStatus(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	run, err := s.ledger.Get(r.Context(), sc, id)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, run)
}

// serveCancel is POST /v1/runs/{id}/cancel (P§7.5): 202, or 409 for a
// run that has finished.
func (s *Server) serveCancel(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	run, err := s.ledger.Cancel(r.Context(), sc, actor(r), id, body.Reason)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, run)
}

// serveSteer is POST /v1/runs/{id}/steer (P§7.5): user messages for a
// running run, delivered at its next drain point. 202; 409 when the run
// is not running; 400 invalid_steer for a message that is not a user
// message.
func (s *Server) serveSteer(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	if err := s.ledger.Steer(r.Context(), sc, actor(r), id, body.Messages); err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, map[string]any{"run_id": id, "steered": len(body.Messages)})
}

// serveResume is POST /v1/runs/{id}/resume (P§7.6, explicit): a new
// run on the parked run's pinned version, with decisions.
func (s *Server) serveResume(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	var body struct {
		Decisions []wire.Decision `json:"decisions"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	run, err := s.ledger.Resume(r.Context(), sc, actor(r), id, body.Decisions)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, run)
}
