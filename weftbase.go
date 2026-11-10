// Package weftbase is the kernel: it opens the stores, wires the
// domains, the API and the Console, and runs the roles a node was given
// (plan §4.3, §5.1). One binary embeds it (cmd/weftbase); an app embeds
// it too and mounts Handler under a prefix.
//
//	app, err := weftbase.New(weftbase.DataDir(".weftbase"), weftbase.BootstrapKey(key))
//	defer app.Shutdown(ctx)
//	go app.Run(ctx)
//	http.ListenAndServe("127.0.0.1:7331", app.Handler())
//
// The kernel holds no global state, parses no flags and never exits
// the process.
package weftbase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	httpapi "github.com/weftbase/weftbase/api/http"
	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/console"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/internal/dblock"
	"github.com/weftbase/weftbase/internal/reqlog"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/clickhouse"
	obsqlite "github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/queue"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
	"github.com/weftbase/weftbase/worker/hub"
)

// DefaultDataDir is the data directory when DataDir is not given.
const DefaultDataDir = ".weftbase"

// minRetention is Retain's floor.
const minRetention = time.Hour

// ErrLocked is New's error when another process holds a SQLite store
// of this data dir.
var ErrLocked = errors.New("weftbase: another server holds this data dir")

// App is one node: its stores, its wiring and its roles.
type App struct {
	cfg   config
	log   *slog.Logger
	roles roleSet

	control    *sqlite.DB
	traces     obsdb.DB
	ownsTraces bool
	locks      []*dblock.Lock
	lockPaths  []string

	events   *events.Log
	keys     *keys.Service
	sessions *authz.Sessions
	auth     *authz.Authenticator
	api      *v1.Server
	router   *httpapi.Router
	registry *registry.Registry
	queue    *queue.Queue
	ledger   *sessions.Ledger
	hub      *hub.Hub
	locals   map[string]*localBuild // by digest

	mu       sync.Mutex
	url      string // where the node is served (Serving)
	buildKey string // the ingest key local builds report with, minted on first use
	cancel   context.CancelFunc
	done     chan struct{}
	shutdown bool
}

// New opens the stores and wires the node. It fails on a store it
// cannot open, a data dir another server holds (ErrLocked), a
// postgres:// control store (not supported yet), a retention under an
// hour, or a bootstrap key that does not import.
func New(opts ...Option) (_ *App, err error) {
	c := config{console: true, base: "/"}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if len(c.roles) == 0 {
		c.roles = AllRoles
	}
	if c.retain < 0 || (c.retain > 0 && c.retain < minRetention) {
		return nil, errors.New("weftbase: retention must be at least 1h: a run silent for 30 s counts as interrupted, so a short retention would sweep a run paused at a breakpoint")
	}
	if c.dataDir == "" {
		c.dataDir = DefaultDataDir
	}
	a := &App{cfg: c, log: c.logger, roles: c.roles}
	if a.log == nil {
		a.log = slog.Default()
	}
	defer func() {
		if err != nil {
			_ = a.closeStores()
		}
	}()
	controlPath, err := a.controlPath()
	if err != nil {
		return nil, err
	}
	tracePath, traceCH, err := a.tracePath()
	if err != nil {
		return nil, err
	}
	for _, p := range []string{controlPath, tracePath} {
		if p == "" || p == ":memory:" {
			continue
		}
		if err := a.lock(p); err != nil {
			return nil, err
		}
	}
	ctx := context.Background()
	if a.control, err = sqlite.Open(ctx, controlPath); err != nil {
		return nil, fmt.Errorf("weftbase: %w", err)
	}
	switch {
	case c.traces != nil:
		a.traces = c.traces
	case traceCH:
		if a.traces, err = clickhouse.Open(c.traceDSN); err != nil {
			return nil, fmt.Errorf("weftbase: trace database: %w", err)
		}
		a.ownsTraces = true
	default:
		if a.traces, err = obsqlite.Open(tracePath); err != nil {
			return nil, fmt.Errorf("weftbase: trace database %s: %w", tracePath, err)
		}
		a.ownsTraces = true
	}

	a.events = events.New(a.control)
	a.keys = keys.New(a.control, a.events)
	a.sessions = authz.NewSessions(a.control)
	a.sessions.Audit(a.events)
	a.registry = registry.New(a.control, a.events)
	a.queue = queue.New(a.control)
	a.ledger = sessions.New(a.control, a.events, a.registry, a.queue, sessions.WithPolicy(c.policy))
	a.hub = hub.New(hub.Config{
		DB: a.control, Events: a.events, Queue: a.queue, Registry: a.registry, Sink: a.ledger,
		LeaseTTL: c.leaseTTL, Silence: c.silence, Logger: a.log,
	})
	a.ledger.SetCanceler(a.hub)
	a.ledger.SetSteerer(a.hub)
	a.locals = map[string]*localBuild{}
	if a.roles.has(RoleSupervisor) {
		if err := a.restoreBuilds(); err != nil {
			return nil, err
		}
	}
	var authOpts []authz.Option
	if c.open {
		authOpts = append(authOpts, authz.Open())
	}
	a.auth = authz.NewAuthenticator(a.keys, a.sessions, authOpts...)
	if c.bootstrapKey != "" {
		if _, err := a.keys.Import(ctx, tenant.Default(), "bootstrap", c.bootstrapKey, events.System); err != nil {
			return nil, fmt.Errorf("weftbase: bootstrap key: %w", err)
		}
	}

	reqs := reqlog.New()
	var caps []string
	if a.roles.has(RoleIngest) {
		caps = append(caps, "ingest")
	}
	if traceCH || strings.Contains(fmt.Sprintf("%T", a.traces), "clickhouse") {
		caps = append(caps, "clickhouse")
	}
	v1opts := []v1.Option{
		v1.DB(a.traces), v1.Events(a.events), v1.Capabilities(caps...),
		v1.Retain(c.retain), v1.IngestCounts(reqs.Ingest),
		v1.Registry(a.registry), v1.Ledger(a.ledger),
	}
	if c.pricing != nil {
		v1opts = append(v1opts, v1.Pricing(*c.pricing))
	}
	if len(c.manifest) > 0 {
		v1opts = append(v1opts, v1.Manifest(c.manifest))
	}
	a.api = v1.New(v1opts...)

	rc := httpapi.Config{
		Auth: a.auth, AllowOrigins: c.origins, RequestLog: reqs, Logger: a.log, Ready: a.ready,
	}
	if a.roles.has(RoleAPI) {
		rc.Sessions = a.sessions
		rc.Routes = append(rc.Routes, toRoutes(a.api.Routes())...)
		if c.console {
			rc.Console = console.Handler(console.Options{Base: c.base, Title: c.title})
		}
	}
	if a.roles.has(RoleIngest) {
		rc.Routes = append(rc.Routes, toRoutes(a.api.IngestRoutes())...)
	}
	a.router = httpapi.New(rc)
	return a, nil
}

func toRoutes(in []v1.Route) []httpapi.Route {
	out := make([]httpapi.Route, 0, len(in))
	for _, r := range in {
		out = append(out, httpapi.Route{Method: r.Method, Pattern: r.Pattern, Action: r.Action, Handler: r.Handler})
	}
	return out
}

// controlPath resolves ControlDB to a SQLite path.
func (a *App) controlPath() (string, error) {
	dsn := a.cfg.controlDSN
	switch {
	case dsn == "":
		return a.inDataDir("control.db")
	case strings.HasPrefix(dsn, "sqlite://"):
		p := strings.TrimPrefix(dsn, "sqlite://")
		if p == "" {
			return "", errors.New("weftbase: --control-db sqlite:// needs a path")
		}
		return p, nil
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "", errors.New("weftbase: a postgres:// control store is not supported yet (Stage 1); use sqlite://path")
	}
	return "", errors.New("weftbase: the control store must be sqlite://path")
}

// tracePath resolves TraceDB: a SQLite path, or clickhouse.
func (a *App) tracePath() (path string, clickhouse bool, err error) {
	if a.cfg.traces != nil {
		return "", false, nil
	}
	dsn := a.cfg.traceDSN
	switch {
	case dsn == "":
		p, err := a.inDataDir("traces.db")
		return p, false, err
	case strings.HasPrefix(dsn, "sqlite://"):
		p := strings.TrimPrefix(dsn, "sqlite://")
		if p == "" {
			return "", false, errors.New("weftbase: --trace-db sqlite:// needs a path")
		}
		return p, false, nil
	case strings.HasPrefix(dsn, "clickhouse://"):
		return "", true, nil
	}
	return "", false, errors.New("weftbase: the trace store must be sqlite://path or clickhouse://user:pass@host:9000/db")
}

func (a *App) inDataDir(name string) (string, error) {
	if err := os.MkdirAll(a.cfg.dataDir, 0o700); err != nil {
		return "", fmt.Errorf("weftbase: data dir: %w", err)
	}
	return filepath.Join(a.cfg.dataDir, name), nil
}

// lock takes the OS lock beside a SQLite file: one server per file.
func (a *App) lock(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("weftbase: %w", err)
		}
	}
	l, err := dblock.Acquire(path)
	if err != nil {
		if errors.Is(err, dblock.ErrHeld) {
			if h, ok := dblock.ReadHolder(path); ok {
				return fmt.Errorf("%w: server running at %s (pid %d) on %s", ErrLocked, h.URL, h.PID, path)
			}
			return fmt.Errorf("%w: another server holds %s", ErrLocked, path)
		}
		return fmt.Errorf("weftbase: lock %s: %w", path, err)
	}
	a.locks = append(a.locks, l)
	a.lockPaths = append(a.lockPaths, path)
	return nil
}

// Serving tells the node the base URL it is served at (no trailing
// slash; under a prefix, with it). Call it once the listener is up,
// before Local: local builds are handed that URL for their telemetry,
// and the store locks record it, so a second start names who holds
// them (best effort).
func (a *App) Serving(url string) {
	a.mu.Lock()
	a.url = strings.TrimRight(url, "/")
	a.mu.Unlock()
	for _, l := range a.locks {
		l.SetHolder(dblock.Holder{URL: url, PID: os.Getpid()})
	}
}

// Handler serves the node per its roles: /v1 (REST and SSE), the
// Console at / (api); /otlp/v1/* (ingest); /healthz, /readyz and
// /metrics always. Mount it under a prefix with http.StripPrefix and
// Base.
func (a *App) Handler() http.Handler { return a.router }

// Routes is the route table the handler serves.
func (a *App) Routes() []httpapi.Route { return a.router.Routes() }

// Run runs the node's background work until ctx ends or Shutdown: the
// outbox hub's pump (api: the /v1/events subscribers); the worker hub's
// claim loop, which dispatches queued runs to the attached executors,
// the timer loop (overdue timers first: the queue time-to-live, approval
// expiry, maximum pin age) and the retention sweeper (scheduler). It
// returns nil when stopped.
func (a *App) Run(ctx context.Context) error {
	a.mu.Lock()
	if a.shutdown {
		a.mu.Unlock()
		return errors.New("weftbase: Run after Shutdown")
	}
	if a.cancel != nil {
		a.mu.Unlock()
		return errors.New("weftbase: Run called twice")
	}
	ctx, cancel := context.WithCancel(ctx)
	a.cancel, a.done = cancel, make(chan struct{})
	done := a.done
	a.mu.Unlock()
	defer close(done)
	defer cancel()

	var wg sync.WaitGroup
	if a.roles.has(RoleAPI) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.events.Hub().Run(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("weftbase: the outbox hub stopped", "err", err)
			}
		}()
	}
	if a.roles.has(RoleScheduler) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.hub.Run(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("weftbase: the worker hub stopped", "err", err)
			}
		}()
	}
	if a.roles.has(RoleScheduler) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.queue.RunTimers(ctx, a.cfg.timerPoll, a.ledger.OnTimer, a.log)
		}()
	}
	if a.roles.has(RoleScheduler) && a.cfg.retain > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.sweep(ctx)
		}()
	}
	wg.Wait()
	return nil
}

// sweepInterval is the retention timer: min(d/10, 1h), at least a
// minute.
func sweepInterval(d time.Duration) time.Duration {
	return max(min(d/10, time.Hour), time.Minute)
}

// sweep deletes, every interval, the runs that are not running and
// were last seen longer than the retention ago (obsdb.DB.Sweep), from
// the trace store only. A failed sweep is one Warn line; a closed
// store stops the sweeper.
func (a *App) sweep(ctx context.Context) {
	iv := a.cfg.sweepEvery
	if iv <= 0 {
		iv = sweepInterval(a.cfg.retain)
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		cutoff := time.Now().Add(-a.cfg.retain)
		n, err := a.traces.Sweep(ctx, cutoff)
		switch {
		case errors.Is(err, obsdb.ErrClosed):
			a.log.Debug("weftbase: sweeper stopped: trace database closed")
			return
		case err != nil && ctx.Err() == nil:
			a.log.Warn("weftbase: retention sweep failed", "retention", a.cfg.retain.String(), "err", err)
		case err == nil:
			a.log.Debug("weftbase: retention sweep", "deleted_runs", n, "retention", a.cfg.retain.String(), "cutoff", cutoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Ready reports whether the node can serve: its stores answer.
func (a *App) Ready() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.ready(ctx)
}

func (a *App) ready(ctx context.Context) error {
	a.mu.Lock()
	down := a.shutdown
	a.mu.Unlock()
	if down {
		return errors.New("shutting down")
	}
	if err := a.control.Ping(ctx); err != nil {
		return fmt.Errorf("control store: %w", err)
	}
	if _, err := a.traces.Runs(ctx, obsdb.RunQuery{Limit: 1}); err != nil {
		return fmt.Errorf("trace store: %w", err)
	}
	return nil
}

// Shutdown stops Run, closes the stores the node opened and releases
// the data-dir locks. Serving connections are the caller's
// http.Server's to drain first.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if a.shutdown {
		a.mu.Unlock()
		return nil
	}
	a.shutdown = true
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Drain the executors: running runs get drainTimeout to finish,
	// then end canceled (cause shutdown); their ends are recorded
	// before the stores close.
	herr := a.hub.Close(ctx, time.Now().Add(a.cfg.drainTimeout()))
	a.closeLocals()
	return errors.Join(herr, a.closeStores())
}

func (a *App) closeStores() error {
	var errs []error
	if a.ownsTraces && a.traces != nil {
		errs = append(errs, a.traces.Close())
		a.traces = nil
	}
	if a.control != nil {
		errs = append(errs, a.control.Close())
		a.control = nil
	}
	for _, l := range a.locks {
		l.Release()
	}
	a.locks = nil
	return errors.Join(errs...)
}

// Keys is the API key service (mint, list, revoke).
func (a *App) Keys() *keys.Service { return a.keys }

// Sessions is the Console sign-in store (CreateLink prints a link).
func (a *App) Sessions() *authz.Sessions { return a.sessions }

// Events is the control store's outbox.
func (a *App) Events() *events.Log { return a.events }

// Traces is the trace store.
func (a *App) Traces() obsdb.DB { return a.traces }

// DataDir is the data directory.
func (a *App) DataDir() string { return a.cfg.dataDir }

// LockPaths are the SQLite files this node holds the lock of.
func (a *App) LockPaths() []string { return append([]string(nil), a.lockPaths...) }

// HasRole reports whether the node runs r.
func (a *App) HasRole(r Role) bool { return a.roles.has(r) }
