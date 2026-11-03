package v1

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// The manifest store's route (U27; UX-E-16): GET /v1/manifests/{hash}
// answers the agent configuration a run's manifest_hash names — the
// one-agent core.Manifest document stored under that sha256 — after it
// stopped being the current one. The server writes the store from the
// weft.json Manifest(...) configures (at New, each agent's document).
// The run document
// says manifest_available when its hash resolves. Present when the DB
// is an obsdb.ManifestStore (both first-party backends are);
// meta.capabilities_off says why when not.
//
// Retention: manifests are kept forever — one small row per distinct
// agent configuration; DELETE /v1/runs/{id}, DELETE
// /v1/sessions/{id} and Retain's sweeper never touch them, so a run
// that remains always resolves its hash (obsdb.ManifestStore).

// manifestFileWriteTimeout bounds New's write of the file's agents.
const manifestFileWriteTimeout = 5 * time.Second

// manifestStore is the DB's manifest store; nil when it has none.
func (s *Server) manifestStore() obsdb.ManifestStore {
	ms, _ := s.db.(obsdb.ManifestStore)
	return ms
}

// storeFileManifest writes each agent of the configured weft.json to
// the manifest store (New calls it once): a run of that configuration
// keeps resolving it after the file changes. A file that does not parse
// writes nothing (manifest_check reports it); a failed write is one
// WARN — the store is a convenience, never a reason not to serve.
func (s *Server) storeFileManifest() {
	ms := s.manifestStore()
	if ms == nil || len(s.manifest) == 0 {
		return
	}
	docs, err := manifestAgentDocs(s.manifest)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), manifestFileWriteTimeout)
	defer cancel()
	for _, d := range docs {
		m := obsdb.StoredManifest{Hash: obsdb.ManifestHash(d.body), Body: d.body, Source: obsdb.ManifestSourceFile}
		if err := ms.PutManifest(ctx, m); err != nil {
			slog.Warn("weftbase: the manifest store refused the weft.json's agent; its runs will not resolve their manifest_hash",
				"agent", d.name, "manifest_hash", m.Hash, "err", err)
		}
	}
}

// storedManifestView is GET /v1/manifests/{hash}'s answer.
type storedManifestView struct {
	ManifestHash string `json:"manifest_hash"`
	// Agent is the document's agent name ("" when it names none).
	Agent string `json:"agent"`
	// Source is where the server first saw it: file | runtime.
	Source    string    `json:"source"`
	FirstSeen time.Time `json:"first_seen"`
	// CurrentManifestHash is the hash the current manifest
	// (/v1/manifest's source, manifest_check's) gives this agent; ""
	// when the current manifest names no such agent. Equal to
	// ManifestHash when the run's configuration is the current one.
	CurrentManifestHash string `json:"current_manifest_hash"`
	// Manifest is the one-agent document (re-encoded compact; its
	// stored bytes are what ManifestHash hashes).
	Manifest json.RawMessage `json:"manifest"`
}

// serveStoredManifest answers one stored manifest.
func (s *Server) serveStoredManifest(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	ms := s.manifestStore()
	if ms == nil {
		notImplemented(w, r, "the database cannot store manifests")
		return
	}
	m, found, err := ms.Manifest(r.Context(), hash)
	if err != nil {
		dbError(w, r, "manifest", hash, err)
		return
	}
	if !found {
		notFound(w, r, "no manifest "+hash+": weftbase stores a manifest when a weft.json it serves names it")
		return
	}
	v := storedManifestView{ManifestHash: m.Hash, Source: m.Source, FirstSeen: m.FirstSeen, Manifest: m.Body}
	var doc struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if json.Unmarshal(m.Body, &doc) == nil && len(doc.Agents) > 0 {
		v.Agent = doc.Agents[0].Name
	}
	if v.Agent != "" {
		if _, hashes, err := s.checkedManifest(); err == nil {
			for _, a := range hashes {
				if a.name == v.Agent && (v.CurrentManifestHash == "" || a.hash == m.Hash) {
					v.CurrentManifestHash = a.hash
				}
			}
		}
	}
	writeJSON(w, r, http.StatusOK, v)
}

// manifestAvailable is whether hash resolves in the manifest store
// (the run document's manifest_available). A read error answers false:
// the run page never fails over it.
func (s *Server) manifestAvailable(ctx context.Context, hash string) bool {
	ms := s.manifestStore()
	if ms == nil || hash == "" {
		return false
	}
	_, found, err := ms.Manifest(ctx, hash)
	return err == nil && found
}
