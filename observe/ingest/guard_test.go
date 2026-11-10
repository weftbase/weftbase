package ingest

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/weftbase/weftbase/obsdb"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// fakeGate restricts every caller to the runs in own.
type fakeGate struct {
	restricted bool
	own        []string
	asked      [][]string
}

func (g *fakeGate) Restricted(*http.Request) (string, bool) { return "key_build", g.restricted }

func (g *fakeGate) Refused(_ context.Context, _ *http.Request, ids []string) ([]string, error) {
	g.asked = append(g.asked, ids)
	var out []string
	for _, id := range ids {
		if !slices.Contains(g.own, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// TestGuardRefusesOtherRuns: a restricted caller's batch that names a
// run it does not own is refused whole (403) and nothing is written;
// one that names only its runs is written, every row tagged with the
// key; an unrestricted caller passes untouched. Logs and traces, JSON
// and protobuf.
func TestGuardRefusesOtherRuns(t *testing.T) {
	for _, c := range []struct {
		name, file, ct string
		logs           bool
	}{
		{"logs json", "logs.json", "application/json", true},
		{"logs pb", "logs.pb", "application/x-protobuf", true},
		{"traces json", "traces.json", "application/json", false},
		{"traces pb", "traces.pb", "application/x-protobuf", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := fixture(t, c.file)
			send := func(db obsdb.DB, g *fakeGate) *http.Response {
				var h http.Handler = Traces(db, nil, always)
				poster := postTraces
				if c.logs {
					h, poster = Logs(db, nil, always), post
				}
				return poster(t, Guard(h, g, c.logs).ServeHTTP, body, map[string]string{"Content-Type": c.ct})
			}
			// Not its run: refused, nothing written.
			db := openDB(t)
			g := &fakeGate{restricted: true}
			resp := send(db, g)
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "dispatched to its build") {
				t.Fatalf("a foreign run: %d %s", resp.StatusCode, b)
			}
			if len(g.asked) != 1 || len(g.asked[0]) == 0 {
				t.Fatalf("the gate was asked %v", g.asked)
			}
			if n := countRows(t, db); n != 0 {
				t.Errorf("a refused batch wrote %d rows", n)
			}
			// Its runs: written, tagged.
			db = openDB(t)
			g = &fakeGate{restricted: true, own: g.asked[0]}
			if resp := send(db, g); resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("its own runs: %d %s", resp.StatusCode, b)
			}
			ref := openDB(t)
			if resp := send(ref, &fakeGate{}); resp.StatusCode != http.StatusOK {
				t.Fatalf("unrestricted: %d", resp.StatusCode)
			}
			if a, b := countRows(t, db), countRows(t, ref); a == 0 || a != b {
				t.Errorf("guarded wrote %d rows, unguarded %d", a, b)
			}
			checkTagged(t, db, c.logs)
		})
	}
}

// checkTagged: every stored span carries the key's id; for logs (whose
// reads do not return a record's resource) the batch handed on to the
// receiver is checked instead.
func checkTagged(t *testing.T, db obsdb.DB, logs bool) {
	t.Helper()
	ctx := context.Background()
	if logs {
		return // TestGuardTagsWhatItHandsOn
	}
	page, err := db.Runs(ctx, obsdb.RunQuery{ParentRunID: "*", Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range page.Runs {
		spans, err := db.RunSpans(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range spans {
			n++
			if s.Resource[AttrIngestKey] != "key_build" {
				t.Fatalf("span %s resource = %v", s.SpanID, s.Resource)
			}
		}
	}
	if n == 0 {
		t.Error("nothing to check: no spans read back")
	}
}

// TestGuardTagsWhatItHandsOn: the batch a guarded logs request hands on
// carries the key id on every record's resource, over a value the
// sender set itself, and comes decompressed in its own format.
func TestGuardTagsWhatItHandsOn(t *testing.T) {
	body := fixture(t, "logs.pb")
	var got []obsdb.Record
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := new(collogspb.ExportLogsServiceRequest)
		b, _ := io.ReadAll(r.Body)
		if err := proto.Unmarshal(b, m); err != nil {
			t.Errorf("handed on: %v", err)
		}
		got = obsdb.FromOTLPLogs(m)
		w.WriteHeader(http.StatusOK)
	})
	g := &fakeGate{restricted: true}
	resp := post(t, Guard(capture, g, true).ServeHTTP, body, map[string]string{"Content-Type": "application/x-protobuf"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("first: %d", resp.StatusCode)
	}
	g.own = g.asked[0]
	resp = post(t, Guard(capture, g, true).ServeHTTP, body, map[string]string{"Content-Type": "application/x-protobuf"})
	if resp.StatusCode != http.StatusOK || len(got) == 0 {
		t.Fatalf("own runs: %d, %d records", resp.StatusCode, len(got))
	}
	for _, r := range got {
		if r.Resource[AttrIngestKey] != "key_build" {
			t.Fatalf("record resource = %v", r.Resource)
		}
	}
}
