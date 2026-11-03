package weftbase

import (
	"context"
	"errors"
	"fmt"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/worker"
	"github.com/weftbase/weftbase/worker/executor"
)

// Agent serves agents in this process: the embedded placement. They
// register as one build (BuildDigest, else the executable's sha256),
// which records the build and an AgentVersion per agent in the default
// project's test environment, and every queued run of those versions
// is dispatched to them by the scheduler role. Call it before Run.
func (a *App) Agent(agents ...*core.Agent) error {
	digest := a.cfg.buildDigest
	if digest == "" {
		d, err := executor.ExecutableDigest()
		if err != nil {
			return fmt.Errorf("weftbase: the embedded build's digest: %w", err)
		}
		digest = d
	}
	ex, err := executor.NewEmbedded(digest, agents)
	if err != nil {
		return fmt.Errorf("weftbase: %w", err)
	}
	_, err = a.Executor(ex)
	return err
}

// Executor attaches an executor of any placement: its registration
// records its build and AgentVersions, and the scheduler role
// dispatches the runs of those versions to it. Each registered manifest
// is also written to the trace store's manifest store, so a run's
// manifest_hash resolves there (GET /v1/manifests/{hash}).
func (a *App) Executor(ex worker.Executor) ([]registry.Version, error) {
	a.mu.Lock()
	down := a.shutdown
	a.mu.Unlock()
	if down {
		return nil, errors.New("weftbase: Executor after Shutdown")
	}
	ctx := context.Background()
	w, versions, err := a.hub.Attach(ctx, tenant.Default(), ex)
	if err != nil {
		return nil, fmt.Errorf("weftbase: attach: %w", err)
	}
	if ms, ok := a.traces.(obsdb.ManifestStore); ok {
		for _, ag := range w.Registration.Agents {
			m := obsdb.StoredManifest{Hash: ag.ManifestHash, Body: ag.Manifest, Source: "registry"}
			if err := ms.PutManifest(ctx, m); err != nil {
				a.log.Warn("weftbase: the trace store refused a registered manifest", "agent", ag.Name, "err", err)
			}
		}
	}
	return versions, nil
}

// Registry is the agent registry (agents, builds, versions, labels).
func (a *App) Registry() *registry.Registry { return a.registry }

// Ledger is the run ledger (admit, cancel, resume).
func (a *App) Ledger() *sessions.Ledger { return a.ledger }

// Queue is the durable work queue.
func (a *App) Queue() *queue.Queue { return a.queue }
