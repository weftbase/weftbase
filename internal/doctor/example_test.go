package doctor_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/internal/doctor"
)

// ExampleRun checks a server with nothing connected to it: one line
// per check, each read from its /api/meta. No discovery file names it
// (an embedded server writes none): that line warns.
func ExampleRun() {
	dir, _ := os.MkdirTemp("", "doctor")
	defer func() { _ = os.RemoveAll(dir) }()
	srv := v1.New(v1.Open(filepath.Join(dir, "weft.db")))
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
	// ok server
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
