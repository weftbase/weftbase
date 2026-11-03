package sqlite_test

import (
	"context"
	"testing"

	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/store/storetest"
)

// TestConformance runs the control store's conformance suite over
// SQLite: a fresh in-memory database per group.
func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Runner {
		db, err := sqlite.Open(context.Background(), ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	})
}
