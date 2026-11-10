// Command weftbase is the platform's one binary: the server (the
// kernel, package weftbase, by role) and a terminal over its /v1 API,
// for scripts and CI.
//
//	go install github.com/weftbase/weftbase/cmd/weftbase@latest
//
//	weftbase serve  [--roles api,ingest,scheduler,supervisor] [--console] [node flags]
//	weftbase dev    [node flags] [--open]
//	weftbase dev    [node flags] [--open] [--no-watch] [--watch dir] -- command args…
//	weftbase runs   [--url] [--token] [--agent] [--since] [--failed] [--status] [--limit] [--json]
//	weftbase open   [run id | public id | session id | trace id] [--open]
//	weftbase signin [--origin url] [--open]
//	weftbase signout --all
//	weftbase export <run id> [--format json|jsonl|otlp]
//	weftbase doctor
//	weftbase mcp
//	weftbase vacuum [--db] [--retain]
//	weftbase version
//
// The node flags: --addr (WEFTBASE_ADDR), --data (WEFTBASE_DATA, default
// ./.weftbase: control.db, traces.db, and for dev bootstrap.key),
// --control-db sqlite://path (postgres:// is not supported yet),
// --trace-db sqlite://path | clickhouse://…, --manifest (WEFT_MANIFEST,
// else the nearest weft.json upward), --retain (WEFT_RETAIN, at least
// 1h), --pricing (WEFT_PRICING), --allow-origin (repeatable),
// --log-level / --log-format (WEFT_LOG_LEVEL, WEFT_LOG_FORMAT).
//
// # weftbase serve
//
// The kernel with the roles asked for (default all four) behind the
// port policy, until SIGINT, SIGTERM or (unix) SIGHUP. Every /v1 route
// needs an API key (Authorization: Bearer wb_…) or a Console session;
// serve keeps no key of its own: WEFTBASE_BOOTSTRAP_KEY, when set, is
// imported as one. With the api role and the Console on it prints a
// one-time Console sign-in link. It writes no discovery file.
//
// # weftbase dev
//
// serve with every role over SQLite in the data dir, plus what local
// development needs: a bootstrap secret key, wb_sk_test_…, created once
// in <data>/bootstrap.key (0600) and imported (never printed; the
// banner names the file); the discovery file (client/discovery,
// dev.json, 0600: url and the key) apps and the API commands find the
// server through; and a one-time Console sign-in link
// (http://127.0.0.1:7331/#signin=<token>, one use, 15 minutes), opened
// in the browser with --open (on when stdout is a terminal).
//
// The port policy (internal/listen): 127.0.0.1:7331 is the default;
// when busy, GET /v1/meta there (with the bootstrap key, loopback only)
// says whether it serves this data dir's traces — then a second dev is
// refused (or, with an app after "--", reuses it) — else the next free
// port in 7331–7340 is taken, said in one line. --addr pins. One server
// per data dir: the kernel holds the stores' locks (internal/dblock).
//
// `weftbase dev [flags] -- command args…` runs the app beside the
// server with WEFT_ENV=dev (kept when set), WEFTBASE_URL, WEFTBASE_TOKEN
// (the bootstrap key), OTEL_EXPORTER_OTLP_ENDPOINT=<url>/otlp and
// OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%20<key>, restarting it
// on a .go save (--watch dir, --no-watch: exit with the app's code).
//
// # The API commands
//
// runs, open, export, doctor and mcp share one resolver: the server is
// --url, else $WEFTBASE_URL, else the discovery file a running weftbase
// dev wrote (WEFTBASE_DISCOVERY=off skips it), else
// http://127.0.0.1:7331; the key --token, else $WEFTBASE_TOKEN, else the
// discovery file's when that file names the resolved server. They read
// GET /v1/runs, /v1/runs/{id} (ids percent-encoded),
// /v1/runs/{id}/export, /v1/public/{id}, /v1/sessions/{id},
// /v1/traces/{id}, /v1/meta and /readyz through client/rest.
//
// `weftbase vacuum [--db] [--retain]` sweeps (with a retention) and
// vacuums the trace store: --db (WEFTBASE_DB), default
// ./.weftbase/traces.db.
//
// Exit codes: 0 success, 1 a failure (printed as "weftbase: …" on stderr,
// except doctor, whose lines say it), 2 a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/rest"

	"github.com/weftbase/weft/version"
	"github.com/weftbase/weftbase/internal/doctor"
	"github.com/weftbase/weftbase/internal/listen"
	"github.com/weftbase/weftbase/internal/release"
)

const defaultAddr = listen.DefaultAddr

// defaultURL is the server the API clients talk to without --url or
// WEFTBASE_URL.
const defaultURL = "http://" + defaultAddr

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage marks a command-line mistake: exit 2, after the message.
var errUsage = errors.New("usage")

// usageError is a command-line mistake; run exits 2 on it.
func usageError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errUsage}, args...)...)
}

// gotArgs is a usage error's account of the arguments it was given, in
// words, never a Go slice (UX-S5a-11): "none", or each one quoted.
func gotArgs(args []string) string {
	if len(args) == 0 {
		return "none"
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return strings.Join(q, " ")
}

// printDefaults is fs.PrintDefaults with each flag spelled the way the
// usage lines and the docs spell it, --name (the flag package accepts
// both; UX-S5a-11).
func printDefaults(fs *flag.FlagSet) {
	var b strings.Builder
	out := fs.Output()
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(out)
	_, _ = io.WriteString(out, dashDash(b.String()))
}

// dashDash turns flag.PrintDefaults' "  -name" lines into "  --name".
func dashDash(s string) string {
	lines := strings.SplitAfter(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "  -") && !strings.HasPrefix(l, "  --") {
			lines[i] = "  --" + l[3:]
		}
	}
	return strings.Join(lines, "")
}

const usage = `weftbase — the weft platform's command line: the server and a terminal over its /v1 API

Usage:
  weftbase serve   [--roles api,ingest,scheduler,supervisor] [--console] [node flags]
  weftbase dev     [node flags] [--open]   (every role, SQLite in --data, a bootstrap key, a Console sign-in link)
  weftbase dev     [dev's flags] [--no-watch] [--watch dir] -- command args…   (server and your app: go run ., go run ./cmd/app)
  weftbase runs    [--url] [--token] [--agent] [--since] [--failed] [--status] [--limit] [--json]
  weftbase open    [id] [--url] [--token] [--open]   (a run, public, session or trace id; none: the server root)
  weftbase signin  [--url] [--token] [--origin url] [--open]   (a fresh one-time Console sign-in link from the running server)
  weftbase signout --all [--url] [--token]   (end every Console session and unused sign-in link)
  weftbase export  <run id> [--url] [--token] [--format json|jsonl|otlp]
  weftbase doctor  [--url] [--token]
  weftbase mcp     [--url] [--token]   (a read-only stdio MCP server over the /v1 API; weftbase mcp -h lists its tools)
  weftbase vacuum  [--db] [--retain]
  weftbase version

Node flags: [--addr] [--data] [--control-db] [--trace-db] [--manifest] [--retain] [--pricing] [--allow-origin] [--log-level] [--log-format]

Environment (each mirrors a flag): WEFTBASE_ADDR (--addr), WEFTBASE_DATA (--data),
WEFTBASE_TOKEN (--token: an API key), WEFT_MANIFEST (--manifest), WEFTBASE_URL (--url;
unset, the API commands read the discovery file a running weftbase dev wrote,
WEFTBASE_DISCOVERY=off skips it, else http://127.0.0.1:7331), WEFTBASE_DB (vacuum's --db),
WEFT_RETAIN (--retain; a Go duration of at least 1h, 0 = off), WEFT_PRICING (--pricing),
WEFT_LOG_LEVEL (--log-level: debug|info|warn|error), WEFT_LOG_FORMAT (--log-format: text|json).
WEFTBASE_BOOTSTRAP_KEY: a secret key serve imports.
Run "weftbase <command> -h" for a command's flags.
`

// run is the command line: it dispatches on the subcommand, prints a
// failure as one "weftbase: …" line on stderr and returns the exit code
// (0 ok, 1 failure, 2 usage). Split from main so every subcommand is
// testable.
func run(args []string, stdout, stderr io.Writer) int {
	err := dispatch(args, stdout, stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, new(exitCode)):
		// weftbase dev --no-watch: the app's own exit code, already said.
		var code exitCode
		errors.As(err, &code)
		return int(code)
	case errors.Is(err, doctor.ErrUnhealthy):
		// A failed doctor check has printed its own lines.
		return 1
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, "weftbase:", strings.TrimPrefix(err.Error(), "usage: "))
		return 2
	case errors.Is(err, errFlag):
		// The flag package printed the error and the command's flags.
		return 2
	default:
		_, _ = fmt.Fprintln(stderr, "weftbase:", err)
		return 1
	}
}

// errFlag marks a flag-parse failure the flag package already printed.
var errFlag = errors.New("bad flags")

// parse parses a subcommand's flags; a parse error (already printed by
// the flag package, with the flags) is errFlag, -h is flag.ErrHelp.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errFlag, err)
	}
	return nil
}

func dispatch(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return usageError("a command is required")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "runs":
		return runRuns(rest, stdout, stderr)
	case "open":
		return runOpen(rest, stdout, stderr)
	case "signin":
		return runSignIn(rest, stdout, stderr)
	case "signout":
		return runSignOut(rest, stdout, stderr)
	case "export":
		return runExport(rest, stdout, stderr)
	case "doctor":
		return runDoctor(rest, stdout, stderr)
	case "mcp":
		return runMCP(rest, stdout, stderr)
	case "vacuum":
		return runVacuum(rest, stdout, stderr)
	case "version", "--version", "-version":
		if len(rest) > 0 {
			return usageError("version takes no arguments (got %s)", gotArgs(rest))
		}
		_, err := fmt.Fprintf(stdout, "weftbase %s (weft %s)\n", release.Version, version.Runtime())
		return err
	case "dev":
		// Server alone, or server and the app after "--" (the dev loop,
		// whose own flags are --watch and --no-watch).
		if devLoopArgs(rest) {
			return runDev(rest, stdout, stderr)
		}
		return runDevServer(rest, stdout, stderr)
	case "serve":
		return runServe(rest, stdout, stderr)
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return usageError("unknown command %q", cmd)
	}
}

// devLoopArgs reports whether `weftbase dev`'s arguments ask for the dev
// loop (runDev): a "--" before the app's command, or one of the loop's
// own flags (--watch, --no-watch). Anything else is server alone
// (runServer).
func devLoopArgs(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return true
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && (name == "watch" || name == "no-watch") {
			return true
		}
	}
	return false
}

// newFlags is a subcommand's flag set: errors and -h go to stderr.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("weftbase "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage of weftbase %s:\n", name)
		printDefaults(fs)
	}
	return fs
}

// runServe is `weftbase serve`: the kernel with the roles asked for,
// behind the port policy, until a signal. It writes no discovery file
// and keeps no bootstrap key: callers bring keys (WEFTBASE_BOOTSTRAP_KEY,
// when set, is imported as one).
func runServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("serve", stderr)
	nf := addNodeFlags(fs)
	roles := fs.String("roles", "api,ingest,scheduler,supervisor", "the node's `roles`, comma-separated: api, ingest, scheduler, supervisor")
	console := fs.Bool("console", true, "serve the Console at / (the api role)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError("serve takes no arguments (got %s)", gotArgs(fs.Args()))
	}
	rs, err := weftbase.ParseRoles(*roles)
	if err != nil {
		return usageError("--roles: %v", err)
	}
	restoreLog, err := nf.logs.install(stderr)
	if err != nil {
		return err
	}
	defer restoreLog()
	res, err := nf.resolve()
	if err != nil {
		return err
	}
	opts := []weftbase.Option{weftbase.Roles(rs...), weftbase.Console(*console), nodeLogger()}
	key := os.Getenv("WEFTBASE_BOOTSTRAP_KEY")
	if key != "" {
		opts = append(opts, weftbase.BootstrapKey(key))
	}
	cfg := nodeConfig{want: wantAddr(*nf.addr, defaultAddr), res: res, opts: opts, key: key,
		signIn: true, console: *console, out: stdout}
	return runNode(cfg, false, func(n *node) {
		_, _ = fmt.Fprintf(stdout, "weftbase: http://%s/ (roles %s)\n", n.addr, rolesLabel(rs))
		printStores(stdout, res)
		if key != "" {
			_, _ = fmt.Fprintln(stdout, "weftbase: bootstrap key from WEFTBASE_BOOTSTRAP_KEY")
		}
		printNotes(stdout, res.notes)
	})
}

// runDevServer is `weftbase dev` with no app (no "--"): every role,
// SQLite under the data dir, the bootstrap key in <data>/bootstrap.key,
// the discovery file and a one-time Console sign-in link.
func runDevServer(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("dev", stderr)
	nf := addNodeFlags(fs)
	open := fs.Bool("open", stdoutIsTTY(),
		"open the browser on the Console's one-time sign-in link (default: on when stdout is a terminal)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage: weftbase dev [flags]   (the server alone; weftbase dev [flags] [--watch dir] [--no-watch] -- command args… also runs your app)")
		printDefaults(fs)
		_, _ = fmt.Fprint(fs.Output(), `With an app after "--" (the dev loop), also:
  --no-watch
    	do not restart the app on a .go change; weftbase dev exits with the app's exit code
  --watch dir
    	dir to watch for .go changes, repeatable (default: the working directory)
`)
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError("dev takes no arguments; run an app after \"--\" (got %s)", gotArgs(fs.Args()))
	}
	restoreLog, err := nf.logs.install(stderr)
	if err != nil {
		return err
	}
	defer restoreLog()
	cfg, err := devConfig(nf, stdout)
	if err != nil {
		return err
	}
	return runNode(cfg, *open, func(n *node) { devBanner(stdout, n, cfg) })
}

// devConfig is `weftbase dev`'s node: the shared flags, every role, the
// bootstrap key (created once), the discovery file and the sign-in link.
func devConfig(nf *nodeFlags, out io.Writer) (nodeConfig, error) {
	res, err := nf.resolve()
	if err != nil {
		return nodeConfig{}, err
	}
	key, keyPath, created, err := bootstrapKey(res.data)
	if err != nil {
		return nodeConfig{}, fmt.Errorf("bootstrap key: %w", err)
	}
	w := wantAddr(*nf.addr, devDefaultAddr)
	w.span = devSpan
	return nodeConfig{want: w, res: res, key: key, keyPath: keyPath, keyCreated: created,
		opts:   []weftbase.Option{weftbase.BootstrapKey(key), nodeLogger()},
		signIn: true, disc: true, console: true, out: out}, nil
}

// devBanner is `weftbase dev`'s banner, the key never printed.
func devBanner(out io.Writer, n *node, cfg nodeConfig) {
	_, _ = fmt.Fprintf(out, "weftbase: http://%s/\n", n.addr)
	printStores(out, cfg.res)
	if cfg.keyCreated {
		_, _ = fmt.Fprintf(out, "weftbase: bootstrap key created in %s (a secret key: apps get it through the discovery file or WEFTBASE_TOKEN)\n", cfg.keyPath)
	} else {
		_, _ = fmt.Fprintf(out, "weftbase: bootstrap key in %s\n", cfg.keyPath)
	}
	printNotes(out, cfg.res.notes)
}

func printStores(out io.Writer, r resolved) {
	_, _ = fmt.Fprintf(out, "weftbase: data %s (control %s, traces %s)\n", r.data, orNone(r.controlPath), r.traceLabel)
}

func printNotes(out io.Writer, notes []string) {
	for _, n := range notes {
		if n != "" {
			_, _ = fmt.Fprintln(out, n)
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "in memory"
	}
	return s
}

func rolesLabel(rs []weftbase.Role) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return strings.Join(out, ",")
}

// want is the address the command wants and whether it is pinned.
type want struct {
	addr   string
	pinned bool
	span   int // ports an unpinned start tries (0 = listen.DefaultSpan)
}

// wantAddr resolves the listen address: --addr, then WEFTBASE_ADDR
// (both pin it), else def — the one default, unpinned.
func wantAddr(flagAddr, def string) want {
	if flagAddr != "" {
		return want{addr: flagAddr, pinned: true}
	}
	if env := os.Getenv("WEFTBASE_ADDR"); env != "" {
		return want{addr: env, pinned: true}
	}
	return want{addr: def}
}

// clientFlags adds --url and --token to an API client's flag set.
// Both default to "" and are resolved after parsing (resolveServer):
// --help must not print a token, and an unset --url is what lets the
// discovery file name the server.
func clientFlags(fs *flag.FlagSet) (base, token *string) {
	base = fs.String("url", "", "the server to talk to (default: $WEFTBASE_URL, else the discovery file's, else "+defaultURL+")")
	token = fs.String("token", "", "API token (default: $WEFTBASE_TOKEN, else the discovery file's when it names that server; never printed)")
	return base, token
}

// clientFor is the client of the server the flags resolve to, the one
// stderr line naming it when the user did not (resolveServer).
func clientFor(urlFlag, tokenFlag string, stderr io.Writer) (*rest.Client, serverTarget) {
	t := resolveServer(urlFlag, tokenFlag)
	t.say(stderr)
	return rest.New(t.base, t.token), t
}

// runDoctor is `weftbase doctor`: the flags mirror WEFTBASE_URL and
// WEFTBASE_TOKEN one to one; internal/doctor does the checking.
func runDoctor(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("doctor", stderr)
	base, token := clientFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError("doctor takes no arguments (got %s)", gotArgs(fs.Args()))
	}
	t := resolveServer(*base, *token)
	t.say(stderr)
	return doctor.Run(context.Background(), stdout, doctor.Target{
		URL:       t.base,
		Token:     t.token,
		Discovery: t.discovery(),
	})
}

// httpServer builds the listener's server. The header read and idle
// keep-alives are bounded — a client that opens a connection and never
// finishes its request headers must not hold it forever. The write
// side and the body read are deliberately not: /v1/runs/stream is
// long-lived (each frame carries its own
// write deadline in the handlers), and a 16 MiB ingest upload over a
// slow link is legitimate.
func httpServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// serveUntil serves on ln until stop delivers a
// signal (or Serve fails), then stops gracefully. `weftbase dev` owns its
// signals (the app stops first) and hands its own channel here.
func serveUntil(httpSrv *http.Server, ln net.Listener, stdout io.Writer, stop <-chan os.Signal) error {
	// Shutdown never cancels a request's context — it waits for the
	// connection to go idle, which a stream never does. Every request's
	// context derives from this one, canceled when the shutdown begins,
	// so a live stream ends at once instead
	// of holding the grace window.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	httpSrv.BaseContext = func(net.Listener) context.Context { return base }
	httpSrv.RegisterOnShutdown(cancelBase)
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		_, _ = fmt.Fprintf(stdout, "weftbase: shutting down (%v)\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			// Streams never go idle: after the grace window the socket
			// force-closes so the process can still exit.
			_ = httpSrv.Close()
		}
		return nil
	}
}

// maskDSN replaces a DSN userinfo's password with ***. A DSN without
// a password is returned unchanged; so is anything that does not parse
// as scheme://…@host (the banner stays honest rather than empty).
func maskDSN(dsn string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return dsn
	}
	// The userinfo ends at the authority's last '@' (url.Parse's rule,
	// the driver's): a password may hold one.
	authority, path := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, path = rest[:i], rest[i:]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return dsn
	}
	userinfo, hostPart := authority[:at], authority[at+1:]+path
	user, _, hasPassword := strings.Cut(userinfo, ":")
	if !hasPassword {
		return dsn
	}
	return scheme + "://" + user + ":***@" + hostPart
}
