package clickhouse

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// The pinned exporter version whose INSERT shape the tables must stay
// compatible with (S3.6, Q6). The migration file records it; this test
// fails when the pin moves, which is a schema decision.
const pinnedExporterVersion = "v0.162.0"

func TestMigrationPinsExporterVersion(t *testing.T) {
	body, err := migrationsFS.ReadFile("migrations/" + migrations[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), pinnedExporterVersion) {
		t.Errorf("migration 0001 does not name the pinned exporter version %s", pinnedExporterVersion)
	}
	if highestMigration() != WantSchemaVersion {
		t.Errorf("highest migration = %d, want %d (%s)", highestMigration(), WantSchemaVersion, WantSchemaThrough)
	}
}

// The embedded migration files are numbered 1..N with no gap, N the
// version a fresh database reaches (WantSchemaVersion): a dropped or
// misnumbered file fails here — WantMigrationRows, derived from the
// files, would follow a drop silently (U27-fix).
func TestMigrationFilesAreContiguous(t *testing.T) {
	for v := 1; v <= len(migrations); v++ {
		if migrations[v] == "" {
			t.Errorf("migration %04d missing: the embedded files are %v, want 1..%d with no gap", v, sortedVersions(), len(migrations))
		}
	}
	if len(migrations) != WantSchemaVersion || highestMigration() != WantSchemaVersion {
		t.Errorf("%d migration files, highest %d; want exactly 1..%d (%s)", len(migrations), highestMigration(), WantSchemaVersion, WantSchemaThrough)
	}
}

// The collector-shape columns must exist in our DDL: every column the
// pinned exporter's INSERT names (its traces_insert.sql and
// logs_insert.sql at that tag, plus the EventName feature column it
// fills when DESC TABLE finds it) has to appear in the CREATE TABLE.
// The server-backed collector-shape test proves the insert lands; this
// one catches a rename offline.
func TestCollectorInsertColumnsExist(t *testing.T) {
	body, err := migrationsFS.ReadFile("migrations/" + migrations[1])
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(body)
	tracesInsert := []string{
		"Timestamp", "TraceId", "SpanId", "ParentSpanId", "TraceState", "SpanName",
		"SpanKind", "ServiceName", "ResourceAttributes", "ScopeName", "ScopeVersion",
		"SpanAttributes", "Duration", "StatusCode", "StatusMessage",
		"Events.Timestamp", "Events.Name", "Events.Attributes",
		"Links.TraceId", "Links.SpanId", "Links.TraceState", "Links.Attributes",
	}
	logsInsert := []string{
		"Timestamp", "TraceId", "SpanId", "TraceFlags", "SeverityText", "SeverityNumber",
		"ServiceName", "Body", "ResourceSchemaUrl", "ResourceAttributes",
		"ScopeSchemaUrl", "ScopeName", "ScopeVersion", "ScopeAttributes", "LogAttributes",
		"EventName", // the pinned exporter fills it when the column exists; ours does
	}
	for _, table := range []struct {
		name string
		cols []string
	}{
		{"otel_traces", tracesInsert},
		{"otel_logs", logsInsert},
	} {
		create := createBlock(ddl, table.name)
		if create == "" {
			t.Fatalf("no CREATE TABLE %s in migration", table.name)
		}
		for _, col := range table.cols {
			if !columnDefined(create, col) {
				t.Errorf("%s: collector insert column %q missing from our DDL", table.name, col)
			}
		}
	}
}

func createBlock(ddl, table string) string {
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS "+table+" (")
	if start < 0 {
		return ""
	}
	rest := ddl[start:]
	end := strings.Index(rest, ") ENGINE")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// columnDefined reports whether col is declared in a CREATE block. A
// dotted name is the collector's spelling of a Nested element
// (Events.Timestamp): it matches the nested declaration "Events Nested
// ( Timestamp ... )" by its parts.
func columnDefined(create, col string) bool {
	if name, sub, dotted := strings.Cut(col, "."); dotted {
		nested := regexp.MustCompile("(?ms)^\\s*`?" + name + "`?\\s+Nested\\s*\\((.*?)\\)\\s*(CODEC|$)")
		m := nested.FindStringSubmatch(create)
		if m == nil {
			return false
		}
		subRe := regexp.MustCompile("(?m)^\\s*`?" + sub + "`?\\s")
		return subRe.MatchString(m[1])
	}
	re := regexp.MustCompile("(?m)^\\s*`?" + col + "`?\\s")
	return re.MatchString(create)
}

// The engine and ORDER BY lines S3.6 names.
func TestSpecEnginesPresent(t *testing.T) {
	body, err := migrationsFS.ReadFile("migrations/" + migrations[1])
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(body)
	for _, want := range []string{
		"ReplacingMergeTree(InsertTime)\nORDER BY (ProjectId, Env, TenantId, RunId, Kind, Pos)",
		"AggregatingMergeTree\nORDER BY (ProjectId, Env, TenantId, RunId)",
		"ORDER BY (ProjectId, Env, TenantId, ServiceName, SpanName, toDateTime(Timestamp))",
		"ORDER BY (ProjectId, Env, TenantId, toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)",
		"LogAttributes['weft.record'] IN ('event', 'messages', 'request', 'prompt', 'tools', 'raw')",
		"INDEX idx_weft_session_id SessionId TYPE bloom_filter(0.01)",
		"INDEX idx_weft_public_id PublicId TYPE bloom_filter(0.01)",
		"INDEX idx_weft_agent Agent TYPE bloom_filter(0.01)",
		"INDEX idx_trace_id TraceId TYPE bloom_filter(0.001)",
		"TTL Timestamp + toIntervalSecond(7776000)", // spans: 90 d
		"TTL Timestamp + toIntervalSecond(2592000)", // content: 30 d
		"TTL Time + toIntervalSecond(2592000)",      // weft_records' own content TTL
		"TTL LastSeen + toIntervalSecond(7776000)",  // runs: 90 d
		"SimpleAggregateFunction(min, DateTime64(9))",
		"SimpleAggregateFunction(max, Nullable(DateTime64(9)))",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("migration missing %q", want)
		}
	}
}

func TestSplitStatements(t *testing.T) {
	// Multi-line statements (the DDL's shape) must survive intact.
	got := splitStatements("-- a comment\nCREATE TABLE a (\n    x UInt8,\n    s String\n);\n\n-- another\nCREATE TABLE b (y String);\nSELECT 1;")
	want := []string{"CREATE TABLE a (\n    x UInt8,\n    s String\n)", "CREATE TABLE b (y String)", "SELECT 1"}
	if len(got) != len(want) {
		t.Fatalf("statements = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement %d = %q, want %q", i, got[i], want[i])
		}
	}
	if s := splitStatements("-- only comments\n"); len(s) != 0 {
		t.Errorf("comment-only body yielded %#v", s)
	}
}

func TestTTLOptionDefaults(t *testing.T) {
	var cfg openConfig
	TTL(0, 0).apply(&cfg)
	if cfg.contentTTL != 0 || cfg.metaTTL != 0 {
		t.Error("zero TTL args must leave the zero config (Open applies the defaults)")
	}
	cfg = openConfig{}
	TTL(time.Hour, 0).apply(&cfg)
	if cfg.contentTTL != time.Hour {
		t.Errorf("content ttl = %v, want 1h", cfg.contentTTL)
	}
	if cfg.metaTTL != 0 {
		t.Errorf("meta ttl = %v, want untouched", cfg.metaTTL)
	}
	// Open seeds the defaults; an override changes one class only.
	cfg = openConfig{contentTTL: DefaultContentTTL, metaTTL: DefaultMetaTTL}
	TTL(0, 48*time.Hour).apply(&cfg)
	if cfg.contentTTL != DefaultContentTTL {
		t.Errorf("content ttl changed by a zero arg: %v", cfg.contentTTL)
	}
	if cfg.metaTTL != 48*time.Hour {
		t.Errorf("meta ttl = %v, want 48h", cfg.metaTTL)
	}
	if DefaultContentTTL != 30*24*time.Hour || DefaultMetaTTL != 90*24*time.Hour {
		t.Error("S3.6 defaults are 30 d content / 90 d spans and runs")
	}
}

// A window renders as whole seconds, rounded up — never the zero
// interval a truncated sub-second window produced, which expires every
// row on arrival.
func TestTTLSecondsRoundUp(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want int64
	}{
		{500 * time.Millisecond, 1}, {time.Nanosecond, 1}, {time.Second, 1},
		{1500 * time.Millisecond, 2}, {time.Hour, 3600}, {DefaultContentTTL, 2592000}, {DefaultMetaTTL, 7776000},
	} {
		if got := ttlSeconds(c.in); got != c.want {
			t.Errorf("ttlSeconds(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestKeepDeltasOption(t *testing.T) {
	var cfg openConfig
	KeepDeltas().apply(&cfg)
	if !cfg.keepDeltas {
		t.Error("KeepDeltas did not arm delta storage")
	}
}

// The span kind and status names the pinned exporter writes (pdata's
// String() spellings — exporter_traces.go at v0.162.0), round-tripped,
// and the OTLP proto value names older rows carry still read.
func TestOTLPEnumNames(t *testing.T) {
	for kind := 0; kind <= 6; kind++ {
		if got := spanKindInt(spanKindName(kind)); got != kind && kind <= 5 {
			t.Errorf("span kind %d round trip = %d", kind, got)
		}
	}
	if spanKindName(0) != "Unspecified" || spanKindName(1) != "Internal" ||
		spanKindName(3) != "Client" {
		t.Error("span kind names must be pdata's SpanKind.String() spellings the exporter writes")
	}
	if spanKindInt("weird") != 0 {
		t.Error("unknown kind reads unspecified")
	}
	for code := 0; code <= 3; code++ {
		if got := statusCodeInt(statusCodeName(code)); got != code && code <= 2 {
			t.Errorf("status code %d round trip = %d", code, got)
		}
	}
	if statusCodeName(2) != "Error" || statusCodeName(1) != "Ok" || statusCodeName(0) != "Unset" {
		t.Error("status code names must be pdata's StatusCode.String() spellings the exporter writes")
	}
	// Rows from before migration 0003 (and older exporters).
	if spanKindInt("SPAN_KIND_SERVER") != 2 || statusCodeInt("STATUS_CODE_ERROR") != 2 ||
		statusCodeInt("STATUS_CODE_OK") != 1 {
		t.Error("the OTLP proto value names must still read")
	}
}

func TestAttrStringify(t *testing.T) {
	got := stringAttrs(map[string]any{
		"s": "x", "b": true, "i": int64(7), "f": 1.5,
		"arr": []any{"a", int64(1)}, "nil": nil,
	})
	want := map[string]string{
		"s": "x", "b": "true", "i": "7", "f": "1.5", "arr": `["a",1]`, "nil": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q = %q, want %q", k, got[k], v)
		}
	}
	if got := stringAttrs(nil); len(got) != 0 {
		t.Errorf("nil attrs = %v, want empty non-nil map for the driver", got)
	}
}

func TestJSONColumns(t *testing.T) {
	if jsonOrNull(nil) != "" || jsonOrNull(map[string]any{}) != "" {
		t.Error("empty maps must serialize to '' (the stock-collector default)")
	}
	if js := jsonOrNull(map[string]any{"n": int64(1)}); js != `{"n":1}` {
		t.Errorf("typed json = %q", js)
	}
	if s := eventsJSON(nil); s != "" {
		t.Errorf("no events = %q, want ''", s)
	}
	if s := eventsJSON([]obsdb.SpanEvent{{Name: "exception"}}); !strings.Contains(s, `"Name":"exception"`) {
		t.Errorf("events json = %q", s)
	}
}

// eventsFromJSONOrNested must decode the weft events column with the
// UseNumber rule — an int64 event attribute stays int64 (S3.1/S3.3,
// the same guarantee the top-level Attrs round trip pins; plain
// json.Unmarshal would return float64) — and the collector's nested
// arrays carry their string values through.
func TestEventsDecodeTyped(t *testing.T) {
	at := time.Unix(0, 1790845923120000000).UTC()
	js := eventsJSON([]obsdb.SpanEvent{{
		Time:  at,
		Name:  "gen_ai.content.prompt",
		Attrs: map[string]any{"retry.count": int64(3), "ratio": 0.5, "ok": true},
	}})
	got := eventsFromJSONOrNested(js, nil, nil, nil)
	if len(got) != 1 {
		t.Fatalf("events from json = %d, want 1", len(got))
	}
	if n, ok := got[0].Attrs["retry.count"].(int64); !ok || n != 3 {
		t.Errorf("int64 event attr = %#v, want int64(3)", got[0].Attrs["retry.count"])
	}
	if f, ok := got[0].Attrs["ratio"].(float64); !ok || f != 0.5 {
		t.Errorf("float event attr = %#v, want float64(0.5)", got[0].Attrs["ratio"])
	}
	if b, ok := got[0].Attrs["ok"].(bool); !ok || !b {
		t.Errorf("bool event attr = %#v, want true", got[0].Attrs["ok"])
	}
	if !got[0].Time.Equal(at) || got[0].Name != "gen_ai.content.prompt" {
		t.Errorf("event = %v %q, want %v %q", got[0].Time, got[0].Name, at, "gen_ai.content.prompt")
	}
	fallback := eventsFromJSONOrNested("", []time.Time{at}, []string{"exception"},
		[]map[string]string{{"exception.type": "RuntimeError"}})
	if len(fallback) != 1 || fallback[0].Name != "exception" ||
		fallback[0].Attrs["exception.type"] != "RuntimeError" {
		t.Errorf("nested fallback = %+v", fallback)
	}
}

func TestSpanDuration(t *testing.T) {
	start := time.Now()
	if d := spanDuration(start, start.Add(1500*time.Millisecond)); d != 1500000000 {
		t.Errorf("duration = %d", d)
	}
	if d := spanDuration(start, start); d != 0 {
		t.Errorf("zero-length span = %d", d)
	}
	if d := spanDuration(start.Add(time.Second), start); d != 0 {
		t.Errorf("inverted span must floor at 0, got %d", d)
	}
}

func TestSeverityNumberClamp(t *testing.T) {
	for _, c := range []struct{ in, want int }{{-1, 0}, {0, 0}, {9, 9}, {24, 24}, {25, 24}} {
		if got := severityNumber(c.in); int(got) != c.want {
			t.Errorf("severityNumber(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// contractKeys must be exactly obsdb.MetaOf's exclusion set: every key
// in the copy drops a "v"-valued attribute from the metadata, and a
// sample of non-contract keys survives. MetaOf stays the one
// implementation; this pins the SQL filter's copy to it.
func TestContractKeysMatchMetaOf(t *testing.T) {
	for _, k := range contractKeys {
		if meta := obsdb.MetaOf(map[string]any{k: "v"}); len(meta) != 0 {
			t.Errorf("contract key %q survives MetaOf; the SQL copy is stale", k)
		}
	}
	for _, k := range []string{"tenant", "enduser.id", "weft.session.parent", "env"} {
		if meta := obsdb.MetaOf(map[string]any{k: "v"}); meta[k] != "v" {
			t.Errorf("metadata key %q dropped by MetaOf", k)
		}
	}
}

// The Runs meta filter compiles to a JSON condition over the metadata
// column — the SQL shape of a MetaOf subset match — applied to the
// grouped rows, never to weft_runs' stored ones; the row-level narrowing
// beside it (idx_meta_kv's LIKE over a row's union, 0034) only picks the
// runs to collapse.
func TestRunsInnerMetaFilter(t *testing.T) {
	sql, _ := runsInner(obsdb.RunQuery{Meta: map[string]string{"tenant": "acme"}}, runsCut{})
	for _, want := range []string{
		"JSONHas(Meta, ?)",
		"JSONExtractString(Meta, ?) = ?",
		"FROM weft_runs WHERE RunId IN (SELECT t.RunId FROM weft_runs AS t WHERE toJSONString(CAST(t.MetaKV, 'Map(String, String)')) LIKE ?) GROUP BY RunId) WHERE ParentRunID = '' AND CallerRunID = '' AND JSONHas(Meta, ?)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("runsInner missing %q:\n%s", want, sql)
		}
	}
}

// Every Runs filter reads the grouped row: nothing but the narrowing
// RunId IN (...) may sit between weft_runs and its GROUP BY, or an
// unmerged run is filtered row by row (the child-run leak). An identity
// equality binds twice — once to narrow the scan, once on the grouped
// row — in text order.
func TestRunsInnerFiltersAfterGrouping(t *testing.T) {
	sql, args := runsInner(obsdb.RunQuery{
		Agent: "a", SessionID: "s", PublicID: "p", ParentRunID: "par",
	}, runsCut{})
	inner, outer, ok := strings.Cut(sql, " GROUP BY RunId) WHERE ")
	if !ok {
		t.Fatalf("runsInner has no grouped subquery:\n%s", sql)
	}
	_, where, _ := strings.Cut(inner, " FROM weft_runs WHERE ")
	for _, cond := range strings.Split(where, " AND ") {
		if !strings.HasPrefix(cond, "RunId IN (SELECT r.RunId FROM weft_runs AS r WHERE r.") {
			t.Errorf("row-level filter before GROUP BY: %q", cond)
		}
	}
	if outer != "Agent = ? AND SessionID = ? AND PublicID = ? AND ParentRunID = ?" {
		t.Errorf("grouped-row filter = %q", outer)
	}
	want := []any{"a", "s", "p", "par", "a", "s", "p", "par"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %d binds", args, len(want))
	}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("arg %d = %v, want %v", i, args[i], w)
		}
	}
}

// TestContractKeysCoverNonMeta is TestContractKeysMatchMetaOf's reverse
// (U5U7-fix 6): every key obsdb.MetaOf excludes is in contractKeys, so
// the SQL filters keep no contract key in stored Meta — or it is
// exempt here, with the reason. obsdb.NonMetaKeys lists them.
func TestContractKeysCoverNonMeta(t *testing.T) {
	exempt := map[string]string{}
	for _, k := range obsdb.NonMetaKeys() {
		if slices.Contains(contractKeys, k) {
			continue
		}
		if why, ok := exempt[k]; ok {
			t.Logf("%s exempt: %s", k, why)
			continue
		}
		t.Errorf("MetaOf excludes %q but contractKeys lacks it: ClickHouse's stored Meta keeps it", k)
	}
}

func TestRunsInnerParentScoping(t *testing.T) {
	for _, c := range []struct {
		q    obsdb.RunQuery
		want string
	}{{obsdb.RunQuery{}, "ParentRunID = ''"}, {obsdb.RunQuery{ParentRunID: "p"}, "ParentRunID = ?"}} {
		sql, _ := runsInner(c.q, runsCut{})
		if !strings.Contains(sql, c.want) {
			t.Errorf("runsInner(%+v) missing %q", c.q, c.want)
		}
	}
	sql, _ := runsInner(obsdb.RunQuery{ParentRunID: "*"}, runsCut{})
	if strings.Contains(sql, "ParentRunID =") {
		t.Error("'*' must not scope parentage")
	}
}

// The views' mapFilter tuples must be exactly obsdb.MetaOf's exclusion
// set — the SQL copy of contractKeys, in its order: 0001's two run
// views each carry it once.
func TestMigrationContractTuple(t *testing.T) {
	body, err := migrationsFS.ReadFile("migrations/" + migrations[1])
	if err != nil {
		t.Fatal(err)
	}
	const marker = "NOT has(["
	if n := strings.Count(string(body), marker); n != 2 {
		t.Fatalf("migration 0001: expected the tuple twice, found %d", n)
	}
	rest := string(body)
	for range 2 {
		start := strings.Index(rest, marker) + len(marker)
		end := strings.Index(rest[start:], "], k)")
		if end < 0 {
			t.Fatal("tuple not terminated")
		}
		var sqlKeys []string
		for _, part := range strings.Split(rest[start:start+end], ",") {
			sqlKeys = append(sqlKeys, strings.Trim(strings.TrimSpace(part), "'"))
		}
		if !slices.Equal(sqlKeys, contractKeys) {
			t.Errorf("tuple = %v, want contractKeys %v", sqlKeys, contractKeys)
		}
		rest = rest[start+end:]
	}
}
