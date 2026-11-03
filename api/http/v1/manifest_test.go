package v1

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The registered manifests (plan B4): a runtime's registration carries
// each agent's core.Manifest; server remembers it by (service, manifest
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

// TestWithSourcesClash: a weft.json with a sources key of its own gets
// Server's in its place — one key, never a duplicate.
func TestWithSourcesClash(t *testing.T) {
	file := []byte(`{"weft":1,"sources":"mine","agents":[]}`)
	h := Handler(DB(fixtureDB(t)), Manifest(file))
	code, _, body := get(t, h, "/weftbase/v1/manifest")
	if code != http.StatusOK || strings.Count(body, `"sources"`) != 1 || strings.Contains(body, `"mine"`) {
		t.Fatalf("manifest = %d %s, want one sources key, server's", code, body)
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
