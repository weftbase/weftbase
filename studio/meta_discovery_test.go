package studio

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weftbase/internal/discovery"
)

// writeDiscoveryFile writes a discovery file naming pid (0600, as
// `weftbase dev` writes it) and returns its path.
func writeDiscoveryFile(t *testing.T, pid int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), discovery.FileName)
	b, err := json.Marshal(discovery.Info{URL: "http://127.0.0.1:7347", Token: "tok", PID: pid, Started: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var metaDiscoveryRe = regexp.MustCompile(`"discovery_file": "[^"]*"`)

// TestMetaDiscoveryFile (B7, UX-S5a-10's Go half): api/meta serves the
// discovery file DiscoveryFile names while the file there names this
// process — never one not written, removed, or taken over by another
// Studio — under db.path's rule (the auth matrix pins the panel
// tokens'); an embedded Studio (no option) serves none.
func TestMetaDiscoveryFile(t *testing.T) {
	path := writeDiscoveryFile(t, os.Getpid())
	read := func(opts ...Option) map[string]any {
		t.Helper()
		code, _, body := get(t, Handler(append([]Option{DB(fixtureDB(t))}, opts...)...), "/studio/api/meta")
		if code != http.StatusOK {
			t.Fatalf("meta: %d %s", code, body)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := read(DiscoveryFile(path))["discovery_file"]; got != path {
		t.Errorf("discovery_file = %v, want %s", got, path)
	}
	code, _, body := get(t, Handler(DB(fixtureDB(t)), DiscoveryFile(path)), "/studio/api/meta")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	wefttest.Golden(t, "testdata/api/meta-discovery.golden.json",
		[]byte(metaDiscoveryRe.ReplaceAllString(metaPIDRe.ReplaceAllString(indent(t, body), `"pid": 1234`), `"discovery_file": "/run/user/1000/weft/studio.json"`)))

	for name, opts := range map[string][]Option{
		"no option":       nil,
		"another process": {DiscoveryFile(writeDiscoveryFile(t, os.Getpid()+1))},
		"never written":   {DiscoveryFile(filepath.Join(t.TempDir(), discovery.FileName))},
	} {
		if m := read(opts...); m["discovery_file"] != nil {
			t.Errorf("%s: discovery_file = %v, want omitted", name, m["discovery_file"])
		}
	}
	// A file removed after the start is not served.
	srv := New(DB(fixtureDB(t)), DiscoveryFile(path))
	t.Cleanup(func() { _ = srv.Close() })
	_ = os.Remove(path)
	if _, _, body := get(t, srv.Handler(), "/studio/api/meta"); strings.Contains(body, "discovery_file") {
		t.Errorf("a removed file is served: %s", body)
	}
}

// TestOffReason (B7-fix): the host's words replace the reason for the
// capability it names and every capability off for the same cause,
// and nothing else; nothing turns on.
func TestOffReason(t *testing.T) {
	words := "weftbase dev was started with --no-ingest"
	srv := New(DB(fixtureDB(t)), NoIngest(), OffReason("ingest", words))
	t.Cleanup(func() { _ = srv.Close() })
	off := srv.capabilitiesOff()
	for c, why := range optionalCapabilities {
		_, isOff := off[c]
		switch {
		case why == offIngest && (!isOff || off[c] != words):
			t.Errorf("%s: %q, want the host's words", c, off[c])
		case why != offIngest && isOff && off[c] != why:
			t.Errorf("%s: %q, want its own reason %q", c, off[c], why)
		}
	}
	on := New(DB(fixtureDB(t)), OffReason("ingest", words))
	t.Cleanup(func() { _ = on.Close() })
	if _, isOff := on.capabilitiesOff()["ingest"]; isOff {
		t.Error("OffReason turned ingest off")
	}
}

// TestOffReasonUnknownPanics: a name that is no optional capability is
// a construction error, never silently ignored.
func TestOffReasonUnknownPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "ingset") {
			t.Errorf("recover = %v, want a panic naming the capability", r)
		}
	}()
	_ = New(DB(fixtureDB(t)), OffReason("ingset", "typo"))
}
