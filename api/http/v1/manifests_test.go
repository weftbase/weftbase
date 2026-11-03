package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weftbase/obsdb"
)

// sameJSON: the route re-encodes the stored document (compact); the
// store keeps its bytes (the obsdbtest Manifests case pins those).
func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// TestFileManifestIsStored (U27): the weft.json Manifest(...) serves is
// stored at New, each agent under the hash its runs carry — a server
// without a replay still resolves them. Kept forever: deleting
// the run, and a retention sweep, leave it.
func TestFileManifestIsStored(t *testing.T) {
	agt := core.New(wefttest.Script(), core.Name("support"), core.Instructions("Be brief."))
	file, err := core.Manifest(agt)
	if err != nil {
		t.Fatal(err)
	}
	db := fixtureDB(t)
	srv := New(DB(db), Manifest(file))
	t.Cleanup(func() { _ = srv.Close() })
	hash := obsdb.ManifestHash(file) // one agent: the file is its document
	seedRun(t, db, "r_mf", "", "", "", map[string]any{"weft.manifest.hash": hash})
	h := srv.Handler()
	code, _, body := get(t, h, "/weftbase/api/manifests/"+hash)
	var got storedManifestView
	decode(t, body, &got)
	if code != http.StatusOK || got.Source != obsdb.ManifestSourceFile || !sameJSON(t, got.Manifest, string(file)) || got.CurrentManifestHash != hash {
		t.Fatalf("file manifest = %d %s; want the file's agent document, source file, current", code, body)
	}
	var run struct {
		ManifestAvailable bool `json:"manifest_available"`
	}
	_, _, rb := get(t, h, "/weftbase/api/runs/r_mf")
	decode(t, rb, &run)
	if !run.ManifestAvailable {
		t.Errorf("run r_mf = %s; want manifest_available", rb)
	}
	if err := db.DeleteRun(context.Background(), "r_mf"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sweep(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if code, _, body := get(t, h, "/weftbase/api/manifests/"+hash); code != http.StatusOK {
		t.Errorf("after DeleteRun and Sweep: %d %s; manifests are kept forever", code, body)
	}
}

// noManifests is an obsdb.DB that searches but stores no manifests: the
// embedded interfaces hide the backend's PutManifest and Manifest.
type noManifests struct {
	obsdb.DB
	obsdb.Searcher
}

// TestManifestsCapabilityFollowsTheDB (U27-fix): a database that is not
// an obsdb.ManifestStore registers no GET /api/manifests/{hash} and
// /api/meta reports the capability off with its reason — search, which
// the wrapper keeps, stays on; over sqlite the capability is on.
func TestManifestsCapabilityFollowsTheDB(t *testing.T) {
	fx := fixtureDB(t)
	h := Handler(DB(noManifests{fx, fx.(obsdb.Searcher)}))
	_, _, body := get(t, h, "/weftbase/api/meta")
	var meta struct {
		Capabilities    []string          `json:"capabilities"`
		CapabilitiesOff map[string]string `json:"capabilities_off"`
	}
	decode(t, body, &meta)
	if slices.Contains(meta.Capabilities, "manifests") || !slices.Contains(meta.Capabilities, "search") {
		t.Errorf("capabilities = %v, want search and no manifests over a DB that stores none", meta.Capabilities)
	}
	if meta.CapabilitiesOff["manifests"] != offManifests {
		t.Errorf("capabilities_off = %v, want manifests: %q", meta.CapabilitiesOff, offManifests)
	}
	if _, off := meta.CapabilitiesOff["search"]; off {
		t.Errorf("capabilities_off = %v, want search on", meta.CapabilitiesOff)
	}
	if code, _, _ := get(t, h, "/weftbase/api/manifests/"+strings.Repeat("a", 64)); code != http.StatusNotFound {
		t.Errorf("GET /api/manifests/{hash} over a DB that stores none = %d, want 404", code)
	}
	_, _, body = get(t, Handler(DB(fixtureDB(t))), "/weftbase/api/meta")
	meta.Capabilities, meta.CapabilitiesOff = nil, nil
	decode(t, body, &meta)
	if _, off := meta.CapabilitiesOff["manifests"]; off || !slices.Contains(meta.Capabilities, "manifests") {
		t.Errorf("meta over sqlite = %s, want manifests on", body)
	}
}
