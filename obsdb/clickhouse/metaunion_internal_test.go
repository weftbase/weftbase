package clickhouse

import (
	"regexp"
	"strings"
	"testing"

	"github.com/weftbase/weftbase/obsdb"
)

// Migration 0034 and the reads over it (the conformance row MetaUnion,
// which the gated suite runs against a server): a run's metadata is the
// union of its rows' maps — MetaKV, a maxMap over each row's pairs that
// an insert fills from the row's own Meta (the column's DEFAULT), so
// the views, the derived-row insert and the migrations' backfills need
// no change — never one row's map (max(Meta), which a background merge
// made permanent). These tests run without a server.

func TestMigration0034AddsTheMetaUnion(t *testing.T) {
	b, err := migrationsFS.ReadFile("migrations/" + migrations[34])
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitStatements(string(b))
	want := []string{
		"ALTER TABLE weft_runs ADD COLUMN IF NOT EXISTS MetaKV SimpleAggregateFunction(maxMap, Tuple(Array(String), Array(String))) " +
			"DEFAULT (mapKeys(JSONExtract(Meta, 'Map(String, String)')), mapValues(JSONExtract(Meta, 'Map(String, String)')))",
		"ALTER TABLE weft_runs ADD INDEX IF NOT EXISTS idx_meta_kv " + rowMetaJSON("") + " TYPE ngrambf_v1(4, 16384, 3, 0) GRANULARITY 1",
		"ALTER TABLE weft_runs MATERIALIZE INDEX idx_meta_kv",
		"ALTER TABLE weft_runs DROP INDEX IF EXISTS idx_meta",
	}
	if len(stmts) != len(want) {
		t.Fatalf("0034 has %d statements, want %d:\n%s", len(stmts), len(want), strings.Join(stmts, "\n"))
	}
	for i := range want {
		if stmts[i] != want[i] {
			t.Errorf("0034 statement %d:\n got  %s\n want %s", i, stmts[i], want[i])
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

// TestMigration0035ReadsMetaAsValidUTF8: 0034's DEFAULT handed Meta to
// JSONExtract as stored, and JSONExtract refuses a document holding a
// byte that is not UTF-8 (the views copy an attribute's bytes with
// toJSONString), so such a row's MetaKV was empty and the run read no
// metadata (conformance row Strings). 0035 restates the DEFAULT over
// toValidUTF8(Meta) and repairs exactly the rows whose Meta is invalid,
// keeping the pairs their MetaKV already holds.
func TestMigration0035ReadsMetaAsValidUTF8(t *testing.T) {
	b, err := migrationsFS.ReadFile("migrations/" + migrations[35])
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitStatements(string(b))
	parsed := "JSONExtract(toValidUTF8(Meta), 'Map(String, String)')"
	merged := "mapUpdate(" + parsed + ", " + rowMetaMap("") + ")"
	want := []string{
		"ALTER TABLE weft_runs MODIFY COLUMN MetaKV DEFAULT (mapKeys(" + parsed + "), mapValues(" + parsed + "))",
		"ALTER TABLE weft_runs UPDATE MetaKV = (mapKeys(" + merged + "), mapValues(" + merged + "))\nWHERE NOT isValidUTF8(Meta)",
	}
	if len(stmts) != len(want) {
		t.Fatalf("0035 has %d statements, want %d:\n%s", len(stmts), len(want), strings.Join(stmts, "\n"))
	}
	for i := range want {
		if stmts[i] != want[i] {
			t.Errorf("0035 statement %d:\n got  %s\n want %s", i, stmts[i], want[i])
		}
	}
}
