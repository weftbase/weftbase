package v1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// The run's app logs (plan A7):
//
//	GET /v1/runs/{id}/logs?from=&limit=&severity=
//
// The non-weft log records of the run's traces — the app's own slog or
// OTel Logs API lines — attributed to the run through the spans they
// were emitted under (obsdb.DB.OtherLogs).

// maxLogsLimit is the largest logs page (obsdb's LogQuery.PageLimit
// cap): a larger limit is clamped, not refused.
const maxLogsLimit = 1000

// logRow is one app log record. severity is the OTLP short name
// ("INFO", "WARN2"; "" when unspecified) and severity_number the
// number it names; attrs are the record's attributes (strings on a
// ClickHouse backend); span_id is absent for none. event_name is the
// OTLP record's EventName (gen_ai.user.message, a GenAI event: its body
// is the event's JSON), absent for a plain log line (UX-H-10).
type logRow struct {
	Index          int64          `json:"index"`
	Time           time.Time      `json:"time"`
	Severity       string         `json:"severity"`
	SeverityNumber int            `json:"severity_number"`
	EventName      string         `json:"event_name,omitempty"`
	Body           string         `json:"body"`
	Attrs          map[string]any `json:"attrs"`
	SpanID         string         `json:"span_id,omitempty"`
	// RunID is the run the line belongs to.
	RunID string `json:"run_id"`
}

// logsPage is GET /v1/runs/{id}/logs. NextFrom is the next page's
// from (the last index + 1) when the page is full; absent on the last
// page. A run whose logs cannot be attributed (it has no span) carries
// the not_recorded badge beside an empty list.
//
// Holes is every hole of the page, deduped, in ADR 0028 §11's order —
// the step route's shape ({hole, reason, fix?}); [] when none:
//
//   - not_recorded: the run has no span to attribute logs through;
//     or (obsdb.LogPage.NoBridge) its root span is stored without
//     weft.logs.bridged and no line is attributed to it — the app's
//     slog was not bridged, the reason and fix name otel.Slog() (U11.1:
//     the hint, never a loss);
//   - truncated: the run's traces hold more than obsdb.MaxLogCandidates
//     log lines in its window — the cap applies before attribution, so
//     a flooding subagent or sibling counts — and later lines of this
//     run may be missing;
//   - gap: lines in the run's trace and window name a span that is not
//     stored (dropped, or an enclosing app span still open); they may
//     be this run's or another's in the same trace.
//
// badge, reason and fix are the first hole, for the simplest client.
//
// Partial is a live condition, not a hole (the badge table is closed):
// the run is running, so lines under its in-flight spans are not
// attributable yet and indexes may shift when those spans arrive — a
// client paging with from may re-read lines and miss as many until
// the run ends (obsdb.ReadOtherLogs; a stable cursor is phase-2 work).
// PartialReason says so; with no hole, reason carries it too, so a
// page can be partial with a reason and no badge.
type logsPage struct {
	Logs     []logRow `json:"logs"`
	NextFrom *int64   `json:"next_from,omitempty"`
	// Runs and NextCursor are set by the wide form (q, chain or tree):
	// the runs read, in order, and where the next page starts.
	Runs          []string   `json:"runs,omitempty"`
	NextCursor    string     `json:"next_cursor,omitempty"`
	Partial       bool       `json:"partial,omitempty"`
	PartialReason string     `json:"partial_reason,omitempty"`
	Holes         []stepHole `json:"holes"`
	badgeFields
}

// logsTruncatedFix is the truncated hole's fix on this route: the
// table's, for the reader's own candidate cap (obsdb.CauseLogCap).
var logsTruncatedFix = func() string { _, fix := obsdb.HoleNoteFor(obsdb.HoleTruncated, obsdb.CauseLogCap); return fix }()

// logsCap is obsdb.MaxLogCandidates as the reason spells it: digits in
// groups of three ("10 000").
var logsCap = func() string {
	d := strconv.Itoa(obsdb.MaxLogCandidates)
	var b strings.Builder
	for i, c := range d {
		if i > 0 && (len(d)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}()

// logsPartialReason is the reason a running run's page carries.
const logsPartialReason = "spans are exported when they end: lines under in-flight spans appear once they do, and indexes may shift"

// logsNoSpansReason words the not_recorded hole of a run with no span:
// HoleNote's own reason is the request record's.
const logsNoSpansReason = "the run was recorded without a tracer: app logs are attributed to a run through the spans they were emitted under, and this run stored none"

// logsNoBridgeReason and logsNoBridgeFix word the not_recorded hint of
// a run whose app slog was not bridged (obsdb.LogPage.NoBridge): the
// empty page's cause, so it never reads "the app logged nothing".
const (
	logsNoBridgeReason = "no app logs: no weft slog bridge was recorded for this run (its root span carries no weft.logs.bridged), so its slog lines — the app's own, weft.Logger's, mw.Log's, mw.Audit's — did not reach this store; if the app bridges slog itself (contrib otelslog, the OTel Logs API), it logged nothing under this run"
	logsNoBridgeFix    = "bridge slog: otel.Install(…, otel.Slog()), or log through slog.New(otel.SlogHandler(h))"
)

// serveRunLogs answers /v1/runs/{id}/logs?from=&limit=&severity=: one
// page of the run's app logs in time order. from is the first index
// (next_from feeds straight back in); limit 0 = 100, max 1000;
// severity keeps the records at or above a level (trace, debug, info,
// warn, error, fatal, a SeverityText spelling or a number 1–24) without
// renumbering them.
func (s *Server) serveRunLogs(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	if q.Has("q") || q.Has("chain") || q.Has("tree") || q.Has("cursor") {
		s.serveRunLogsWide(w, r, id)
		return
	}
	var query obsdb.LogQuery
	if v := q.Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, r, "from must be a non-negative log index")
			return
		}
		query.From = n
	}
	limit, ok := limitParam(w, r, q)
	if !ok {
		return
	}
	query.Limit = min(limit, maxLogsLimit)
	if v := q.Get("severity"); v != "" {
		n, ok := obsdb.ParseSeverity(v)
		if !ok {
			badRequest(w, r, "severity must be trace, debug, info, warn, error, fatal or a number 1-24")
			return
		}
		query.MinSeverity = n
	}
	page, err := s.db.OtherLogs(r.Context(), id, query)
	logs := page.Logs
	out := logsPage{Logs: []logRow{}}
	holes := holeSet{}
	var he *obsdb.HoleError
	switch {
	case errors.As(err, &he):
		if he.Hole == obsdb.HoleNotRecorded {
			// A run with no span: the reason is this route's, the fix
			// the no-spans cause's (install a tracer).
			_, fix := obsdb.HoleNoteFor(obsdb.HoleNotRecorded, obsdb.CauseNoSpans)
			holes.add(he.Hole, logsNoSpansReason, fix)
		} else {
			holes.note(he.Hole)
		}
		out.setHoles(holes.list())
		writeJSON(w, r, http.StatusOK, out)
		return
	case err != nil:
		dbError(w, r, "logs of run", id, err)
		return
	}
	for _, l := range logs {
		attrs := l.Attrs
		if attrs == nil {
			attrs = map[string]any{}
		}
		out.Logs = append(out.Logs, logRow{
			Index: l.Index, Time: l.Time, Severity: obsdb.SeverityText(l.Severity), SeverityNumber: l.Severity,
			EventName: l.EventName, Body: l.Body, Attrs: attrs, SpanID: l.SpanID, RunID: id,
		})
	}
	if n := len(logs); n > 0 && n >= query.PageLimit() {
		next := logs[n-1].Index + 1
		out.NextFrom = &next
	}
	if page.Truncated {
		holes.add(obsdb.HoleTruncated, "the run's traces hold more than "+logsCap+" log lines in its window: only lines among the first "+
			logsCap+" by time were read; later lines of this run may be missing", logsTruncatedFix)
	}
	if page.Gap > 0 {
		holes.add(obsdb.HoleGap, gapReason(page.Gap), holeFix(obsdb.HoleGap))
	}
	if page.NoBridge {
		holes.add(obsdb.HoleNotRecorded, logsNoBridgeReason, logsNoBridgeFix)
	}
	out.setHoles(holes.list())
	if page.Partial {
		out.Partial, out.PartialReason = true, logsPartialReason
		if out.Badge == "" {
			out.Reason = logsPartialReason
		}
	}
	writeJSON(w, r, http.StatusOK, out)
}

// setHoles sets the page's holes and, from the first, its badge.
func (p *logsPage) setHoles(hs []stepHole) {
	p.Holes = hs
	if len(hs) > 0 {
		p.badgeFields = badgeFields{Badge: hs[0].Hole, Reason: hs[0].Reason, Fix: hs[0].Fix}
	}
}

// gapReason words the gap hole for n lines.
func gapReason(n int) string {
	if n == 1 {
		return "1 log line in this run's trace and time window names a span that is not stored (dropped, or still open): it may belong to this run or to another in the same trace"
	}
	return strconv.Itoa(n) + " log lines in this run's trace and time window name a span that is not stored (dropped, or still open): they may belong to this run or to another in the same trace"
}

// logCounts is the run document's log_warn / log_error (U4,
// docs/record-contract.md §6): the app's own log records under the
// run's spans at WARN (13–16) and at ERROR or above (17–24), counted at
// read time — an app log carries trace and span ids, not the run id,
// so it is attributed through the run's spans — in one scan the
// backend narrows to WARN and above (obsdb.DB.CountLogs), never a walk
// over every line's page. capped is the scan's cap
// (obsdb.MaxLogCandidates such lines): the counts are then floors, the
// document's logs_capped, drawn "≥". Counts, never content, so every
// scope that reads the run reads them. A read failure counts nothing:
// the counters are a summary, never the reason a run page fails.
func (s *Server) logCounts(ctx context.Context, runID string) (warn, errs int64, capped bool) {
	c, err := s.db.CountLogs(ctx, runID, 13)
	if err != nil {
		return 0, 0, false
	}
	return c.Warn, c.Error, c.Capped
}

// descendantsCap bounds the runs a tree read follows.
const descendantsCap = 500

// descendants are the runs under id (its subagent children, theirs, and
// so on), breadth first, at most descendantsCap; truncated says there
// are more.
func (s *Server) descendants(ctx context.Context, id string) (ids []string, truncated bool) {
	level := []string{id}
	for depth := 0; len(level) > 0 && depth < 16; depth++ {
		var next []string
		for _, parent := range level {
			page, err := s.db.Runs(ctx, obsdb.RunQuery{ParentRunID: parent, Limit: 500, Oldest: true})
			if err != nil {
				continue
			}
			for _, kid := range page.Runs {
				if len(ids) >= descendantsCap {
					return ids, true
				}
				ids = append(ids, kid.ID)
				next = append(next, kid.ID)
			}
			if page.NextBefore != nil {
				truncated = true
			}
		}
		level = next
	}
	return ids, truncated
}

// serveRunLogsWide is GET /v1/runs/{id}/logs with q, chain or tree: the
// lines of several runs in one paged read, each with its run_id.
//
//	chain=1   every run of the run's chain (a parked run, its resumes)
//	tree=1    with each of those, the runs under it (subagent children)
//	q=        only lines whose body or an attribute value contains the
//	          text (case folded)
//	cursor=   the previous page's next_cursor
//
// Lines come run by run (the chain in order, each run before its
// descendants), in time order inside a run. severity and limit as the
// plain form; from is the plain form's cursor and is not read here.
func (s *Server) serveRunLogsWide(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	flag := func(name string) (bool, bool) {
		v := q.Get(name)
		if v == "" {
			return false, true
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, r, name+" must be true or false (1 or 0)")
		}
		return b, err == nil
	}
	chain, ok := flag("chain")
	if !ok {
		return
	}
	tree, ok := flag("tree")
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, q)
	if !ok {
		return
	}
	limit = min(obsdb.LimitOf(limit), maxLogsLimit)
	var minSeverity int
	if v := q.Get("severity"); v != "" {
		n, ok := obsdb.ParseSeverity(v)
		if !ok {
			badRequest(w, r, "severity must be trace, debug, info, warn, error, fatal or a number 1-24")
			return
		}
		minSeverity = n
	}
	ctl := s.control(r)
	of, found := ctl.read(s.coster(r.Context()), id)
	if !found {
		notFound(w, r, "no run "+id)
		return
	}
	heads := []string{id}
	if chain {
		if ch := ctl.chain(s.coster(r.Context()), of); ch != nil {
			heads = heads[:0]
			for _, m := range ch.Runs {
				heads = append(heads, m.ID)
			}
		}
	}
	out := logsPage{Logs: []logRow{}}
	holes := holeSet{}
	for _, h := range heads {
		out.Runs = append(out.Runs, h)
		if tree {
			kids, cut := s.descendants(r.Context(), h)
			out.Runs = append(out.Runs, kids...)
			if cut {
				holes.add(obsdb.HoleTruncated, "the run tree holds more runs than this read follows ("+strconv.Itoa(descendantsCap)+"): the logs of the runs past them are not here", "")
			}
		}
	}
	// The cursor: the index of the run in Runs, and the line to read
	// from in it.
	k, from := 0, int64(0)
	if c := q.Get("cursor"); c != "" {
		a, b, cut := strings.Cut(c, ":")
		ki, err1 := strconv.Atoi(a)
		fi, err2 := strconv.ParseInt(b, 10, 64)
		if !cut || err1 != nil || err2 != nil || ki < 0 || fi < 0 {
			badRequest(w, r, "cursor is a previous page's next_cursor")
			return
		}
		k, from = ki, fi
	}
	needle := strings.ToLower(q.Get("q"))
	matches := func(l obsdb.OtherLog) bool {
		if needle == "" || strings.Contains(strings.ToLower(l.Body), needle) {
			return true
		}
		for _, v := range l.Attrs {
			if strings.Contains(strings.ToLower(fmt.Sprint(v)), needle) {
				return true
			}
		}
		return false
	}
	for ; k < len(out.Runs) && out.NextCursor == ""; k, from = k+1, 0 {
		run := out.Runs[k]
		for {
			page, err := s.db.OtherLogs(r.Context(), run, obsdb.LogQuery{From: from, Limit: 500, MinSeverity: minSeverity})
			if err != nil {
				break // a run with no span has no lines to attribute
			}
			if page.Truncated {
				holes.add(obsdb.HoleTruncated, "a run's traces hold more than "+logsCap+" log lines in its window: later lines of it may be missing", logsTruncatedFix)
			}
			for _, l := range page.Logs {
				from = l.Index + 1
				if !matches(l) {
					continue
				}
				attrs := l.Attrs
				if attrs == nil {
					attrs = map[string]any{}
				}
				out.Logs = append(out.Logs, logRow{Index: l.Index, Time: l.Time, Severity: obsdb.SeverityText(l.Severity), SeverityNumber: l.Severity,
					EventName: l.EventName, Body: l.Body, Attrs: attrs, SpanID: l.SpanID, RunID: run})
				if len(out.Logs) >= limit {
					out.NextCursor = strconv.Itoa(k) + ":" + strconv.FormatInt(from, 10)
					break
				}
			}
			if out.NextCursor != "" || len(page.Logs) < 500 {
				break
			}
		}
	}
	out.setHoles(holes.list())
	writeJSON(w, r, http.StatusOK, out)
}
