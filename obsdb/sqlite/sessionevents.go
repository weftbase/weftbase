package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// SessionEvents pages a session's thread records and compaction markers
// from the session_events index (obsdb.SessionEventReader; migration
// 0022): one session (its public id's under q.PublicID) in (time, run,
// kind, body) order — the ClickHouse reader's order, body the tie-break
// two records of one time, run and kind need — session_events_time
// serving both the count and the page.
func (d *DB) SessionEvents(ctx context.Context, q obsdb.SessionEventQuery) (_ obsdb.SessionEventPage, err error) {
	if err := d.checkOpen(); err != nil {
		return obsdb.SessionEventPage{}, err
	}
	defer d.closedErr(&err)
	page := obsdb.SessionEventPage{Events: []obsdb.SessionEvent{}}
	if q.SessionID == "" {
		return page, nil
	}
	where := `session_id = ? AND (? = '' OR public_id = ?)`
	args := []any{q.SessionID, q.PublicID, q.PublicID}
	if len(q.RunIDs) > 0 {
		// q.RunIDs: the runs' events alone (a runless one is none of them).
		where += ` AND run_id IN (?` + strings.Repeat(`, ?`, len(q.RunIDs)-1) + `)`
		for _, id := range q.RunIDs {
			args = append(args, id)
		}
	}
	if len(q.Kinds) > 0 {
		// q.Kinds: obsdb.SessionEventKind's rule over the stored name
		// (the writer stores the derived one, obsdb.SessionEventName).
		in := `(?` + strings.Repeat(`, ?`, len(q.Kinds)-1) + `)`
		where += ` AND ((kind = 'compaction' AND 'compaction' IN ` + in + `)
			OR (kind <> 'compaction' AND (CASE WHEN substr(event_name, 1, 12) = 'weft.thread.' THEN substr(event_name, 13) ELSE event_name END) IN ` + in + `))`
		for range 2 {
			for _, k := range q.Kinds {
				args = append(args, k)
			}
		}
	}
	if err := d.reads.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_events WHERE `+where, args...).Scan(&page.Total); err != nil {
		return obsdb.SessionEventPage{}, err
	}
	offset := max(q.Offset, 0)
	rs, err := d.reads.QueryContext(ctx,
		`SELECT run_id, kind, event_name, public_id, content, time_ns, body FROM session_events
		WHERE `+where+`
		ORDER BY time_ns, run_id, kind, body LIMIT ? OFFSET ?`,
		append(args, q.PageLimit(), offset)...)
	if err != nil {
		return obsdb.SessionEventPage{}, err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		e := obsdb.SessionEvent{SessionID: q.SessionID}
		var ns int64
		var body string
		if err := rs.Scan(&e.RunID, &e.Kind, &e.Name, &e.PublicID, &e.Content, &ns, &body); err != nil {
			return obsdb.SessionEventPage{}, err
		}
		e.Time = time.Unix(0, ns).UTC()
		e.Body = json.RawMessage(body)
		page.Events = append(page.Events, e)
	}
	return page, rs.Err()
}
