package v1

import (
	"context"
	"encoding/json"
	"net/http"
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
	code, _, body := get(t, h, "/weftbase/v1/manifests/"+hash)
	var got storedManifestView
	decode(t, body, &got)
	if code != http.StatusOK || got.Source != obsdb.ManifestSourceFile || !sameJSON(t, got.Manifest, string(file)) || got.CurrentManifestHash != hash {
		t.Fatalf("file manifest = %d %s; want the file's agent document, source file, current", code, body)
	}
	var run struct {
		ManifestAvailable bool `json:"manifest_available"`
	}
	_, _, rb := get(t, h, "/weftbase/v1/runs/r_mf")
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
	if code, _, body := get(t, h, "/weftbase/v1/manifests/"+hash); code != http.StatusOK {
		t.Errorf("after DeleteRun and Sweep: %d %s; manifests are kept forever", code, body)
	}
}

// noManifests is an obsdb.DB that searches but stores no manifests: the
// embedded interfaces hide the backend's PutManifest and Manifest.
type noManifests struct {
	obsdb.DB
	obsdb.Searcher
}

// TestManifestsFollowTheDB (U27-fix): over a database that is not an
// obsdb.ManifestStore, GET /v1/manifests/{hash} answers 501.
func TestManifestsFollowTheDB(t *testing.T) {
	fx := fixtureDB(t)
	h := Handler(DB(noManifests{fx, fx.(obsdb.Searcher)}))
	if code, _, _ := get(t, h, "/weftbase/v1/manifests/"+strings.Repeat("a", 64)); code != http.StatusNotImplemented {
		t.Errorf("GET /v1/manifests/{hash} over a DB that stores none = %d, want 501", code)
	}
}
