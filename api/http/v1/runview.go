package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/tenant"
)

// A run is in two stores: the trace store has what it did (rows
// derived from spans and records), the run ledger what the platform
// decided about it (its AgentVersion, how that was chosen, whether it
// waits in the queue or on a person). This file joins them for the
// reads, so no client derives a run's state itself:
//
//   - display_status is the one word a list draws: queued, running,
//     waiting (parked on a person, not resumed), succeeded, failed,
//     cancelled, interrupted. The ledger's status wins where the run
//     has a ledger row; a run only the trace store knows (an app's own
//     run, a subagent's child) reads its trace row. The cause stays on
//     the row beside it: stopped_by, error_type, cancel_cause, err.
//   - agent_version, resolved_via and placement come from the ledger
//     and the registry (labels as they are now, not as they were).
//   - a chain is one logical run across parks: the parked run, the run
//     that resumed it, and so on. chain carries the totals and the
//     waits between its runs.

// The display statuses.
const (
	displayQueued      = "queued"
	displayRunning     = "running"
	displayWaiting     = "waiting"
	displaySucceeded   = "succeeded"
	displayFailed      = "failed"
	displayCancelled   = "cancelled"
	displayInterrupted = "interrupted"
)

// The approvals outcomes of a logical run.
const (
	approvalsPending  = "pending"
	approvalsExpired  = "expired"
	approvalsDenied   = "denied"
	approvalsApproved = "approved"
)

var displayStatuses = []string{displayQueued, displayRunning, displayWaiting, displaySucceeded, displayFailed,
	displayCancelled, displayInterrupted}

// versionRef is a run's AgentVersion on a row: the labels are the ones
// it holds at read time.
type versionRef struct {
	ID     string   `json:"id"`
	Number int      `json:"number"`
	Labels []string `json:"labels"`
}

// controlFields are what the ledger and the registry add to a run row.
type controlFields struct {
	// DisplayStatus is always set (see the file comment).
	DisplayStatus string `json:"display_status"`
	// AgentVersion, ResolvedVia (label:<l> | latest | pin | migrate)
	// and Placement (embedded | local) are set on a run the ledger
	// admitted; absent on a run only the trace store knows.
	AgentVersion *versionRef `json:"agent_version,omitempty"`
	ResolvedVia  string      `json:"resolved_via,omitempty"`
	Placement    string      `json:"placement,omitempty"`
	// ManifestDrift: the manifest the run reported (manifest_hash) is
	// not the one its AgentVersion registered.
	ManifestDrift bool `json:"manifest_drift,omitempty"`
	// LedgerOnly marks a row the trace store does not hold yet (a
	// queued run): everything a trace would say is empty.
	LedgerOnly bool `json:"ledger_only,omitempty"`
	// Chain is set where the run is one of several a park and its
	// resume link (on a list only with chains=1).
	Chain *chainDoc `json:"chain,omitempty"`
	// ApprovalsOutcome sums up the approvals the logical run waited on
	// (set with its chain): pending while one is open, else expired
	// when one lapsed with nobody deciding (the node denied the call
	// and resumed the run), else denied when a person refused one, else
	// approved. A run can end succeeded with its call never made: this
	// is where that reads.
	ApprovalsOutcome string `json:"approvals_outcome,omitempty"`

	// parkedAt is when the ledger parked the run. The ledger learns of
	// a park from the worker's ack, before the run's last records reach
	// the trace store: until they do the trace row still reads running,
	// with no end and no usage, and the wait starts here.
	parkedAt *time.Time
}

// chainDoc is one logical run across parks: its runs in order, the
// state of the whole (the last run's), the totals, and each wait.
type chainDoc struct {
	Runs          []chainRun `json:"runs"`
	DisplayStatus string     `json:"display_status"`
	// ApprovalsOutcome and the counts sum up the chain's approvals (see
	// the row's approvals_outcome).
	ApprovalsOutcome  string `json:"approvals_outcome,omitempty"`
	ApprovalsExpired  int    `json:"approvals_expired"`
	ApprovalsDenied   int    `json:"approvals_denied"`
	ApprovalsApproved int    `json:"approvals_approved"`
	ApprovalsPending  int    `json:"approvals_pending"`
	// StoppedBy, ErrorType, CancelCause and Err are the last run's: the
	// cause of the chain's state.
	StoppedBy   string     `json:"stopped_by,omitempty"`
	ErrorType   string     `json:"error_type,omitempty"`
	CancelCause string     `json:"cancel_cause,omitempty"`
	Err         string     `json:"err,omitempty"`
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished"`
	// DurationMS is first start to last end (to now while it goes on);
	// WaitingMS the part spent waiting on a person.
	DurationMS int64      `json:"duration_ms"`
	WaitingMS  int64      `json:"waiting_ms"`
	Steps      int        `json:"steps"`
	Usage      core.Usage `json:"usage"`
	// CostUSD sums the runs' own costs; CostKnown is false when any is
	// unknown (the sum is then of the known ones).
	CostUSD   float64   `json:"cost_usd"`
	CostKnown bool      `json:"cost_known"`
	Waits     []waitDoc `json:"waits"`
}

type chainRun struct {
	ID            string     `json:"id"`
	DisplayStatus string     `json:"display_status"`
	Started       time.Time  `json:"started"`
	Finished      *time.Time `json:"finished"`
}

// waitDoc is one wait on a person: after Run parked, until ResumedBy
// started (To null while it lasts).
type waitDoc struct {
	Run       string     `json:"run"`
	ResumedBy string     `json:"resumed_by,omitempty"`
	From      time.Time  `json:"from"`
	To        *time.Time `json:"to"`
	// Approvals are the calls waited on, as the ledger records them;
	// DecidedBy the distinct actors who decided them.
	Approvals []approvalDoc  `json:"approvals"`
	DecidedBy []events.Actor `json:"decided_by"`
}

// control is one request's view of the ledger and the registry.
type control struct {
	s          *Server
	ctx        context.Context
	sc         tenant.Scope
	versions   map[string]registry.Version // by id, of the agents asked about
	agents     map[string]bool
	placements map[string]string
}

func (s *Server) control(r *http.Request) *control {
	_, sc, _ := authz.From(r.Context())
	return &control{s: s, ctx: r.Context(), sc: sc, versions: map[string]registry.Version{}, agents: map[string]bool{}}
}

// ledgerRuns reads the ledger rows of ids (none without a ledger).
func (c *control) ledgerRuns(ids []string) map[string]sessions.Run {
	if c.s.ledger == nil || c.sc.IsZero() {
		return nil
	}
	m, err := c.s.ledger.ByID(c.ctx, c.sc, ids)
	if err != nil {
		return nil
	}
	return m
}

func (c *control) version(agent, id string) (registry.Version, string, bool) {
	if c.s.registry == nil || c.sc.IsZero() {
		return registry.Version{}, "", false
	}
	if !c.agents[agent] {
		c.agents[agent] = true
		if vs, err := c.s.registry.Versions(c.ctx, c.sc, agent); err == nil {
			for _, v := range vs {
				c.versions[v.ID] = v
			}
		}
	}
	if c.placements == nil {
		c.placements, _ = c.s.registry.Placements(c.ctx, c.sc)
		if c.placements == nil {
			c.placements = map[string]string{}
		}
	}
	v, ok := c.versions[id]
	return v, c.placements[v.BuildID], ok
}

// displayStatus is the one word for a run: the ledger's where it has a
// row (lr non-nil), else the trace row's.
func displayStatus(tr *runRow, lr *sessions.Run) string {
	if lr != nil {
		switch lr.Status {
		case sessions.StatusQueued:
			return displayQueued
		case sessions.StatusRunning:
			return displayRunning
		case sessions.StatusParked:
			// A parked run that was resumed is done: its part succeeded
			// (the chain carries on in the resume).
			if lr.ResumedBy != "" {
				return displaySucceeded
			}
			return displayWaiting
		case sessions.StatusSucceeded:
			return displaySucceeded
		case sessions.StatusCanceled:
			return displayCancelled
		default:
			return displayFailed
		}
	}
	switch {
	case tr.ErrorType == "cancelled":
		return displayCancelled
	case tr.Status == string(obsdb.StatusSucceeded) && tr.Pending > 0 && tr.ResumedBy == "":
		return displayWaiting
	}
	return tr.Status
}

// decorate sets the control fields of trace rows.
func (c *control) decorate(rows []runRow) {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.ParentRunID == "" {
			ids = append(ids, r.ID)
		}
	}
	ledger := c.ledgerRuns(ids)
	for i := range rows {
		r := &rows[i]
		lr, ok := ledger[r.ID]
		if !ok {
			r.DisplayStatus = displayStatus(r, nil)
			continue
		}
		c.fromLedger(r, lr)
	}
}

func (c *control) fromLedger(r *runRow, lr sessions.Run) {
	r.DisplayStatus = displayStatus(r, &lr)
	r.ResolvedVia = lr.ResolvedVia
	if lr.Status == sessions.StatusParked {
		r.parkedAt = lr.ParkedAt
		if r.Finished == nil && r.Usage == (core.Usage{}) {
			// What the ack reported, until the trace's own figure lands.
			r.Usage = core.Usage{InputTokens: lr.Usage.InputTokens, OutputTokens: lr.Usage.OutputTokens}
		}
	}
	if lr.ResumedBy != "" && r.ResumedBy == "" {
		r.ResumedBy = lr.ResumedBy // the resume is admitted before any span of it arrives
	}
	if v, placement, ok := c.version(lr.Agent, lr.AgentVersion); ok {
		labels := v.Labels
		if labels == nil {
			labels = []string{}
		}
		r.AgentVersion = &versionRef{ID: v.ID, Number: v.Number, Labels: labels}
		r.Placement = placement
		r.ManifestDrift = r.ManifestHash != "" && v.ManifestHash != "" && r.ManifestHash != v.ManifestHash
	} else {
		r.AgentVersion = &versionRef{ID: lr.AgentVersion, Labels: []string{}}
	}
}

// ledgerRow is the row of a run only the ledger knows (queued: nothing
// of it has been traced).
func (c *control) ledgerRow(lr sessions.Run) runRow {
	r := runRow{ID: lr.ID, Agent: lr.Agent, Meta: map[string]string{}, Started: lr.CreatedAt, LastSeen: lr.CreatedAt,
		Status: lr.Status, ResumedFrom: lr.ResumeOf, ResumedBy: lr.ResumedBy, Err: lr.Error, CancelCause: lr.CancelCause}
	for k, v := range lr.Metadata {
		r.Meta[k] = v
	}
	r.SessionID = lr.Metadata["weft.session.id"]
	if lr.Input != nil {
		r.PromptPreview = inputPreview(*lr.Input)
	}
	if lr.FinishedAt != nil {
		f := *lr.FinishedAt
		r.Finished = &f
	}
	r.Cache = "not_reported"
	r.LedgerOnly = true
	c.fromLedger(&r, lr)
	return r
}

// read is one run's row with its control fields: the trace store's,
// else the ledger's own (a run no trace names yet).
func (c *control) read(cost *coster, id string) (runRow, bool) {
	if det, err := c.s.db.Run(c.ctx, id); err == nil {
		rows := []runRow{cost.row(det.RunRow)}
		c.decorate(rows)
		return rows[0], true
	}
	if lr, ok := c.ledgerRuns([]string{id})[id]; ok {
		return c.ledgerRow(lr), true
	}
	return runRow{}, false
}

// chain builds the chain a run belongs to, from any of its runs; nil
// when the run is alone (never parked, never a resume).
func (c *control) chain(cost *coster, of runRow) *chainDoc {
	if of.ResumedFrom == "" && of.ResumedBy == "" && of.DisplayStatus != displayWaiting {
		return nil
	}
	const maxLinks = 64
	read := func(id string) (runRow, bool) { return c.read(cost, id) }
	runs := []runRow{of}
	for n := 0; runs[0].ResumedFrom != "" && n < maxLinks; n++ {
		prev, ok := read(runs[0].ResumedFrom)
		if !ok {
			break
		}
		runs = append([]runRow{prev}, runs...)
	}
	for n := 0; runs[len(runs)-1].ResumedBy != "" && n < maxLinks; n++ {
		next, ok := read(runs[len(runs)-1].ResumedBy)
		if !ok {
			break
		}
		runs = append(runs, next)
	}
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.ID
	}
	var approvals map[string][]sessions.Approval
	if c.s.ledger != nil && !c.sc.IsZero() {
		approvals, _ = c.s.ledger.RunApprovals(c.ctx, c.sc, ids...)
	}
	last := runs[len(runs)-1]
	doc := &chainDoc{DisplayStatus: last.DisplayStatus, StoppedBy: last.StoppedBy, ErrorType: last.ErrorType,
		CancelCause: last.CancelCause, Err: last.Err, Started: runs[0].Started, CostKnown: true, Waits: []waitDoc{}}
	now := c.s.now()
	end := now
	if last.Finished != nil && last.DisplayStatus != displayWaiting {
		f := *last.Finished
		doc.Finished, end = &f, f
	}
	for i, r := range runs {
		doc.Runs = append(doc.Runs, chainRun{ID: r.ID, DisplayStatus: r.DisplayStatus, Started: r.Started, Finished: r.Finished})
		doc.Steps += r.Steps
		doc.Usage = doc.Usage.Add(r.Usage)
		if r.CostUSD != nil {
			doc.CostUSD += *r.CostUSD
		}
		if !r.CostKnown && !r.LedgerOnly {
			doc.CostKnown = false
		}
		parked := r.DisplayStatus == displayWaiting || r.ResumedBy != ""
		from := r.Finished
		if from == nil {
			from = r.parkedAt
		}
		if !parked || from == nil {
			continue
		}
		w := waitDoc{Run: r.ID, ResumedBy: r.ResumedBy, From: *from, Approvals: c.approvalDocs(approvals[r.ID]), DecidedBy: []events.Actor{}}
		if w.Approvals == nil {
			w.Approvals = []approvalDoc{}
		}
		for _, a := range w.Approvals {
			if a.Actor != nil && !slices.Contains(w.DecidedBy, *a.Actor) {
				w.DecidedBy = append(w.DecidedBy, *a.Actor)
			}
			switch a.Status {
			case sessions.ApprovalPending:
				doc.ApprovalsPending++
			case sessions.ApprovalExpired:
				doc.ApprovalsExpired++
			case sessions.ApprovalDenied:
				doc.ApprovalsDenied++
			case sessions.ApprovalApproved, sessions.ApprovalResolved:
				doc.ApprovalsApproved++
			}
		}
		until := now
		if i+1 < len(runs) && !runs[i+1].LedgerOnly {
			t := runs[i+1].Started
			w.To, until = &t, t
		}
		doc.WaitingMS += max(until.Sub(w.From).Milliseconds(), 0)
		doc.Waits = append(doc.Waits, w)
	}
	switch {
	case doc.ApprovalsPending > 0:
		doc.ApprovalsOutcome = approvalsPending
	case doc.ApprovalsExpired > 0:
		doc.ApprovalsOutcome = approvalsExpired
	case doc.ApprovalsDenied > 0:
		doc.ApprovalsOutcome = approvalsDenied
	case doc.ApprovalsApproved > 0:
		doc.ApprovalsOutcome = approvalsApproved
	}
	doc.DurationMS = max(end.Sub(doc.Started).Milliseconds(), 0)
	doc.CostUSD = round6(doc.CostUSD)
	return doc
}

// displayParam reads ?display_status=: the word, and the trace-store
// filter that finds its candidates (the rows are then held to the
// word). queued has none: those rows are the ledger's.
func displayParam(w http.ResponseWriter, r *http.Request, q map[string][]string) (word string, st obsdb.Status, ok bool) {
	vs := q["display_status"]
	if len(vs) == 0 || vs[0] == "" {
		return "", "", true
	}
	word = vs[0]
	if !slices.Contains(displayStatuses, word) {
		badRequest(w, r, "display_status is queued, running, waiting, succeeded, failed, cancelled or interrupted")
		return "", "", false
	}
	switch word {
	case displayWaiting:
		st = "parked"
	case displayCancelled:
		st = "cancelled"
	case displayQueued:
	default:
		st = obsdb.Status(word)
	}
	return word, st, true
}

// inputPreview is a queued run's prompt preview: its prompt, else its
// last user message's text.
func inputPreview(in wire.Input) string {
	if in.Prompt != "" {
		return obsdb.PromptPreview(in.Prompt)
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		var m core.Message
		if json.Unmarshal(in.Messages[i], &m) == nil && m.Role == core.RoleUser {
			return obsdb.PromptPreview(m.Text())
		}
	}
	return ""
}

// view is how a read of runs is shown, for the list and the search
// alike (display_status, chains and approvals mean the same in both):
//
//   - the control fields on every row (control.decorate);
//   - chains: one row per logical run. Where the read is unfiltered and
//     newest first, a resume is dropped (the run it continues is a row
//     of its own, at its start). Where the read is filtered or sorted
//     (mapped: a status, a search), a chain is shown where its last run
//     is read, as its first run's row; a run that was resumed is
//     skipped. Either way a chain is one row, on one page only;
//   - display: the rows held to a display status (a chain's is its
//     last run's);
//   - approvals: the chains held to the outcome of their approvals.
//
// The trace store pages before any of this, so a reader that asks for
// limit rows keeps reading the store until it has them (fill).
type view struct {
	display   string
	chains    bool
	approvals string // "" | pending | expired | denied | approved
	mapped    bool
}

// folds reports whether the view drops or replaces rows: a page then
// has to be filled, and the store's total is an upper bound.
func (v view) folds() bool { return v.chains || v.display != "" || v.approvals != "" }

// keep is one decorated row through the view: the row to show, or
// false.
func (v view) keep(ctl *control, cost *coster, row runRow) (runRow, bool) {
	if v.chains {
		if v.mapped && row.ResumedBy != "" {
			return row, false // its chain is shown where its last run is
		}
		if !v.mapped && row.ResumedFrom != "" {
			return row, false // its chain's first run is a row of its own
		}
		if ch := ctl.chain(cost, row); ch != nil {
			if ch.Runs[0].ID != row.ID {
				head, ok := ctl.read(cost, ch.Runs[0].ID)
				if !ok {
					return row, false
				}
				row = head
			}
			row.Chain, row.DisplayStatus, row.ApprovalsOutcome = ch, ch.DisplayStatus, ch.ApprovalsOutcome
		}
	}
	if v.display != "" && row.DisplayStatus != v.display {
		return row, false
	}
	if v.approvals != "" && row.ApprovalsOutcome != v.approvals {
		return row, false
	}
	return row, true
}

// viewParams reads display_status, chains and approvals, which the list
// and the search share; display_status sets the trace-store status that
// finds its candidates (status is the caller's own, "" when none was
// given).
func viewParams(w http.ResponseWriter, r *http.Request, q map[string][]string, status *obsdb.Status) (v view, ok bool) {
	display, dst, ok := displayParam(w, r, q)
	if !ok {
		return v, false
	}
	if display != "" {
		if *status != "" {
			badRequest(w, r, "give status or display_status, not both")
			return v, false
		}
		// The store is not asked for the trace status the word maps to:
		// where a run has a ledger row the ledger's word wins, and the
		// two can differ (a run the ledger ended whose last records are
		// still on their way, a run lost with its worker). Every row is
		// read and held to its display status, so the answer is exact
		// and agrees with the statistics.
		_ = dst
		v.mapped = true
	}
	v.display = display
	if vs := q["chains"]; len(vs) > 0 && vs[0] != "" {
		b, err := strconv.ParseBool(vs[0])
		if err != nil {
			badRequest(w, r, "chains must be true or false (1 or 0)")
			return v, false
		}
		v.chains = b
	}
	if vs := q["approvals"]; len(vs) > 0 && vs[0] != "" {
		switch vs[0] {
		case approvalsPending, approvalsExpired, approvalsDenied, approvalsApproved:
		default:
			badRequest(w, r, "approvals is pending, expired, denied or approved")
			return v, false
		}
		if !v.chains {
			badRequest(w, r, "approvals needs chains=1: an approval's outcome is its logical run's")
			return v, false
		}
		v.approvals = vs[0]
	}
	return v, true
}

// fillScanPages bounds how many store pages one filled answer reads: a
// filter that keeps almost nothing ends there with a cursor, the page
// short, rather than reading the whole store.
const fillScanPages = 40

// fillStorePage is the store page a folded read asks for.
const fillStorePage = 200

// queuedRows are the queued runs the trace store does not know, for the
// first page of a list no other filter narrows.
func (s *Server) queuedRows(r *http.Request, ctl *control, query obsdb.RunQuery, v view, have []runRow) []runRow {
	narrowed := query.SessionID != "" || query.PublicID != "" || query.StoppedBy != "" || query.ErrorType != "" ||
		query.IDPrefix != "" || len(query.Errs) > 0 || len(query.Meta) > 0 || query.TurnBelow > 0 || query.TurnAbove > 0 ||
		(query.ParentRunID != "" && query.ParentRunID != "*") || !query.Before.IsZero() || v.approvals != ""
	if s.ledger == nil || ctl.sc.IsZero() || narrowed || query.Status != "" || (v.display != "" && v.display != displayQueued) {
		return nil
	}
	queued, err := s.ledger.Queued(r.Context(), ctl.sc, query.Agent, 100)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, row := range have {
		seen[row.ID] = true
	}
	var extra []runRow
	for _, lr := range queued {
		if seen[lr.ID] || (v.chains && lr.ResumeOf != "") {
			continue
		}
		extra = append(extra, ctl.ledgerRow(lr))
	}
	return extra
}

// serveLedgerRun answers GET /v1/runs/{id} for a run only the ledger
// knows; false when it is not one.
func (s *Server) serveLedgerRun(w http.ResponseWriter, r *http.Request, id string) bool {
	ctl := s.control(r)
	lr, ok := ctl.ledgerRuns([]string{id})[id]
	if !ok {
		return false
	}
	row := ctl.ledgerRow(lr)
	if row.Chain = ctl.chain(s.coster(r.Context()), row); row.Chain != nil {
		row.ApprovalsOutcome = row.Chain.ApprovalsOutcome
	}
	page := runPage{runDoc: runDoc{runRow: row, Children: []runRow{}, Holes: []stepHole{}}, Compactions: []runCompaction{}}
	ctl.approvals(&page)
	writeJSON(w, r, http.StatusOK, page)
	return true
}

// approvals sets a run page's approval records, and names who decided
// in its decided / resumes blocks where no record of the run did.
func (c *control) approvals(page *runPage) {
	if c.s.ledger == nil || c.sc.IsZero() {
		return
	}
	parked := page.ID
	if page.ResumedFrom != "" {
		parked = page.ResumedFrom
	}
	m, err := c.s.ledger.RunApprovals(c.ctx, c.sc, page.ID, parked)
	if err != nil {
		return
	}
	page.Approvals = c.approvalDocs(m[page.ID])
	for doc, of := range map[*decisionDoc]string{page.Decided: page.ID, page.Resumes: parked} {
		if doc == nil {
			continue
		}
		for _, a := range m[of] {
			if a.Actor == nil {
				continue
			}
			if doc.Actor == "" {
				doc.Actor, doc.ActorKind = a.Actor.ID, a.Actor.Kind
			}
			for i := range doc.Calls {
				if call := &doc.Calls[i]; call.CallID == a.CallID {
					call.ApprovalID = a.ID
					if call.Who == "" {
						call.Who = a.Actor.ID
					}
				}
			}
		}
	}
}

// serveChainSpans is GET /v1/runs/{id}/spans?tree=1&chain=1: the trees
// of every run of id's chain in one list, with the waits between them.
func (s *Server) serveChainSpans(w http.ResponseWriter, r *http.Request, tr obsdb.TreeReader, id string) {
	ctl := s.control(r)
	cost := s.coster(r.Context())
	var of runRow
	if det, err := s.db.Run(r.Context(), id); err == nil {
		one := []runRow{cost.row(det.RunRow)}
		ctl.decorate(one)
		of = one[0]
	} else if lr, ok := ctl.ledgerRuns([]string{id})[id]; ok {
		of = ctl.ledgerRow(lr)
	} else {
		dbError(w, r, "run", id, err)
		return
	}
	out := treeSpansDoc{Spans: []spanDTO{}, Holes: []treeHole{}, Chain: []string{id}}
	if ch := ctl.chain(cost, of); ch != nil {
		out.Chain, out.Waits = out.Chain[:0], ch.Waits
		for _, run := range ch.Runs {
			out.Chain = append(out.Chain, run.ID)
		}
	}
	for _, run := range out.Chain {
		doc, err := s.treeSpans(r.Context(), tr, run)
		if err != nil {
			continue // a run of the chain with no trace yet (queued)
		}
		out.Spans = append(out.Spans, doc.Spans...)
		out.Holes = append(out.Holes, doc.Holes...)
	}
	writeJSON(w, r, http.StatusOK, out)
}
