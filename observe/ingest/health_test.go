package ingest

import (
	"context"
	"testing"

	"github.com/weftbase/weftbase/obsdb"
)

// TestIngestHealthCarriers: the ingest health hears
// weft.records.dropped from a heartbeat (a long run's, mid-run) and from
// a run's run_start and run_finish (every run's), keeping the largest
// per service; any other record's attribute is not its to read.
func TestIngestHealthCarriers(t *testing.T) {
	var h Health
	hub := h.Hub(obsdb.NewHub())
	publish := func(svc, record, typ string, n int64) {
		hub.Publish(context.Background(), obsdb.Frame{Kind: obsdb.FrameRecord,
			Record: &obsdb.Record{Service: svc, Attrs: map[string]any{"weft.records.dropped": n}},
			Weft:   obsdb.Weft{Record: record, EventType: typ}})
	}
	records := func(svc string) int64 {
		for _, d := range h.Snapshot().Dropped {
			if d.Service == svc {
				return d.Records
			}
		}
		return 0
	}
	publish("app", "heartbeat", "", 4)
	if got := records("app"); got != 4 {
		t.Errorf("after a heartbeat: %d, want 4", got)
	}
	publish("app", "event", "run_finish", 9)
	if got := records("app"); got != 9 {
		t.Errorf("after a run_finish: %d, want 9", got)
	}
	publish("app", "event", "run_start", 6) // an older count: the max stays
	publish("app", "event", "step_start", 99)
	publish("app", "delta", "", 99)
	if got := records("app"); got != 9 {
		t.Errorf("after run_start 6 and two non-carriers of 99: %d, want 9", got)
	}
	publish("other", "event", "run_start", 2)
	if got := records("other"); got != 2 {
		t.Errorf("another service's run_start: %d, want 2", got)
	}
}
