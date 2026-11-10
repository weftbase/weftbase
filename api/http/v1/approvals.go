package v1

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/sessions"
)

// Approvals (P§10, first slice): every call a run parked on is one
// approval of the run ledger. The inbox lists them (pending first), a
// decision binds to the arguments its decider saw (args_sha256), and
// the run resumes once every approval of it is decided.

// approvalRoutes are the approval operations.
func (s *Server) approvalRoutes() []Route {
	return []Route{
		{"GET", "/v1/approvals", authz.ApprovalsRead, s.serveApprovals},
		{"GET", "/v1/approvals/{id}", authz.ApprovalsRead, withID("id", s.serveApproval)},
		{"POST", "/v1/approvals/{id}/decision", authz.ApprovalsDecide, withID("id", s.serveApprovalDecision)},
	}
}

// approvalError maps the approval errors onto their codes; the rest are
// the control surface's.
func approvalError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sessions.ErrArgsChanged):
		writeError(w, r, http.StatusConflict, "args_changed", err.Error())
	case errors.Is(err, sessions.ErrNotPending):
		writeError(w, r, http.StatusConflict, "not_pending", err.Error())
	default:
		controlError(w, r, err)
	}
}

// serveApprovals is GET /v1/approvals: pending first (oldest request
// first), then decided (newest decision first). status is pending,
// decided, or one status (approved, denied, resolved, expired,
// cancelled); agent, tool and run narrow; limit (100, at most 500) and
// offset page.
func (s *Server) serveApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.needLedger(w, r) {
		return
	}
	q := r.URL.Query()
	aq := sessions.ApprovalQuery{Status: q.Get("status"), Agent: q.Get("agent"), Tool: q.Get("tool"), RunID: q.Get("run")}
	switch aq.Status {
	case "", "pending", "decided", sessions.ApprovalApproved, sessions.ApprovalDenied, sessions.ApprovalResolved,
		sessions.ApprovalExpired, sessions.ApprovalCancelled:
	default:
		badRequest(w, r, "status is pending, decided, approved, denied, resolved, expired or cancelled")
		return
	}
	for name, dst := range map[string]*int{"limit": &aq.Limit, "offset": &aq.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				badRequest(w, r, name+" is a non-negative integer")
				return
			}
			*dst = n
		}
	}
	_, sc, _ := authz.From(r.Context())
	page, err := s.ledger.Approvals(r.Context(), sc, aq)
	if err != nil {
		approvalError(w, r, err)
		return
	}
	ctl := s.control(r)
	out := approvalsPage{Approvals: make([]approvalDoc, 0, len(page.Approvals)), Total: page.Total, Pending: page.Pending, NextOffset: page.NextOffset}
	for _, a := range page.Approvals {
		out.Approvals = append(out.Approvals, ctl.approval(a))
	}
	if ctl.unavailable(w, r) {
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

// approvalDoc is an approval on the wire: the ledger's record, with its
// AgentVersion as run rows carry it (id, number, the labels it holds
// now).
type approvalDoc struct {
	sessions.Approval
	AgentVersion versionRef `json:"agent_version"`
}

type approvalsPage struct {
	Approvals  []approvalDoc `json:"approvals"`
	Total      int           `json:"total"`
	Pending    int           `json:"pending"`
	NextOffset *int          `json:"next_offset"`
}

func (c *control) approval(a sessions.Approval) approvalDoc {
	return approvalDoc{Approval: a, AgentVersion: *c.versionRef(a.Agent, a.AgentVersion)}
}

func (c *control) approvalDocs(as []sessions.Approval) []approvalDoc {
	if as == nil {
		return nil
	}
	out := make([]approvalDoc, len(as))
	for i, a := range as {
		out[i] = c.approval(a)
	}
	return out
}

// serveApproval is GET /v1/approvals/{id}.
func (s *Server) serveApproval(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	a, err := s.ledger.Approval(r.Context(), sc, id)
	if err != nil {
		approvalError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.control(r).approval(a))
}

// serveApprovalDecision is POST /v1/approvals/{id}/decision: approve,
// deny, or resolve (content is the result the model reads; the tool
// does not run), bound to the arguments by args_sha256 (409 args_changed on
// another hash). 200 with the approval; resumed_by names the run that
// continues the parked one once this was its last pending approval. The
// same decision again is a no-op (200); another one on a decided
// approval is 409 not_pending.
func (s *Server) serveApprovalDecision(w http.ResponseWriter, r *http.Request, id string) {
	if !s.needLedger(w, r) {
		return
	}
	var body struct {
		Decision   string `json:"decision"`
		Reason     string `json:"reason"`
		Content    string `json:"content"`
		ArgsSHA256 string `json:"args_sha256"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, sc, _ := authz.From(r.Context())
	a, err := s.ledger.Decide(r.Context(), sc, actor(r), id, body.Decision, body.Reason, body.Content, body.ArgsSHA256)
	if err != nil {
		approvalError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, s.control(r).approval(a))
}
