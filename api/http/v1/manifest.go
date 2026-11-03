package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// api/manifest: the agents this server knows, from the weft.json
// passed to Manifest(...). The response carries a sources array naming
// the file and each agent's own hash.

// manifestSourceFile is the one manifest source kind: the weft.json.
const manifestSourceFile = "file"

// Why a capability is off (meta.capabilities_off).
const (
	offIngest    = "OTLP ingest is off: the NoIngest option turned it off"
	offSearch    = "the database cannot search: its obsdb.DB does not implement obsdb.Searcher (obsdb/sqlite and obsdb/clickhouse both do)"
	offSpansTree = "the database cannot read a run tree: its obsdb.DB does not implement obsdb.TreeReader (obsdb/sqlite and obsdb/clickhouse both do); GET /api/runs/{id}/spans?tree=1 answers 501"
	offStats     = "the database cannot aggregate: its obsdb.DB does not implement obsdb.Aggregator (obsdb/sqlite and obsdb/clickhouse both do); GET /api/stats and the error_class facet and filter are off"
	offTimeline  = "the database cannot read a session's state log: its obsdb.DB does not implement obsdb.SessionEventReader (obsdb/sqlite and obsdb/clickhouse both do); GET /api/sessions/{id}/timeline and /entries answer 501"
)

// optionalCapabilities is every capability a route group registers
// only under an option, with the reason meta.capabilities_off gives
// when it is absent — the one table capabilitiesOff reads (a test
// checks every optional group has an entry).
var optionalCapabilities = map[string]string{
	"ingest":     offIngest,
	"search":     offSearch,
	"manifests":  offManifests,
	"spans_tree": offSpansTree,
	"timeline":   offTimeline,
	"stats":      offStats,
}

// manifestSourceView is one entry of api/manifest's sources.
type manifestSourceView struct {
	Source       string `json:"source"` // file
	ManifestHash string `json:"manifest_hash"`
	// Agents names the source's agents with each one's own hash (the
	// weft.manifest.hash its runs carry).
	Agents []manifestAgentView `json:"agents"`
}

type manifestAgentView struct {
	Name         string `json:"name"`
	ManifestHash string `json:"manifest_hash"`
}

// manifestSources lists every source: the file, when configured.
func (s *Server) manifestSources() []manifestSourceView {
	out := []manifestSourceView{}
	if len(s.manifest) > 0 {
		sum := sha256.Sum256(s.manifest)
		v := manifestSourceView{Source: manifestSourceFile, ManifestHash: hex.EncodeToString(sum[:]), Agents: []manifestAgentView{}}
		if hashes, err := manifestAgentHashes(s.manifest); err == nil {
			for _, a := range hashes {
				v.Agents = append(v.Agents, manifestAgentView{a.name, a.hash})
			}
		}
		out = append(out, v)
	}
	return out
}

// serveManifest answers api/manifest: the file manifest with sources
// added, else 404 saying why.
func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request) {
	if len(s.manifest) == 0 {
		notFound(w, r, "no manifest: no weft.json configured (the Manifest option, or weftbase dev --manifest)")
		return
	}
	writeRaw(w, r, withSources(s.manifest, s.manifestSources()))
}

// withSources splices "sources" into the file manifest as its last key,
// the file's own bytes kept verbatim before it (a file that has a
// sources key already is re-encoded with the server's in its place). A file that is not a
// JSON object is returned as given (manifest_check reports the parse
// error).
func withSources(manifest []byte, sources []manifestSourceView) []byte {
	var obj map[string]json.RawMessage
	if json.Unmarshal(manifest, &obj) != nil || obj == nil {
		return manifest
	}
	b, err := json.Marshal(sources)
	if err != nil {
		return manifest
	}
	if _, clash := obj["sources"]; clash {
		// The file has a sources key of its own: the server's replaces it
		// (one key, never a duplicate), re-encoded through the map.
		obj["sources"] = b
		if out, err := json.Marshal(obj); err == nil {
			return append(out, '\n')
		}
		return manifest
	}
	body := bytes.TrimRight(manifest, " \t\r\n")
	head := bytes.TrimRight(body[:len(body)-1], " \t\r\n")
	out := append([]byte(nil), head...)
	if len(obj) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"sources":`...)
	out = append(out, b...)
	return append(out, '}', '\n')
}

// writeRaw writes body as JSON verbatim.
func writeRaw(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// checkedManifest is what manifest_check compares runs with: the file's
// agents. source is "" when there is none.
func (s *Server) checkedManifest() (source string, hashes []agentHash, err error) {
	if len(s.manifest) == 0 {
		return "", nil, nil
	}
	hashes, err = manifestAgentHashes(s.manifest)
	return manifestSourceFile, hashes, err
}

// capabilitiesOff names each optional capability this server does not
// report and why (meta.capabilities_off): computed from capabilityList,
// so a capability a host declares itself (Capabilities) is never both
// on and off. Nil when nothing is off.
func (s *Server) capabilitiesOff() map[string]string {
	on := map[string]bool{}
	for _, c := range s.capabilityList() {
		on[c] = true
	}
	off := map[string]string{}
	for c, why := range optionalCapabilities {
		if !on[c] {
			off[c] = why
			for host, words := range s.offReasons {
				if optionalCapabilities[host] == why {
					off[c] = words
				}
			}
		}
	}
	if len(off) == 0 {
		return nil
	}
	return off
}
