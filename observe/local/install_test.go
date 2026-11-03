package local

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
)

// TestInstallFallsBackToLocalWhenEnvDestinationsFail: Install with only
// environment destinations, none of which builds, still records — into
// $WEFTBASE_DB — and DB reads it. A program that named a destination
// gets no fallback.
func TestInstallFallsBackToLocalWhenEnvDestinationsFail(t *testing.T) {
	isolate(t)
	db := filepath.Join(t.TempDir(), "fallback.db")
	t.Setenv("WEFTBASE_DB", db)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "ftp://collector.internal:4318")

	shutdown := Install(otel.Heartbeat(0))
	defer shutdown()
	local := DB()
	if local == nil {
		t.Fatal("Install recorded nothing: no Local fallback after every env destination failed")
	}
	res, err := core.New(wefttest.Script(wefttest.Say("kept"))).Generate(context.Background(), core.Prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Run(context.Background(), res.ID); err != nil {
		t.Fatalf("the run did not reach the fallback Local: %v", err)
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
	if DB() != nil {
		t.Error("Install handed a program that named its destinations an unrequested Local")
	}
	if p := otel.Installed(); p != nil && strings.Contains(strings.Join(p.Destinations(), ","), "local") {
		t.Errorf("destinations %v carry a Local", p.Destinations())
	}
}
