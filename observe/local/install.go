package local

import (
	"context"

	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/obsdb"
)

// Install is exporter.Install with Local("") as the zero-config
// fallback: with no destination configured (in code, the OTEL_*
// variables, WEFTBASE_URL or the discovery file) it writes
// $WEFTBASE_DB or ./.weftbase/weftbase.db — and so it does, with one
// WARN, when only environment destinations were configured and none
// could be built.
func Install(opts ...otel.Option) func() {
	return exporter.Install(append(append([]otel.Option(nil), opts...), exporter.WithFallback(Local("")))...)
}

// Start is exporter.Start: the platform's environment, no fallback.
func Start(ctx context.Context, opts ...otel.Option) (*otel.Pipeline, error) {
	return exporter.Start(ctx, opts...)
}

// DB returns the installed pipeline's Local database — for
// v1.DB(local.DB()), the handle an embedded server shares with the
// pipeline: DBOf(otel.Installed()). nil before an install, after its
// shutdown, or without a Local.
func DB() obsdb.DB {
	p := otel.Installed()
	if p == nil {
		return nil
	}
	return DBOf(p)
}

// DBOf returns the database of p's Local destination (the last one
// configured that is open), else nil — for any pipeline: Install,
// exporter.Start (otel.NoGlobal included) or a plain
// otel.Install/otel.Start given a Local.
func DBOf(p *otel.Pipeline) obsdb.DB {
	if p == nil {
		return nil
	}
	var ms []*marker
	for _, t := range p.Tags() {
		if m, ok := t.(*marker); ok {
			ms = append(ms, m)
		}
	}
	handleMu.Lock()
	defer handleMu.Unlock()
	for i := len(ms) - 1; i >= 0; i-- {
		if h := ms[i].handle; h != nil && h.isOpen() {
			return h.db
		}
	}
	return nil
}
