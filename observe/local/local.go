// Package local is the embedded in-process sink: weft's traces and
// records written synchronously into an obsdb/sqlite database through
// otel.Sink, with no network hop. It is the server module's half of
// the exporter split: client/exporter sends to a weftbase server, and
// takes this package's Local as its zero-config fallback
// (exporter.WithFallback(local.Local("")), which Install here wires).
package local

import (
	"context"
	"os"
	"sync"

	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Local is the local sink: obsdb/sqlite at path ("" = $WEFTBASE_DB or
// ./.weftbase/weftbase.db), opened when the pipeline starts and written
// synchronously through otel.Sink — nothing emitted is lost, and the
// DB's in-process hub is the live lane a server sharing the handle
// (v1.DB(local.DB())) reads. Content on, traces + logs, raw bodies and
// app logs eligible (a Sink is first-party). Delta records reach the
// store and are counted by Write, not stored. The database closes when
// the pipeline shuts down. A Local is an exporter.Named destination: it
// switches the discovery file off in exporter.Install and Start, so the
// pipeline writes to the stores it names (and the environment's), never
// into a running server it found on disk — add exporter.Remote(url,
// key) to write to one as well.
func Local(path string, opts ...otel.DestOption) otel.Option {
	m := &marker{}
	return exporter.Named(otel.Tag(m, otel.Sink("local", func(context.Context) (sdktrace.SpanExporter, sdklog.Exporter, error) {
		p := path
		if p == "" {
			p = defaultLocalPath(os.Getenv)
		}
		db, err := sqlite.Open(p)
		if err != nil {
			return nil, nil, err
		}
		h := openHandle(db, m)
		return &localSpanExporter{h: h}, &localLogExporter{h: h}, nil
	}, opts...)))
}

// marker tags a Local's destination, so DB and DBOf find its open
// database on any pipeline that built one (Pipeline.Tags).
type marker struct {
	handle *localHandle // its open database, under handleMu
}

// handleMu guards every marker's handle.
var handleMu sync.Mutex

// defaultLocalPath is where Local writes when no path is given: $WEFTBASE_DB
// or ./.weftbase/weftbase.db.
func defaultLocalPath(getenv func(string) string) string {
	if p := getenv("WEFTBASE_DB"); p != "" {
		return p
	}
	return ".weftbase/weftbase.db"
}

// localHandle is one opened Local database, shared by its span and log
// exporters: it closes when both have been shut down (otel shuts a
// Sink's exporters down after the providers, or at once when the
// pipeline fails to build).
type localHandle struct {
	db     obsdb.DB
	m      *marker
	mu     sync.Mutex
	refs   int
	closed bool
}

func openHandle(db obsdb.DB, m *marker) *localHandle {
	h := &localHandle{db: db, m: m, refs: 2}
	handleMu.Lock()
	m.handle = h
	handleMu.Unlock()
	return h
}

func (h *localHandle) release() error {
	h.mu.Lock()
	h.refs--
	last := h.refs == 0 && !h.closed
	if last {
		h.closed = true
	}
	h.mu.Unlock()
	if !last {
		return nil
	}
	handleMu.Lock()
	if h.m.handle == h {
		h.m.handle = nil
	}
	handleMu.Unlock()
	return h.db.Close()
}

func (h *localHandle) isOpen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed
}

// localSpanExporter writes SDK spans into the shared obsdb.DB. A failed
// write returns its error: otel's Sink counts it as the destination's
// drop and names it in its throttled WARN.
type localSpanExporter struct {
	h    *localHandle
	once sync.Once
}

func (e *localSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if len(spans) == 0 {
		return nil
	}
	return e.h.db.Write(ctx, obsdb.Batch{Spans: FromSDKSpans(spans)})
}

func (e *localSpanExporter) Shutdown(context.Context) error {
	var err error
	e.once.Do(func() { err = e.h.release() })
	return err
}

// localLogExporter writes SDK log records into the shared obsdb.DB.
type localLogExporter struct {
	h    *localHandle
	once sync.Once
}

func (e *localLogExporter) Export(ctx context.Context, recs []sdklog.Record) error {
	if len(recs) == 0 {
		return nil
	}
	return e.h.db.Write(ctx, obsdb.Batch{Records: FromSDKRecords(recs)})
}

func (e *localLogExporter) Shutdown(context.Context) error {
	var err error
	e.once.Do(func() { err = e.h.release() })
	return err
}

func (e *localLogExporter) ForceFlush(context.Context) error { return nil }
