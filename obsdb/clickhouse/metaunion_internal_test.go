package clickhouse

import (
	"regexp"
	"strings"
	"testing"

	"github.com/weftbase/weftbase/obsdb"
)

// The metadata's union and the reads over it (the conformance row
// MetaUnion, which the gated suite runs against a server): a run's
// metadata is the union of its rows' maps — MetaKV, a maxMap over each
// row's pairs that an insert fills from the row's own Meta (the
// column's DEFAULT, read through toValidUTF8: JSONExtract refuses a
// document holding a byte that is not UTF-8), so the views and the
// derived-row insert need no change — never one row's map (max(Meta),
// which a background merge made permanent). These tests run without a
// server.

func TestMigrationDeclaresTheMetaUnion(t *testing.T) {
	b, err := migrationsFS.ReadFile("migrations/" + migrations[1])
	if err != nil {
		t.Fatal(err)
	}
	parsed := "JSONExtract(toValidUTF8(Meta), 'Map(String, String)')"
	for _, want := range []string{
		"MetaKV SimpleAggregateFunction(maxMap, Tuple(Array(String), Array(String))) DEFAULT (mapKeys(" + parsed + "), mapValues(" + parsed + "))",
		"INDEX idx_meta_kv " + rowMetaJSON("") + " TYPE ngrambf_v1(4, 16384, 3, 0) GRANULARITY 1",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("0001 does not declare %s", want)
		}
	}
}

// TestReadsUseTheMetaUnion: no read of a run's metadata takes one row's
// map — every collapse reads metaUnion, every FINAL read and every raw
// row read the row's MetaKV — and the tag narrowing asks the index over
// the union (idx_meta_kv), so a run whose pair survives only in the
// union is not pruned before the exact match.
func TestReadsUseTheMetaUnion(t *testing.T) {
	oneRow := regexp.MustCompile(`max\(Meta\)|JSONExtractString\(Meta, '|[a-z]\.Meta\b|\bMeta LIKE`)
	cut := runsCut{}
	inner, _ := runsInner(obsdb.RunQuery{Meta: map[string]string{"tenant": "t1"}}, cut)
	stats := statsBelow("SELECT 1 AS rid", "pub", map[string]string{"tenant": "t1"})
	ts := turnScope{meta: map[string]string{"tenant": "t1"}}
	narrow, _ := ts.narrow()
	reads := map[string]string{
		"runsInner":   inner,
		"runFinal":    runFinalColumns,
		"treeRows":    treeRowColumns,
		"sessionRuns": sessionRuns(" WHERE "+strings.Join(narrow, " AND "), "", cut, true),
		"statsBelow":  stats,
		"rowMetaCond": rowMetaCond("r."),
	}
	for name, sql := range reads {
		if m := oneRow.FindString(sql); m != "" {
			t.Errorf("%s reads one row's map (%q):\n%s", name, m, sql)
		}
		if !strings.Contains(sql, "MetaKV") {
			t.Errorf("%s does not read MetaKV:\n%s", name, sql)
		}
	}
	if !strings.Contains(inner, rowMetaJSON("t.")+` LIKE ?`) {
		t.Errorf("runsInner's tag narrowing is not the idx_meta_kv expression:\n%s", inner)
	}
	if strings.Replace(runFinalColumns, rowMetaJSON("")+" AS Meta,", "Meta,", 1) != runColumns {
		t.Errorf("runFinalColumns is not runColumns with Meta read from MetaKV:\n%s", runFinalColumns)
	}
}
