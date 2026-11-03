package studio

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// The registered manifests (plan B4): a runtime's registration carries
// each agent's core.Manifest; Studio remembers it by (service, manifest
// hash), serves it at api/manifest when no weft.json is configured, and
// says which hash a connected runtime holds now.

// b4Do sends one request with an optional bearer.
func b4Do(t *testing.T, method, url, bearer, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type b4Manifest struct {
	Weft   int `json:"weft"`
	Agents []struct {
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
	} `json:"agents"`
	Sources []struct {
		Source       string     `json:"source"`
		ManifestHash string     `json:"manifest_hash"`
		Service      string     `json:"service"`
		Live         *bool      `json:"live"`
		RegisteredAt *time.Time `json:"registered_at"`
		RuntimeID    string     `json:"runtime_id"`
		Agents       []struct {
			Name         string `json:"name"`
			ManifestHash string `json:"manifest_hash"`
		} `json:"agents"`
	} `json:"sources"`
}

// TestWithSourcesClash: a weft.json with a sources key of its own gets
// Studio's in its place — one key, never a duplicate.
func TestWithSourcesClash(t *testing.T) {
	file := []byte(`{"weft":1,"sources":"mine","agents":[]}`)
	h := Handler(DB(fixtureDB(t)), Manifest(file))
	code, _, body := get(t, h, "/studio/api/manifest")
	if code != http.StatusOK || strings.Count(body, `"sources"`) != 1 || strings.Contains(body, `"mine"`) {
		t.Fatalf("manifest = %d %s, want one sources key, Studio's", code, body)
	}
	var doc struct {
		Sources []struct {
			Source string `json:"source"`
		} `json:"sources"`
	}
	decode(t, body, &doc)
	if len(doc.Sources) != 1 || doc.Sources[0].Source != "file" {
		t.Errorf("sources = %+v", doc.Sources)
	}
}

// TestCapabilitiesOffTable: every capability a group registers only
// under an option has a reason in optionalCapabilities, and every
// entry there is such a capability; a capability a host declares
// itself is never reported off.
func TestCapabilitiesOffTable(t *testing.T) {
	caps := func(opts ...Option) map[string]bool {
		srv := New(append([]Option{Open(filepath.Join(t.TempDir(), "weft.db"))}, opts...)...)
		t.Cleanup(func() { _ = srv.Close() })
		out := map[string]bool{}
		for _, c := range srv.capabilityList() {
			out[c] = true
		}
		return out
	}
	all := caps(Token("t"))
	// The least: no ingest, and a database that cannot search (the
	// search group registers only over an obsdb.Searcher).
	none := caps(NoIngest(), DB(cannotSearch{fixtureDB(t)}))
	optional := map[string]bool{}
	for c := range all {
		if !none[c] {
			optional[c] = true
			if optionalCapabilities[c] == "" {
				t.Errorf("optional capability %q has no capabilities_off reason", c)
			}
		}
	}
	for c := range optionalCapabilities {
		if !optional[c] {
			t.Errorf("capabilities_off reason for %q, which is not an optional group's capability", c)
		}
	}
	srv := New(Open(filepath.Join(t.TempDir(), "weft.db")), NoIngest(), Capabilities("ingest"))
	t.Cleanup(func() { _ = srv.Close() })
	if off := srv.capabilitiesOff(); off["ingest"] != "" {
		t.Errorf("declared ingest: capabilities_off = %v, want ingest absent", off)
	}
}

// cannotSearch is an obsdb.DB without obsdb.Searcher: the embedded
// interface hides the backend's Search.
type cannotSearch struct{ obsdb.DB }

// TestSearchCapabilityFollowsTheDB: a database that cannot search
// registers no search route and reports the capability off with its
// reason; one that can lists it.
func TestSearchCapabilityFollowsTheDB(t *testing.T) {
	_, _, body := get(t, Handler(DB(cannotSearch{fixtureDB(t)})), "/studio/api/meta")
	var meta struct {
		Capabilities    []string          `json:"capabilities"`
		CapabilitiesOff map[string]string `json:"capabilities_off"`
	}
	decode(t, body, &meta)
	for _, c := range meta.Capabilities {
		if c == "search" {
			t.Errorf("capabilities = %v, want no search over a DB that cannot", meta.Capabilities)
		}
	}
	if meta.CapabilitiesOff["search"] != offSearch {
		t.Errorf("capabilities_off = %v, want search: %q", meta.CapabilitiesOff, offSearch)
	}
	if code, _, _ := get(t, Handler(DB(cannotSearch{fixtureDB(t)})), "/studio/api/search?q=x"); code != http.StatusNotFound {
		t.Errorf("GET /api/search over a DB that cannot search = %d, want 404", code)
	}
	_, _, body = get(t, Handler(DB(fixtureDB(t))), "/studio/api/meta")
	meta.CapabilitiesOff = nil
	decode(t, body, &meta)
	if _, off := meta.CapabilitiesOff["search"]; off || !strings.Contains(body, `"search"`) {
		t.Errorf("meta over sqlite = %s, want search on", body)
	}
}
