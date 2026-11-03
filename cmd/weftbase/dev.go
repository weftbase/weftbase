package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/weftbase/weftbase/internal/dblock"
	"github.com/weftbase/weftbase/internal/discovery"
	"github.com/weftbase/weftbase/internal/listen"
	"github.com/weftbase/weftbase/studio"
)

// `weftbase dev` (plan B1.2, FEATURES D7): the server and the app in
// one command, the app restarted on a .go save.
//
// Everything it does is something the app or the shell can do itself:
// it is the server alone (`weftbase dev` without "--": the same flags,
// the same port policy) plus the app run with four environment
// variables set — WEFT_ENV=dev, WEFTBASE_URL and WEFTBASE_TOKEN (the
// exporter's destination) and WEFTBASE_DB (the otel local sink's path:
// the same file the server serves).

const (
	// devStopGrace is how long a stopping app has between the polite
	// signal and SIGKILL.
	devStopGrace = 5 * time.Second
	// devDebounce coalesces a burst of saves (an editor's write +
	// rename, a gofmt pass, a branch switch) into one restart.
	devDebounce = 300 * time.Millisecond
)

// devDefaultAddr and devSpan are the port policy's unpinned start
// (defaultAddr, listen.DefaultSpan); variables so a test may move them
// to free ports.
var (
	devDefaultAddr = defaultAddr
	devSpan        = 0
)

// exitCode is a run outcome that exits with a given code and prints
// nothing more: `weftbase dev --no-watch` exits with the app's code.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// skipDirs are the directory names the watcher never descends into.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"testdata": true, ".weftbase": true, "dist": true,
}

// runDev is `weftbase dev [flags] -- command args…`: Studio and the app.
func runDev(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("dev", stderr)
	db := fs.String("db", "",
		"`sqlite://path` or `clickhouse://user:pass@host:9000/db` (default: $WEFTBASE_DB or ./.weftbase/weftbase.db)")
	addr := fs.String("addr", "",
		"listen `address` (default: $WEFTBASE_ADDR, else "+defaultAddr+"); --addr pins; without it weftbase reuses a running one on the same DB or takes the next free port in 7331–7340")
	token := fs.String("token", "",
		"API token (default: $WEFTBASE_TOKEN, else the database's stable token, <db>.token); the app receives it as WEFTBASE_TOKEN")
	manifest := fs.String("manifest", "",
		"`path` to the app's weft.json (default: $WEFT_MANIFEST, else the nearest weft.json from the working directory upward)")
	open := fs.Bool("open", stdoutIsTTY(),
		"open the browser on the UI, the token in the URL fragment (default: on when stdout is a terminal)")
	rotate := fs.Bool("rotate-token", false,
		"write a new stable dev token beside the database (<db>.token) before serving; an action, so no environment mirror")
	retain := retainFlag(fs, "to keep runs after they were last seen: older ones that are not running are deleted from weftbase's tables")
	pricing := pricingFlag(fs)
	logs := addLogFlags(fs)
	noWatch := fs.Bool("no-watch", false,
		"do not restart the app on a .go change; weftbase dev exits with the app's exit code")
	var roots []string
	fs.Func("watch", "`dir` to watch for .go changes, repeatable (default: the working directory)",
		func(s string) error { roots = append(roots, s); return nil })
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage: weftbase dev [flags] -- command args…   (without \"--\", weftbase dev serves alone)")
		_, _ = fmt.Fprintln(fs.Output(), "  Run a package (go run ., go run ./cmd/app) or a module build, never go run main.go:\n  a file list has no build info, so its manifest sources read main/… and its runs read stale against weft.json.")
		printDefaults(fs)
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	restoreLog, err := logs.install(stderr)
	if err != nil {
		return err
	}
	defer restoreLog()
	argv := fs.Args()
	if len(argv) == 0 {
		return usageError(`dev's loop runs a command: weftbase dev [flags] -- command args… (go run ., go run ./cmd/app)`)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		roots = []string{wd}
	}
	if !*noWatch {
		for _, r := range roots {
			if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
				return usageError("--watch %s: not a directory", r)
			}
		}
	}
	opts, _, err := manifestOptions(*manifest, wd)
	if err != nil {
		return err
	}
	retainOpts, _, err := retainOptions(*retain)
	if err != nil {
		return err
	}
	opts = append(opts, retainOpts...)
	pricingOpts, _, err := pricingOptions(*pricing)
	if err != nil {
		return err
	}
	opts = append(opts, pricingOpts...)

	// Signals are ours from here on: Ctrl-C stops the app first, then
	// the server. Armed before anything starts, so none is lost.
	// SIGHUP (the terminal closed) is one of them on unix: its default
	// action would end weftbase dev without stopping the app.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, devSignals...)
	defer signal.Stop(sigs)

	out := &lockedWriter{w: stdout}
	w := wantAddr(*addr, devDefaultAddr)
	w.span = devSpan
	st, err := devStudio(*db, w, *token, *rotate, out, opts...)
	if err != nil {
		return err
	}
	stopSig := os.Signal(syscall.SIGTERM)
	defer func() { st.stop(stopSig) }()

	if *open {
		openBrowser(out, uiLink(st.addr, st.token))
	} else if line := tokenLine(st, fixedToken(*token) != ""); line != "" {
		_, _ = fmt.Fprintln(out, line)
	}

	var changes <-chan string
	if !*noWatch {
		ch, closeWatch, err := watchGo(roots, devDebounce, out)
		if err != nil {
			return err
		}
		defer closeWatch()
		changes = ch
	}
	d := &devLoop{
		argv:    argv,
		env:     devEnv(os.Environ(), st.url, st.token, st.dbPath),
		stdout:  stdout,
		stderr:  stderr,
		out:     out,
		link:    st.link,
		noWatch: *noWatch,
	}
	err = d.run(sigs, changes)
	if d.stopSig != nil {
		stopSig = d.stopSig
	}
	return err
}

// devEnv is the app's environment: the parent's, plus WEFT_ENV=dev
// (kept when already set to something), WEFTBASE_URL,
// WEFTBASE_TOKEN and WEFTBASE_DB (when the server serves a file) —
// those three override what the shell had, so the app and the server
// agree.
func devEnv(parent []string, url, token, dbPath string) []string {
	set := map[string]string{
		"WEFTBASE_URL":   url,
		"WEFTBASE_TOKEN": token,
	}
	if dbPath != "" {
		set["WEFTBASE_DB"] = dbPath
	}
	envSet := false
	var env []string
	for _, kv := range parent {
		k, v, _ := strings.Cut(kv, "=")
		if _, ours := set[k]; ours {
			continue
		}
		if k == "WEFT_ENV" {
			if v == "" {
				continue
			}
			envSet = true
		}
		env = append(env, kv)
	}
	if !envSet {
		env = append(env, "WEFT_ENV=dev")
	}
	for _, k := range []string{"WEFTBASE_URL", "WEFTBASE_TOKEN", "WEFTBASE_DB"} {
		if v, ok := set[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// devStudioRun is the Studio `weftbase dev` talks to: one it started in
// this process, or a running one it reuses.
type devStudioRun struct {
	addr, url, link, token, dbPath string
	stop                           func(os.Signal) // stops a Studio this process started
}

// devStudio applies `weftbase dev`'s port policy and starts Studio in
// this process, or reuses one already serving the same database. The
// probe carries the fixed token (--token, WEFTBASE_TOKEN), else the
// database's stable token (plan B3) to an address this user's
// discovery file names: a Studio on the same database serves exactly
// that, so a reuse hands the app the token it needs. A
// running Studio walled by another token answers 401 and is skipped
// for the next port. A Studio this process starts writes the discovery
// file; a reused one owns its own.
func devStudio(dbFlag string, w want, tokenFlag string, rotate bool, out io.Writer, opts ...studio.Option) (*devStudioRun, error) {
	dbPath, err := dbFile(dbFlag)
	if err != nil {
		return nil, err
	}
	choose := func() (listen.Choice, error) {
		return listen.Choose(context.Background(), listen.Request{
			Addr: w.addr, Pinned: w.pinned, Span: w.span, DBPath: dbPath,
			Token: fixedToken(tokenFlag), TokenFor: stableProbe(dbPath),
			Known: knownStudio(dbPath),
		})
	}
	reuse := func(addr string, pid int, tok string) *devStudioRun {
		// The app and the one line dial loopback.
		dial := loopbackAddr(addr)
		_, _ = fmt.Fprintln(out, listen.Choice{Addr: dial, Reuse: true, PID: pid}.ReuseLine())
		reuseNotes(out, dial, rotate)
		return &devStudioRun{addr: dial, url: "http://" + dial, dbPath: dbPath, token: tok, link: uiLink(dial, ""), stop: func(os.Signal) {}}
	}
	// The database's lock first, as weftbase dev takes it (B7-fix2):
	// held, the holder serves this file — reuse it when something
	// trusted names it (dblock.ReadHolder's rules: this user's 0600
	// file, a loopback IP, a live pid — it is handed the database's
	// token), else let the port probe look for it.
	lock, err := lockDB(dbPath)
	if errors.Is(err, errHeld) {
		if addr, pid, ok := dbHolder(dbPath); ok {
			tok := fixedToken(tokenFlag)
			if tok == "" {
				tok = readTokenFile(tokenPath(dbPath))
			}
			return reuse(addr, pid, tok), nil
		}
		choice, cerr := choose()
		if cerr != nil {
			return nil, cerr
		}
		if !choice.Reuse {
			_ = choice.Listener.Close()
			return nil, heldError(dbPath)
		}
		return reuse(choice.Addr, choice.PID, probeToken(dbPath, tokenFlag, loopbackAddr(choice.Addr))), nil
	}
	if err != nil {
		return nil, err
	}
	choice, err := choose()
	if err != nil {
		lock.Release()
		return nil, err
	}
	if choice.Reuse {
		// A Studio serving the file without its lock (a weft before it,
		// an app's embedded Studio): reused, as before the lock.
		lock.Release()
		return reuse(choice.Addr, choice.PID, probeToken(dbPath, tokenFlag, loopbackAddr(choice.Addr))), nil
	}
	// The app and the one line dial loopback: a Studio bound to every
	// interface (--addr 0.0.0.0:7331) is reachable there, and otel's
	// Studio destination refuses plaintext to a non-loopback host.
	dial := loopbackAddr(choice.Addr)
	r := &devStudioRun{addr: dial, url: "http://" + dial, dbPath: dbPath, stop: func(os.Signal) {}}
	if choice.Note != "" {
		_, _ = fmt.Fprintln(out, "studio: "+choice.Note)
	}
	// The banner is the one line's job here: serveBoot's goes nowhere.
	srv, err := serveBootWith(dbFlag, choice.Addr, tokenFlag, rotate, io.Discard, append(discoveryOption(), opts...)...)
	if err != nil {
		_ = choice.Listener.Close()
		lock.Release()
		return nil, err
	}
	// Named once it can serve (never a Studio that failed to boot).
	lock.SetHolder(dblock.Holder{URL: r.url, PID: os.Getpid()})
	r.token = srv.token
	// A fixed or stable token stays out of the log, link included
	// (B1.1's rule; the stable token is the signing key too); only one
	// generated for this process (a database with no file) is nobody's
	// secret yet and opens the UI.
	if srv.tok.printable() {
		r.link = uiLink(dial, srv.token)
	} else {
		r.link = uiLink(dial, "")
	}
	if srv.tok.rotated {
		_, _ = fmt.Fprintf(out, "studio: token rotated: a new one in %s\n", srv.tok.path)
	}
	var disc *discovery.Written
	if discovery.Off(os.Getenv) {
		_, _ = fmt.Fprintln(out, discoveryOffLine+"; the app gets WEFTBASE_URL regardless")
	} else if w, derr := writeDiscovery(r.url, srv.token, dbPath); derr != nil {
		_, _ = fmt.Fprintf(out, "studio: no discovery file (%v); the app gets WEFTBASE_URL regardless\n", derr)
	} else {
		disc = w
	}
	stopCh := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- serveUntil(httpServer(choice.Addr, srv.Handler()), choice.Listener, out, stopCh) }()
	stopFresh := keepFresh(disc)
	r.stop = func(sig os.Signal) {
		defer lock.Release()
		defer disc.Remove()
		defer stopFresh()
		stopCh <- sig
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(out, "weftbase dev: studio:", err)
		}
		_ = srv.Close()
	}
	return r, nil
}

// tokenLine says where the UI's token is (UX-D-25) when no browser
// opened with it and the link does not carry it: the database's stable
// token file (weftbase open --with-token prints the signed-in link), or the
// fixed token the flag or the environment set. "" when the link carries
// the token (one generated for this process) or there is none.
func tokenLine(st *devStudioRun, fixed bool) string {
	switch {
	case st.token == "" || strings.Contains(st.link, "#token="):
		return ""
	case fixed:
		return "studio: token: the one --token / WEFTBASE_TOKEN set"
	case st.dbPath != "" && readTokenFile(tokenPath(st.dbPath)) == st.token:
		return "studio: token: " + tokenPath(st.dbPath) + " (weftbase open --with-token)"
	case st.dbPath != "":
		// A reused Studio whose token is not the file's (its own
		// discovery file names it): never a file that would not sign in.
		return "studio: token: the running Studio's, not " + tokenPath(st.dbPath) + " (weftbase open --with-token)"
	}
	return ""
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

// devLoop runs the app and restarts it: one child at a time.
type devLoop struct {
	argv           []string
	env            []string
	stdout, stderr io.Writer     // the app's own output, passed through
	out            *lockedWriter // weftbase dev's lines
	link           string
	noWatch        bool
	stopSig        os.Signal // the signal that ended the loop, if one did
}

// run starts the app and serves the events until a signal (the app
// stops first, with the same signal; nil) or — under --no-watch — the
// app's own exit (its exit code).
func (d *devLoop) run(sigs <-chan os.Signal, changes <-chan string) error {
	// A Ctrl-C buffered while Studio started (the port probes, the
	// database) ends here: the app is never started only to be killed.
	select {
	case sig := <-sigs:
		d.stopSig = sig
		return nil
	default:
	}
	cur := d.start()
	if cur == nil && d.noWatch {
		// Nothing will ever restart it: the shell's "command not found".
		return exitCode(127)
	}
	for {
		var done <-chan struct{}
		if cur != nil {
			done = cur.done
		}
		select {
		case sig := <-sigs:
			d.stopSig = sig
			if cur != nil {
				cur.stop(appSignal(sig))
			}
			return nil
		case path := <-changes:
			d.say("%s changed; restarting the app", d.rel(path))
			if cur != nil {
				cur.stop(syscall.SIGTERM)
			}
			// A Ctrl-C during the stop's grace window ends here: no
			// fresh app started only to be killed.
			select {
			case sig := <-sigs:
				d.stopSig = sig
				return nil
			default:
			}
			cur = d.start()
		case <-done:
			cur.reap()
			code := cur.exitCode()
			if d.noWatch {
				d.say("app pid %d exited (%s)", cur.pid, cur.status())
				if code == 0 {
					return nil
				}
				if code < 0 {
					code = 1
				}
				return exitCode(code)
			}
			d.say("app pid %d exited (%s); a .go save restarts it", cur.pid, cur.status())
			cur = nil
		}
	}
}

// start starts the app and prints its one line. A start that fails (no
// such command) is said and waited out like an exit.
func (d *devLoop) start() *child {
	c, err := startChild(d.argv, d.env, d.stdout, d.stderr)
	if err != nil {
		d.say("could not start %q: %v", strings.Join(d.argv, " "), err)
		return nil
	}
	// The one line for the app:
	//
	//	studio <link> · app pid <n>
	_, _ = fmt.Fprintf(d.out, "studio %s · app pid %d\n", d.link, c.pid)
	return c
}

// say prints one "weftbase dev: …" line.
func (d *devLoop) say(format string, args ...any) {
	_, _ = fmt.Fprintf(d.out, "weftbase dev: "+format+"\n", args...)
}

// rel is path relative to the working directory when it is under it.
func (d *devLoop) rel(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if r, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return path
}

// lockedWriter serialises weftbase dev's own lines (the server and the
// loop write from two goroutines).
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
