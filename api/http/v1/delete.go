package v1

import (
	"errors"
	"net/http"

	"github.com/weftbase/weftbase/obsdb"
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
	Note      string `json:"note"`
}

// serveDeleteRun answers DELETE /v1/runs/{id}: the run and its
// subagent children (run ids carry slashes: everything after
// /v1/runs/ is the id). A later read is 404. A live stream following
// the deleted run emits nothing more unless the app keeps writing to
// it; a later write starts a new run row under the same id.
func (s *Server) serveDeleteRun(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.db.DeleteRun(r.Context(), id); err != nil {
		deleteError(w, r, "run", id, err)
		return
	}
	writeJSON(w, r, http.StatusOK, deleteDoc{Deleted: "run", RunID: id, Note: deleteRunNote})
}

// serveDeleteSession answers DELETE /v1/sessions/{id}: the session's
// observability rows — never the app's conversation store.
func (s *Server) serveDeleteSession(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.db.DeleteSession(r.Context(), id); err != nil {
		deleteError(w, r, "session", id, err)
		return
	}
	writeJSON(w, r, http.StatusOK, deleteDoc{Deleted: "session", SessionID: id, Note: deleteSessionNote})
}

func deleteError(w http.ResponseWriter, r *http.Request, noun, id string, err error) {
	if errors.Is(err, obsdb.ErrNotFound) {
		notFound(w, r, "no "+noun+" "+id)
		return
	}
	writeError(w, r, http.StatusInternalServerError, "internal", "delete "+noun+" "+id+": "+err.Error())
}
