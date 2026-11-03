package obsdbtest

import (
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// metaUnion: a run's metadata is the union of what its rows carry, on
// every backend and through every read — never one row's map. Run
// r_mu's records and invoke span carry the caller's tenant; a tool span
// of the same run carries only weft.session.title, whose JSON
// ({"weft.session.title":…}) sorts above {"tenant":…}: a backend that
// kept the greatest row's map (ClickHouse's max(Meta) before migration
// 0034) lost the tenant — the tag a panel token is scoped by — and the
// title with the other order. Run, Runs and Sessions under the tag
// filter, the session's title, the run tree's tag scope and Stats'
// tag filter all keep both.
func metaUnion(open func(t *testing.T) obsdb.DB) func(*testing.T) {
	return func(t *testing.T) {
		db := open(t)
		const id, other = "r_mu", "r_mu_other"
		ident := func(session, tenant string) map[string]any {
			return map[string]any{"weft.session.id": session, "weft.public_id": "pub_" + session, "tenant": tenant}
		}
		recs := []obsdb.Record{
			record(id, "event", "run_start", 0, `{"type":"run_start","id":"`+id+`"}`, ident("s_mu", "t1")),
			record(id, "event", "run_finish", 1, `{"type":"run_finish","run_id":"`+id+`","usage":{"input_tokens":10,"output_tokens":2},"steps":1}`, ident("s_mu", "t1")),
			record(other, "event", "run_start", 0, `{"type":"run_start","id":"`+other+`"}`, ident("s_mu2", "t2")),
			record(other, "event", "run_finish", 1, `{"type":"run_finish","run_id":"`+other+`","steps":1}`, ident("s_mu2", "t2")),
		}
		inv := invokeSpan(id, 1, ident("s_mu", "t1"))
		inv.SpanID = "0d0d0d0d0d0d0d01"
		tool := obsdb.Span{
			TraceID: inv.TraceID, SpanID: "0d0d0d0d0d0d0d02", ParentSpanID: inv.SpanID,
			Name: "execute_tool lookup", Kind: 1, Start: at(time.Second), End: at(2 * time.Second), StatusCode: 1,
			Service: "conf-svc", Resource: map[string]any{"service.name": "conf-svc"},
			Attrs: map[string]any{
				"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "lookup", "weft.run.id": id,
				"weft.session.id": "s_mu", "weft.session.title": "zz-title",
			},
		}
		if err := db.Write(ctx(), obsdb.Batch{Records: recs, Spans: []obsdb.Span{inv, tool}}); err != nil {
			t.Fatal(err)
		}

		det, err := db.Run(ctx(), id)
		if err != nil {
			t.Fatal(err)
		}
		if det.Meta["tenant"] != "t1" || det.Meta[obsdb.SessionTitleKey] != "zz-title" {
			t.Errorf("Run(%s).Meta = %v, want tenant t1 and the title: the union of its rows", id, det.Meta)
		}
		tag := map[string]string{"tenant": "t1"}
		page, err := db.Runs(ctx(), obsdb.RunQuery{Meta: tag})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Runs) != 1 || page.Runs[0].ID != id || page.Runs[0].Meta[obsdb.SessionTitleKey] != "zz-title" {
			t.Errorf("Runs{tenant=t1} = %v (%+v), want %s alone with the union", idsOf(page.Runs), page.Runs, id)
		}
		sp, err := db.Sessions(ctx(), obsdb.SessionQuery{Meta: tag})
		if err != nil {
			t.Fatal(err)
		}
		if len(sp.Sessions) != 1 || sp.Sessions[0].ID != "s_mu" || sp.Sessions[0].Title != "zz-title" {
			t.Errorf("Sessions{tenant=t1} = %+v, want s_mu titled zz-title", sp.Sessions)
		}
		if tr, ok := db.(obsdb.TreeReader); ok {
			tree, err := tr.RunTreeSpans(ctx(), obsdb.TreeQuery{RunID: id, Meta: tag})
			if err != nil {
				t.Fatal(err)
			}
			if len(tree.Spans) != 2 || tree.Hidden {
				t.Errorf("RunTreeSpans{%s, tenant=t1} = %d spans, hidden %v; want both spans of the run", id, len(tree.Spans), tree.Hidden)
			}
		}
		if a, ok := db.(obsdb.Aggregator); ok {
			q := obsdb.StatsQuery{RunQuery: obsdb.RunQuery{Meta: tag}, By: obsdb.StatsByAgent,
				Since: base.Add(-time.Hour), Until: base.Add(time.Hour), Buckets: 1}
			st, err := a.Stats(ctx(), q)
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Groups) != 1 || st.Groups[0].Runs != 1 {
				t.Errorf("Stats{tenant=t1} groups = %+v, want one group of one run", st.Groups)
			}
		}
	}
}
