package weftbase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/client/wire"
	buildworker "github.com/weftbase/weftbase/client/worker"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
	"github.com/weftbase/weftbase/worker/executor/local"
)

// The local placement (P§5.7, M§4.3): a build is an executable that
// serves its agents with client/worker. Local copies it into the build
// store (<data>/builds/<sha256>), so the bytes behind a digest never
// change, and the supervisor role runs it as a child over a Unix socket
// under <data>/run. A build that a newer one of the same agents
// replaces is retired: drained, and started again only for runs pinned
// to it. At New, every stored build the registry knows is attached
// again without starting (its registration comes from the registry):
// a parked run resumes on its pinned build after a restart or a new
// deploy.

// LocalOption configures a local build.
type LocalOption func(*localConfig)

type localConfig struct {
	env    []string
	args   []string
	output io.Writer
}

// LocalEnv adds KEY=VALUE environment variables for the build's child.
func LocalEnv(kv ...string) LocalOption {
	return func(c *localConfig) { c.env = append(c.env, kv...) }
}

// LocalArgs are extra arguments for the build's child.
func LocalArgs(args ...string) LocalOption {
	return func(c *localConfig) { c.args = append(c.args, args...) }
}

// LocalOutput takes the child's stdout and stderr (default the node's
// stderr).
func LocalOutput(w io.Writer) LocalOption { return func(c *localConfig) { c.output = w } }

// localBuild is one attached local build.
type localBuild struct {
	ex       *local.Executor
	agents   []string
	versions []registry.Version
}

func (a *App) buildsDir() string { return filepath.Join(a.cfg.dataDir, "builds") }
func (a *App) socketDir() string { return filepath.Join(a.cfg.dataDir, "run") }

// Local runs the executable at path as a local build: it is copied
// into the build store, started under the supervisor role, and its
// registration records the build and an AgentVersion per agent (none is
// labelled). Older local builds serving any of the same agents are
// retired. Calling it again with the same bytes returns the versions
// it already serves.
func (a *App) Local(path string, opts ...LocalOption) ([]registry.Version, error) {
	if !a.roles.has(RoleSupervisor) {
		return nil, errors.New("weftbase: local builds run under the supervisor role")
	}
	var c localConfig
	for _, o := range opts {
		o(&c)
	}
	digest, err := buildworker.FileDigest(path)
	if err != nil {
		return nil, fmt.Errorf("weftbase: build %s: %w", path, err)
	}
	a.mu.Lock()
	if b, ok := a.locals[digest]; ok && b.versions != nil {
		a.mu.Unlock()
		return b.versions, nil
	}
	a.mu.Unlock()
	stored, err := a.storeBuild(path, digest)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if old, ok := a.locals[digest]; ok {
		// Attached at boot from the registry, lazily: this deploy makes
		// it current again.
		delete(a.locals, digest)
		a.mu.Unlock()
		a.hub.Detach(old.ex)
		_ = old.ex.Close()
	} else {
		a.mu.Unlock()
	}
	ex, err := local.New(local.Config{Path: stored, Digest: digest, Args: c.args, Env: c.env, SocketDir: a.socketDir(),
		Output: c.output, Logger: a.log, Telemetry: a.telemetryFor(digest)})
	if err != nil {
		return nil, fmt.Errorf("weftbase: %w", err)
	}
	versions, err := a.Executor(ex)
	if err != nil {
		_ = ex.Close()
		return nil, err
	}
	b := &localBuild{ex: ex, versions: versions}
	for _, v := range versions {
		b.agents = append(b.agents, v.Agent)
	}
	a.mu.Lock()
	var retire []*localBuild
	for d, other := range a.locals {
		if d != digest && !other.ex.Retired() && overlaps(other.agents, b.agents) {
			retire = append(retire, other)
		}
	}
	a.locals[digest] = b
	a.mu.Unlock()
	for _, old := range retire {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), a.cfg.drainTimeout()+10*time.Second)
			defer cancel()
			if err := old.ex.Retire(ctx, time.Now().Add(a.cfg.drainTimeout())); err != nil {
				a.log.Warn("weftbase: retiring a build", "digest", old.ex.Digest(), "err", err)
			}
			// Retired and drained: its key goes (a start for a pinned
			// run mints another).
			a.revokeBuildKey(old.ex.Digest())
		}()
	}
	return versions, nil
}

// buildKeyPrefix names a build's ingest key: "build:" and its digest.
const buildKeyPrefix = "build:"

// buildKey is a build's ingest key, minted on first use.
type buildKey struct{ id, secret string }

// telemetryFor is a build's Telemetry (local.Config).
func (a *App) telemetryFor(digest string) func() (string, string) {
	return func() (string, string) { return a.buildTelemetry(digest) }
}

// buildTelemetry is where a local build's traces and logs go: this
// node's URL (Serving) and the build's own worker key, which may ingest
// and nothing else of the API, and ingest only the records of the runs
// dispatched to that build (buildGate). The key is minted at the
// build's first start and lives in memory (the store keeps its hash
// only); it is revoked when the build is retired (a later start of the
// retired build mints another) and at Shutdown, and the keys earlier
// starts of the node minted are revoked at New. A node that does not
// ingest, or was never told its URL, hands out nothing: the build runs,
// and reports nowhere (its own environment's WEFTBASE_URL and
// WEFTBASE_TOKEN are never inherited).
func (a *App) buildTelemetry(digest string) (url, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.url == "" || !a.roles.has(RoleIngest) || a.shutdown {
		return "", ""
	}
	if k, ok := a.buildKeys[digest]; ok {
		return a.url, k.secret
	}
	k, secret, err := a.keys.Mint(context.Background(), tenant.Default(), keys.Worker, buildKeyPrefix+digest, events.System)
	if err != nil {
		a.log.Warn("weftbase: no ingest key for a local build; its traces are not collected", "digest", digest, "err", err)
		return "", ""
	}
	a.buildKeys[digest] = buildKey{id: k.ID, secret: secret}
	return a.url, secret
}

// revokeBuildKey revokes digest's ingest key, if one is live.
func (a *App) revokeBuildKey(digest string) {
	a.mu.Lock()
	k, ok := a.buildKeys[digest]
	delete(a.buildKeys, digest)
	a.mu.Unlock()
	if ok {
		if err := a.keys.Revoke(context.Background(), tenant.Default(), k.id, events.System); err != nil {
			a.log.Warn("weftbase: revoking a build's ingest key", "digest", digest, "err", err)
		}
	}
}

// revokeBuildKeys revokes every build's ingest key this node minted
// (Shutdown).
func (a *App) revokeBuildKeys() {
	a.mu.Lock()
	ds := make([]string, 0, len(a.buildKeys))
	for d := range a.buildKeys {
		ds = append(ds, d)
	}
	a.mu.Unlock()
	for _, d := range ds {
		a.revokeBuildKey(d)
	}
}

// revokeStaleBuildKeys revokes the build ingest keys earlier starts of
// the node minted (and the single shared key of releases before them).
func (a *App) revokeStaleBuildKeys(ctx context.Context) error {
	ks, err := a.keys.List(ctx, tenant.Default())
	if err != nil {
		return err
	}
	for _, k := range ks {
		if k.Kind == keys.Worker && k.RevokedAt == nil && (strings.HasPrefix(k.Name, buildKeyPrefix) || k.Name == "local-builds") {
			if err := a.keys.Revoke(ctx, tenant.Default(), k.ID, events.System); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildGate is OTLP ingest's ownership check (observe/ingest.Gate): a
// worker key writes only the runs dispatched to its build, and their
// subagent children. A worker key that names no build (minted by an
// operator) writes no run's records.
type buildGate struct{ a *App }

func (g buildGate) Restricted(r *http.Request) (string, bool) {
	p, _, ok := authz.From(r.Context())
	if !ok || p.Kind != authz.Key || p.KeyKind != keys.Worker {
		return "", false
	}
	return p.ID, true
}

func (g buildGate) Refused(ctx context.Context, r *http.Request, runIDs []string) ([]string, error) {
	p, sc, _ := authz.From(ctx)
	digest, err := g.a.keyBuild(ctx, sc, p.ID)
	if err != nil {
		return nil, err
	}
	if digest == "" {
		return runIDs, nil
	}
	owned, err := g.a.ledger.DispatchedToBuild(ctx, sc, digest, runIDs)
	if err != nil {
		return nil, err
	}
	var refused []string
	for _, id := range runIDs {
		if !owned[id] {
			refused = append(refused, id)
		}
	}
	return refused, nil
}

// keyBuild is the digest of the build a worker key was minted for
// (empty: none).
func (a *App) keyBuild(ctx context.Context, sc tenant.Scope, keyID string) (string, error) {
	a.mu.Lock()
	for d, k := range a.buildKeys {
		if k.id == keyID {
			a.mu.Unlock()
			return d, nil
		}
	}
	a.mu.Unlock()
	ks, err := a.keys.List(ctx, sc)
	if err != nil {
		return "", err
	}
	for _, k := range ks {
		if k.ID == keyID {
			if d, ok := strings.CutPrefix(k.Name, buildKeyPrefix); ok {
				return d, nil
			}
			return "", nil
		}
	}
	return "", nil
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// storeBuild copies path into the build store as <hex>, executable,
// unless it is there already.
func (a *App) storeBuild(path, digest string) (string, error) {
	dir := a.buildsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("weftbase: build store: %w", err)
	}
	dst := filepath.Join(dir, strings.TrimPrefix(digest, "sha256:"))
	if d, err := buildworker.FileDigest(dst); err == nil && d == digest {
		return dst, nil
	}
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if d, err := buildworker.FileDigest(tmp.Name()); err != nil || d != digest {
		return "", fmt.Errorf("weftbase: build %s changed while it was copied", path)
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// restoreBuilds attaches every stored build the registry knows, lazily
// and retired: nothing starts until a run pinned to one is claimed.
func (a *App) restoreBuilds() error {
	entries, err := os.ReadDir(a.buildsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("weftbase: build store: %w", err)
	}
	ctx := context.Background()
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		digest := "sha256:" + e.Name()
		kind, agents, err := a.registry.BuildAgents(ctx, tenant.Default(), digest)
		if err != nil || len(agents) == 0 {
			a.log.Debug("weftbase: a stored build the registry does not know", "digest", digest, "err", err)
			continue
		}
		reg := wire.Registration{Placement: wire.PlacementLocal, Build: wire.Build{Digest: digest, Kind: kind}}
		names := make([]string, 0, len(agents))
		for _, ag := range agents {
			reg.Agents = append(reg.Agents, wire.AgentRegistration{Name: ag.Name, Manifest: ag.Manifest, ManifestHash: ag.ManifestHash})
			names = append(names, ag.Name)
		}
		ex, err := local.New(local.Config{Path: filepath.Join(a.buildsDir(), e.Name()), Digest: digest, Known: &reg,
			SocketDir: a.socketDir(), Logger: a.log, Telemetry: a.telemetryFor(digest)})
		if err != nil {
			a.log.Warn("weftbase: restoring a stored build", "digest", digest, "err", err)
			continue
		}
		_ = ex.Retire(ctx, time.Now())
		if _, _, err := a.hub.Attach(ctx, tenant.Default(), ex); err != nil {
			_ = ex.Close()
			a.log.Warn("weftbase: restoring a stored build", "digest", digest, "err", err)
			continue
		}
		a.mu.Lock()
		a.locals[digest] = &localBuild{ex: ex, agents: names}
		a.mu.Unlock()
	}
	return nil
}

// closeLocals closes every local build's executor (after the hub drained
// them).
func (a *App) closeLocals() {
	a.mu.Lock()
	bs := make([]*localBuild, 0, len(a.locals))
	for _, b := range a.locals {
		bs = append(bs, b)
	}
	a.locals = map[string]*localBuild{}
	a.mu.Unlock()
	for _, b := range bs {
		_ = b.ex.Close()
	}
}
