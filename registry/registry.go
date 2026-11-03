// Package registry holds agents, builds, AgentVersions, labels and the
// manifests builds registered (P§5).
//
// An AgentVersion is immutable: (agent, build digest, manifest hash) is
// unique, its number increments per agent and its content hash is
// sha256 of the digest and the manifest hash. A registration of an
// unseen pair creates one; it is never labelled until someone promotes
// it. A label is a movable pointer; Promote moves it and writes an
// audited label.moved event. Resolve turns a selector into the version
// a new run takes.
//
// The registry is project-level: every method takes a tenant.Scope and
// reads its project and env; rows carry the project-level tenant.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Agent is a stable name in a project, with its labels.
type Agent struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"` // label → agv_…
	Versions  int               `json:"versions"`
	CreatedAt time.Time         `json:"created_at"`
}

// Build is an immutable code artifact, by digest.
type Build struct {
	ID        string    `json:"id"`
	Digest    string    `json:"digest"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// Version is an AgentVersion.
type Version struct {
	ID           string    `json:"id"`
	Agent        string    `json:"agent"`
	Number       int       `json:"number"`
	BuildID      string    `json:"build"`
	BuildDigest  string    `json:"build_digest"`
	ManifestHash string    `json:"manifest_hash"`
	ContentHash  string    `json:"content_hash"`
	Labels       []string  `json:"labels"`
	CreatedAt    time.Time `json:"created_at"`
}

// Manifest is a stored one-agent manifest.
type Manifest struct {
	Hash      string          `json:"manifest_hash"`
	Agent     string          `json:"agent"`
	Body      json.RawMessage `json:"manifest"`
	CreatedAt time.Time       `json:"created_at"`
}

// AgentSpec is one agent a build registers.
type AgentSpec struct {
	Name         string
	Manifest     []byte
	ManifestHash string
}

// Errors.
var (
	// ErrHashMismatch: a manifest whose sha256 is not its stated hash.
	ErrHashMismatch = errors.New("registry: manifest hash does not match its bytes")
	// ErrInvalid: a malformed name, digest or selector.
	ErrInvalid = errors.New("registry: invalid")
)

// Registry is the registry service over the control store.
type Registry struct {
	db  store.Runner
	log *events.Log
	now func() time.Time
}

// Option configures a Registry.
type Option func(*Registry)

// Clock sets the time source (tests).
func Clock(now func() time.Time) Option { return func(r *Registry) { r.now = now } }

// New returns the registry over db, emitting through log.
func New(db store.Runner, log *events.Log, opts ...Option) *Registry {
	r := &Registry{db: db, log: log, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ContentHash is an AgentVersion's content hash: sha256 over the build
// digest and the manifest hash.
func ContentHash(digest, manifestHash string) string {
	sum := sha256.Sum256([]byte(digest + "\n" + manifestHash))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ManifestHash is sha256 (lowercase hex) of a manifest's bytes: the
// weft.manifest.hash its runs carry.
func ManifestHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (r *Registry) ts() string { return r.now().UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func checkScope(s tenant.Scope) error {
	if s.IsZero() {
		return tenant.ErrZeroScope
	}
	return nil
}

// Register records a build and the agents it serves, in tx: the build
// row, each agent, each manifest and, for every unseen (digest,
// manifest hash), a new AgentVersion. It returns the versions in the
// order of agents. A new build writes build.pushed.
func (r *Registry) Register(tx store.Tx, s tenant.Scope, digest, kind string, agents []AgentSpec) ([]Version, error) {
	if err := checkScope(s); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) <= len("sha256:") {
		return nil, fmt.Errorf("%w: build digest %q is not sha256:<hex>", ErrInvalid, digest)
	}
	build, created, err := r.ensureBuild(tx, s, digest, kind)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	out := make([]Version, 0, len(agents))
	for _, a := range agents {
		if a.Name == "" {
			return nil, fmt.Errorf("%w: an agent needs a name", ErrInvalid)
		}
		if ManifestHash(a.Manifest) != a.ManifestHash {
			return nil, fmt.Errorf("%w: agent %s", ErrHashMismatch, a.Name)
		}
		if err := r.ensureAgent(tx, s, a.Name); err != nil {
			return nil, err
		}
		if _, err := store.Exec(tx, `INSERT INTO manifests (project_id, env, tenant_id, hash, agent, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, env, hash) DO NOTHING`,
			s.Project(), s.Env(), tenant.ProjectTenant, a.ManifestHash, a.Name, string(a.Manifest), r.ts()); err != nil {
			return nil, fmt.Errorf("registry: manifest: %w", err)
		}
		v, err := r.ensureVersion(tx, s, a.Name, build, a.ManifestHash)
		if err != nil {
			return nil, err
		}
		names = append(names, a.Name)
		out = append(out, v)
	}
	if created {
		if _, err := r.log.Emit(tx, s, events.Event{Type: events.BuildPushed, ResourceKind: "build", ResourceID: build.ID},
			map[string]any{"build": build.ID, "digest": build.Digest, "kind": build.Kind, "agents": names}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Registry) ensureBuild(tx store.Tx, s tenant.Scope, digest, kind string) (Build, bool, error) {
	var b Build
	var created string
	err := store.QueryRow(tx, `SELECT id, digest, kind, created_at FROM builds WHERE project_id = ? AND env = ? AND digest = ?`,
		s.Project(), s.Env(), digest).Scan(&b.ID, &b.Digest, &b.Kind, &created)
	if err == nil {
		b.CreatedAt = parseTS(created)
		return b, false, nil
	}
	if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Build{}, false, err
	}
	if kind == "" {
		kind = "external"
	}
	b = Build{ID: "bld_" + ulid.New(), Digest: digest, Kind: kind, CreatedAt: r.now().UTC()}
	_, err = store.Exec(tx, `INSERT INTO builds (id, project_id, env, tenant_id, digest, kind, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.ID, s.Project(), s.Env(), tenant.ProjectTenant, b.Digest, b.Kind, b.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Build{}, false, fmt.Errorf("registry: build: %w", err)
	}
	return b, true, nil
}

func (r *Registry) ensureAgent(tx store.Tx, s tenant.Scope, name string) error {
	_, err := store.Exec(tx, `INSERT INTO agents (id, project_id, env, tenant_id, name, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, env, name) DO NOTHING`,
		"ag_"+ulid.New(), s.Project(), s.Env(), tenant.ProjectTenant, name, r.ts())
	if err != nil {
		return fmt.Errorf("registry: agent: %w", err)
	}
	return nil
}

const selectVersion = `SELECT id, agent, number, build_id, build_digest, manifest_hash, content_hash, created_at FROM agent_versions`

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created string
	if err := row.Scan(&v.ID, &v.Agent, &v.Number, &v.BuildID, &v.BuildDigest, &v.ManifestHash, &v.ContentHash, &created); err != nil {
		return Version{}, store.NotFound(err)
	}
	v.CreatedAt = parseTS(created)
	return v, nil
}

func (r *Registry) ensureVersion(tx store.Tx, s tenant.Scope, agent string, b Build, manifestHash string) (Version, error) {
	v, err := scanVersion(store.QueryRow(tx, selectVersion+` WHERE project_id = ? AND env = ? AND agent = ? AND build_digest = ? AND manifest_hash = ?`,
		s.Project(), s.Env(), agent, b.Digest, manifestHash))
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return v, err
	}
	var n int
	if err := store.QueryRow(tx, `SELECT COALESCE(MAX(number), 0) FROM agent_versions WHERE project_id = ? AND env = ? AND agent = ?`,
		s.Project(), s.Env(), agent).Scan(&n); err != nil {
		return Version{}, err
	}
	v = Version{ID: "agv_" + ulid.New(), Agent: agent, Number: n + 1, BuildID: b.ID, BuildDigest: b.Digest,
		ManifestHash: manifestHash, ContentHash: ContentHash(b.Digest, manifestHash), CreatedAt: r.now().UTC()}
	_, err = store.Exec(tx, `INSERT INTO agent_versions (id, project_id, env, tenant_id, agent, number, build_id, build_digest, manifest_hash, content_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, s.Project(), s.Env(), tenant.ProjectTenant, v.Agent, v.Number, v.BuildID, v.BuildDigest, v.ManifestHash, v.ContentHash,
		v.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Version{}, fmt.Errorf("registry: version: %w", err)
	}
	return v, nil
}

// RegisterIn is Register in its own transaction.
func (r *Registry) RegisterIn(ctx context.Context, s tenant.Scope, digest, kind string, agents []AgentSpec) ([]Version, error) {
	var out []Version
	err := r.db.InTx(ctx, func(tx store.Tx) error {
		var err error
		out, err = r.Register(tx, s, digest, kind, agents)
		return err
	})
	return out, err
}

// ValidLabel reports whether l is a label name: 1–64 of [a-z0-9._-].
func ValidLabel(l string) bool {
	if l == "" || len(l) > 64 {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// PromoteRequest moves a label.
type PromoteRequest struct {
	Agent   string
	Label   string
	Version string // agv_…
	// Expected is the version the caller believes the label holds ("":
	// the label must not exist yet, unless Force); a mismatch is
	// store.ErrConflict.
	Expected string
	// Force skips the Expected check.
	Force  bool
	Reason string
	Actor  events.Actor
}

// Promote moves a label to a version of its agent and writes an
// audited label.moved event (from, to, reason, actor) in the same
// transaction.
func (r *Registry) Promote(tx store.Tx, s tenant.Scope, p PromoteRequest) error {
	if err := checkScope(s); err != nil {
		return err
	}
	if !ValidLabel(p.Label) {
		return fmt.Errorf("%w: label %q", ErrInvalid, p.Label)
	}
	v, err := r.version(tx, s, p.Version)
	if err != nil {
		return err
	}
	if v.Agent != p.Agent {
		return fmt.Errorf("%w: %s is a version of %s, not %s", ErrInvalid, v.ID, v.Agent, p.Agent)
	}
	var from string
	err = store.QueryRow(tx, `SELECT version_id FROM labels WHERE project_id = ? AND env = ? AND agent = ? AND label = ?`,
		s.Project(), s.Env(), p.Agent, p.Label).Scan(&from)
	if err != nil && !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return err
	}
	if !p.Force && from != p.Expected {
		return fmt.Errorf("%w: label %s holds %q, the caller expected %q", store.ErrConflict, p.Label, from, p.Expected)
	}
	if _, err := store.Exec(tx, `INSERT INTO labels (project_id, env, tenant_id, agent, label, version_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, env, agent, label) DO UPDATE SET version_id = excluded.version_id, updated_at = excluded.updated_at`,
		s.Project(), s.Env(), tenant.ProjectTenant, p.Agent, p.Label, v.ID, r.ts()); err != nil {
		return fmt.Errorf("registry: label: %w", err)
	}
	_, err = r.log.Emit(tx, s, events.Event{Type: events.LabelMoved, ResourceKind: "agent", ResourceID: p.Agent, Actor: p.Actor, Audited: true},
		map[string]any{"agent": p.Agent, "label": p.Label, "from": from, "to": v.ID, "reason": p.Reason})
	return err
}

// PromoteIn is Promote in its own transaction.
func (r *Registry) PromoteIn(ctx context.Context, s tenant.Scope, p PromoteRequest) error {
	return r.db.InTx(ctx, func(tx store.Tx) error { return r.Promote(tx, s, p) })
}

// How a run's version was chosen (run.resolved_via).
const (
	ViaLatest = "latest"
	ViaPin    = "pin"
)

// Resolve turns a selector into a version of agent, in tx:
// "label:<l>" (resolved_via label:<l>), an agv_… id (pin), or "" — the
// agent's prod label when it has one, else its newest version
// (latest). An unknown agent, label or version is store.ErrNotFound.
func (r *Registry) Resolve(tx store.Tx, s tenant.Scope, agent, selector string) (Version, string, error) {
	if err := checkScope(s); err != nil {
		return Version{}, "", err
	}
	switch {
	case strings.HasPrefix(selector, "agv_"):
		v, err := r.version(tx, s, selector)
		if err != nil {
			return Version{}, "", err
		}
		if v.Agent != agent {
			return Version{}, "", fmt.Errorf("%w: version %s of agent %s", store.ErrNotFound, selector, agent)
		}
		return v, ViaPin, nil
	case strings.HasPrefix(selector, "label:"):
		l := strings.TrimPrefix(selector, "label:")
		v, err := r.labelled(tx, s, agent, l)
		return v, "label:" + l, err
	case selector == "":
		v, err := r.labelled(tx, s, agent, "prod")
		if err == nil {
			return v, "label:prod", nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return Version{}, "", err
		}
		v, err = scanVersion(store.QueryRow(tx, selectVersion+` WHERE project_id = ? AND env = ? AND agent = ? ORDER BY number DESC LIMIT 1`,
			s.Project(), s.Env(), agent))
		if err != nil {
			return Version{}, "", fmt.Errorf("agent %s: %w", agent, err)
		}
		return v, ViaLatest, nil
	}
	return Version{}, "", fmt.Errorf("%w: selector %q (want label:<name> or agv_…)", ErrInvalid, selector)
}

func (r *Registry) labelled(tx store.Tx, s tenant.Scope, agent, label string) (Version, error) {
	v, err := scanVersion(store.QueryRow(tx, `SELECT v.id, v.agent, v.number, v.build_id, v.build_digest, v.manifest_hash, v.content_hash, v.created_at
		FROM labels l JOIN agent_versions v ON v.id = l.version_id
		WHERE l.project_id = ? AND l.env = ? AND l.agent = ? AND l.label = ?`, s.Project(), s.Env(), agent, label))
	if err != nil {
		return Version{}, fmt.Errorf("label %s of agent %s: %w", label, agent, err)
	}
	return v, nil
}

func (r *Registry) version(tx store.Tx, s tenant.Scope, id string) (Version, error) {
	v, err := scanVersion(store.QueryRow(tx, selectVersion+` WHERE project_id = ? AND env = ? AND id = ?`, s.Project(), s.Env(), id))
	if err != nil {
		return Version{}, fmt.Errorf("version %s: %w", id, err)
	}
	return v, nil
}

// Lookup reads one AgentVersion in tx (without its labels).
func (r *Registry) Lookup(tx store.Tx, s tenant.Scope, id string) (Version, error) {
	if err := checkScope(s); err != nil {
		return Version{}, err
	}
	return r.version(tx, s, id)
}

// Version reads one AgentVersion with its labels.
func (r *Registry) Version(ctx context.Context, s tenant.Scope, id string) (Version, error) {
	if err := checkScope(s); err != nil {
		return Version{}, err
	}
	var v Version
	err := r.db.Read(ctx, func(tx store.Tx) error {
		var err error
		if v, err = r.version(tx, s, id); err != nil {
			return err
		}
		labels, err := r.labelsOf(tx, s, v.Agent)
		for l, id := range labels {
			if id == v.ID {
				v.Labels = append(v.Labels, l)
			}
		}
		return err
	})
	return v, err
}

func (r *Registry) labelsOf(tx store.Tx, s tenant.Scope, agent string) (map[string]string, error) {
	rows, err := store.Query(tx, `SELECT label, version_id FROM labels WHERE project_id = ? AND env = ? AND agent = ? ORDER BY label`,
		s.Project(), s.Env(), agent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var l, id string
		if err := rows.Scan(&l, &id); err != nil {
			return nil, err
		}
		out[l] = id
	}
	return out, rows.Err()
}

// Agents lists the scope's agents by name, with their labels.
func (r *Registry) Agents(ctx context.Context, s tenant.Scope) ([]Agent, error) {
	if err := checkScope(s); err != nil {
		return nil, err
	}
	var out []Agent
	err := r.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, `SELECT a.id, a.name, a.created_at,
			(SELECT COUNT(*) FROM agent_versions v WHERE v.project_id = a.project_id AND v.env = a.env AND v.agent = a.name)
			FROM agents a WHERE a.project_id = ? AND a.env = ? ORDER BY a.name`, s.Project(), s.Env())
		if err != nil {
			return err
		}
		for rows.Next() {
			var a Agent
			var created string
			if err := rows.Scan(&a.ID, &a.Name, &created, &a.Versions); err != nil {
				_ = rows.Close()
				return err
			}
			a.CreatedAt = parseTS(created)
			out = append(out, a)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			if out[i].Labels, err = r.labelsOf(tx, s, out[i].Name); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// Versions lists an agent's versions, newest first, with their labels.
// An unknown agent is store.ErrNotFound.
func (r *Registry) Versions(ctx context.Context, s tenant.Scope, agent string) ([]Version, error) {
	if err := checkScope(s); err != nil {
		return nil, err
	}
	var out []Version
	err := r.db.Read(ctx, func(tx store.Tx) error {
		var n int
		if err := store.QueryRow(tx, `SELECT COUNT(*) FROM agents WHERE project_id = ? AND env = ? AND name = ?`,
			s.Project(), s.Env(), agent).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("agent %s: %w", agent, store.ErrNotFound)
		}
		rows, err := store.Query(tx, selectVersion+` WHERE project_id = ? AND env = ? AND agent = ? ORDER BY number DESC`,
			s.Project(), s.Env(), agent)
		if err != nil {
			return err
		}
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			out = append(out, v)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		labels, err := r.labelsOf(tx, s, agent)
		for l, id := range labels {
			for i := range out {
				if out[i].ID == id {
					out[i].Labels = append(out[i].Labels, l)
				}
			}
		}
		return err
	})
	return out, err
}

// Manifest reads a stored manifest by hash.
func (r *Registry) Manifest(ctx context.Context, s tenant.Scope, hash string) (Manifest, error) {
	if err := checkScope(s); err != nil {
		return Manifest{}, err
	}
	var m Manifest
	err := r.db.Read(ctx, func(tx store.Tx) error {
		var body, created string
		err := store.QueryRow(tx, `SELECT hash, agent, body, created_at FROM manifests WHERE project_id = ? AND env = ? AND hash = ?`,
			s.Project(), s.Env(), hash).Scan(&m.Hash, &m.Agent, &body, &created)
		if err != nil {
			return fmt.Errorf("manifest %s: %w", hash, store.NotFound(err))
		}
		m.Body, m.CreatedAt = json.RawMessage(body), parseTS(created)
		return nil
	})
	return m, err
}
