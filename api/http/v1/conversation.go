package v1

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/obsdb"
)

// conversationDoc is GET /v1/sessions/{id}/conversation: the session as
// a person reads it, one entry per turn in turn order. A turn that
// parked and was resumed is one turn (its runs are a chain). Paged by
// turn: limit (20, at most 100) and offset.
type conversationDoc struct {
	SessionID string             `json:"session_id"`
	PublicID  string             `json:"public_id,omitempty"`
	Agent     string             `json:"agent"`
	Title     string             `json:"title,omitempty"`
	Status    string             `json:"status"`
	Turns     []conversationTurn `json:"turns"`
	// TotalTurns counts the session's turns (chains folded).
	TotalTurns int  `json:"total_turns"`
	Offset     int  `json:"offset"`
	NextOffset *int `json:"next_offset"`
	// SessionMarks are the session's marks filed under no turn (a
	// grant minted between turns, a compaction on close).
	SessionMarks []sessionEntry `json:"session_marks"`
	// MarksCapped: the session holds more marks than one answer reads;
	// the turns' marks are the first ones only.
	MarksCapped bool `json:"marks_capped,omitempty"`
}

type conversationTurn struct {
	Turn int `json:"turn"`
	// RunID is the turn's first run; Runs every run of it in order (more
	// than one when it parked and was resumed).
	RunID string   `json:"run_id"`
	Runs  []string `json:"runs"`
	// Input is the user's message; InputKnown false when the run's
	// messages were not recorded (content off): Input is then the
	// prompt preview, or empty.
	Input      string `json:"input"`
	InputKnown bool   `json:"input_known"`
	// Reply is the final assistant text; null while there is none (the
	// turn failed, waits, or still runs).
	Reply *string `json:"reply"`
	// DisplayStatus and its cause (as on a run row).
	DisplayStatus string `json:"display_status"`
	StoppedBy     string `json:"stopped_by,omitempty"`
	ErrorType     string `json:"error_type,omitempty"`
	CancelCause   string `json:"cancel_cause,omitempty"`
	Err           string `json:"err,omitempty"`
	// EndedBy says what ended a turn a newer message felled: the run
	// and turn that did, and how (interrupt | rollback).
	EndedBy     string `json:"ended_by,omitempty"`
	EndedByTurn int    `json:"ended_by_turn,omitempty"`
	EndedKind   string `json:"ended_kind,omitempty"`

	Started    time.Time  `json:"started"`
	Finished   *time.Time `json:"finished"`
	DurationMS int64      `json:"duration_ms"`
	WaitingMS  int64      `json:"waiting_ms"`
	ModelCalls int        `json:"model_calls"`
	ToolCalls  int        `json:"tool_calls"`
	Usage      core.Usage `json:"usage"`
	CostUSD    *float64   `json:"cost_usd"`
	CostKnown  bool       `json:"cost_known"`

	AgentVersion *versionRef `json:"agent_version,omitempty"`
	// Waits are the turn's waits on a person, with the approvals and
	// who decided them (the ledger's records).
	Waits []waitDoc `json:"waits"`
	// Marks are the session's own marks filed under the turn's runs,
	// as the timeline has them: approvals, grants, steer and queue,
	// compaction (fields tokens_before / tokens_after), branch and fork,
	// delegation — each with its lane, time, fields and actor.
	Marks []sessionEntry `json:"marks"`
	// Compactions and DelegatedSessions count two of those kinds.
	Compactions       int `json:"compactions,omitempty"`
	DelegatedSessions int `json:"delegated_sessions,omitempty"`
}

// conversationMarkCap bounds the marks one conversation answer reads.
const conversationMarkCap = 2000

// serveSessionConversation is GET /v1/sessions/{id}/conversation.
func (s *Server) serveSessionConversation(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	limit, offset := 20, 0
	for name, dst := range map[string]*int{"limit": &limit, "offset": &offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				badRequest(w, r, name+" is a non-negative integer")
				return
			}
			*dst = n
		}
	}
	if limit == 0 || limit > 100 {
		limit = min(max(limit, 20), 100)
	}
	det, err := s.db.Session(r.Context(), id)
	if err != nil {
		dbError(w, r, "session", id, err)
		return
	}
	cost := s.coster(r.Context())
	cost.primeChildren(det.Runs)
	rows := make([]runRow, 0, len(det.Runs))
	for _, rr := range det.Runs {
		rows = append(rows, cost.row(rr))
	}
	ctl := s.control(r)
	ctl.decorate(rows)
	numberDerivedTurns(rows)
	_ = s.turnMarks(r.Context(), rows)

	// Fold each resume into the turn it continues.
	byID := map[string]int{}
	var turns [][]runRow
	for _, row := range rows {
		if i, ok := byID[row.ResumedFrom]; ok && row.ResumedFrom != "" {
			turns[i] = append(turns[i], row)
			byID[row.ID] = i
			continue
		}
		byID[row.ID] = len(turns)
		turns = append(turns, []runRow{row})
	}

	sr := sessRow(det.SessionRow)
	doc := conversationDoc{SessionID: id, PublicID: det.PublicID, Agent: det.Agent, Title: sr.Title, Status: sr.Status,
		Turns: []conversationTurn{}, TotalTurns: len(turns), Offset: offset, SessionMarks: []sessionEntry{}}

	// The session's marks, by run.
	marks := map[string][]sessionEntry{}
	if er, ok := s.db.(obsdb.SessionEventReader); ok {
		mb := s.newMarkBuilder(det)
		for read := 0; ; {
			page, err := er.SessionEvents(r.Context(), mb.query(obsdb.SessionEventQuery{SessionID: id, Offset: read,
				Limit: min(obsdb.MaxSessionEventLimit, conversationMarkCap-read)}))
			if err != nil || len(page.Events) == 0 {
				break
			}
			for _, e := range page.Events {
				m, ok := mb.mark(e)
				switch {
				case !ok:
				case m.RunID == nil:
					doc.SessionMarks = append(doc.SessionMarks, m)
				default:
					marks[*m.RunID] = append(marks[*m.RunID], m)
				}
			}
			if read += len(page.Events); read >= conversationMarkCap {
				doc.MarksCapped = read < page.Total
				break
			}
		}
	}

	for i := offset; i < len(turns) && i < offset+limit; i++ {
		doc.Turns = append(doc.Turns, s.conversationTurn(r, ctl, cost, turns[i], marks))
	}
	if next := offset + len(doc.Turns); next < len(turns) {
		doc.NextOffset = &next
	}
	writeJSON(w, r, http.StatusOK, doc)
}

func (s *Server) conversationTurn(r *http.Request, ctl *control, cost *coster, runs []runRow, marks map[string][]sessionEntry) conversationTurn {
	head, last := runs[0], runs[len(runs)-1]
	t := conversationTurn{Turn: head.Turn, RunID: head.ID, Runs: []string{}, Input: head.PromptPreview,
		DisplayStatus: last.DisplayStatus, StoppedBy: last.StoppedBy, ErrorType: last.ErrorType, CancelCause: last.CancelCause,
		Err: last.Err, Started: head.Started, Finished: last.Finished, AgentVersion: head.AgentVersion,
		CostKnown: true, Waits: []waitDoc{}, Marks: []sessionEntry{}}
	var usd float64
	for _, run := range runs {
		t.Runs = append(t.Runs, run.ID)
		t.ModelCalls += run.Steps
		t.Usage = t.Usage.Add(run.Usage)
		t.Marks = append(t.Marks, marks[run.ID]...)
		t.Compactions += run.SessionCompactions
		t.DelegatedSessions += run.DelegatedSessions
		if run.EndedBy != "" {
			t.EndedBy, t.EndedByTurn, t.EndedKind = run.EndedBy, run.EndedByTurn, run.EndedKind
		}
		for _, c := range []*float64{run.CostUSD, run.DelegatedCostUSD} {
			if c != nil {
				usd += *c
			}
		}
		if !run.CostKnown || (run.ChildrenCount > 0 && !run.DelegatedCostKnown) {
			t.CostKnown = false
		}
	}
	usd = round6(usd)
	t.CostUSD = &usd
	end := s.now()
	if t.Finished != nil && t.DisplayStatus != displayWaiting {
		end = *t.Finished
	} else {
		t.Finished = nil
	}
	t.DurationMS = max(end.Sub(t.Started).Milliseconds(), 0)
	if ch := ctl.chain(cost, head); ch != nil {
		t.Waits, t.WaitingMS, t.DisplayStatus = ch.Waits, ch.WaitingMS, ch.DisplayStatus
	}

	// The words: the user's message from the first run's input, the
	// reply from the last run's last assistant text.
	for k, run := range runs {
		batches, err := s.db.TranscriptBatches(r.Context(), run.ID)
		if err != nil {
			continue
		}
		for _, b := range batches {
			var msgs []core.Message
			if json.Unmarshal(b.Messages, &msgs) != nil {
				continue
			}
			for _, m := range msgs {
				switch {
				case b.Input && k == 0 && m.Role == core.RoleUser:
					t.Input, t.InputKnown = m.Text(), true // the last user message of the input wins
				case !b.Input && m.Role == core.RoleAssistant:
					for _, p := range m.Content {
						if _, ok := p.(core.ToolCallPart); ok {
							t.ToolCalls++
						}
					}
					if text := m.Text(); text != "" && k == len(runs)-1 {
						t.Reply = &text
					}
				}
			}
		}
	}
	if t.DisplayStatus != displaySucceeded {
		t.Reply = nil // text before a failure, a park or a cancel is not the turn's answer
	}
	return t
}
