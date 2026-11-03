package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// /v1/manifest: the agents this server knows, from the weft.json
// passed to Manifest(...). The response carries a sources array naming
// the file and each agent's own hash.

// manifestSourceFile is the one manifest source kind: the weft.json.
const manifestSourceFile = "file"

// manifestSourceView is one entry of /v1/manifest's sources.
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

// serveManifest answers /v1/manifest: the file manifest with sources
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
