// Command build is the demo's agent build: the refunds agent served
// with client/worker, the way any agent build is. The demo compiles it
// twice (-X main.release=1 and 2: two builds, two AgentVersions) and
// hands each binary to the node's local placement, which runs it as a
// supervised child process.
//
// A build imports weft and the client module only, never the server.
package main

import (
	"log"
	"os"
	"strconv"

	"github.com/weftbase/weft/otel"

	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/client/worker"
	"github.com/weftbase/weftbase/examples/demo/shop"
)

// release is set at link time: go build -ldflags "-X main.release=2".
var release = "1"

func main() {
	rel, _ := strconv.Atoi(release)
	speed, _ := strconv.ParseFloat(os.Getenv("DEMO_SPEED"), 64)

	// The build's traces and logs go to the node that supervises it:
	// the supervisor sets WEFTBASE_URL and WEFTBASE_TOKEN, and Install
	// reads them.
	flush := exporter.Install(otel.Service("refunds-build"), otel.Slog())
	err := worker.Serve(worker.FromEnv(), worker.Agent(shop.Refunds(rel, shop.Speed(speed))))
	flush()
	if err != nil {
		log.Fatal(err)
	}
}
