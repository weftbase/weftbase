package weftbase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/tenant"
)

// bootstrap is a well-formed secret key of the test env.
const bootstrap = "wb_sk_test_kernelkernelkernelkernelkernel23"

func newApp(t *testing.T, opts ...Option) (*App, *httptest.Server) {
	t.Helper()
	app, err := New(append([]Option{DataDir(t.TempDir()), BootstrapKey(bootstrap)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		ts.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		if err := app.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return app, ts
}

func call(t *testing.T, method, url, key, ctype string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestKernelServes: New over a temp data dir lays out the two stores,
// the handler serves /healthz, /readyz and /v1/meta with the bootstrap
// key (and 401 without it), and an OTLP batch posted to /otlp/v1/*
// with the key is a run on GET /v1/runs.
func TestKernelServes(t *testing.T) {
	app, ts := newApp(t)
	for _, f := range []string{"control.db", "traces.db"} {
		if _, err := os.Stat(filepath.Join(app.DataDir(), f)); err != nil {
			t.Errorf("data dir lacks %s: %v", f, err)
		}
	}
	if code, body := call(t, "GET", ts.URL+"/healthz", "", "", nil); code != 200 {
		t.Errorf("/healthz: %d %s", code, body)
	}
	if code, body := call(t, "GET", ts.URL+"/readyz", "", "", nil); code != 200 {
		t.Errorf("/readyz: %d %s", code, body)
	}
	if err := app.Ready(); err != nil {
		t.Errorf("Ready: %v", err)
	}
	if code, _ := call(t, "GET", ts.URL+"/v1/meta", "", "", nil); code != http.StatusUnauthorized {
		t.Errorf("/v1/meta without a key: %d, want 401", code)
	}
	code, body := call(t, "GET", ts.URL+"/v1/meta", bootstrap, "", nil)
	var meta struct {
		ServerVersion string   `json:"server_version"`
		Capabilities  []string `json:"capabilities"`
		Principal     struct {
			Kind, KeyKind string
			Scope         struct{ Project, Env, Tenant string }
		} `json:"principal"`
	}
	if err := json.Unmarshal([]byte(body), &meta); code != 200 || err != nil {
		t.Fatalf("/v1/meta: %d %s", code, body)
	}
	if meta.Principal.Kind != "key" || meta.Principal.Scope.Project != tenant.DefaultProject ||
		meta.Principal.Scope.Env != "test" || strings.Join(meta.Capabilities, ",") != "ingest" {
		t.Errorf("meta = %+v", meta)
	}

	// The record contract's failed run, through ingest.
	dir := filepath.Join("obsdb", "testdata", "contract", "failed")
	for _, f := range []struct{ file, route string }{{"traces.json", "/otlp/v1/traces"}, {"logs.json", "/otlp/v1/logs"}} {
		b, err := os.ReadFile(filepath.Join(dir, f.file))
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := call(t, "POST", ts.URL+f.route, "", "application/json", b); code != http.StatusUnauthorized {
			t.Errorf("%s without a key: %d, want 401", f.route, code)
		}
		if code, body := call(t, "POST", ts.URL+f.route, bootstrap, "application/json", b); code != 200 {
			t.Fatalf("%s: %d %s", f.route, code, body)
		}
	}
	var page struct {
		Runs []struct {
			ID, Status, Agent string
		} `json:"runs"`
	}
	code, body = call(t, "GET", ts.URL+"/v1/runs", bootstrap, "", nil)
	if err := json.Unmarshal([]byte(body), &page); code != 200 || err != nil {
		t.Fatalf("/v1/runs: %d %s", code, body)
	}
	if len(page.Runs) != 1 || page.Runs[0].ID != "a1100000000000000000000000000001" || page.Runs[0].Status != "failed" || page.Runs[0].Agent != "support" {
		t.Errorf("runs = %+v", page.Runs)
	}
	// The Console answers the root.
	if code, body := call(t, "GET", ts.URL+"/", "", "", nil); code != 200 || !strings.Contains(strings.ToLower(body), "<!doctype html") {
		t.Errorf("Console: %d", code)
	}
	// The bootstrap key's import was an audited outbox event.
	evs, err := app.Events().After(context.Background(), 0, 10)
	if err != nil || len(evs) != 1 || evs[0].Type != "key.minted" {
		t.Errorf("outbox = %+v %v", evs, err)
	}
}

// TestKernelRoles: an ingest-only node serves OTLP and health, never
// /v1 or the Console; an api-only node serves no OTLP.
func TestKernelRoles(t *testing.T) {
	_, ingestOnly := newApp(t, Roles(RoleIngest))
	if code, _ := call(t, "GET", ingestOnly.URL+"/v1/meta", bootstrap, "", nil); code != http.StatusNotFound {
		t.Errorf("ingest node /v1/meta: %d, want 404", code)
	}
	if code, _ := call(t, "GET", ingestOnly.URL+"/", "", "", nil); code != http.StatusNotFound {
		t.Errorf("ingest node /: %d, want 404", code)
	}
	if code, _ := call(t, "POST", ingestOnly.URL+"/otlp/v1/traces", bootstrap, "application/json", []byte("{}")); code != 200 {
		t.Errorf("ingest node OTLP: %d", code)
	}
	_, apiOnly := newApp(t, Roles(RoleAPI), Console(false))
	if code, _ := call(t, "POST", apiOnly.URL+"/otlp/v1/traces", bootstrap, "application/json", []byte("{}")); code != http.StatusNotFound {
		t.Errorf("api node OTLP: %d, want 404", code)
	}
	if code, _ := call(t, "GET", apiOnly.URL+"/", "", "", nil); code != http.StatusNotFound {
		t.Errorf("api node with the Console off /: %d, want 404", code)
	}
	if code, body := call(t, "GET", apiOnly.URL+"/v1/meta", bootstrap, "", nil); code != 200 || !strings.Contains(body, `"capabilities":[]`) {
		t.Errorf("api node meta: %d %s", code, body)
	}
	if _, err := ParseRoles("api,bogus"); err == nil {
		t.Error("ParseRoles took an unknown role")
	}
	if rs, err := ParseRoles("ingest, api,ingest"); err != nil || len(rs) != 2 {
		t.Errorf("ParseRoles = %v %v", rs, err)
	}
}

// TestKernelOpen: Open admits a credential-less loopback request as the
// Local principal.
func TestKernelOpen(t *testing.T) {
	_, ts := newApp(t, Open())
	if code, body := call(t, "GET", ts.URL+"/v1/meta", "", "", nil); code != 200 || !strings.Contains(body, `"kind":"local"`) {
		t.Errorf("open meta: %d %s", code, body)
	}
}

// TestKernelRefusals: the errors New returns instead of serving.
func TestKernelRefusals(t *testing.T) {
	dir := t.TempDir()
	for name, opts := range map[string][]Option{
		"postgres":      {ControlDB("postgres://u:p@localhost/db")},
		"bad control":   {ControlDB("mysql://x")},
		"bad trace":     {TraceDB("file.db")},
		"short retain":  {Retain(time.Minute)},
		"bad bootstrap": {BootstrapKey("nope")},
		"live key":      {BootstrapKey("wb_sk_live_abcdefghijklmnopqrstuvwx")},
	} {
		app, err := New(append([]Option{DataDir(filepath.Join(dir, strings.ReplaceAll(name, " ", "_")))}, opts...)...)
		if err == nil {
			_ = app.Shutdown(context.Background())
			t.Errorf("%s: New succeeded", name)
		}
	}
	if _, err := New(DataDir(dir), ControlDB("postgres://x")); err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Errorf("postgres error = %v", err)
	}
}

// TestKernelDataDirLock: a second node on the same data dir is refused
// (ErrLocked) until the first shuts down.
func TestKernelDataDirLock(t *testing.T) {
	dir := t.TempDir()
	a, err := New(DataDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	a.Serving("http://127.0.0.1:7331")
	if _, err := New(DataDir(dir)); !errors.Is(err, ErrLocked) {
		t.Errorf("second New = %v, want ErrLocked", err)
	}
	if len(a.LockPaths()) != 2 {
		t.Errorf("LockPaths = %v", a.LockPaths())
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := New(DataDir(dir))
	if err != nil {
		t.Fatalf("New after Shutdown: %v", err)
	}
	_ = b.Shutdown(context.Background())
}

// sweptDB counts Sweep calls.
type sweptDB struct {
	obsdb.DB
	swept chan time.Time
}

func (s sweptDB) Sweep(ctx context.Context, before time.Time) (int, error) {
	select {
	case s.swept <- before:
	default:
	}
	return 0, nil
}

// TestKernelSweeper: the scheduler role sweeps the trace store with the
// retention's cutoff; a node without it never sweeps.
func TestKernelSweeper(t *testing.T) {
	app, _ := newApp(t)
	db := sweptDB{DB: app.Traces(), swept: make(chan time.Time, 1)}

	sched, err := New(DataDir(t.TempDir()), Traces(db), Retain(2*time.Hour), sweepEvery(10*time.Millisecond), Roles(RoleScheduler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sched.Run(ctx) }()
	select {
	case before := <-db.swept:
		if d := time.Since(before); d < 2*time.Hour || d > 2*time.Hour+time.Minute {
			t.Errorf("cutoff %v ago, want the retention", d)
		}
	case <-time.After(5 * time.Second):
		t.Error("the scheduler never swept")
	}
	cancel()
	<-done
	if err := sched.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A handed-in trace store stays the caller's.
	if _, err := app.Traces().Runs(context.Background(), obsdb.RunQuery{Limit: 1}); err != nil {
		t.Errorf("Shutdown closed a caller's trace store: %v", err)
	}

	api, err := New(DataDir(t.TempDir()), Traces(db), Retain(2*time.Hour), sweepEvery(10*time.Millisecond), Roles(RoleAPI))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); _ = api.Run(ctx) }()
	select {
	case <-db.swept:
		t.Error("an api-only node swept")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	<-done
	_ = api.Shutdown(context.Background())
	if sweepInterval(720*time.Hour) != time.Hour || sweepInterval(2*time.Hour) != 12*time.Minute || sweepInterval(time.Hour) != 6*time.Minute {
		t.Error("sweepInterval")
	}
}

// TestKernelMountedUnderAPrefix: Handler mounts under a prefix with
// StripPrefix and Base.
func TestKernelMountedUnderAPrefix(t *testing.T) {
	app, err := New(DataDir(t.TempDir()), BootstrapKey(bootstrap), Base("/weftbase/"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	mux := http.NewServeMux()
	mux.Handle("/weftbase/", http.StripPrefix("/weftbase", app.Handler()))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if code, body := call(t, "GET", ts.URL+"/weftbase/v1/meta", bootstrap, "", nil); code != 200 {
		t.Errorf("mounted meta: %d %s", code, body)
	}
	if code, body := call(t, "GET", ts.URL+"/weftbase/", "", "", nil); code != 200 || !strings.Contains(body, `<base href="/weftbase/">`) {
		t.Errorf("mounted Console: %d %.200s", code, body)
	}
}
