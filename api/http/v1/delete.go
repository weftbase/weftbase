package v1

import (
	"errors"
	"net/http"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/sessions"
)

// Deletion (plan A7, §13.13): DELETE /v1/runs/{id} and
// DELETE /v1/sessions/{id}, like the kernel's retention sweeper, touch
// obsdb's tables only — a SQLite file that also holds thread's sessions
// keeps them byte-identical: the app's conversation store is the app's.

// Notes the two verbs answer with: what was deleted, said plainly.
const (
	deleteRunNote = "the run's observability rows (spans, records, app logs, search index) and its subagent children's " +
		"are deleted from weftbase's database, with the runs derived from stock spans under its tools"
	deleteSessionNote = "the session's observability rows (every run's spans, records, app logs, search index, " +
		"children included) are deleted from weftbase's database; the app's conversation store is untouched"
)

// deleteDoc is both verbs' 200 body.
type deleteDoc struct {
	Deleted   string `json:"deleted"` // "run" | "session"
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// RunIDs are the runs removed: the run's whole chain (a parked run
	// and its resumes), or the session's runs. LedgerRuns and Approvals
	// count the control-store rows that went with them.
	RunIDs     []string `json:"run_ids,omitempty"`
	LedgerRuns int      `json:"ledger_runs"`
	Approvals  int      `json:"approvals"`
	Note       string   `json:"note"`
}

// serveDeleteRun answers DELETE /v1/runs/{id}: the run and its
// subagent children (run ids carry slashes: everything after
// /v1/runs/ is the id). A later read is 404. A live stream following
// the deleted run emits nothing more unless the app keeps writing to
// it; a later write starts a new run row under the same id.
//
// The whole logical run goes: every run of its chain (a parked run and
// what resumed it), their subagent children, the ledger rows and the
// approval records. The ledger goes first, in one transaction that
// refuses a run the platform still has in hand — queued, running,
// waiting on a person (409 run_active: cancel first) — and writes the
// audited run.deleted (a run only the trace store knows is audited
// too); the trace store's rows after it. A retry of a delete the trace
// store failed finds the ledger done and finishes.
func (s *Server) serveDeleteRun(w http.ResponseWriter, r *http.Request, id string) {
	ctl := s.control(r)
	cost := s.coster(r.Context())
	of, ok := ctl.read(cost, id)
	ids := []string{id}
	if ok {
		if ch := ctl.chain(cost, of); ch != nil {
			ids = ids[:0]
			for _, m := range ch.Runs {
				ids = append(ids, m.ID)
			}
		}
	}
	if ctl.unavailable(w, r) {
		return
	}
	if !ok || !s.owns(w, r, ctl, ids) {
		if !ok {
			notFound(w, r, "no run "+id)
		}
		return
	}
	doc := deleteDoc{Deleted: "run", RunID: id, RunIDs: ids, Note: deleteRunNote}
	if ctl.controlled() {
		runs, approvals, err := s.ledger.Delete(r.Context(), ctl.sc, actor(r), ids)
		if !deleteRefused(w, r, err) {
			return
		}
		doc.LedgerRuns, doc.Approvals = runs, approvals
	}
	for _, run := range ids {
		if err := s.db.DeleteRun(r.Context(), run); err != nil && !errors.Is(err, obsdb.ErrNotFound) {
			deleteError(w, r, "run", run, err)
			return
		}
	}
	writeJSON(w, r, http.StatusOK, doc)
}

// owns refuses (404, as for a run that does not exist) a delete that
// names a run another tenant holds in the ledger: the trace store is
// not tenant-scoped yet and cannot tell, the ledger can. A
// project-level caller owns every tenant's runs.
func (s *Server) owns(w http.ResponseWriter, r *http.Request, ctl *control, ids []string) bool {
	if !ctl.controlled() || ctl.sc.ProjectLevel() {
		return true
	}
	owners, err := s.ledger.Owners(r.Context(), ctl.sc, ids)
	if err != nil {
		ctl.fail(err)
		ctl.unavailable(w, r)
		return false
	}
	for _, id := range ids {
		if t, ok := owners[id]; ok && t != ctl.sc.Tenant() {
			notFound(w, r, "no run "+ids[0])
			return false
		}
	}
	return true
}

// deleteRefused answers a failed ledger delete; true when it did not
// fail.
func deleteRefused(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, sessions.ErrActive):
		writeError(w, r, http.StatusConflict, "run_active", err.Error()+": cancel it (POST /v1/runs/{id}/cancel) first")
	default:
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the control store could not delete: "+err.Error())
	}
	return false
}

// serveDeleteSession answers DELETE /v1/sessions/{id}: every run of the
// session, the queued ones no trace names yet included, ledger first
// (one transaction: refused with 409 run_active while a run of it is in
// hand, so no turn admitted meanwhile is left half-deleted; audited
// session.deleted), then the trace store's rows.
func (s *Server) serveDeleteSession(w http.ResponseWriter, r *http.Request, id string) {
	ctl := s.control(r)
	det, err := s.db.Session(r.Context(), id)
	traced := err == nil
	if err != nil && !errors.Is(err, obsdb.ErrNotFound) {
		deleteError(w, r, "session", id, err)
		return
	}
	var ids []string
	for _, rr := range det.Runs {
		ids = append(ids, rr.ID)
	}
	if !s.owns(w, r, ctl, ids) {
		return
	}
	doc := deleteDoc{Deleted: "session", SessionID: id, Note: deleteSessionNote}
	if ctl.controlled() {
		all, runs, approvals, err := s.ledger.DeleteSession(r.Context(), ctl.sc, actor(r), id, ids)
		if !deleteRefused(w, r, err) {
			return
		}
		ids, doc.LedgerRuns, doc.Approvals = all, runs, approvals
	}
	if len(ids) == 0 {
		notFound(w, r, "no session "+id)
		return
	}
	doc.RunIDs = ids
	if traced {
		if err := s.db.DeleteSession(r.Context(), id); err != nil && !errors.Is(err, obsdb.ErrNotFound) {
			deleteError(w, r, "session", id, err)
			return
		}
	}
	writeJSON(w, r, http.StatusOK, doc)
}

func deleteError(w http.ResponseWriter, r *http.Request, noun, id string, err error) {
	if errors.Is(err, obsdb.ErrNotFound) {
		notFound(w, r, "no "+noun+" "+id)
		return
	}
	writeError(w, r, http.StatusInternalServerError, "internal", "delete "+noun+" "+id+": "+err.Error())
}
