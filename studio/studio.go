package studio

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weft/version"
	"github.com/weftbase/weftbase/console"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/studio/internal/reqlog"
)

// Version is the version the server reports as api/meta's
// studio_version: the framework module's tag, [version.Version] — one
// version for every layer. It moves with the module's release, nothing
// else.
const Version = version.Version

const defaultTitle = "weftbase"

// Option configures New.
type Option func(*config)

type config struct {
	db           obsdb.DB
	dbSet        bool   // DB appeared among the options (DB(nil) is an error)
	dbPath       string // set by Open: opened at New time
	live         obsdb.Hub
	noIngest     bool
	ingestToken  string
	token        string
	origins      []string
	base         string
	title        string
	manifest     []byte
	capabilities []string
	retain       time.Duration     // Retain: 0 = off
	sweepEvery   time.Duration     // the sweeper's interval in tests; 0 = sweepInterval(retain)
	pricing      *PricingTable     // Pricing; New resolves it (resolvePricing), never nil after
	discovery    string            // DiscoveryFile: the file the hosting command writes for this server
	offReasons   map[string]string // OffReason: capability → the host's own words
}

// DB serves over db (embedded: DB(exporter.LocalDB()) — the same
// handle weft/otel's Local destination writes, so the reads see what
// the run wrote, live, through the handle's own hub). Without DB or
// Open, New opens the history database at $WEFTBASE_DB or ./.weftbase/weftbase.db
// (history only: a second handle on the file, no live lane — passing
// the pipeline's handle is what makes it live). A nil db panics at New
// time — a server with nothing to read is a construction error.
func DB(db obsdb.DB) Option {
	return func(c *config) { c.db, c.dbSet, c.dbPath = db, true, "" }
}

// Open is shorthand for DB(sqlite.Open(path)): the server over an
// obsdb sqlite file, created when missing. Opening happens at New time
// and panics on failure — a server that cannot reach its database is a
// construction error, not a serving one.
func Open(path string) Option {
	return func(c *config) { c.dbPath = path }
}

// Live serves the live stream from h (S4.1). The default is the DB's
// own hub when it implements Hub() — the embedded sub-100 ms lane — else
// an in-process obsdb.NewHub() fed by ingest.
func Live(h obsdb.Hub) Option {
	return func(c *config) { c.live = h }
}

// NoIngest turns the OTLP ingest routes off: a read-only server. The
// ingest capability disappears from api/meta with them.
func NoIngest() Option {
	return func(c *config) { c.noIngest = true }
}

// IngestToken requires `Authorization: Bearer <tok>` on the OTLP
// ingest routes. Without one, ingest answers loopback peers only
// (dev mode) — a server bound wide without a token refuses remote
// exporters.
func IngestToken(tok string) Option {
	return func(c *config) { c.ingestToken = tok }
}

// Token protects the JSON API with a bearer token. Without it the API
// is open to a loopback Host or one AllowOrigins names (embedded, same
// origin; any other Host is refused, the DNS-rebinding guard).
func Token(tok string) Option {
	return func(c *config) { c.token = tok }
}

// AllowOrigins permits these origins on the API and ingest routes
// (CORS for a client served from another origin). The default, when a
// Token is configured, is localhost and 127.0.0.1 on any port; with no
// token (same origin) no CORS headers are sent unless origins are
// listed. Without a Token each origin's host:port is also a Host the
// API answers besides the loopback names (an app served at
// http://myapp.internal:8080 lists exactly that); "*" admits every
// Host.
func AllowOrigins(origins ...string) Option {
	return func(c *config) { c.origins = append(c.origins, origins...) }
}

// Base is the URL path the server is mounted at, with leading and
// trailing slash. Default "/". It is written into the Console shell's
// <base href> per request and becomes the router's basepath, so the
// same bundle mounts anywhere.
func Base(path string) Option {
	return func(c *config) {
		p := path
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if !strings.HasSuffix(p, "/") {
			p += "/"
		}
		c.base = p
	}
}

// Manifest supplies core.Manifest bytes (weft.json) for the agent and
// tool cards; without it api/manifest answers 404.
func Manifest(json []byte) Option {
	return func(c *config) { c.manifest = json }
}

// Title overrides the page title (default "weftbase").
func Title(s string) Option {
	return func(c *config) { c.title = s }
}

// Capabilities declares named API capabilities the backing server
// provides beyond what the registered route groups already report
// (ADR 0018 §8). The core groups contribute their own names — live,
// ingest, auth — computed from what is actually registered, never
// hard-coded; this option is for a hosting wrapper's own verbs.
func Capabilities(names ...string) Option {
	return func(c *config) {
		c.capabilities = append(c.capabilities, names...)
	}
}

// DiscoveryFile names the discovery file the command hosting this
// server writes for it (`weftbase dev`: the file apps find a running
// server through). api/meta serves it as discovery_file, under
// db.path's rule, while the file at path names this process — a file
// never written, removed, or since taken over by another server is not
// served. An embedded server writes no such file and leaves it unset.
func DiscoveryFile(path string) Option {
	return func(c *config) { c.discovery = path }
}

// OffReason replaces why meta.capabilities_off says capability is off
// — and every capability off for the same cause — with the host's own
// words: a command can name its flag, not the Go option an embedding
// app would set. It changes words only, never which capabilities are
// on. A capability that is no optional one panics at New (a
// construction error, as Retain's floor is).
func OffReason(capability, why string) Option {
	return func(c *config) {
		if c.offReasons == nil {
			c.offReasons = map[string]string{}
		}
		c.offReasons[capability] = why
	}
}

// Server is one weftbase server: the Console, the JSON API, the live
// stream and — unless disabled — OTLP ingest, over one obsdb.DB.
// Build it with New; serve it with Handler.
type Server struct {
	config
	groups []routeGroup
	mux    *http.ServeMux
	// runRoutes are the /api/runs/{id}/<ext> sub-routes the groups
	// registered (addRunRoute): run ids carry slashes, so the run
	// subtree is one mux handler that dispatches on the last segment.
	runRoutes []runRoute
	// reqs is the server log of failed requests (UX-E-37): the request
	// id on every response, a line per refused ingest batch and 5xx.
	reqs *reqlog.Logger

	// live (config.live, set by New from the Live default) is the hub
	// /api/live streams from.
	now func() time.Time

	// ui serves every path outside the API and ingest trees: the
	// Console.
	ui http.Handler

	// ownsDB: New opened the database itself (Open or the default
	// path), so Close closes it. A DB handed in through DB(...) stays
	// the caller's — otel owns it when embedded.
	ownsDB bool

	// ingestHealth is what the exporting apps reported losing
	// (weft.records.dropped on their heartbeats and run brackets;
	// ingesthealth.go).
	ingestHealth ingestHealth

	// stopSweep stops Retain's sweeper (delete.go); nil without one.
	// sweepMu guards it: Close may race another Close.
	sweepMu   sync.Mutex
	stopSweep func()
}

// New builds a server over one obsdb.DB. Handler() serves the
// Console, the JSON API, the live stream and — unless disabled — OTLP
// ingest.
func New(opts ...Option) *Server {
	c := config{base: "/", title: defaultTitle}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.dbSet && c.db == nil {
		panic("weftbase: New called with a nil DB")
	}
	if c.retain > 0 && c.retain < minRetention {
		panic("weftbase: " + retainFloorMsg)
	}
	for name := range c.offReasons {
		if _, ok := optionalCapabilities[name]; !ok {
			panic("weftbase: OffReason names " + name + ", which is no optional capability")
		}
	}
	resolvePricing(&c)
	s := &Server{config: c, now: time.Now, mux: http.NewServeMux(), reqs: reqlog.New()}
	if c.db == nil {
		path := c.dbPath
		if path == "" {
			path = defaultDBPath()
		}
		db, err := sqlite.Open(path)
		if err != nil {
			panic("weftbase: open " + path + ": " + err.Error())
		}
		s.db, s.ownsDB = db, true
	}
	if s.live == nil {
		if h, ok := s.db.(interface{ Hub() obsdb.Hub }); ok {
			s.live = h.Hub()
		} else {
			s.live = obsdb.NewHub()
		}
	}
	s.ui = console.Handler(console.Options{Base: s.base, Title: s.title})
	s.registerGroups()
	s.storeFileManifest()
	s.mux.HandleFunc("/", s.serveUI)
	s.startSweeper()
	return s
}

// Handler serves the server. Mount it under a prefix with
// http.StripPrefix (and Base), or at the root of its own mux:
//
//	mux.Handle("/weftbase/", http.StripPrefix("/weftbase",
//		studio.Handler(studio.DB(db), studio.Base("/weftbase/"))))
//
// The handler does not bind a port — the caller chooses the address.
// Bind loopback, or configure Token, before exposing it wider.
func (s *Server) Handler() http.Handler { return s }

// Handler is New(opts...).Handler(): the one-liner for an embedded
// server. Nothing can Close the server it builds, so a server with
// Retain is built with New and closed: under Handler the sweeper stops
// only when the DB is closed.
func Handler(opts ...Option) http.Handler {
	return New(opts...).Handler()
}

// Close releases what the server owns: Retain's sweeper, and the
// database when New opened it (Open or the default path). A DB passed
// through DB(...) stays its owner's — closing otel's handle out from
// under the pipeline is not the server's call.
func (s *Server) Close() error {
	s.sweepMu.Lock()
	stop := s.stopSweep
	s.stopSweep = nil
	s.sweepMu.Unlock()
	if stop != nil {
		stop()
	}
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}

// ingestAuthorized is the ingest auth: the configured ingest token as
// a bearer, or — with none configured — loopback peers only (dev mode;
// a server bound wide without a token refuses remote exporters).
//
// "Loopback" is the socket's peer, and two kinds of remote caller
// arrive from it too, so the open rule refuses both:
//
//   - a request a reverse proxy forwarded (it says so: Forwarded,
//     X-Forwarded-For, X-Real-IP) — behind a proxy on the same host
//     every client is a loopback peer, and the open rule would hand
//     ingest to all of them; such a deployment sets IngestToken;
//   - a browser page on an origin nobody allowed (a POST carries its
//     Origin) — any site can aim a form or a DNS-rebound fetch at
//     127.0.0.1. Localhost origins and AllowOrigins pass, the same
//     rule CORS applies.
func (s *Server) ingestAuthorized(r *http.Request) bool {
	if s.ingestToken != "" {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return ok && tokenEqual(tok, s.ingestToken)
	}
	if !remoteIsLoopback(r) {
		return false
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Real-Ip"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(s.origins, true, origin) {
		return false
	}
	return true
}

// remoteIsLoopback reports whether the request's peer is on this
// machine, which is the best a library can know of its bind: a
// loopback bind only ever sees loopback peers. All of 127.0.0.0/8,
// ::1 and the IPv4-mapped form (a dual-stack listener reports
// ::ffff:127.0.0.1) are loopback.
func remoteIsLoopback(r *http.Request) bool {
	return isLoopbackName(hostOnly(r.RemoteAddr))
}

// ServeHTTP routes one request: the registered route groups (the API,
// the live stream, ingest) on the mux, then the Console for every
// other path. Every request carries an X-Request-Id (the client's,
// else one generated) and a refused ingest batch or a 5xx is one line
// on slog.Default naming it (studio/internal/reqlog, UX-E-37).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if s.reqs == nil {
		s.routed().ServeHTTP(w, r)
		return
	}
	s.reqs.Wrap(s.routed()).ServeHTTP(w, r)
}

// routed wraps the mux with the cross-cutting concerns in order: CORS
// on the API and ingest trees, token auth on the API tree only —
// ingest carries its own token.
func (s *Server) routed() http.Handler {
	return s.cors(prefixRouter{
		prefixes: []prefixRoute{
			{"/api/", s.auth(s.mux)},
			{"/v1/", s.mux},
		},
		def: s.mux,
	})
}

// prefixRoute routes one path prefix to one handler.
type prefixRoute struct {
	prefix  string
	handler http.Handler
}

// prefixRouter sends each request to the first prefix that fits, else
// to the default (the Console).
type prefixRouter struct {
	prefixes []prefixRoute
	def      http.Handler
}

func (p prefixRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, pr := range p.prefixes {
		if strings.HasPrefix(r.URL.Path, pr.prefix) {
			pr.handler.ServeHTTP(w, r)
			return
		}
	}
	p.def.ServeHTTP(w, r)
}

// serveUI answers every path the groups do not: unknown API and ingest
// paths are 404s in the error shape, never the Console's shell — a
// mis-routed API call must fail loudly, not hydrate. (Registered routes
// never reach here; a wrong-method request got ServeMux's 405 with
// Allow instead.) Every other path is the Console's.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
		s.serveAPINotFound(w, r)
		return
	}
	s.ui.ServeHTTP(w, r)
}

// defaultDBPath is the default history database: $WEFTBASE_DB, else
// ./.weftbase/weftbase.db.
func defaultDBPath() string {
	if p := os.Getenv("WEFTBASE_DB"); p != "" {
		return p
	}
	return ".weftbase/weftbase.db"
}
