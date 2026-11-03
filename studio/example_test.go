package studio_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/weftbase/weftbase/studio"

	// The sqlite backend — the same one exporter.Local writes.
	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// ExampleHandler is the embedded shape on one screen: a server over an
// obsdb handle, mounted under a prefix. exporter.Install's local sink
// writes the handle in a real app; here a batch stands in for it.
func ExampleHandler() {
	dir, err := os.MkdirTemp("", "weft-studio-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db, err := sqlite.Open(dir + "/weft.db")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = db.Close() }()

	// The mount the doc comment shows; the server serves the Console,
	// the API, the live stream and OTLP ingest under /weftbase/.
	mux := http.NewServeMux()
	mux.Handle("/weftbase/", http.StripPrefix("/weftbase",
		studio.Handler(studio.DB(db), studio.Base("/weftbase/"))))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/weftbase/api/meta")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	fmt.Println(resp.StatusCode)
	// Output:
	// 200
}

// ExampleServer shows the Server surface: New, Handler() to serve,
// Close for what New opened. New without DB or
// Open would open the default path ($WEFTBASE_DB or ./.weftbase/weftbase.db) —
// pinned by TestOpenOption — so the example opens a throwaway sqlite
// file instead of writing into the package directory.
func ExampleServer() {
	dir, err := os.MkdirTemp("", "weft-studio-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	srv := studio.New(studio.Open(dir+"/weft.db"), studio.NoIngest()) // read-only: no OTLP receiver
	defer func() { _ = srv.Close() }()                                // New opened that DB: closed here

	_ = srv.Handler() // mount it; the weftbase binary (cmd/weftbase) does
	fmt.Println("ok")
	// Output:
	// ok
}

// ExampleRetain keeps a month of runs: the sweeper deletes the older
// ones that are not running, from weftbase's tables only, until Close.
// api/meta reports the retention.
func ExampleRetain() {
	dir, err := os.MkdirTemp("", "weft-studio-retain-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	srv := studio.New(studio.Open(dir+"/weft.db"), studio.Retain(30*24*time.Hour))
	defer func() { _ = srv.Close() }()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/meta")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var meta struct {
		Retention *string `json:"retention"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(*meta.Retention)
	// Output: 720h0m0s
}

// ExampleDiscoveryFile is what `weftbase dev` does for the discovery
// file it writes: it names the path to the server, and api/meta serves
// it (to the server token, or loopback with no Token) while the file
// there names this process. Here the
// file is written by hand, as the command writes it: 0600, this pid.
func ExampleDiscoveryFile() {
	dir, err := os.MkdirTemp("", "weft-studio-discovery-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := dir + "/studio.json"
	body := fmt.Sprintf(`{"url":"http://127.0.0.1:7331","pid":%d,"started":%q}`, os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Println(err)
		return
	}
	srv := studio.New(studio.Open(dir+"/weft.db"), studio.DiscoveryFile(path))
	defer func() { _ = srv.Close() }()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/meta")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var meta struct {
		DiscoveryFile string `json:"discovery_file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(meta.DiscoveryFile == path)
	// Output: true
}

// ExampleOffReason is how a command words a capability's absence for a
// user who never wrote Go: meta's capabilities_off says the flag.
func ExampleOffReason() {
	dir, err := os.MkdirTemp("", "weft-studio-offreason-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	srv := studio.New(studio.Open(dir+"/weft.db"), studio.NoIngest(),
		studio.OffReason("ingest", "the command was started with --no-ingest"))
	defer func() { _ = srv.Close() }()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/meta")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	var meta struct {
		Off map[string]string `json:"capabilities_off"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(meta.Off["ingest"])
	// Output: the command was started with --no-ingest
}
