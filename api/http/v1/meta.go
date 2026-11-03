package v1

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/internal/release"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/observe/ingest"
)

// GET /v1/meta: what this server is and who is asking, computed from
// the server's own state, never hard-coded. `weftbase doctor` reads its
// checks from here and /readyz.

// metaDoc is GET /v1/meta's body.
type metaDoc struct {
	WeftVersion string `json:"weft_version"`
	// ServerVersion is weftbase's own release (internal/release).
	ServerVersion string `json:"server_version"`
	// Capabilities are the optional features this deployment has on:
	// ingest (the ingest role), clickhouse (the trace store), and later
	// workers, approvals and tenants. Never null.
	Capabilities []string `json:"capabilities"`
	// Principal is who is asking and the scope it acts in.
	Principal metaPrincipal `json:"principal"`
	// Ingest is the ingest health (U5): the services whose heartbeats
	// and run brackets reported records their destination lost on the
	// way here, and the batches this node accepted and refused.
	Ingest *ingest.Report `json:"ingest,omitempty"`
	// Pricing summarizes the table usage is priced with.
	Pricing            metaPricing `json:"pricing"`
	DB                 metaDB      `json:"db"`
	InterruptedAfterMs int64       `json:"interrupted_after_ms"`
	Content            metaContent `json:"content"`
	// Retention is the trace retention as a Go duration string
	// ("720h0m0s"); null: none configured. The key is always present.
	Retention *string `json:"retention"`
	// ManifestCheck compares the manifest (weft.json) with the
	// manifest hash each agent's latest stored run recorded. Null
	// without a manifest.
	ManifestCheck *metaManifest `json:"manifest_check"`
	// PID is the serving process's id, for a caller allowed the host's
	// details (hostDetails); omitted for anyone else.
	PID int `json:"pid,omitempty"`
}

// metaPrincipal is meta's "who am I".
type metaPrincipal struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	KeyKind    string    `json:"key_kind,omitempty"`
	Scope      metaScope `json:"scope"`
	AllTenants bool      `json:"all_tenants"`
}

type metaScope struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Tenant  string `json:"tenant"`
}

// metaDB names the trace database. Kind is everyone's; Path and Size
// only a caller's allowed the host's details (hostDetails), omitted,
// never nulled, for anyone else. Both are omitted for a database with
// no file (":memory:", ClickHouse).
type metaDB struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	// Size is the file's bytes on disk, its WAL sidecar included.
	Size *int64 `json:"size,omitempty"`
}

// metaContent is what the server can know about content as a
// destination: its own ingest policy (content is stored as received;
// the server never strips) and the latest stored run's content mark.
// The app's per-destination policy is the app's — the server sees only
// what arrived.
type metaContent struct {
	Ingest string `json:"ingest"` // "as_received"
	// Latest is the latest top-level run's mark; null when no run is
	// stored yet (or it could not be read).
	Latest *metaContentRun `json:"latest"`
	// Error is the read that failed while looking for the latest run
	// (logged too); latest is then null for that reason, not because no
	// run is stored.
	Error string `json:"error,omitempty"`
}

// metaContentRun is one run's content mark with the server's reading of it.
type metaContentRun struct {
	RunID string `json:"run_id"`
	// Mark is the run's first event's weft.content: full, stripped (a
	// content-off destination chain), none (the agent captured none),
	// or unmarked (no events, or a writer that set no mark).
	Mark string `json:"mark"`
	Note string `json:"note"`
	Fix  string `json:"fix,omitempty"`
	// Started, WeftVersion and Service are the run's own (its row's):
	// when the newest data arrived and which weft and service sent it —
	// `weftbase doctor`'s data line, which warns when the weft differs from
	// this server's. weft_version and service are omitted when the run
	// carries none (a non-weft exporter).
	Started     time.Time `json:"started"`
	WeftVersion string    `json:"weft_version,omitempty"`
	Service     string    `json:"service,omitempty"`
}

// metaManifest is the manifest staleness check.
type metaManifest struct {
	// Source is what was checked: file (weft.json).
	Source  string   `json:"source"`
	Agents  int      `json:"agents"`  // agents in the manifest
	Checked int      `json:"checked"` // of those, agents with a stored run carrying a manifest hash
	Stale   []string `json:"stale"`   // checked agents whose latest run's hash differs; never null
	// Latest is each checked agent's latest run's manifest hash, by
	// agent name (U20.1): what a stale badge names — "the latest run
	// ran d2151022, not this manifest". Absent when nothing was
	// checked.
	Latest map[string]string `json:"latest,omitempty"`
	// Error is the first failure of the check (a manifest that does
	// not parse, a run read that failed; logged too): the counts cover
	// only what was checked before or around it.
	Error string `json:"error,omitempty"`
}

// Content marks /v1/meta reports (content.latest.mark).
const (
	contentIngestAsReceived = "as_received"
	markFull                = "full"
	markStripped            = "stripped"
	markNone                = "none"
	markUnmarked            = "unmarked"
)

// contentNotes is the server's reading of each mark: the note, and the fix
// that names the cause in the app.
var contentNotes = map[string][2]string{
	markFull: {"the latest run's content is stored in full", ""},
	markStripped: {"a content-off destination stripped the latest run's content before it reached weftbase",
		"drop otel.NoContent() from the destination that sends to weftbase"},
	// none is the core's capture-off mark: weft.Content(false), or no
	// destination takes content (core/observe.go's captureOn) — the
	// note names both, never only the option.
	markNone: {"the latest run captured no content: weft.Content(false) on the agent, or no destination takes content",
		"drop weft.Content(false) from the agent, or install a destination that takes content (exporter.Local, or one without otel.NoContent())"},
	markUnmarked: {"the latest run carries no content mark: no events stored, a weft older than the mark, or a non-weft exporter", ""},
}

// serveMeta answers GET /v1/meta.
func (s *Server) serveMeta(w http.ResponseWriter, r *http.Request) {
	p, sc, _ := authz.From(r.Context())
	doc := metaDoc{
		WeftVersion:   weftVersion(),
		ServerVersion: release.Version,
		Capabilities:  append([]string{}, s.capabilities...),
		Principal: metaPrincipal{
			Kind: string(p.Kind), ID: p.ID, KeyKind: string(p.KeyKind), AllTenants: p.AllTenants,
			Scope: metaScope{Project: sc.Project(), Env: sc.Env(), Tenant: sc.Tenant()},
		},
		DB:                 metaDB{Kind: dbKind(s.db)},
		InterruptedAfterMs: obsdb.InterruptedAfter.Milliseconds(),
		Content:            s.latestContent(r.Context()),
		Pricing:            s.metaPricing(r),
		ManifestCheck:      s.manifestCheck(r.Context()),
	}
	if s.retain > 0 {
		r := s.retain.String()
		doc.Retention = &r
	}
	if hostDetails(r) {
		doc.DB.Path, doc.DB.Size = dbFile(s.db)
		doc.PID = os.Getpid()
	}
	ingest := s.ingestHealth.Snapshot()
	if s.ingestCounts != nil {
		ingest = ingest.WithCounts(s.ingestCounts())
	}
	doc.Ingest = &ingest
	writeJSON(w, r, http.StatusOK, doc)
}

// hostDetails is whether the caller may see the host's details — file
// paths, sizes, the pid: a principal allowed settings.read (a secret
// key, an operator, the Local principal).
func hostDetails(r *http.Request) bool {
	p, _, ok := authz.From(r.Context())
	return ok && authz.Can(p, authz.SettingsRead).Allow
}

// dbFile is the database's file and its size on disk (the WAL sidecar
// included), for a backend that names a file (interface{ Path()
// string }: obsdb/sqlite). A file the server cannot stat reports its path
// and no size.
func dbFile(db obsdb.DB) (string, *int64) {
	p, ok := db.(interface{ Path() string })
	if !ok || p.Path() == "" {
		return "", nil
	}
	path := filepath.Clean(p.Path())
	fi, err := os.Stat(path)
	if err != nil {
		return path, nil
	}
	size := fi.Size()
	if wal, err := os.Stat(path + "-wal"); err == nil {
		size += wal.Size()
	}
	return path, &size
}

// latestContent reads the latest top-level run's content mark. Meta
// never fails on an observation: a failed read is logged and reported
// in content.error.
func (s *Server) latestContent(ctx context.Context) metaContent {
	out := metaContent{Ingest: contentIngestAsReceived}
	q := obsdb.RunQuery{Limit: 1}
	page, err := s.db.Runs(ctx, q)
	if err != nil {
		out.Error = metaReadFailed("content: read the latest run", err)
		return out
	}
	if len(page.Runs) == 0 {
		return out
	}
	run := page.Runs[0]
	mark := markUnmarked
	if run.EventCount > 0 {
		ev, err := s.db.Events(ctx, run.ID, -1, 1)
		switch {
		case err != nil:
			out.Error = metaReadFailed("content: read run "+run.ID+"'s first event", err)
		case len(ev.Events) > 0:
			switch m := ev.Events[0].Content; m {
			case markFull, markStripped, markNone:
				mark = m
			}
		}
	}
	n := contentNotes[mark]
	out.Latest = &metaContentRun{RunID: run.ID, Mark: mark, Note: n[0], Fix: n[1],
		Started: run.Started, WeftVersion: run.WeftVersion, Service: run.Service}
	return out
}

// metaReadFailed logs a meta observation that failed (the server has
// no logger of its own: the process's slog default) and returns the
// text meta reports.
func metaReadFailed(what string, err error) string {
	msg := what + ": " + err.Error()
	slog.Warn("weftbase: /v1/meta: "+what, "err", err)
	return msg
}

// manifestCheck compares each manifest agent's hash — sha256 of the
// agent's own one-agent manifest document, the hash the core stamps as
// weft.manifest.hash — with the latest stored run of that agent
// (subagent runs included). The manifest is the weft.json; nil without
// one. A manifest that does not parse checks nothing; it and a failed
// run read are logged and reported in error.
func (s *Server) manifestCheck(ctx context.Context) *metaManifest {
	source, hashes, err := s.checkedManifest()
	if source == "" {
		return nil
	}
	out := &metaManifest{Source: source, Stale: []string{}}
	if err != nil {
		out.Error = metaReadFailed("manifest_check: the manifest does not parse", err)
		return out
	}
	// One check per agent name (a run matching any version of the name
	// is current).
	var names []string
	versions := map[string]map[string]bool{}
	for _, a := range hashes {
		if versions[a.name] == nil {
			versions[a.name] = map[string]bool{}
			names = append(names, a.name)
		}
		versions[a.name][a.hash] = true
	}
	out.Agents = len(names)
	for _, name := range names {
		page, err := s.db.Runs(ctx, obsdb.RunQuery{Agent: name, ParentRunID: "*", Limit: 1})
		if err != nil {
			if out.Error == "" {
				out.Error = metaReadFailed("manifest_check: read agent "+name+"'s latest run", err)
			}
			continue
		}
		if len(page.Runs) == 0 || page.Runs[0].ManifestHash == "" {
			continue
		}
		out.Checked++
		if out.Latest == nil {
			out.Latest = map[string]string{}
		}
		out.Latest[name] = page.Runs[0].ManifestHash
		if !versions[name][page.Runs[0].ManifestHash] {
			out.Stale = append(out.Stale, name)
		}
	}
	return out
}

type agentHash struct{ name, hash string }

// manifestAgentHashes splits a weft.json into its agents and hashes
// each as the core does (core.Manifest(agent), sha256, lowercase hex):
// the one-agent document re-encoded through the same MarshalIndent,
// which reproduces core's bytes for any agent core.Manifest wrote.
func manifestAgentHashes(manifest []byte) ([]agentHash, error) {
	docs, err := manifestAgentDocs(manifest)
	if err != nil {
		return nil, err
	}
	out := make([]agentHash, 0, len(docs))
	for _, d := range docs {
		out = append(out, agentHash{d.name, obsdb.ManifestHash(d.body)})
	}
	return out, nil
}

// agentDoc is one agent of a manifest as its own one-agent document:
// the bytes weft.Manifest writes for that agent alone, whose sha256 is
// the weft.manifest.hash its runs carry.
type agentDoc struct {
	name string
	body []byte
}

// manifestAgentDocs splits a manifest into its agents' one-agent
// documents, each re-encoded as core.Manifest encodes it (two-space
// indent, a trailing newline) — what manifestAgentHashes hashes and
// the manifest store keeps (U27).
func manifestAgentDocs(manifest []byte) ([]agentDoc, error) {
	var doc struct {
		Version int               `json:"weft"`
		Agents  []json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, err
	}
	out := make([]agentDoc, 0, len(doc.Agents))
	for _, raw := range doc.Agents {
		var named struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &named); err != nil {
			return nil, err
		}
		one := struct {
			Version int               `json:"weft"`
			Agents  []json.RawMessage `json:"agents"`
		}{doc.Version, []json.RawMessage{raw}}
		b, err := json.MarshalIndent(one, "", "  ")
		if err != nil {
			return nil, err
		}
		out = append(out, agentDoc{named.Name, append(b, '\n')})
	}
	return out, nil
}
