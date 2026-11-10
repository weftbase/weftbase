package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/internal/dblock"
	"github.com/weftbase/weftbase/internal/listen"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// A node is the kernel (package weftbase) behind the port policy, as
// `weftbase serve` and `weftbase dev` run it: the stores under a data
// dir (or the DSNs given), the HTTP server on the chosen address, the
// kernel's background work (Run), and — for dev — the bootstrap key,
// the discovery file and the Console's one-time sign-in link.

// nodeFlags are the flags serve and dev share.
type nodeFlags struct {
	addr, data, controlDB, traceDB, manifest, retain, pricing *string
	origins                                                   []string
	builds                                                    []string
	logs                                                      logFlags
}

// addNodeFlags adds the shared flags to fs.
func addNodeFlags(fs *flag.FlagSet) *nodeFlags {
	f := &nodeFlags{}
	f.addr = fs.String("addr", "",
		"listen `address` (default: $WEFTBASE_ADDR, else "+defaultAddr+"); --addr pins; without it weftbase takes the next free port in 7331–7340")
	f.data = fs.String("data", "",
		"data `dir`: control.db, traces.db and (dev) bootstrap.key (default: $WEFTBASE_DATA, else ./"+weftbase.DefaultDataDir+")")
	f.controlDB = fs.String("control-db", "",
		"the control store, `sqlite://path` (default: <data>/control.db; postgres:// is not supported yet)")
	f.traceDB = fs.String("trace-db", "",
		"the trace store, `sqlite://path` or `clickhouse://user:pass@host:9000/db` (default: <data>/traces.db)")
	f.manifest = fs.String("manifest", "",
		"`path` to the app's weft.json, served at /v1/manifest and checked against the latest runs (default: $WEFT_MANIFEST, else the nearest weft.json from the working directory upward)")
	f.retain = retainFlag(fs, "to keep runs after they were last seen: older ones that are not running are deleted from the trace store")
	f.pricing = pricingFlag(fs)
	fs.Func("allow-origin", "an `origin` (scheme://host:port) allowed by CORS, repeatable (default: none, no CORS headers are sent; never for a request without a credential)",
		func(s string) error { f.origins = append(f.origins, s); return nil })
	fs.Func("build", "a build's executable `path` to run as a local child (the supervisor role), repeatable: it serves its agents with client/worker (worker.Serve(worker.FromEnv(), …)); a newer build of the same agents retires the older one",
		func(s string) error { f.builds = append(f.builds, s); return nil })
	f.logs = addLogFlags(fs)
	return f
}

// dataDir is --data, else WEFTBASE_DATA, else ./.weftbase.
func (f *nodeFlags) dataDir() string {
	if *f.data != "" {
		return *f.data
	}
	if d := os.Getenv("WEFTBASE_DATA"); d != "" {
		return d
	}
	return weftbase.DefaultDataDir
}

// storePath is the absolute SQLite file a store DSN names under dir
// ("" for a store with no file: ClickHouse, ":memory:"). A malformed
// DSN is the kernel's error to report.
func storePath(dsn, dir, name string) string {
	var p string
	switch {
	case dsn == "":
		p = filepath.Join(dir, name)
	case strings.HasPrefix(dsn, "sqlite://"):
		p = strings.TrimPrefix(dsn, "sqlite://")
	}
	if p == "" || p == ":memory:" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// resolved is a node's configuration once the flags are read.
type resolved struct {
	data        string
	controlPath string // the control store's file ("" for none)
	tracePath   string // the trace store's file ("" for ClickHouse)
	traceLabel  string
	opts        []weftbase.Option
	notes       []string
	builds      []string // --build paths
}

// resolve reads the flags into kernel options and the banner's notes.
func (f *nodeFlags) resolve() (resolved, error) {
	r := resolved{data: f.dataDir()}
	r.controlPath = storePath(*f.controlDB, r.data, "control.db")
	r.tracePath = storePath(*f.traceDB, r.data, "traces.db")
	r.traceLabel = r.tracePath
	if r.tracePath == "" {
		r.traceLabel = maskDSN(*f.traceDB)
	}
	r.opts = []weftbase.Option{weftbase.DataDir(r.data)}
	r.builds = f.builds
	if *f.controlDB != "" {
		r.opts = append(r.opts, weftbase.ControlDB(*f.controlDB))
	}
	if *f.traceDB != "" {
		r.opts = append(r.opts, weftbase.TraceDB(*f.traceDB))
	}
	if len(f.origins) > 0 {
		r.opts = append(r.opts, weftbase.AllowOrigins(f.origins...))
	}
	wd, err := os.Getwd()
	if err != nil {
		return r, err
	}
	for _, fn := range []func() ([]weftbase.Option, string, error){
		func() ([]weftbase.Option, string, error) { return manifestOptions(*f.manifest, wd) },
		func() ([]weftbase.Option, string, error) { return retainOptions(*f.retain) },
		func() ([]weftbase.Option, string, error) { return pricingOptions(*f.pricing) },
	} {
		o, note, err := fn()
		if err != nil {
			return r, err
		}
		r.opts = append(r.opts, o...)
		r.notes = append(r.notes, note)
	}
	return r, nil
}

// bootstrapKeyFile is where `weftbase dev` keeps its bootstrap key.
const bootstrapKeyFile = "bootstrap.key"

// bootstrapKey reads <data>/bootstrap.key, creating it (0600, a fresh
// wb_sk_test_ key) when missing. created says it was made now.
func bootstrapKey(data string) (key, path string, created bool, err error) {
	path = filepath.Join(data, bootstrapKeyFile)
	if err := os.MkdirAll(data, 0o700); err != nil {
		return "", path, false, err
	}
	discovery.GuardDir(data)
	if b, err := os.ReadFile(path); err == nil {
		key = strings.TrimSpace(string(b))
		if k, env, ok := keys.Parse(key); !ok || k != keys.Secret || env != tenant.DefaultEnv {
			return "", path, false, fmt.Errorf("%s: not a wb_sk_test_ key; delete it and start again for a new one", path)
		}
		return key, path, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", path, false, err
	}
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", path, false, err
	}
	key = "wb_sk_test_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another start made it first: read theirs.
		return bootstrapKey(data)
	}
	if err != nil {
		return "", path, false, err
	}
	if _, err := fh.WriteString(key + "\n"); err != nil {
		_ = fh.Close()
		return "", path, false, err
	}
	return key, path, true, fh.Close()
}

// nodeConfig is what startNode starts.
type nodeConfig struct {
	want       want
	res        resolved
	opts       []weftbase.Option // beyond res.opts: roles, console, the key
	key        string            // the key the probe sends and the app gets ("" none)
	keyPath    string            // where dev keeps it
	keyCreated bool              // dev made it on this start
	reuse      bool              // dev's loop: a server already on this data dir is reused
	signIn     bool              // print a one-time Console sign-in link
	disc       bool              // write the discovery file
	console    bool
	out        io.Writer
}

// node is a running (or reused) server.
type node struct {
	addr, url, key string
	signIn         string // the one-time sign-in link ("" none)
	notes          string // lines to print after the banner (the discovery file's)
	reused         bool
	pid            int
	stop           func(os.Signal) // stops a server this process started; a no-op on a reused one
	done           <-chan struct{} // closed when the HTTP server ends (nil when reused)
	err            func() error    // why it ended, once done is closed
}

// startNode applies the port policy, builds the kernel and serves it in
// the background. A server already serving this data dir is reused
// (cfg.reuse: dev's loop) or refused.
func startNode(cfg nodeConfig) (*node, error) {
	reused := func(addr string, pid int) *node {
		dial := loopbackAddr(addr)
		_, _ = fmt.Fprintln(cfg.out, listen.Choice{Addr: dial, Reuse: true, PID: pid}.ReuseLine())
		reuseNotes(cfg.out, dial)
		return &node{addr: dial, url: "http://" + dial, key: cfg.key, reused: true, pid: pid, stop: func(os.Signal) {}}
	}
	if cfg.reuse {
		if addr, pid, ok := dataHolder(cfg.res); ok {
			return reused(addr, pid), nil
		}
	}
	choice, err := listen.Choose(context.Background(), listen.Request{
		Addr: cfg.want.addr, Pinned: cfg.want.pinned, Span: cfg.want.span,
		DBPath: cfg.res.tracePath, Token: cfg.key, Known: knownServer(cfg.res.tracePath),
	})
	if err != nil {
		return nil, err
	}
	if choice.Reuse {
		if cfg.reuse {
			return reused(choice.Addr, choice.PID), nil
		}
		return nil, alreadyRunning(choice.Addr, choice.PID, cfg.res.data)
	}
	if choice.Note != "" {
		_, _ = fmt.Fprintln(cfg.out, "weftbase: "+choice.Note)
	}
	app, err := weftbase.New(append(append([]weftbase.Option{}, cfg.res.opts...), cfg.opts...)...)
	if err != nil {
		_ = choice.Listener.Close()
		if errors.Is(err, weftbase.ErrLocked) {
			if addr, pid, ok := dataHolder(cfg.res); ok {
				if cfg.reuse {
					return reused(addr, pid), nil
				}
				return nil, alreadyRunning(addr, pid, cfg.res.data)
			}
			return nil, fmt.Errorf("%w: one server per data dir — stop it first, or start this one on another --data", err)
		}
		return nil, err
	}
	// The app and the banner dial loopback: a server bound to every
	// interface (--addr 0.0.0.0:7331) is reachable there.
	dial := loopbackAddr(choice.Addr)
	n := &node{addr: dial, url: "http://" + dial, key: cfg.key}
	app.Serving(n.url)
	var buildNotes []string
	for _, b := range cfg.res.builds {
		vs, err := app.Local(b)
		if err != nil {
			_ = choice.Listener.Close()
			_ = app.Shutdown(context.Background())
			return nil, fmt.Errorf("--build %s: %w", b, err)
		}
		var names []string
		for _, v := range vs {
			names = append(names, fmt.Sprintf("%s %s (#%d)", v.Agent, v.ID, v.Number))
		}
		buildNotes = append(buildNotes, fmt.Sprintf("weftbase: build %s (%s): %s", b, shortDigest(vs), strings.Join(names, ", ")))
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = app.Run(runCtx)
	}()
	if cfg.signIn && app.HasRole(weftbase.RoleAPI) && cfg.console {
		if tok, err := app.Sessions().CreateLink(context.Background(), tenant.Default()); err == nil {
			n.signIn = n.url + "/#signin=" + url.QueryEscape(tok)
		}
	}
	var disc *discovery.Written
	if cfg.disc {
		// Said after the banner (runNode, the dev loop): the caller
		// prints n.notes.
		var b strings.Builder
		disc = announce(&b, n.url, cfg.key, cfg.res.tracePath)
		n.notes = strings.TrimSuffix(b.String(), "\n")
	}
	if len(buildNotes) > 0 {
		n.notes = strings.TrimPrefix(n.notes+"\n"+strings.Join(buildNotes, "\n"), "\n")
	}
	stopFresh := keepFresh(disc)
	stopCh := make(chan os.Signal, 1)
	finished := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = serveUntil(httpServer(choice.Addr, app.Handler()), choice.Listener, cfg.out, stopCh)
		close(finished)
	}()
	n.done = finished
	n.err = func() error { return serveErr }
	var once sync.Once
	n.stop = func(sig os.Signal) {
		once.Do(func() {
			select {
			case <-finished:
			default:
				stopCh <- sig
				<-finished
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				_, _ = fmt.Fprintln(cfg.out, "weftbase: server:", serveErr)
			}
			cancelRun()
			<-runDone
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := app.Shutdown(ctx); err != nil {
				_, _ = fmt.Fprintln(cfg.out, "weftbase: shutdown:", err)
			}
			stopFresh()
			disc.Remove()
		})
	}
	return n, nil
}

// dataHolder is the server holding this data dir's stores, from their
// lock files (dblock.ReadHolder's trust rules: this user's 0600 file, a
// loopback URL, a live pid).
func dataHolder(r resolved) (addr string, pid int, ok bool) {
	for _, p := range []string{r.controlPath, r.tracePath} {
		if p == "" {
			continue
		}
		if h, ok := dblock.ReadHolder(p); ok {
			if u, err := url.Parse(h.URL); err == nil && u.Host != "" {
				return u.Host, h.PID, true
			}
		}
	}
	return "", 0, false
}

// alreadyRunning is the refusal of a second server on one data dir.
func alreadyRunning(addr string, pid int, data string) error {
	p := "pid unknown"
	if pid > 0 {
		p = fmt.Sprintf("pid %d", pid)
	}
	return fmt.Errorf("weftbase already running at http://%s (%s) on %s: one server per data dir — use it, or stop it first",
		loopbackAddr(addr), p, data)
}

// loopbackAddr is addr with an unspecified host (0.0.0.0, ::, empty)
// replaced by 127.0.0.1: where a client on this machine dials it.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// runNode is `weftbase serve` and `weftbase dev` without "--": the node
// in the foreground until SIGINT, SIGTERM or (unix) SIGHUP.
func runNode(cfg nodeConfig, open bool, banner func(*node)) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, devSignals...)
	defer signal.Stop(stop)
	n, err := startNode(cfg)
	if err != nil {
		return err
	}
	if n.reused {
		return alreadyRunning(n.addr, n.pid, cfg.res.data)
	}
	banner(n)
	if n.notes != "" {
		_, _ = fmt.Fprintln(cfg.out, n.notes)
	}
	if n.signIn != "" {
		if open {
			openBrowser(cfg.out, n.signIn)
		} else {
			_, _ = fmt.Fprintf(cfg.out, "weftbase: sign in to the Console: %s (one use, %s)\n", n.signIn, signInTTL)
		}
	}
	select {
	case sig := <-stop:
		n.stop(sig)
	case <-n.done:
		n.stop(nil)
		if err := n.err(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// signInTTL is how long a printed sign-in link stays valid (authz's
// LinkTTL), for the banner.
const signInTTL = "15 min"

// manifestOptions resolves the manifest: --manifest, then
// WEFT_MANIFEST, else the nearest weft.json from dir upward. The file
// is read once, at start; note is the one line that says which file
// was loaded, or that none was found. A named file that cannot be read
// is a start error, never a server silently serving without the
// manifest it was given.
func manifestOptions(flagPath, dir string) (opts []weftbase.Option, note string, err error) {
	path, from := flagPath, "--manifest"
	if path == "" {
		path, from = os.Getenv("WEFT_MANIFEST"), "WEFT_MANIFEST"
	}
	if path == "" {
		path, from = findUpward(dir, "weft.json"), "found upward"
		if path == "" {
			return nil, "weftbase: no weft.json from " + dir + " upward; no manifest (--manifest names one)", nil
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if from == "found upward" {
			return nil, "", fmt.Errorf("manifest %s: %w", path, err)
		}
		return nil, "", fmt.Errorf("%s: %w", from, err)
	}
	// The top-level shape weft.Manifest writes: an object with the
	// format version and the agents. A file that is not is a start
	// error, never served verbatim.
	var shape struct {
		Weft   int               `json:"weft"`
		Agents []json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(b, &shape); err != nil {
		return nil, "", fmt.Errorf("manifest %s: does not parse: %w", path, err)
	}
	return []weftbase.Option{weftbase.Manifest(b)}, "weftbase: manifest " + path + " (" + from + ")", nil
}

// findUpward is the nearest regular file called name in dir or one of
// its parents, "" when there is none up to the filesystem root.
func findUpward(dir, name string) string {
	for {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// nodeLogger is the kernel's logger: slog's default, which the log
// flags installed.
func nodeLogger() weftbase.Option { return weftbase.Logger(slog.Default()) }

// shortDigest is a build's digest, shortened for a banner line.
func shortDigest(vs []registry.Version) string {
	if len(vs) == 0 {
		return "no agents"
	}
	d := vs[0].BuildDigest
	if len(d) > len("sha256:")+12 {
		d = d[:len("sha256:")+12]
	}
	return d
}
