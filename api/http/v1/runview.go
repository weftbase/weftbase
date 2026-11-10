package v1

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/weftbase/weftbase/store"
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

	// endedParked is when the ledger ended a run that was waiting on a
	// person (cancelled, its version retired) without resuming it: the
	// wait lasted until then, and so did the logical run — the trace's
	// own end is the park.
	endedParked *time.Time

	// resumeQueued: the run was resumed, and its resume is queued with
	// no trace yet (the statistics count the chain here, as queued).
	resumeQueued bool

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

// control is one request's view of the ledger and the registry: what
// it read is kept for the request (a live stream keeps one per
// connection, refreshed after controlTTL), so a page asks each store a
// bounded number of times whatever its row count.
//
// A control-store read that fails is never guessed around: the first
// error is kept (err), every read after it is skipped, and the handler
// answers 503 unavailable (unavailable) instead of a page that would
// show the trace store's word where the ledger's belongs.
type control struct {
	s          *Server
	ctx        context.Context
	sc         tenant.Scope
	versions   map[string]registry.Version // by id, of the agents asked about
	agents     map[string]bool
	placements map[string]string
	// ledger holds the ledger rows read so far (with their chains'
	// runs), asked the ids already asked for.
	ledger map[string]sessions.Run
	asked  map[string]bool
	// rows are the run rows read so far for chains (by id; missing the
	// ids no store holds), chains the chains built.
	rows    map[string]runRow
	missing map[string]bool
	chains  map[string]*chainDoc
	// approvals are the approval records read so far, by run id.
	approvalsOf map[string][]sessions.Approval
	approvalsAt map[string]bool
	// content is whether the caller may read run content (runs.content):
	// without it a row's prompt_preview and its title are redacted.
	content bool
	err     error
	// readAt is when the caches were filled (live: they are dropped
	// after controlTTL).
	readAt time.Time
}

// controlTTL is how long a live stream's control keeps what it read of
// the ledger before a frame reads it again: one read per connection per
// TTL however many frames arrive, never a frame's worth stale for long.
const controlTTL = time.Second

func (s *Server) control(r *http.Request) *control {
	p, sc, _ := authz.From(r.Context())
	c := &control{s: s, ctx: r.Context(), sc: sc, content: authz.Can(p, authz.RunsContent).Allow}
	c.reset()
	return c
}

// reset drops what the control read.
func (c *control) reset() {
	c.versions, c.agents, c.placements = map[string]registry.Version{}, map[string]bool{}, nil
	c.ledger, c.asked = map[string]sessions.Run{}, map[string]bool{}
	c.rows, c.missing, c.chains = map[string]runRow{}, map[string]bool{}, map[string]*chainDoc{}
	c.approvalsOf, c.approvalsAt = map[string][]sessions.Approval{}, map[string]bool{}
	c.err, c.readAt = nil, c.s.now()
}

// fresh resets a long-lived control (a live stream's) once its reads
// are older than controlTTL.
func (c *control) fresh() {
	if c.s.now().Sub(c.readAt) >= controlTTL {
		c.reset()
	}
}

// fail keeps the first control-store error.
func (c *control) fail(err error) {
	if err != nil && c.err == nil {
		c.err = err
	}
}

// unavailable answers 503 when a control-store read failed; true when
// it did (the handler then writes nothing else).
func (c *control) unavailable(w http.ResponseWriter, r *http.Request) bool {
	if c.err == nil {
		return false
	}
	writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the control store could not be read: "+c.err.Error())
	return true
}

// controlled reports whether the request reads the ledger at all.
func (c *control) controlled() bool { return c.s.ledger != nil && !c.sc.IsZero() }

// lookup reads the ledger rows of ids not asked for yet (with every run
// of their chains), in one read.
func (c *control) lookup(ids []string) {
	if !c.controlled() || c.err != nil {
		return
	}
	var ask []string
	for _, id := range ids {
		if !c.asked[id] {
			c.asked[id] = true
			ask = append(ask, id)
		}
	}
	if len(ask) == 0 {
		return
	}
	m, err := c.s.ledger.ByID(c.ctx, c.sc, ask)
	if err != nil {
		c.fail(err)
		return
	}
	for id, run := range m {
		c.ledger[id] = run
		c.asked[id] = true
	}
}

// ledgerRuns reads the ledger rows of ids (none without a ledger).
func (c *control) ledgerRuns(ids []string) map[string]sessions.Run {
	c.lookup(ids)
	out := map[string]sessions.Run{}
	for _, id := range ids {
		if run, ok := c.ledger[id]; ok {
			out[id] = run
		}
	}
	return out
}

func (c *control) version(agent, id string) (registry.Version, string, bool) {
	if c.s.registry == nil || c.sc.IsZero() {
		return registry.Version{}, "", false
	}
	if !c.agents[agent] && c.err == nil {
		c.agents[agent] = true
		vs, err := c.s.registry.Versions(c.ctx, c.sc, agent)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			c.fail(err)
		}
		for _, v := range vs {
			c.versions[v.ID] = v
		}
	}
	if c.placements == nil && c.err == nil {
		var err error
		if c.placements, err = c.s.registry.Placements(c.ctx, c.sc); err != nil {
			c.fail(err)
		}
		if c.placements == nil {
			c.placements = map[string]string{}
		}
	}
	v, ok := c.versions[id]
	return v, c.placements[v.BuildID], ok
}

// versionRef is the agent_version every document carries: the
// version's id, number and the labels it holds now.
func (c *control) versionRef(agent, id string) *versionRef {
	if id == "" {
		return nil
	}
	ref := &versionRef{ID: id, Labels: []string{}}
	if v, _, ok := c.version(agent, id); ok {
		ref.Number = v.Number
		if v.Labels != nil {
			ref.Labels = v.Labels
		}
	}
	return ref
}

// approvalsFor reads the approval records of runIDs not read yet, in one
// read, and returns those of runIDs.
func (c *control) approvalsFor(runIDs ...string) map[string][]sessions.Approval {
	out := map[string][]sessions.Approval{}
	if !c.controlled() {
		return out
	}
	var ask []string
	for _, id := range runIDs {
		if !c.approvalsAt[id] {
			c.approvalsAt[id] = true
			ask = append(ask, id)
		}
	}
	if len(ask) > 0 && c.err == nil {
		m, err := c.s.ledger.RunApprovals(c.ctx, c.sc, ask...)
		if err != nil {
			c.fail(err)
		}
		for id, as := range m {
			c.approvalsOf[id] = as
		}
	}
	for _, id := range runIDs {
		if as, ok := c.approvalsOf[id]; ok {
			out[id] = as
		}
	}
	return out
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
		case sessions.StatusCancelled:
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

// decorate sets the control fields of trace rows, and redacts what the
// caller may not read (redact).
func (c *control) decorate(rows []runRow) {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.ParentRunID == "" {
			ids = append(ids, r.ID)
		}
	}
	c.lookup(ids)
	for i := range rows {
		r := &rows[i]
		c.redact(r)
		lr, ok := c.ledger[r.ID]
		if !ok || r.ParentRunID != "" {
			r.DisplayStatus = displayStatus(r, nil)
			continue
		}
		c.fromLedger(r, lr)
	}
}

// redact clears what a row would show of the run's content to a caller
// without runs.content: its prompt preview and its session title. Today
// every credential that reads runs reads content too (authz.Can), so
// nothing changes on the wire until restricted keys land; the spans
// route redacts its content attributes the same way (redactSpans).
func (c *control) redact(r *runRow) {
	if c.content {
		return
	}
	r.PromptPreview = ""
	if _, ok := r.Meta[obsdb.SessionTitleKey]; ok {
		meta := make(map[string]string, len(r.Meta))
		for k, v := range r.Meta {
			if k != obsdb.SessionTitleKey {
				meta[k] = v
			}
		}
		r.Meta = meta
	}
}

func (c *control) fromLedger(r *runRow, lr sessions.Run) {
	r.DisplayStatus = displayStatus(r, &lr)
	r.ResolvedVia = lr.ResolvedVia
	if lr.ParkedAt != nil && lr.ResumedBy == "" && sessions.Terminal(lr.Status) && lr.FinishedAt != nil {
		f := *lr.FinishedAt
		r.parkedAt, r.endedParked = lr.ParkedAt, &f
	}
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
	if next, ok := c.ledger[lr.ResumedBy]; ok && lr.ResumedBy != "" && next.Status == sessions.StatusQueued {
		r.resumeQueued = true
	}
	if lr.ResumeOf != "" && r.ResumedFrom == "" {
		r.ResumedFrom = lr.ResumeOf
	}
	r.AgentVersion = c.versionRef(lr.Agent, lr.AgentVersion)
	if v, placement, ok := c.version(lr.Agent, lr.AgentVersion); ok {
		r.Placement = placement
		r.ManifestDrift = r.ManifestHash != "" && v.ManifestHash != "" && r.ManifestHash != v.ManifestHash
	}
}

// ledgerRow is the row of a run only the ledger knows (queued: nothing
// of it has been traced). Its status is absent: status is the trace
// store's word, and the trace store has none; display_status says what
// the ledger knows.
func (c *control) ledgerRow(lr sessions.Run) runRow {
	r := runRow{ID: lr.ID, Agent: lr.Agent, Meta: map[string]string{}, Started: lr.CreatedAt, LastSeen: lr.CreatedAt,
		ResumedFrom: lr.ResumeOf, ResumedBy: lr.ResumedBy, Err: lr.Error, CancelCause: lr.CancelCause}
	for k, v := range lr.Metadata {
		r.Meta[k] = v
	}
	r.SessionID = lr.Metadata[sessions.MetaSessionID]
	if n, err := strconv.Atoi(lr.Metadata[sessions.MetaTurn]); err == nil {
		r.Turn = n
	}
	if lr.Input != nil {
		r.PromptPreview = inputPreview(*lr.Input)
	}
	if lr.FinishedAt != nil {
		f := *lr.FinishedAt
		r.Finished = &f
	}
	r.Cache = "not_reported"
	r.LedgerOnly = true
	c.redact(&r)
	c.fromLedger(&r, lr)
	return r
}

// fetch reads the rows of ids not read yet, batched: the trace store's
// (one Runs read per 500 ids, children included, decorated), else the
// ledger's own (a run no trace names yet); an id neither holds is
// missing.
func (c *control) fetch(cost *coster, ids []string) {
	var need []string
	for _, id := range ids {
		if _, ok := c.rows[id]; !ok && !c.missing[id] && !slices.Contains(need, id) {
			need = append(need, id)
		}
	}
	for len(need) > 0 && c.err == nil {
		n := min(len(need), 500)
		batch := need[:n]
		need = need[n:]
		page, err := c.s.db.Runs(c.ctx, obsdb.RunQuery{IDs: batch, ParentRunID: "*", Limit: 500})
		if err != nil {
			c.fail(err)
			return
		}
		cost.primeTrees(page.Runs)
		rows := make([]runRow, 0, len(page.Runs))
		for _, rec := range page.Runs {
			rows = append(rows, cost.row(rec))
		}
		c.decorate(rows)
		for _, r := range rows {
			c.rows[r.ID] = r
		}
		c.lookup(batch)
		for _, id := range batch {
			if _, ok := c.rows[id]; ok {
				continue
			}
			if lr, ok := c.ledger[id]; ok {
				c.rows[id] = c.ledgerRow(lr)
			} else {
				c.missing[id] = true
			}
		}
	}
}

// read is one run's row with its control fields: the trace store's,
// else the ledger's own (a run no trace names yet).
func (c *control) read(cost *coster, id string) (runRow, bool) {
	c.fetch(cost, []string{id})
	r, ok := c.rows[id]
	return r, ok
}

// inChain reports whether a row is one of several runs a park and its
// resume link.
func inChain(r runRow) bool {
	return r.ResumedFrom != "" || r.ResumedBy != "" || r.DisplayStatus == displayWaiting || r.endedParked != nil
}

// chain builds the chain a run belongs to, from any of its runs; nil
// when the run is alone (never parked, never a resume).
func (c *control) chain(cost *coster, of runRow) *chainDoc {
	return c.chainsOf(cost, []runRow{of})[of.ID]
}

// maxLinks bounds a chain's runs on either side of a row.
const maxLinks = 64

// chainsOf builds the chains of a set of rows (keyed by row id; a row
// alone has none) with batched reads: every run of the rows' chains the
// ledger holds is already read with them (control.lookup); the rest are
// read one link further per round, one trace read per round for every
// row at once, so a page costs O(chain length) reads, not O(rows); the
// approvals of every chain in one read.
func (c *control) chainsOf(cost *coster, rows []runRow) map[string]*chainDoc {
	out := map[string]*chainDoc{}
	var todo []runRow
	for _, r := range rows {
		if _, ok := c.rows[r.ID]; !ok {
			c.rows[r.ID] = r
		}
		if doc, ok := c.chains[r.ID]; ok {
			if doc != nil {
				out[r.ID] = doc
			}
			continue
		}
		if inChain(r) {
			todo = append(todo, r)
		} else {
			c.chains[r.ID] = nil
		}
	}
	if len(todo) == 0 {
		return out
	}
	// The ledger's runs of the rows' chains, read with the rows.
	var seed []string
	for _, r := range todo {
		if lr, ok := c.ledger[r.ID]; ok {
			for id, m := range c.ledger {
				if m.ChainID == lr.ChainID && m.ChainID != "" {
					seed = append(seed, id)
				}
			}
		}
	}
	c.fetch(cost, seed)
	// Then link by link: each round reads every row's next missing link.
	for round := 0; round < maxLinks && c.err == nil; round++ {
		var need []string
		for _, r := range todo {
			for _, side := range []func(runRow) string{
				func(x runRow) string { return x.ResumedFrom },
				func(x runRow) string { return x.ResumedBy },
			} {
				at := r
				for n := 0; n < maxLinks; n++ {
					next := side(at)
					if next == "" || c.missing[next] {
						break
					}
					row, ok := c.rows[next]
					if !ok {
						need = append(need, next)
						break
					}
					at = row
				}
			}
		}
		if len(need) == 0 {
			break
		}
		c.fetch(cost, need)
	}
	// The chains, and their approvals in one read.
	members := map[string][]runRow{}
	var all []string
	for _, r := range todo {
		runs := []runRow{r}
		for n := 0; runs[0].ResumedFrom != "" && n < maxLinks; n++ {
			prev, ok := c.rows[runs[0].ResumedFrom]
			if !ok {
				break
			}
			runs = append([]runRow{prev}, runs...)
		}
		for n := 0; runs[len(runs)-1].ResumedBy != "" && n < maxLinks; n++ {
			next, ok := c.rows[runs[len(runs)-1].ResumedBy]
			if !ok {
				break
			}
			runs = append(runs, next)
		}
		members[r.ID] = runs
		for _, m := range runs {
			all = append(all, m.ID)
		}
	}
	approvals := c.approvalsFor(all...)
	for _, r := range todo {
		doc := c.build(members[r.ID], approvals)
		c.chains[r.ID] = doc
		out[r.ID] = doc
	}
	return out
}

// build is one chain's document from its runs, in order.
func (c *control) build(runs []runRow, approvals map[string][]sessions.Approval) *chainDoc {
	last := runs[len(runs)-1]
	doc := &chainDoc{DisplayStatus: last.DisplayStatus, StoppedBy: last.StoppedBy, ErrorType: last.ErrorType,
		CancelCause: last.CancelCause, Err: last.Err, Started: runs[0].Started, CostKnown: true, Waits: []waitDoc{}}
	now := c.s.now()
	end := now
	switch {
	case last.endedParked != nil:
		f := *last.endedParked
		doc.Finished, end = &f, f
	case last.Finished != nil && last.DisplayStatus != displayWaiting && last.DisplayStatus != displayQueued &&
		last.DisplayStatus != displayRunning:
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
		parked := r.DisplayStatus == displayWaiting || r.ResumedBy != "" || r.endedParked != nil
		from := r.Finished
		if from == nil || r.endedParked != nil {
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
		switch {
		case i+1 < len(runs) && !runs[i+1].LedgerOnly:
			t := runs[i+1].Started
			w.To, until = &t, t
		case r.endedParked != nil:
			t := *r.endedParked
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
//   - chains (the default; chains=0 turns it off): one row per logical
//     run. Where the read is unfiltered and in start order, a resume is
//     dropped (the run it continues is a row of its own, at its start).
//     Where the read is filtered or sorted (mapped: a status, a
//     search), a chain is shown where its last traced run is read, as
//     its first run's row; a run that was resumed is skipped unless what
//     resumed it is a queued run no trace names yet (that chain is shown
//     there). Either way a chain is one row, on one page only;
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

// prepare reads, in a bounded number of reads, what keep needs of a
// page of decorated rows: their chains.
func (v view) prepare(ctl *control, cost *coster, rows []runRow) {
	if v.chains {
		ctl.chainsOf(cost, rows)
	}
}

// keep is one decorated row through the view: the row to show, or
// false. prepare has read the page's chains.
func (v view) keep(ctl *control, cost *coster, row runRow) (runRow, bool) {
	if v.chains {
		if v.mapped && row.ResumedBy != "" {
			next, ok := ctl.read(cost, row.ResumedBy)
			if !ok || !next.LedgerOnly {
				return row, false // its chain is shown where its last run is
			}
			// What resumed it is queued, with no trace yet: its chain is
			// shown here, or it would be shown nowhere.
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
// given). chains is on unless chains=0 (false) says otherwise; raw is
// what the caller's own default is when chains is absent (an id_prefix
// lookup, a run's children: every run, as it is).
func viewParams(w http.ResponseWriter, r *http.Request, q map[string][]string, status *obsdb.Status, raw bool) (v view, ok bool) {
	display, _, ok := displayParam(w, r, q)
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
		v.mapped = true
	}
	v.display = display
	v.chains = !raw
	if vs := q["chains"]; len(vs) > 0 && vs[0] != "" {
		switch vs[0] {
		case "1", "true":
			v.chains = true
		case "0", "false":
			v.chains = false
		default:
			badRequest(w, r, "chains is 0 (every run as it is) or 1 (one row per logical run, the default)")
			return v, false
		}
	}
	if vs := q["approvals"]; len(vs) > 0 && vs[0] != "" {
		switch vs[0] {
		case approvalsPending, approvalsExpired, approvalsDenied, approvalsApproved:
		default:
			badRequest(w, r, "approvals is pending, expired, denied or approved")
			return v, false
		}
		if !v.chains {
			badRequest(w, r, "approvals needs the logical runs (not chains=0): an approval's outcome is its logical run's")
			return v, false
		}
		v.approvals = vs[0]
	}
	return v, true
}

// fillScanPages bounds how many store pages one filled answer reads: a
// filter that keeps almost nothing ends there with a cursor, the page
// short, rather than reading the whole store. fillBudget bounds its
// time the same way: past it the page ends where it is, with its cursor
// and partial true.
const (
	fillScanPages = 40
	fillBudget    = 2 * time.Second
)

// fillStorePage is the store page a folded read asks for.
const fillStorePage = 200

// queuedQuery is the ledger's side of a list: the queued runs no trace
// names yet, under the list's filters; ok false when the list's filters
// cannot hold one (a queued run has no stop, no error, no trace).
func (s *Server) queuedQuery(ctl *control, query obsdb.RunQuery, v view) (sessions.QueuedQuery, bool) {
	narrowed := query.PublicID != "" || query.StoppedBy != "" || query.ErrorType != "" ||
		query.IDPrefix != "" || query.Errs != nil || len(query.Meta) > 0 || query.TurnBelow > 0 || query.TurnAbove > 0 ||
		(query.ParentRunID != "" && query.ParentRunID != "*") || v.approvals != ""
	if !ctl.controlled() || narrowed || query.Status != "" || (v.display != "" && v.display != displayQueued) {
		return sessions.QueuedQuery{}, false
	}
	return sessions.QueuedQuery{Agent: query.Agent, SessionID: query.SessionID, Before: query.Before, BeforeID: query.BeforeID,
		Oldest: query.Oldest, Resumes: !v.chains || v.display == displayQueued}, true
}

// pageKey is a row's place in a list: (started, id), newest first
// unless oldest.
type pageKey struct {
	at time.Time
	id string
}

func keyOf(r runRow) pageKey { return pageKey{r.Started, r.ID} }

// ahead reports whether a comes before b in the list's order.
func ahead(a, b pageKey, oldest bool) bool {
	if !a.at.Equal(b.at) {
		return a.at.After(b.at) != oldest
	}
	return (a.id > b.id) != oldest
}

// mergeQueued merges the ledger's queued rows into a list page the trace
// store filled: by (started, id), the page cut at want, its cursor
// where the merged page ends. traceNext is the trace store's own cursor
// (nil when it is exhausted); qFull says the ledger may hold more
// queued rows past the ones read.
func mergeQueued(out *runsPage, queued []runRow, want int, oldest, qFull bool) {
	var cursor *pageKey
	if out.NextBefore != nil {
		k := pageKey{at: *out.NextBefore}
		if out.NextBeforeID != nil {
			k.id = *out.NextBeforeID
		}
		cursor = &k
	}
	merged := append([]runRow{}, out.Runs...)
	for _, q := range queued {
		if cursor == nil || ahead(keyOf(q), *cursor, oldest) {
			merged = append(merged, q)
		}
	}
	slices.SortStableFunc(merged, func(a, b runRow) int {
		switch {
		case ahead(keyOf(a), keyOf(b), oldest):
			return -1
		case ahead(keyOf(b), keyOf(a), oldest):
			return 1
		}
		return 0
	})
	switch {
	case len(merged) > want:
		merged = merged[:want]
		k := keyOf(merged[want-1])
		cursor = &k
	case cursor == nil && qFull && len(merged) > 0:
		k := keyOf(merged[len(merged)-1])
		cursor = &k
	}
	out.Runs = merged
	out.NextBefore, out.NextBeforeID = nil, nil
	if cursor != nil {
		at, id := cursor.at, cursor.id
		out.NextBefore = &at
		if id != "" {
			out.NextBeforeID = &id
		}
	}
}

// serveQueuedRuns answers /v1/runs?display_status=queued: the ledger's
// queued runs alone (no trace names them), paged by (created_at, id)
// and counted exactly; a queued resume reads as its chain's row.
func (s *Server) serveQueuedRuns(w http.ResponseWriter, r *http.Request, ctl *control, qq sessions.QueuedQuery, v view, want int) {
	out := runsPage{Runs: []runRow{}, TotalExact: true}
	qq.Limit = want
	runs, total, err := s.ledger.Queued(r.Context(), ctl.sc, qq)
	if err != nil {
		ctl.fail(err)
	}
	cost := s.coster(r.Context())
	rows := make([]runRow, 0, len(runs))
	for _, lr := range runs {
		rows = append(rows, ctl.ledgerRow(lr))
	}
	v.prepare(ctl, cost, rows)
	for _, row := range rows {
		if kept, ok := v.keep(ctl, cost, row); ok {
			out.Runs = append(out.Runs, kept)
		}
	}
	if ctl.unavailable(w, r) {
		return
	}
	out.Total = total
	if len(runs) == want {
		last := runs[len(runs)-1]
		at, id := last.CreatedAt, last.ID
		out.NextBefore, out.NextBeforeID = &at, &id
	}
	writeJSON(w, r, http.StatusOK, out)
}

// serveLedgerRun answers GET /v1/runs/{id} for a run only the ledger
// knows; false when it is not one (or the ledger could not be read:
// ctl.err, which the caller answers).
func (s *Server) serveLedgerRun(w http.ResponseWriter, r *http.Request, ctl *control, id string) bool {
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
	if ctl.unavailable(w, r) {
		return true
	}
	writeJSON(w, r, http.StatusOK, page)
	return true
}

// approvals sets a run page's approval records, and names who decided
// in its decided / resumes blocks where no record of the run did.
func (c *control) approvals(page *runPage) {
	if !c.controlled() {
		return
	}
	parked := page.ID
	if page.ResumedFrom != "" {
		parked = page.ResumedFrom
	}
	m := c.approvalsFor(page.ID, parked)
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
	of, ok := ctl.read(cost, id)
	if ctl.unavailable(w, r) {
		return
	}
	if !ok {
		notFound(w, r, "no run "+id)
		return
	}
	out := treeSpansDoc{Spans: []spanDTO{}, Holes: []treeHole{}, Chain: []string{id}}
	ch := ctl.chain(cost, of)
	if ctl.unavailable(w, r) {
		return
	}
	if ch != nil {
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
	redactSpans(r, out.Spans)
	writeJSON(w, r, http.StatusOK, out)
}
