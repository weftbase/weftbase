package doctor_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/internal/doctor"
)

// ExampleRun checks a node with nothing connected to it (an Open one:
// no key needed on loopback): one line per check, each read from its
// /v1/meta and /readyz. No discovery file names it (an embedded node
// writes none): that line warns.
func ExampleRun() {
	dir, _ := os.MkdirTemp("", "doctor")
	defer func() { _ = os.RemoveAll(dir) }()
	app, err := weftbase.New(weftbase.DataDir(filepath.Join(dir, "data")), weftbase.Open(),
		weftbase.Logger(slog.New(slog.DiscardHandler)))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()

	var out strings.Builder
	err = doctor.Run(context.Background(), &out, doctor.Target{URL: ts.URL})
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if f := strings.Fields(line); !strings.HasPrefix(line, " ") {
			fmt.Println(f[0], f[1])
		}
	}
	fmt.Println("err:", err)
	// Output:
	// ok server
	// ok key
	// ok ready
	// warn discovery
	// ok db
	// ok content
	// ok data
	// ok otlp
	// ok ingest
	// ok manifest
	// err: <nil>
}
