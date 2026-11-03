package weftbase

import (
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/observe/cost"
	"github.com/weftbase/weftbase/sessions"
)

// Option configures New.
type Option func(*config)

type config struct {
	dataDir      string
	controlDSN   string
	traceDSN     string
	traces       obsdb.DB
	roles        roleSet
	logger       *slog.Logger
	open         bool
	bootstrapKey string
	console      bool
	base         string
	title        string
	origins      []string
	pricing      *cost.Table
	retain       time.Duration
	sweepEvery   time.Duration
	manifest     []byte
	buildDigest  string
	leaseTTL     time.Duration
	drain        time.Duration
	policy       sessions.Policy
	timerPoll    time.Duration
	silence      time.Duration
}

// defaultDrain is how long Shutdown lets running runs finish.
const defaultDrain = 5 * time.Second

func (c *config) drainTimeout() time.Duration {
	if c.drain > 0 {
		return c.drain
	}
	return defaultDrain
}

// DataDir is the directory the SQLite stores live in by default:
// <dir>/control.db and <dir>/traces.db. Default ./.weftbase.
func DataDir(dir string) Option { return func(c *config) { c.dataDir = dir } }

// ControlDB names the control store: sqlite://path (default
// <data>/control.db; sqlite://:memory: for a private in-memory store).
// postgres:// is Stage 1 and not supported yet.
func ControlDB(dsn string) Option { return func(c *config) { c.controlDSN = dsn } }

// TraceDB names the trace store: sqlite://path (default
// <data>/traces.db) or clickhouse://user:pass@host:9000/db.
func TraceDB(dsn string) Option { return func(c *config) { c.traceDSN = dsn } }

// Traces serves over an open trace store instead of TraceDB: the
// embedded placement passes the in-process sink's handle
// (observe/local), so the reads see what the run wrote, live. The
// handle stays the caller's: Shutdown does not close it.
func Traces(db obsdb.DB) Option { return func(c *config) { c.traces = db } }

// Roles selects the node's roles; the default is all four.
func Roles(rs ...Role) Option {
	return func(c *config) { c.roles = slices.Clone(rs) }
}

// Logger takes the server's log lines; the default is slog.Default().
func Logger(l *slog.Logger) Option { return func(c *config) { c.logger = l } }

// Open admits a request with no credential as the Local principal in
// the default scope, from a loopback Host only (the DNS-rebinding
// guard). For the embedded placement behind the host app's own auth,
// and tests; a served node never sets it.
func Open() Option { return func(c *config) { c.open = true } }

// BootstrapKey imports a secret key (wb_sk_test_…) into the default
// project's test environment at New: the key `weftbase dev` keeps in
// <data>/bootstrap.key.
func BootstrapKey(secret string) Option { return func(c *config) { c.bootstrapKey = secret } }

// Console turns the embedded Console on or off (default on; served by
// the api role).
func Console(on bool) Option { return func(c *config) { c.console = on } }

// Base is the URL path the handler is mounted at, with leading and
// trailing slash (default "/"): the Console writes it into its shell's
// <base href>. The caller strips the prefix (http.StripPrefix).
func Base(path string) Option {
	return func(c *config) {
		p := path
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		c.base = p
	}
}

// Title overrides the Console's page title.
func Title(s string) Option { return func(c *config) { c.title = s } }

// AllowOrigins are the CORS origins (default: localhost and 127.0.0.1
// on any port) and, under Open, the extra Hosts the API answers.
func AllowOrigins(origins ...string) Option {
	return func(c *config) { c.origins = append(c.origins, origins...) }
}

// Pricing sets the table usage is priced with (default
// cost.Default()); the CLI reads WEFT_PRICING into it.
func Pricing(t cost.Table) Option { return func(c *config) { c.pricing = &t } }

// Retain keeps traces for d after a run was last seen: the scheduler
// role's sweeper deletes older runs that are not running, every
// min(d/10, 1h), at least a minute apart. 0 (the default) keeps
// everything; under 1h is an error (a run silent for 30 s counts as
// interrupted, so a short retention would sweep a run paused at a
// breakpoint).
func Retain(d time.Duration) Option { return func(c *config) { c.retain = d } }

// Manifest supplies the weft.json (core.Manifest bytes): GET
// /v1/manifest, meta's manifest check and the manifest store.
func Manifest(json []byte) Option { return func(c *config) { c.manifest = slices.Clone(json) } }

// sweepEvery overrides the sweeper's interval (tests).
func sweepEvery(d time.Duration) Option { return func(c *config) { c.sweepEvery = d } }

// BuildDigest names the embedded build's digest ("sha256:<hex>")
// instead of the running executable's sha256: what App.Agent registers
// the agents under.
func BuildDigest(d string) Option { return func(c *config) { c.buildDigest = d } }

// Drain is how long Shutdown lets running runs finish before it
// cancels them (cause shutdown); default 5 s.
func Drain(d time.Duration) Option { return func(c *config) { c.drain = d } }

// leaseTTL overrides the queue lease of a dispatched run (tests).
func leaseTTL(d time.Duration) Option { return func(c *config) { c.leaseTTL = d } }

// QueueTTL is how long a run may wait queued with no worker serving its
// version before it fails worker_lost (default 24 h; negative: never).
func QueueTTL(d time.Duration) Option { return func(c *config) { c.policy.QueueTTL = d } }

// ApprovalExpiry denies the pending calls of a run parked this long
// with no decision, resuming it (default: never).
func ApprovalExpiry(d time.Duration) Option { return func(c *config) { c.policy.ApprovalExpiry = d } }

// MaxPinAge migrates a run parked this long to its agent's prod
// version, or cancels it (cause version_retired) when there is none
// (default 30 days; negative: never).
func MaxPinAge(d time.Duration) Option { return func(c *config) { c.policy.MaxPinAge = d } }

// timerPoll overrides how often the timer loop looks (tests).
func timerPoll(d time.Duration) Option { return func(c *config) { c.timerPoll = d } }

// silence overrides how long a held run may go unacked (tests).
func silence(d time.Duration) Option { return func(c *config) { c.silence = d } }
