package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/weftbase/weft/version"
	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/client/discovery"
)

// The discovery file (plan B3, internal/discovery): `weftbase dev` and
// `weftbase dev` write dev.json once the listener is bound — the real
// port in it — and remove it on a clean exit; exporter.Install and
// runtime.Install read it when WEFTBASE_URL is unset and the
// pipeline names no exporter.Remote or exporter.Local (docs/cli.md "When
// exporter reads the file"), so an app joins the running server with
// no configuration. A reuse writes
// nothing: the running server owns the file. Under WEFTBASE_DISCOVERY=off
// (discovery.Off) neither command writes one, and the start says so in
// one line.

// discoveryOffLine is the start line under WEFTBASE_DISCOVERY=off.
const discoveryOffLine = "weftbase: no discovery file written (WEFTBASE_DISCOVERY=off)"

// writeDiscovery writes the file for the server at url. dbPath is the
// SQLite file, "" for a database with no file.
func writeDiscovery(url, token, dbPath string) (*discovery.Written, error) {
	info := discovery.Info{
		URL:     url,
		Token:   token,
		DB:      dbPath,
		PID:     os.Getpid(),
		Started: time.Now().UTC().Truncate(time.Second),
		Version: version.Runtime(),
	}
	return discovery.Write(info)
}

// announce writes the discovery file and says where in one line (a
// failure is said too: the server serves regardless — apps then need
// WEFTBASE_URL).
func announce(out io.Writer, url, token, dbPath string) *discovery.Written {
	if discovery.Off(os.Getenv) {
		_, _ = fmt.Fprintf(out, "%s; apps need WEFTBASE_URL=%s\n", discoveryOffLine, url)
		return nil
	}
	w, err := writeDiscovery(url, token, dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(out, "weftbase: no discovery file (%v); apps need WEFTBASE_URL=%s\n", err, url)
		return nil
	}
	_, _ = fmt.Fprintf(out, "weftbase: apps find this server through %s (WEFTBASE_URL overrides it, WEFTBASE_DISCOVERY=off ignores it)\n", w.Path)
	return w
}

// knownServer is the address (host:port) of the server this user's
// discovery file names when it serves a database file, "" otherwise
// (no file trusted, WEFTBASE_DISCOVERY=off, a start with no database
// file): the port policy probes it first (listen.Request.Known), so a
// Server on the same file is found whatever its port (UX-S5a-13).
func knownServer(dbPath string) string {
	if dbPath == "" || discovery.Off(os.Getenv) {
		return ""
	}
	info, _, ok := discovery.Lookup(func(k string) string {
		if k == "WEFTBASE_URL" {
			return ""
		}
		return os.Getenv(k)
	}, slog.New(slog.DiscardHandler))
	if !ok || info.DB == "" {
		return ""
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

// discoveryOption names the discovery file a start is about to write to
// its server (v1.DiscoveryFile: /api/meta's discovery_file, which
// Server's connect lines print); none under WEFTBASE_DISCOVERY=off.
func discoveryOption() []v1.Option {
	if discovery.Off(os.Getenv) {
		return nil
	}
	if p := discovery.Path(); p != "" {
		return []v1.Option{v1.DiscoveryFile(p)}
	}
	return nil
}

// reuseNotes are the lines a reusing start adds after its reuse line:
// --rotate-token was not applied (the running server serves its token),
// and the running server's discovery file is missing, when it is.
func reuseNotes(out io.Writer, addr string, rotate bool) {
	if rotate {
		_, _ = fmt.Fprintln(out, "weftbase: --rotate-token not applied: the running server keeps its token (stop it, then start with --rotate-token)")
	}
	if discovery.Find("http://"+addr) == "" {
		_, _ = fmt.Fprintf(out, "weftbase: the running server wrote no discovery file; apps need WEFTBASE_URL=http://%s\n", addr)
	}
}

// refreshEvery is how often a serving server re-stamps its discovery
// file's started time, so it stays fresh past discovery.MaxAge; a
// variable so a test may shorten it.
var refreshEvery = time.Hour

// keepFresh refreshes w every refreshEvery until the returned stop is
// called (stop waits for the refresher). Nil-safe.
func keepFresh(w *discovery.Written) (stop func()) {
	if w == nil {
		return func() {}
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = w.Refresh()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-exited }) }
}
