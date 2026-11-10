package v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
)

// The Phase 1 write surface: agents and their versions (registry),
// labels (promote), and the run ledger's admit, cancel and steer
// (sessions). A parked run resumes through its approvals
// (POST /v1/approvals/{id}/decision), never on its own route. Without
// the registry or the ledger (a read-only server) these routes answer
// 501.

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

// The run state conflicts: each its own code and a stable message (the
// run's id is the request's; the detail is in the server log).
var stateConflicts = []struct {
	err       error
	code, msg string
}{
	{sessions.ErrFinished, "already_finished", "the run has already finished"},
	{sessions.ErrAlreadyResumed, "already_resumed", "the parked run was resumed already: act on the run that resumed it (resumed_by)"},
	{sessions.ErrNotParked, "not_parked", "the run is not parked"},
	{sessions.ErrNotRunning, "not_running", "the run is not running"},
	{sessions.ErrSessionBusy, "session_busy", "the session has a turn queued, running or waiting on a person: a new turn starts when it has ended"},
}

// controlError maps a domain error onto the API's error codes.
func controlError(w http.ResponseWriter, r *http.Request, err error) {
	for _, c := range stateConflicts {
		if errors.Is(err, c.err) {
			writeError(w, r, http.StatusConflict, c.code, c.msg)
			return
		}
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, r, err.Error())
	case errors.Is(err, store.ErrConflict):
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

// ledgerRunDoc is a ledger row on the wire (POST /v1/runs, GET
// /v1/runs/{id}/status, POST /v1/runs/{id}/cancel): agent_version as
// every document carries it ({id, number, labels}).
type ledgerRunDoc struct {
	sessions.Run
	AgentVersion *versionRef `json:"agent_version"`
}

func (s *Server) ledgerDoc(r *http.Request, run sessions.Run) ledgerRunDoc {
	return ledgerRunDoc{Run: run, AgentVersion: s.control(r).versionRef(run.Agent, run.AgentVersion)}
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
	// session_id makes the run the session's next turn (the ledger
	// refuses it while the previous turn is in hand, in the admitting
	// transaction). A caller that sends only the new message
	// (input.prompt) gets the conversation so far put before it: what
	// the session's newest run saw and said, read from the trace store.
	if req.SessionID != "" {
		turn, history, ok := s.sessionContext(w, r, req)
		if !ok {
			return
		}
		req.TraceTurn = turn
		if len(history) > 0 {
			next, _ := json.Marshal(core.User(req.Input.Prompt))
			req.Input = wire.Input{Messages: append(history, next)}
		}
	}
	run, err := s.ledger.Admit(r.Context(), sc, actor(r), req)
	if err != nil {
		controlError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusAccepted, s.ledgerDoc(r, run))
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
	writeJSON(w, r, http.StatusOK, s.ledgerDoc(r, run))
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
	writeJSON(w, r, http.StatusAccepted, s.ledgerDoc(r, run))
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

// settleWait is how long after a turn ended the next turn waits for its
// trace before taking the history from the turn before it: a turn that
// ended with no trace at all (lost before its worker wrote one) has
// nothing to wait for.
const settleWait = 30 * time.Second

// sessionContext reads what the next turn of a session needs from the
// trace store: the highest turn it holds (the ledger numbers the turn
// past it) and, for a caller that sends only input.prompt, the
// conversation so far — everything the session's newest run was given
// and answered. It answers the request itself (false) when the session
// is busy (409 session_busy), when the newest turn's trace has not all
// arrived (409 session_settling: building on half a turn would lose it),
// or when a store cannot be read (503 unavailable): never a guess.
func (s *Server) sessionContext(w http.ResponseWriter, r *http.Request, req sessions.AdmitRequest) (turn int, history []json.RawMessage, ok bool) {
	_, sc, _ := authz.From(r.Context())
	unavailable := func(err error) (int, []json.RawMessage, bool) {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the session could not be read: "+err.Error())
		return 0, nil, false
	}
	settling := func(id string) (int, []json.RawMessage, bool) {
		writeError(w, r, http.StatusConflict, "session_settling",
			"the session's newest turn ("+id+") has ended but its trace has not all arrived: retry in a moment")
		return 0, nil, false
	}
	det, err := s.db.Session(r.Context(), req.SessionID)
	if err != nil && !errors.Is(err, obsdb.ErrNotFound) {
		return unavailable(err)
	}
	var newest *obsdb.RunRow
	for i := range det.Runs {
		rr := &det.Runs[i]
		turn = max(turn, rr.Turn)
		if newest == nil || rr.Started.After(newest.Started) {
			newest = rr
		}
	}
	last, held, err := s.ledger.SessionLast(r.Context(), sc, req.SessionID)
	if err != nil {
		return unavailable(err)
	}
	if held && !sessions.Terminal(last.Status) && (last.Status != sessions.StatusParked || last.ResumedBy == "") {
		controlError(w, r, sessions.ErrSessionBusy)
		return 0, nil, false
	}
	if newest != nil {
		ctl := s.control(r)
		one := []runRow{row(*newest)}
		ctl.decorate(one)
		if ctl.unavailable(w, r) {
			return 0, nil, false
		}
		if st := one[0].DisplayStatus; st == displayRunning || st == displayQueued || st == displayWaiting {
			controlError(w, r, sessions.ErrSessionBusy)
			return 0, nil, false
		}
	}
	if req.Input.Prompt == "" || len(req.Input.Messages) > 0 {
		return turn, nil, true
	}
	// The run the history comes from: the ledger's newest turn when the
	// trace store has all of it, else wait for it (session_settling) —
	// unless it ended long enough ago with no trace at all.
	source := newest
	if held && (newest == nil || !last.CreatedAt.Before(newest.Started)) {
		var traced *obsdb.RunRow
		for i := range det.Runs {
			if det.Runs[i].ID == last.ID {
				traced = &det.Runs[i]
			}
		}
		switch {
		case traced != nil:
			source = traced
		case last.StartedAt != nil && last.FinishedAt != nil && s.now().Sub(*last.FinishedAt) < settleWait:
			return settling(last.ID)
		}
	}
	if source == nil {
		return turn, nil, true
	}
	if source.Settling || source.Finished == nil {
		return settling(source.ID)
	}
	batches, err := s.db.TranscriptBatches(r.Context(), source.ID)
	if err != nil {
		return unavailable(err)
	}
	for _, b := range batches {
		var msgs []json.RawMessage
		if json.Unmarshal(b.Messages, &msgs) == nil {
			history = append(history, msgs...)
		}
	}
	return turn, history, true
}
