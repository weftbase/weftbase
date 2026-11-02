package exporter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// envOnly clears every variable the pipeline reads for destinations
// and switches the discovery file off, so a test sees only what it
// sets.
func envOnly(t *testing.T) {
	t.Helper()
	for _, k := range []string{"WEFTBASE_URL", "WEFTBASE_TOKEN", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"} {
		t.Setenv(k, "")
	}
	t.Setenv("WEFTBASE_DISCOVERY", "off")
}

// wire is a recording OTLP/HTTP server.
type wire struct {
	srv *httptest.Server

	mu       sync.Mutex
	first    map[string]time.Time // path → first request
	auth     []string
	events   []string // the log records' event names, in arrival order
	nRecords int
	gate     chan struct{} // non-nil: /v1/logs waits for it
}

func newWire(t *testing.T) *wire {
	t.Helper()
	w := &wire{first: map[string]time.Time{}}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/logs" && w.gate != nil {
			<-w.gate
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.first[r.URL.Path]; !ok {
			w.first[r.URL.Path] = time.Now()
		}
		w.auth = append(w.auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/logs" {
			var msg collogspb.ExportLogsServiceRequest
			if proto.Unmarshal(body, &msg) == nil {
				for _, rl := range msg.ResourceLogs {
					for _, sl := range rl.ScopeLogs {
						for _, lr := range sl.LogRecords {
							w.events = append(w.events, lr.EventName)
							w.nRecords++
						}
					}
				}
			}
		}
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wire) firstAt(path string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.first[path]
	return at, ok
}

// TestDefaultStudio (otel's TestDefaultStudio before the split, read
// off the wire now that the destination is an otel.OTLP): exporter.Studio
// sends traces and logs with the bearer token, content on (the
// pure-content weft.messages record reaches it), records within the
// 200 ms log delay and spans within the 1 s span delay (the SDK's
// defaults are 1 s and 5 s), and queues 16,384 records (the SDK's
// 2,048 would drop most of a burst the receiver holds up).
func TestDefaultStudio(t *testing.T) {
	envOnly(t)
	w := newWire(t)
	p, err := Start(testCtx(t), otel.NoGlobal(), otel.NoEnv(), otel.Heartbeat(0), Studio(w.srv.URL, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Destinations(); !slices.Equal(got, []string{"studio"}) {
		t.Errorf("Destinations = %v, want [studio]", got)
	}
	emitted := time.Now()
	agt := core.New(wefttest.Script(wefttest.Say("hi")),
		core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := agt.Generate(context.Background(), core.Prompt("go")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		_, logs := w.firstAt("/v1/logs")
		_, traces := w.firstAt("/v1/traces")
		if logs && traces || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	logsAt, ok := w.firstAt("/v1/logs")
	if !ok {
		t.Fatal("no records reached the Studio without a flush")
	}
	if d := logsAt.Sub(emitted); d > 700*time.Millisecond {
		t.Errorf("first records after %v, want within the 200 ms log delay (the SDK default is 1 s)", d)
	}
	tracesAt, ok := w.firstAt("/v1/traces")
	if !ok {
		t.Fatal("no spans reached the Studio within 4 s without a flush (the span delay is 1 s)")
	}
	if d := tracesAt.Sub(emitted); d > 3*time.Second {
		t.Errorf("first spans after %v, want within the 1 s span delay (the SDK default is 5 s)", d)
	}
	_ = p.Shutdown(testCtx(t))
	w.mu.Lock()
	for _, a := range w.auth {
		if a != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", a)
		}
	}
	if !slices.Contains(w.events, "weft.messages") {
		t.Errorf("no weft.messages record reached the Studio (content off?); events %v", w.events)
	}
	w.mu.Unlock()

	// The queue: the receiver holds the first export while a burst of
	// 6,000 records is emitted; all of them arrive.
	q := newWire(t)
	q.gate = make(chan struct{})
	p, err = Start(testCtx(t), otel.NoGlobal(), otel.NoEnv(), otel.Heartbeat(0), Studio(q.srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	logger := p.LoggerProvider().Logger("exporter-test")
	const burst = 6000
	for i := 0; i < burst; i++ {
		var r otellog.Record
		r.SetEventName("app.burst")
		r.SetBody(attribute.StringValue("x"))
		logger.Emit(context.Background(), r)
	}
	close(q.gate)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.Shutdown(ctx)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.nRecords != burst {
		t.Errorf("%d of %d burst records arrived, want all (a 16,384-record queue)", q.nRecords, burst)
	}
	for _, a := range q.auth {
		if a != "" {
			t.Errorf("Authorization = %q without a token, want none", a)
		}
	}
}

// TestPlaintextGuardStaysForCodeAndStudio, the Studio half (the OTLP
// half stays in weft/otel): an http:// WEFTBASE_URL to a non-loopback
// host is skipped with one WARN — it carries a bearer token, and
// plaintext to another host takes Studio(url, token, otel.Insecure())
// in code — so Start has nothing to build. In code, Studio over http://
// to a non-loopback host is refused without otel.Insecure() and built
// with it; http:// to loopback needs nothing.
func TestPlaintextGuardStaysForCodeAndStudio(t *testing.T) {
	envOnly(t)
	logs := captureLog(t)
	t.Setenv("WEFTBASE_URL", "http://studio.internal:7331")
	t.Setenv("WEFTBASE_TOKEN", "tok")
	if _, err := Start(testCtx(t), otel.NoGlobal(), otel.Heartbeat(0)); !errors.Is(err, otel.ErrNoDestinations) {
		t.Errorf("env WEFTBASE_URL=http://non-loopback: err = %v, want ErrNoDestinations (skipped)", err)
	}
	if w := lines(logs.String(), "WARN"); len(w) != 1 || !strings.Contains(w[0], "WEFTBASE_URL skipped") ||
		!strings.Contains(w[0], "needs otel.Insecure()") || strings.Contains(w[0], "tok") {
		t.Errorf("WARN lines %q, want one naming WEFTBASE_URL and otel.Insecure(), never the token", w)
	}
	t.Setenv("WEFTBASE_URL", "")
	t.Setenv("WEFTBASE_TOKEN", "")

	_, err := Start(testCtx(t), otel.NoGlobal(), otel.NoEnv(), otel.Heartbeat(0), Studio("http://studio.internal:7331", "tok"))
	if err == nil || !strings.Contains(err.Error(), "Insecure()") {
		t.Errorf("code Studio(http://non-loopback) without Insecure: err = %v, want the refusal", err)
	}
	p, err := Start(testCtx(t), otel.NoGlobal(), otel.NoEnv(), otel.Heartbeat(0),
		Studio("http://studio.internal:7331", "tok", otel.Insecure()))
	if err != nil {
		t.Errorf("code Studio(http://non-loopback, Insecure): %v", err)
	} else {
		_ = p.Shutdown(testCtx(t))
	}
	t.Setenv("WEFTBASE_URL", "http://127.0.0.1:7331")
	p, err = Start(testCtx(t), otel.NoGlobal(), otel.Heartbeat(0))
	if err != nil {
		t.Fatalf("env WEFTBASE_URL=http://loopback: %v", err)
	}
	defer func() { _ = p.Shutdown(testCtx(t)) }()
	if got := p.Destinations(); !slices.Equal(got, []string{"studio(env)"}) {
		t.Errorf("Destinations = %v, want [studio(env)]", got)
	}
}

// TestInstallFallsBackToLocalWhenEnvDestinationsFail: Install with only
// environment destinations, none of which builds, still records — into
// $WEFTBASE_DB — and LocalDB reads it. Start keeps failing; a program
// that named a destination gets no fallback.
func TestInstallFallsBackToLocalWhenEnvDestinationsFail(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, warn string
	}{
		// Refused by this package (plaintext + token to another host).
		{"WEFTBASE_URL", "WEFTBASE_URL", "http://studio.internal:7331", "WEFTBASE_URL skipped"},
		// Refused by otel when it builds the destination.
		{"OTEL endpoint", "OTEL_EXPORTER_OTLP_ENDPOINT", "ftp://collector.internal:4318", "using the fallback destination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envOnly(t)
			db := filepath.Join(t.TempDir(), "fallback.db")
			t.Setenv("WEFTBASE_DB", db)
			t.Setenv("WEFTBASE_TOKEN", "tok")
			t.Setenv(tc.key, tc.value)
			if _, err := Start(testCtx(t), otel.NoGlobal(), otel.Heartbeat(0)); err == nil {
				t.Fatal("Start with only a broken env destination succeeded; want the error")
			}

			logs := captureLog(t)
			shutdown := Install(otel.Heartbeat(0))
			defer shutdown()
			local := LocalDB()
			if local == nil {
				t.Fatalf("Install recorded nothing: no Local fallback after every env destination failed; log: %s", logs.String())
			}
			res, err := core.New(wefttest.Script(wefttest.Say("kept"))).Generate(context.Background(), core.Prompt("go"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := local.Run(context.Background(), res.ID); err != nil {
				t.Fatalf("the run did not reach the fallback Local: %v", err)
			}
			if n := strings.Count(logs.String(), tc.warn); n != 1 {
				t.Errorf("want one %q WARN, got %d: %s", tc.warn, n, logs.String())
			}
			shutdown()
			reopened, err := sqliteOpen(db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			if _, err := reopened.Run(context.Background(), res.ID); err != nil {
				t.Errorf("the run is not in $WEFTBASE_DB (%s): %v", db, err)
			}

			// A named destination that fails gets no fallback.
			shutdown2 := Install(otel.NoEnv(), otel.Heartbeat(0), otel.OTLP("http://otel-collector:4318"))
			defer shutdown2()
			if LocalDB() != nil {
				t.Error("Install handed a program that named its destinations an unrequested Local")
			}
		})
	}
}

// TestEnvDedup, the Studio half: WEFTBASE_URL on the URL a destination
// named in code already exports to adds nothing — an explicit
// exporter.Studio or a plain otel.OTLP wins — and on another URL it is
// kept. StudioEndpoint reports the explicit Studio's token, not the
// environment's.
func TestEnvDedup(t *testing.T) {
	envOnly(t)
	t.Setenv("WEFTBASE_URL", "https://studio.example")
	t.Setenv("WEFTBASE_TOKEN", "env-token")

	for _, tc := range []struct {
		name     string
		opt      otel.Option
		want     []string
		endpoint string
	}{
		{"explicit Studio", Studio("https://studio.example/", "real"), []string{"studio"}, "real"},
		{"explicit OTLP", otel.OTLP("https://studio.example"), []string{"otlp"}, ""},
		{"other URL", otel.OTLP("https://elsewhere.example"), []string{"otlp", "studio(env)"}, "env-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Start(testCtx(t), otel.NoGlobal(), otel.Heartbeat(0), tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Shutdown(testCtx(t)) }()
			if got := p.Destinations(); !slices.Equal(got, tc.want) {
				t.Errorf("Destinations = %v, want %v", got, tc.want)
			}
			if _, tok := StudioEndpointOf(p); tok != tc.endpoint {
				t.Errorf("StudioEndpoint token = %q, want %q", tok, tc.endpoint)
			}
		})
	}
}

// A plain otel.Install with exporter.Local and exporter.Studio (what
// MIGRATION-0.21.md once suggested) reports both: StudioEndpoint is
// the Studio's, LocalDB the Local's — and neither outlives the
// pipeline's shutdown. A later otel.Install with neither reports
// nothing.
func TestPlainOtelInstallKnowsItsStudioAndLocal(t *testing.T) {
	envOnly(t)
	w := newWire(t)
	stop := otel.Install(otel.Heartbeat(0), Local(filepath.Join(t.TempDir(), "app.db")), Studio(w.srv.URL, "tok"))
	if u, tok := StudioEndpoint(); u != w.srv.URL || tok != "tok" {
		t.Errorf("StudioEndpoint = %q, %q, want %q, tok", u, tok, w.srv.URL)
	}
	if LocalDB() == nil {
		t.Error("LocalDB = nil, want the Local's database")
	}
	p := otel.Installed()
	stop()
	if u, _ := StudioEndpoint(); u != "" || LocalDB() != nil {
		t.Errorf("after shutdown: StudioEndpoint %q, LocalDB %v; want neither", u, LocalDB())
	}
	if LocalDBOf(p) != nil {
		t.Error("LocalDBOf a shut-down pipeline is its closed database, want nil")
	}

	stop = otel.Install(otel.Heartbeat(0), otel.Exporters(nil, newMemExporter()))
	defer stop()
	if u, _ := StudioEndpoint(); u != "" || LocalDB() != nil {
		t.Errorf("a pipeline without Studio or Local: StudioEndpoint %q, LocalDB %v", u, LocalDB())
	}
}

// Install reports only the pipeline it built: when it records nothing
// (every named destination failed), StudioEndpoint and LocalDB do not
// describe a previous pipeline as this call's — they read the one
// still installed, if any, which is the earlier one, unchanged.
func TestInstallAttachesToItsOwnPipeline(t *testing.T) {
	envOnly(t)
	w := newWire(t)
	logs := captureLog(t)
	stop1 := Install(otel.Heartbeat(0), Studio(w.srv.URL, "first"))
	defer stop1()
	first := otel.Installed()
	stop2 := Install(otel.Heartbeat(0), otel.NoEnv(), otel.OTLP("ftp://broken:1"))
	defer stop2()
	if otel.Installed() != first {
		t.Fatal("a failed Install replaced the installed pipeline")
	}
	if u, tok := StudioEndpoint(); u != w.srv.URL || tok != "first" {
		t.Errorf("StudioEndpoint = %q, %q, want the first pipeline's", u, tok)
	}
	if strings.Contains(logs.String(), "joined the running Studio") {
		t.Errorf("a join was claimed: %s", logs.String())
	}
}
