// Package v1 holds the handlers of weftbase's /v1 read surface and the
// OTLP ingest routes, over one obsdb.DB: runs (search folded into
// GET /v1/runs?q=), their sub-resources, sessions, traces, stats, the
// diff, manifests, pricing, deletion, the run and session live streams
// and the /v1/events resource stream over the control store's outbox.
//
// The package owns no router, no authentication and no guards: it
// lists its routes (Routes, IngestRoutes), each with the authz action
// it needs, and api/http mounts them behind its middleware. A handler
// reads the caller from authz.From. The read logic moves on into
// observe/query, sessions and observe/export in later steps.
package v1

import (
	"time"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/internal/reqlog"
	"github.com/weftbase/weftbase/obsdb"
	pricing "github.com/weftbase/weftbase/observe/cost"
	"github.com/weftbase/weftbase/observe/ingest"
)

// Option configures New.
type Option func(*config)

type config struct {
	db           obsdb.DB
	live         obsdb.Hub
	events       *events.Log
	manifest     []byte
	capabilities []string
	retain       time.Duration
	pricing      *pricing.Table
	ingestCounts func() reqlog.IngestCounts
}

// DB serves over db: the trace store.
func DB(db obsdb.DB) Option {
	return func(c *config) { c.db = db }
}

// Live serves the run and session streams from h. The default is the
// DB's own hub when it implements Hub() (obsdb/sqlite), else an
// in-process obsdb.NewHub() fed by ingest.
func Live(h obsdb.Hub) Option {
	return func(c *config) { c.live = h }
}

// Events serves GET /v1/events from the control store's outbox. Without
// it the route answers 501.
func Events(l *events.Log) Option {
	return func(c *config) { c.events = l }
}

// Manifest supplies core.Manifest bytes (weft.json): GET /v1/manifest,
// meta's manifest_check, and the manifest store's file agents. Without
// it GET /v1/manifest answers 404.
func Manifest(json []byte) Option {
	return func(c *config) { c.manifest = json }
}

// Capabilities are the optional features this deployment has on
// (ingest, clickhouse, workers, approvals, tenants): GET /v1/meta lists
// them. The kernel computes them from its roles and stores.
func Capabilities(names ...string) Option {
	return func(c *config) { c.capabilities = append(c.capabilities, names...) }
}

// Retain is the trace retention the kernel's sweeper applies; meta
// reports it. 0 is none.
func Retain(d time.Duration) Option {
	return func(c *config) { c.retain = max(d, 0) }
}

// IngestCounts supplies the accepted and refused OTLP batch counts the
// router's request log keeps (meta's ingest block).
func IngestCounts(f func() reqlog.IngestCounts) Option {
	return func(c *config) { c.ingestCounts = f }
}

// Server is the /v1 handler set over one obsdb.DB.
type Server struct {
	config
	now func() time.Time

	// ingestHealth is what the exporting apps reported losing
	// (weft.records.dropped on their heartbeats and run brackets;
	// observe/ingest's Health).
	ingestHealth ingest.Health
}

// New builds the handler set. A missing or nil DB panics: a server
// with nothing to read is a construction error.
func New(opts ...Option) *Server {
	var c config
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.db == nil {
		panic("weftbase/api/http/v1: New needs a non-nil DB")
	}
	resolvePricing(&c)
	s := &Server{config: c, now: time.Now}
	if s.live == nil {
		if h, ok := s.db.(interface{ Hub() obsdb.Hub }); ok {
			s.live = h.Hub()
		} else {
			s.live = obsdb.NewHub()
		}
	}
	s.storeFileManifest()
	return s
}
