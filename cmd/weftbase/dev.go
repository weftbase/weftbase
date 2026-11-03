package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// `weftbase dev` (plan B1.2, FEATURES D7): the server and the app in
// one command, the app restarted on a .go save.
//
// Everything it does is something the app or the shell can do itself:
// it is the server alone (`weftbase dev` without "--": the same flags,
// the same port policy) plus the app run with its environment set —
// WEFT_ENV=dev, WEFTBASE_URL and WEFTBASE_TOKEN (client/exporter's
// destination: the bootstrap key), and OTEL_EXPORTER_OTLP_ENDPOINT
// (<url>/otlp) with OTEL_EXPORTER_OTLP_HEADERS (the key as the bearer)
// for an app on any other OpenTelemetry SDK.

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

// runDev is `weftbase dev [flags] -- command args…`: server and the app.
func runDev(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("dev", stderr)
	nf := addNodeFlags(fs)
	open := fs.Bool("open", stdoutIsTTY(),
		"open the browser on the Console's one-time sign-in link (default: on when stdout is a terminal)")
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
	restoreLog, err := nf.logs.install(stderr)
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
	cfg, err := devConfig(nf, nil)
	if err != nil {
		return err
	}

	// Signals are ours from here on: Ctrl-C stops the app first, then
	// the server. Armed before anything starts, so none is lost.
	// SIGHUP (the terminal closed) is one of them on unix: its default
	// action would end weftbase dev without stopping the app.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, devSignals...)
	defer signal.Stop(sigs)

	out := &lockedWriter{w: stdout}
	cfg.out = out
	cfg.reuse = true
	st, err := startNode(cfg)
	if err != nil {
		return err
	}
	stopSig := os.Signal(syscall.SIGTERM)
	defer func() { st.stop(stopSig) }()
	if !st.reused {
		devBanner(out, st, cfg)
		if st.notes != "" {
			_, _ = fmt.Fprintln(out, st.notes)
		}
	}
	link := st.url + "/"
	if st.signIn != "" {
		if *open {
			openBrowser(out, st.signIn)
		} else {
			_, _ = fmt.Fprintf(out, "weftbase: sign in to the Console: %s (one use, %s)\n", st.signIn, signInTTL)
		}
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
		env:     devEnv(os.Environ(), st.url, st.key),
		stdout:  stdout,
		stderr:  stderr,
		out:     out,
		link:    link,
		noWatch: *noWatch,
	}
	err = d.run(sigs, changes)
	if d.stopSig != nil {
		stopSig = d.stopSig
	}
	return err
}

// devEnv is the app's environment: the parent's, plus WEFT_ENV=dev
// (kept when already set to something), WEFTBASE_URL, WEFTBASE_TOKEN
// (the bootstrap key), OTEL_EXPORTER_OTLP_ENDPOINT (<url>/otlp) and
// OTEL_EXPORTER_OTLP_HEADERS (the key as the bearer, percent-encoded as
// the OTel spec reads the variable) — those four override what the
// shell had, so the app and the server agree. weft's otel pipeline
// de-duplicates the OTLP destination against WEFTBASE_URL's by URL.
func devEnv(parent []string, url, key string) []string {
	ours := []string{"WEFTBASE_URL", "WEFTBASE_TOKEN", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS"}
	set := map[string]string{
		"WEFTBASE_URL":                url,
		"WEFTBASE_TOKEN":              key,
		"OTEL_EXPORTER_OTLP_ENDPOINT": url + "/otlp",
	}
	if key != "" {
		set["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer%20" + key
	}
	envSet := false
	var env []string
	for _, kv := range parent {
		k, v, _ := strings.Cut(kv, "=")
		if slices.Contains(ours, k) {
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
	for _, k := range ours {
		if v, ok := set[k]; ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
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
	// A Ctrl-C buffered while server started (the port probes, the
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
	//	server <link> · app pid <n>
	_, _ = fmt.Fprintf(d.out, "weftbase %s · app pid %d\n", d.link, c.pid)
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
