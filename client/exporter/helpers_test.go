package exporter

import (
	"context"
	"sync"
	"testing"

	"github.com/weftbase/weft/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// remoteOf returns the Remote destination p built, installed or not:
// the environment's (WEFTBASE_URL or the discovery file) when it added
// one, else the last exporter.Remote configured. "" for a nil pipeline
// and for one without a Remote.
func remoteOf(p *otel.Pipeline) (url, key string) {
	ms := markersOf(p)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].kind == kindRemote {
			return ms[i].url, ms[i].key
		}
	}
	return "", ""
}

// memExporter is an in-memory log exporter.
type memExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func newMemExporter() *memExporter { return &memExporter{} }

func (m *memExporter) Export(_ context.Context, recs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		m.records = append(m.records, r.Clone())
	}
	return nil
}

func (m *memExporter) Shutdown(context.Context) error   { return nil }
func (m *memExporter) ForceFlush(context.Context) error { return nil }

// has reports whether a record names runID.
func (m *memExporter) has(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		found := false
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			if kv.Value.AsString() == runID {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
