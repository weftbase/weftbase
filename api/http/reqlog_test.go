package httpapi

// Server's server log (UX-E-37, audit row D10): a refused ingest batch
// is one WARN line on slog.Default naming its reason and the request
// id; a 5xx is one ERROR line with the request id, which the response
// carries too (X-Request-Id, and request_id in the 5xx body).
import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// captureLog points slog.Default at a buffer for the test.
func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &b
}

func linesOf(b *strings.Builder) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestServerLogNamesARefusedIngestBatch(t *testing.T) {
	logged := captureLog(t)
	h := newStack(t, stackOpts{open: true}).router
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/otlp/v1/traces", strings.NewReader("{not otlp"))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed batch: %d %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("X-Request-Id")
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("X-Request-Id = %q, want a generated id", id)
	}
	lines := linesOf(logged)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want one: %q", len(lines), lines)
	}
	l := lines[0]
	for _, want := range []string{"level=WARN", `msg="weftbase: ingest batch refused"`, "request_id=" + id,
		"status=400", "path=/otlp/v1/traces", "code=bad_request", `reason="decode application/json:`} {
		if !strings.Contains(l, want) {
			t.Errorf("the line %q lacks %q", l, want)
		}
	}

	// The same refusal again inside the minute is counted, not logged;
	// a client's own well-formed request id is echoed.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/otlp/v1/traces", strings.NewReader("{not otlp"))
	req.RemoteAddr = "127.0.0.1:5556"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "exporter-42")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("X-Request-Id"); got != "exporter-42" {
		t.Errorf("echoed X-Request-Id = %q", got)
	}
	if n := len(linesOf(logged)); n != 1 {
		t.Errorf("a repeat refusal inside the window logged: %d lines", n)
	}
}

func TestServerLogNamesA5xxWithItsRequestID(t *testing.T) {
	logged := captureLog(t)
	h := newStack(t, stackOpts{open: true, db: func(db obsdb.DB) obsdb.DB { return failingRuns{db} }}).router
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/runs", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET api/runs over a failing DB: %d %s", w.Code, w.Body.String())
	}
	id := w.Header().Get("X-Request-Id")
	var body struct {
		Error struct {
			Code, Message string
			RequestID     string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if id == "" || body.Error.RequestID != id {
		t.Errorf("5xx body request_id %q, header %q: want the same id", body.Error.RequestID, id)
	}
	var errLines []string
	for _, l := range linesOf(logged) {
		if strings.Contains(l, "level=ERROR") {
			errLines = append(errLines, l)
		}
	}
	if len(errLines) != 1 {
		t.Fatalf("ERROR lines %q, want one", errLines)
	}
	for _, want := range []string{`msg="weftbase: request failed"`, "request_id=" + id, "status=500", "path=/v1/runs", "code=internal"} {
		if !strings.Contains(errLines[0], want) {
			t.Errorf("the line %q lacks %q", errLines[0], want)
		}
	}

	// A 4xx on the API is the client's: no line.
	logged.Reset()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/runs/nope", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || logged.Len() != 0 {
		t.Errorf("a 404: %d, logged %q", w.Code, logged.String())
	}
}

// P8-fix-a 13: /v1/meta's ingest section counts the batches this
// Server accepted and refused, with the last refusal and its time.
func TestMetaIngestCounts(t *testing.T) {
	captureLog(t)
	s := newStack(t, stackOpts{})
	post := func(body string) {
		s.do(t, req{method: "POST", path: "/otlp/v1/traces", key: s.key, body: body, header: map[string]string{"Content-Type": "application/json"}})
	}
	post(`{"resourceSpans":[]}`)
	post("{not otlp")
	_, body := s.do(t, req{path: "/v1/meta", key: s.key})
	var meta struct {
		Ingest *struct {
			Accepted  int64      `json:"accepted"`
			Refused   int64      `json:"refused"`
			LastError string     `json:"last_error"`
			LastAt    *time.Time `json:"last_at"`
		} `json:"ingest"`
	}
	if err := json.Unmarshal([]byte(body), &meta); err != nil || meta.Ingest == nil {
		t.Fatalf("meta %s (%v)", body, err)
	}
	in := meta.Ingest
	if in.Accepted != 1 || in.Refused != 1 || in.LastAt == nil ||
		!strings.HasPrefix(in.LastError, "400 bad_request: decode application/json") {
		t.Errorf("ingest counts %+v", *in)
	}
}

// failingRuns fails every runs read.
type failingRuns struct{ obsdb.DB }

func (failingRuns) Runs(context.Context, obsdb.RunQuery) (obsdb.RunPage, error) {
	return obsdb.RunPage{}, errors.New("disk on fire")
}
