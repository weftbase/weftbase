package exporter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// The remote destination's transport, end to end against a recording
// server: OTLP/HTTP protobuf on /otlp/v1/traces and /otlp/v1/logs with
// the bearer key — S2.2's row, pinned by what actually leaves the process. (An
// http:// test server is loopback, which the transport allows without
// Insecure().)
func TestRemoteTransportEndToEnd(t *testing.T) {
	type request struct {
		path        string
		auth        string
		contentType string
		body        []byte
	}
	var mu sync.Mutex
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, request{
			path:        r.URL.Path,
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := Start(testCtx(t), otel.NoGlobal(), otel.NoEnv(),
		Remote(srv.URL, "wb-key"),
		otel.Heartbeat(0), // the tracker's records are not this test's subject
	)
	if err != nil {
		t.Fatal(err)
	}
	agt := core.New(
		wefttest.Script(wefttest.Say("remote")),
		core.Name("remote-demo"),
		core.TracerProvider(p.TracerProvider()),
		core.LoggerProvider(p.LoggerProvider()),
	)
	if _, err := agt.Generate(context.Background(), core.Prompt("go"),
		core.Metadata(map[string]string{"weft.session.id": "remote-s"})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var sawTraces, sawLogs bool
	for _, req := range requests {
		if req.auth != "Bearer wb-key" {
			t.Errorf("%s: Authorization = %q, want the bearer token", req.path, req.auth)
		}
		switch req.path {
		case "/otlp/v1/traces":
			sawTraces = true
			if !strings.HasPrefix(req.contentType, "application/x-protobuf") {
				t.Errorf("traces content type = %q, want protobuf", req.contentType)
			}
			var msg coltracepb.ExportTraceServiceRequest
			if err := proto.Unmarshal(req.body, &msg); err != nil {
				t.Errorf("traces body is not an OTLP protobuf request: %v", err)
			}
		case "/otlp/v1/logs":
			sawLogs = true
			var msg collogspb.ExportLogsServiceRequest
			if err := proto.Unmarshal(req.body, &msg); err != nil {
				t.Errorf("logs body is not an OTLP protobuf request: %v", err)
			}
			// A weft record is in the payload, with the run id and the
			// kind the receiver will key on.
			found := false
			for _, rl := range msg.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, lr := range sl.LogRecords {
						if lr.EventName == "weft.event" {
							found = true
							if lr.Attributes == nil {
								t.Error("weft.event record carries no attributes")
							}
						}
					}
				}
			}
			if !found {
				t.Error("no weft.event record in the OTLP logs payload")
			}
		default:
			t.Errorf("unexpected request path %q", req.path)
		}
	}
	if !sawTraces || !sawLogs {
		t.Errorf("paths served: traces=%v logs=%v, want both", sawTraces, sawLogs)
	}
}
