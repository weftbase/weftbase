package v1

import (
	"errors"
	"net/http"

	"github.com/weftbase/weftbase/authz"
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
func (s *Server) serveDeleteRun(w http.ResponseWriter, r *http.Request, id string) {
	// The whole logical run goes: every run of its chain (a parked run
	// and what resumed it), their subagent children, the ledger rows
	// and the approval records. A run the platform still has in hand —
	// queued, running, waiting on a person — is refused: cancel first.
	ctl := s.control(r)
	cost := s.coster(r.Context())
	of, ok := ctl.read(cost, id)
	if !ok {
		notFound(w, r, "no run "+id)
		return
	}
	members := []runRow{of}
	ids := []string{id}
	if ch := ctl.chain(cost, of); ch != nil {
		members, ids = members[:0], ids[:0]
		for _, m := range ch.Runs {
			ids = append(ids, m.ID)
			if row, ok := ctl.read(cost, m.ID); ok {
				members = append(members, row)
			}
		}
	}
	if !s.deletable(w, r, "run", members) {
		return
	}
	doc := deleteDoc{Deleted: "run", RunID: id, RunIDs: ids, Note: deleteRunNote}
	found := false
	for _, run := range ids {
		switch err := s.db.DeleteRun(r.Context(), run); {
		case err == nil:
			found = true
		case !errors.Is(err, obsdb.ErrNotFound):
			deleteError(w, r, "run", run, err)
			return
		}
	}
	if !s.deleteLedger(w, r, ids, &doc, found) {
		return
	}
	writeJSON(w, r, http.StatusOK, doc)
}

// deletable refuses (409 run_active) when a run of rows is one the
// ledger still has in hand. A run only the trace store knows is never
// refused: nothing here runs it.
func (s *Server) deletable(w http.ResponseWriter, r *http.Request, noun string, rows []runRow) bool {
	for _, row := range rows {
		if row.AgentVersion == nil {
			continue
		}
		switch row.DisplayStatus {
		case displayQueued, displayRunning, displayWaiting:
			writeError(w, r, http.StatusConflict, "run_active", "run "+row.ID+" is "+row.DisplayStatus+
				": cancel it (POST /v1/runs/{id}/cancel) before deleting the "+noun)
			return false
		}
	}
	return true
}

// deleteLedger removes the runs' ledger rows and approvals; false when
// it answered (an error, or nothing anywhere to delete).
func (s *Server) deleteLedger(w http.ResponseWriter, r *http.Request, ids []string, doc *deleteDoc, found bool) bool {
	if s.ledger != nil {
		if _, sc, _ := authz.From(r.Context()); !sc.IsZero() {
			runs, approvals, err := s.ledger.Delete(r.Context(), sc, actor(r), ids)
			switch {
			case errors.Is(err, sessions.ErrActive):
				writeError(w, r, http.StatusConflict, "run_active", err.Error())
				return false
			case err != nil:
				controlError(w, r, err)
				return false
			}
			doc.LedgerRuns, doc.Approvals = runs, approvals
			found = found || runs > 0
		}
	}
	if !found {
		notFound(w, r, "nothing to delete under "+doc.RunID+doc.SessionID)
		return false
	}
	return true
}

func (s *Server) serveDeleteSession(w http.ResponseWriter, r *http.Request, id string) {
	// The session's runs go from the ledger too, with their approvals;
	// refused while one of them is still in hand.
	det, err := s.db.Session(r.Context(), id)
	if err != nil {
		deleteError(w, r, "session", id, err)
		return
	}
	rows := make([]runRow, 0, len(det.Runs))
	ids := make([]string, 0, len(det.Runs))
	for _, rr := range det.Runs {
		rows = append(rows, row(rr))
		ids = append(ids, rr.ID)
	}
	s.control(r).decorate(rows)
	if !s.deletable(w, r, "session", rows) {
		return
	}
	if err := s.db.DeleteSession(r.Context(), id); err != nil {
		deleteError(w, r, "session", id, err)
		return
	}
	doc := deleteDoc{Deleted: "session", SessionID: id, RunIDs: ids, Note: deleteSessionNote}
	if !s.deleteLedger(w, r, ids, &doc, true) {
		return
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
