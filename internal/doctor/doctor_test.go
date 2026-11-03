package doctor_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/internal/doctor"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

const tok = "wb_sk_test_devtoken"

// newServer serves a node over a temp data dir with tok as its
// bootstrap key.
func newServer(t *testing.T, opts ...weftbase.Option) *httptest.Server {
	t.Helper()
	return newServerIn(t, t.TempDir(), opts...)
}

// newServerIn is newServer over the data dir dir.
func newServerIn(t *testing.T, dir string, opts ...weftbase.Option) *httptest.Server {
	t.Helper()
	app, err := weftbase.New(append([]weftbase.Option{weftbase.DataDir(dir), weftbase.BootstrapKey(tok),
		weftbase.Logger(slog.New(slog.DiscardHandler))}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func run(t *testing.T, url, token string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := doctor.Run(context.Background(), &out, doctor.Target{URL: url, Token: token})
	return out.String(), err
}

// TestEveryLineMapsToMeta: every check line's label is in Lines, in
// Lines' order, every label prints, and every field a line names exists
// in the /v1/meta answer of a server where each field is present (the
// server token, a manifest, a content-off run with its fix).
func TestEveryLineMapsToMeta(t *testing.T) {
	agent := []core.Option{core.Name("orders")}
	manifest, err := core.Manifest(core.New(wefttest.Script(), agent...))
	if err != nil {
		t.Fatal(err)
	}
	ts := newServer(t, weftbase.Manifest(manifest))
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, tok, otel.NoContent()), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	a := core.New(wefttest.Script(wefttest.Say("done")), append(agent, core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))...)
	if _, err := a.Generate(ctx, core.Prompt("go")); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// One refused batch (no ingest token): ingest.last_error / last_at
	// are omitempty, served once a refusal happened.
	refused, err := http.Post(ts.URL+"/otlp/v1/traces", "application/x-protobuf", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	_ = refused.Body.Close()
	if refused.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an ingest POST with no token: %s, want 401", refused.Status)
	}

	var healthy map[string]any
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/meta", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		healthy = nil
		if err := json.Unmarshal(b, &healthy); err != nil {
			t.Fatal(err)
		}
		if l, _ := healthy["content"].(map[string]any)["latest"].(map[string]any); l["mark"] == "stripped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("meta never read the stripped run: %s", b)
		}
	}
	// The error fields are omitempty: a second server whose reads fail
	// and whose manifest does not parse serves them.
	broken := metaJSON(t, newServer(t, weftbase.Traces(failingDB{memDB(t)}), weftbase.Manifest([]byte(`{"weft":`))))
	has := func(doc map[string]any, f string) bool {
		var v any = doc
		for _, k := range strings.Split(f, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return false
			}
			if v, ok = m[k]; !ok {
				return false
			}
		}
		return true
	}
	for _, l := range doctor.Lines {
		meta := 0
		for _, f := range l.Fields {
			switch {
			case strings.HasPrefix(f, "env:"), strings.HasPrefix(f, "local:"), f == "readyz":
				continue
			case f == "status":
				meta++
				continue
			}
			meta++
			if !has(healthy, f) && !has(broken, f) {
				t.Errorf("line %q reads %s, which /v1/meta does not serve: %v / %v", l.Label, f, healthy, broken)
			}
		}
		// Every line reads server; the shell's env only beside it.
		if meta == 0 {
			t.Errorf("line %q names no /v1/meta field (status or a body field): %v", l.Label, l.Fields)
		}
	}

	out, err := run(t, ts.URL, tok)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	var labels []string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, " ") {
			continue // a continuation of the check above
		}
		f := strings.Fields(line)
		if len(f) < 2 || !slices.Contains([]string{"ok", "warn", "FAIL"}, f[0]) {
			t.Fatalf("line %q is neither a check nor a continuation", line)
		}
		labels = append(labels, f[1])
	}
	var want []string
	for _, l := range doctor.Lines {
		want = append(want, l.Label)
	}
	if !slices.Equal(labels, want) {
		t.Errorf("doctor printed checks %v, want Lines' %v:\n%s", labels, want, out)
	}
	for _, s := range []string{"content   server stores content as received; latest run", ": stripped", "fix: drop otel.NoContent()",
		"ok   data      newest run ", " by weft ", "ok   otlp      OTLP at " + ts.URL + "/otlp/v1/traces",
		"ok   key       accepted: key sk key_", " in prj_default/test/ten_project", "ok   ready     the stores answer",
		"warn discovery no discovery file",
		"db        sqlite /", "manifest  weft.json: 1 agent, 1 checked against their latest runs: current",
		// The ingest counters (UX-E-37): the app's batches accepted, the
		// one refused above, and the refusal with its fix.
		"warn ingest    ", " OTLP batches accepted, 1 refused; no app reported lost records",
		"- last refused at ", ": 401 ", "fix: server's log names each refused batch with its request id"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// TestUnreachable: a closed port is the clear first line and
// ErrUnhealthy (exit 1), within the timeout; a server that never
// answers ends with the caller's deadline, never a hang.
func TestUnreachable(t *testing.T) {
	out, err := run(t, "http://127.0.0.1:1", "")
	if !errors.Is(err, doctor.ErrUnhealthy) || !strings.HasPrefix(out, "weftbase not reachable at http://127.0.0.1:1: ") {
		t.Errorf("closed port: %v\n%s", err, out)
	}

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(hang.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var b strings.Builder
	start := time.Now()
	err = doctor.Run(ctx, &b, doctor.Target{URL: hang.URL})
	if !errors.Is(err, doctor.ErrUnhealthy) || !strings.HasPrefix(b.String(), "weftbase not reachable at "+hang.URL+": ") || time.Since(start) > 2*time.Second {
		t.Errorf("hung server: %v after %v\n%s", err, time.Since(start), b.String())
	}
}

// TestToken: no key against a server that wants one, and a wrong one,
// are failed key lines naming WEFTBASE_TOKEN; an Open node (embedded,
// loopback) needs none and answers as the local principal.
func TestToken(t *testing.T) {
	ts := newServer(t)
	for _, c := range []struct{ token, want string }{
		{"", "FAIL key       the server requires an API key: `weftbase dev` keeps its bootstrap key in <data>/bootstrap.key"},
		{"wb_sk_test_wrong", "FAIL key       refused: not a live key of this server (WEFTBASE_TOKEN / --token)"},
	} {
		out, err := run(t, ts.URL, c.token)
		if !errors.Is(err, doctor.ErrUnhealthy) || !strings.Contains(out, c.want) {
			t.Errorf("token %q: %v\n%s", c.token, err, out)
		}
	}
	open := newServer(t, weftbase.Open())
	out, err := run(t, open.URL, "")
	if err != nil || !strings.Contains(out, "ok   key       accepted: local local in prj_default/test/ten_project") || !strings.Contains(out, "ok   db        sqlite /") {
		t.Errorf("open node: %v\n%s", err, out)
	}
}

// metaJSON reads a test server's /v1/meta with the bootstrap key.
func metaJSON(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/meta", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// memDB is an empty in-memory obsdb.
func memDB(t *testing.T) obsdb.DB {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// failingDB fails every run read.
type failingDB struct{ obsdb.DB }

func (failingDB) Runs(context.Context, obsdb.RunQuery) (obsdb.RunPage, error) {
	return obsdb.RunPage{}, errors.New("disk on fire")
}

// TestReadErrorsWarn: a server whose reads fail, or whose manifest does
// not parse, is a warn line carrying meta's error — not "no run stored
// yet"; the store that fails reads fails readiness too (the ready line).
func TestReadErrorsWarn(t *testing.T) {
	ts := newServer(t, weftbase.Traces(failingDB{memDB(t)}), weftbase.Manifest([]byte(`{"weft":`)))
	out, err := run(t, ts.URL, tok)
	if !errors.Is(err, doctor.ErrUnhealthy) {
		t.Fatalf("doctor: %v, want unhealthy (not ready)\n%s", err, out)
	}
	for _, want := range []string{
		"FAIL ready     GET /readyz answered 503: not ready: trace store: disk on fire",
		"warn content   server stores content as received; the latest run could not be read: content: read the latest run: disk on fire",
		"warn manifest  the check failed: manifest_check: the manifest does not parse",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no run stored yet") {
		t.Errorf("a failed read reads as an empty database:\n%s", out)
	}
}

// TestStaleManifest: the stale line names both sides — weft.json or the
// app may be the newer one.
func TestStaleManifest(t *testing.T) {
	older, err := core.Manifest(core.New(wefttest.Script(), core.Name("orders"), core.Instructions("v0")))
	if err != nil {
		t.Fatal(err)
	}
	ts := newServer(t, weftbase.Manifest(older))
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, tok), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	a := core.New(wefttest.Script(wefttest.Say("done")), core.Name("orders"), core.Instructions("v1"),
		core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := a.Generate(ctx, core.Prompt("go")); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	want := "warn manifest  weft.json: stale for orders: weft.json and the latest runs disagree (the latest run of orders ran manifest "
	want2 := "): regenerate weft.json, or redeploy the app if weft.json is newer"
	var out string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		out, _ = run(t, ts.URL, tok)
		if strings.Contains(out, want) && strings.Contains(out, want2) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestRedirectNotFollowed: the doctor talks to --url only — a server
// URL that redirects elsewhere is a failed line naming the target, and
// the target is never asked.
func TestRedirectNotFollowed(t *testing.T) {
	asked := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/meta", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	out, err := run(t, redirect.URL, tok)
	if !errors.Is(err, doctor.ErrUnhealthy) || asked ||
		!strings.Contains(out, "FAIL key       /v1/meta answered 302: a redirect to "+target.URL+"/v1/meta, not followed") {
		t.Errorf("redirect: %v (target asked: %v)\n%s", err, asked, out)
	}
}

// TestIngestRefusalWindow: a refused batch warns while the last refusal
// is within the hour; an older one reads ok, its line still printed.
func TestIngestRefusalWindow(t *testing.T) {
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{{2 * time.Minute, "warn ingest    "}, {2 * time.Hour, "ok   ingest    "}} {
		at := time.Now().Add(-c.ago).UTC().Format(time.RFC3339)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"weft_version":"v","server_version":"s",`+
				`"db":{"kind":"sqlite"},"content":{"ingest":"as_received"},`+
				`"ingest":{"dropped":[],"accepted":7,"refused":1,"last_error":"401 unauthorized: no token","last_at":"`+at+`"}}`)
		}))
		out, _ := run(t, ts.URL, tok)
		ts.Close()
		if !strings.Contains(out, c.want+"7 OTLP batches accepted, 1 refused") || !strings.Contains(out, "- last refused at "+at) {
			t.Errorf("last refusal %s ago: want %q\n%s", c.ago, c.want, out)
		}
	}
}

// TestControlCharactersStripped: what the doctor prints comes from
// Server — a service name an OTLP sender chose among it — and a control
// character in it never reaches the terminal raw.
func TestControlCharactersStripped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"weft_version":"v\u001b[2J","server_version":"s",`+
			`"db":{"kind":"sqlite"},"content":{"ingest":"as_received"},`+
			`"ingest":{"dropped":[{"service":"evil\u001b]0;owned\u0007\rsvc","records":3,"last_heard":"2026-10-13T00:00:00Z"}],`+
			`"accepted":7,"refused":2,"last_error":"401 unauthorized: bad\u001b[2Jtoken","last_at":"2026-10-13T00:00:01Z"}}`)
	}))
	t.Cleanup(ts.Close)
	out, _ := run(t, ts.URL, tok)
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Errorf("control characters reached the output: %q", out)
	}
	if !strings.Contains(out, "warn ingest    7 OTLP batches accepted, 2 refused; 1 service(s) reported records lost") ||
		!strings.Contains(out, "- last refused at 2026-10-13T00:00:01Z: 401 unauthorized: bad [2Jtoken") {
		t.Errorf("ingest counters = %q, want the counts and the last refusal, its control characters as spaces", out)
	}
	if !strings.Contains(out, "evil ]0;owned  svc: 3 records dropped") {
		t.Errorf("service line = %q, want its control characters as spaces", out)
	}
	// weft.records.dropped counts failed exports too: the fix names both.
	if !strings.Contains(out, "fix: queue full or exports failing — the app's WARN names which") {
		t.Errorf("ingest fix = %q, want it to name the queue and failing exports", out)
	}
}

// TestDiscoveryLine (UX-S5a-01, UX-S5a-08): the discovery line says
// which server the file this shell reads names — this one, another
// (and two on one database), none, or none read under
// WEFTBASE_DISCOVERY=off. The same server reached by another spelling of
// its host is this server (its pid). The source is the command's
// stderr line, never repeated on stdout.
func TestDiscoveryLine(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "traces.db")
	ts := newServerIn(t, dir)
	check := func(d doctor.Discovery, want ...string) {
		t.Helper()
		var out strings.Builder
		err := doctor.Run(context.Background(), &out, doctor.Target{URL: ts.URL, Token: tok, Discovery: d})
		if err != nil {
			t.Fatalf("doctor: %v\n%s", err, out.String())
		}
		for _, w := range want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("discovery %+v: output lacks %q:\n%s", d, w, out.String())
			}
		}
	}
	check(doctor.Discovery{Path: "/run/weft/dev.json", URL: ts.URL, DB: db, PID: 7},
		"ok   server    reachable at "+ts.URL+" (weft ",
		"ok   discovery /run/weft/dev.json -> "+ts.URL+" (this server)")
	other := strings.Replace(ts.URL, "127.0.0.1", "localhost", 1)
	check(doctor.Discovery{Path: "/run/weft/dev.json", URL: other, DB: db, PID: os.Getpid()},
		"ok   discovery /run/weft/dev.json -> "+other+" (this server)")
	check(doctor.Discovery{Path: "/run/weft/dev.json", URL: "http://127.0.0.1:7347", DB: db, PID: 42},
		"warn discovery /run/weft/dev.json -> http://127.0.0.1:7347, ANOTHER server (pid 42, db "+db+")",
		"- two servers serve "+db+": stop one")
	check(doctor.Discovery{Off: true}, "ok   discovery WEFTBASE_DISCOVERY=off in this shell")
	check(doctor.Discovery{}, "warn discovery no discovery file")

	var out strings.Builder
	err := doctor.Run(context.Background(), &out, doctor.Target{URL: "http://127.0.0.1:1"})
	if !errors.Is(err, doctor.ErrUnhealthy) || !strings.HasPrefix(out.String(), "weftbase not reachable at http://127.0.0.1:1: ") {
		t.Errorf("unreachable default: %v\n%s", err, out.String())
	}
}

// TestTokenAdvice (B7-fix): a 401 with no key names where dev keeps
// it and the resolver's other way to reach it.
func TestTokenAdvice(t *testing.T) {
	out, _ := run(t, newServer(t).URL, "")
	if !strings.Contains(out, "- or run from the directory the server started in, where its discovery file is found") {
		t.Errorf("401 advice:\n%s", out)
	}
}

// TestDataAndOtlpLines (UX-S5a-08): the newest run from another weft
// warns with both versions; a node with and without the ingest role
// each read as such.
func TestDataAndOtlpLines(t *testing.T) {
	serve := func(body string) string {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		t.Cleanup(ts.Close)
		out, _ := run(t, ts.URL, tok)
		return out
	}
	at := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	out := serve(`{"weft_version":"v0.19.0","server_version":"v0.19.0",` +
		`"db":{"kind":"sqlite"},"capabilities":["ingest"],"ingest_open":true,` +
		`"content":{"ingest":"as_received","latest":{"run_id":"r1","mark":"full","note":"n","started":"` + at + `","weft_version":"v0.15.0","service":"shop"}}}`)
	for _, want := range []string{
		"warn data      newest run 2m0s ago by weft v0.15.0 (service shop), this server is weft v0.19.0: the versions differ",
		"ok   otlp      OTLP at ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out = serve(`{"weft_version":"v","server_version":"s","db":{"kind":"sqlite"},` +
		`"capabilities":[],"content":{"ingest":"as_received"}}`)
	if !strings.Contains(out, "warn otlp      this node runs no ingest role") ||
		!strings.Contains(out, "ok   data      no run stored yet") {
		t.Errorf("ingest off:\n%s", out)
	}
}
