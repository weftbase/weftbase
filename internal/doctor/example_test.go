package doctor_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/weftbase/weftbase/internal/doctor"
	"github.com/weftbase/weftbase/studio"
)

// ExampleRun checks a server with nothing connected to it: one line
// per check, each read from its /api/meta. No discovery file names it
// (an embedded server writes none): that line warns.
func ExampleRun() {
	dir, _ := os.MkdirTemp("", "doctor")
	defer func() { _ = os.RemoveAll(dir) }()
	srv := studio.New(studio.Open(filepath.Join(dir, "weft.db")))
	defer func() { _ = srv.Close() }()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var out strings.Builder
	err := doctor.Run(context.Background(), &out, doctor.Target{URL: ts.URL})
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if f := strings.Fields(line); !strings.HasPrefix(line, " ") {
			fmt.Println(f[0], f[1])
		}
	}
	fmt.Println("err:", err)
	// Output:
	// ok studio
	// ok token
	// warn discovery
	// ok db
	// ok content
	// ok data
	// ok otlp
	// ok ingest
	// ok manifest
	// err: <nil>
}
