// Package httpapi is weftbase's HTTP front: the router and its
// middleware — request ids (internal/reqlog), CORS, the guards,
// authentication through authz into the request context, and
// authz.Can per route from one route table — and the routes that are
// the server's own: /healthz, /readyz, /metrics and the Console's
// sign-in (/v1/auth/sessions). The /v1 handlers and the OTLP receiver
// come from api/http/v1; the Console is mounted at / with the lowest
// priority: /v1/*, /otlp/*, /healthz, /readyz and /metrics never fall
// through to it.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/internal/httpx"
	"github.com/weftbase/weftbase/internal/reqlog"
)

// Route is one row of the route table: a ServeMux pattern's method and
// path, the action a caller needs (authz.Public: no credential), and
// the handler.
type Route struct {
	Method  string
	Pattern string
	Action  authz.Action
	Handler http.Handler
}

// Config is what the router serves.
type Config struct {
	// Auth verifies credentials. Required.
	Auth *authz.Authenticator
	// Sessions backs the Console sign-in routes; nil turns them off.
	Sessions *authz.Sessions
	// Routes are the mounted operations (api/http/v1's Routes and
	// IngestRoutes, as the kernel's roles select them).
	Routes []Route
	// Console serves every path outside the API trees; nil answers 404.
	Console http.Handler
	// Ready reports readiness (/readyz); nil is always ready.
	Ready func(context.Context) error
	// AllowOrigins are the CORS origins (default: localhost and
	// 127.0.0.1 on any port) and, for the Local principal, the extra
	// Hosts the API answers.
	AllowOrigins []string
	// RequestLog is the request log; nil makes a fresh one.
	RequestLog *reqlog.Logger
	// Logger takes the router's own lines; nil is slog.Default().
	Logger *slog.Logger
	// Now is the clock (tests); nil is time.Now.
	Now func() time.Time
}

// Router is the HTTP front. Build it with New.
type Router struct {
	auth     *authz.Authenticator
	sessions *authz.Sessions
	origins  []string
	console  http.Handler
	ready    func(context.Context) error
	reqs     *reqlog.Logger
	log      *slog.Logger
	now      func() time.Time

	routes  []Route
	mux     *http.ServeMux
	metrics *metrics
	handler http.Handler
}

// New builds the router over c. A route table with a duplicate or
// malformed pattern, or a route without an action, panics: the table
// is code, and a broken one is a construction error.
func New(c Config) *Router {
	if c.Auth == nil {
		panic("httpapi: New needs an Authenticator")
	}
	rt := &Router{
		auth: c.Auth, sessions: c.Sessions, origins: c.AllowOrigins, console: c.Console,
		ready: c.Ready, reqs: c.RequestLog, log: c.Logger, now: c.Now,
		mux: http.NewServeMux(), metrics: newMetrics(),
	}
	if rt.reqs == nil {
		rt.reqs = reqlog.New()
	}
	if rt.log == nil {
		rt.log = slog.Default()
	}
	if rt.now == nil {
		rt.now = time.Now
	}
	rt.routes = append(rt.ownRoutes(), c.Routes...)
	for _, r := range rt.routes {
		if r.Action == "" {
			panic("httpapi: route " + r.Method + " " + r.Pattern + " has no authz action")
		}
		rt.mux.Handle(r.Method+" "+r.Pattern, rt.wrap(r))
	}
	rt.handler = rt.reqs.Wrap(rt.cors(http.HandlerFunc(rt.serve)))
	return rt
}

// ownRoutes are the router's own operations.
func (rt *Router) ownRoutes() []Route {
	out := []Route{
		{"GET", "/healthz", authz.Public, http.HandlerFunc(rt.serveHealthz)},
		{"GET", "/readyz", authz.Public, http.HandlerFunc(rt.serveReadyz)},
		{"GET", "/metrics", authz.Public, http.HandlerFunc(rt.serveMetrics)},
	}
	if rt.sessions != nil {
		out = append(out,
			Route{"POST", "/v1/auth/sessions", authz.Public, http.HandlerFunc(rt.serveSignIn)},
			Route{"DELETE", "/v1/auth/sessions/current", authz.AuthSession, http.HandlerFunc(rt.serveSignOut)},
		)
	}
	return out
}

// Routes is the route table, the router's own rows first.
func (rt *Router) Routes() []Route { return append([]Route(nil), rt.routes...) }

// RequestLog is the router's request log (its ingest counters feed
// /v1/meta and /metrics).
func (rt *Router) RequestLog() *reqlog.Logger { return rt.reqs }

// ServeHTTP serves one request.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	rt.handler.ServeHTTP(w, r)
}

// apiPath reports whether p belongs to the API trees, which never fall
// through to the Console.
func apiPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/metrics", "/v1", "/otlp":
		return true
	}
	return strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/otlp/")
}

// serve routes one request: a table route, else a JSON 404 or 405 on
// the API trees, else the Console.
func (rt *Router) serve(w http.ResponseWriter, r *http.Request) {
	if _, pattern := rt.mux.Handler(r); pattern != "" {
		rt.mux.ServeHTTP(w, r)
		return
	}
	if !apiPath(r.URL.Path) {
		if rt.console != nil {
			rt.console.ServeHTTP(w, r)
			return
		}
		writeError(w, r, http.StatusNotFound, "not_found", "the Console is off on this server")
		return
	}
	if allow := rt.allowed(r); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			r.Method+" is not allowed on "+r.URL.Path+" (allowed: "+strings.Join(allow, ", ")+")")
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "no such route "+r.URL.Path)
}

// allowed lists the methods some route serves at r's path.
func (rt *Router) allowed(r *http.Request) []string {
	var out []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r2 := r.Clone(r.Context())
		r2.Method = m
		if _, p := rt.mux.Handler(r2); p != "" {
			out = append(out, m)
			if m == http.MethodGet {
				out = append(out, http.MethodHead)
			}
		}
	}
	return out
}

// wrap is one route's middleware: the URL-credential refusal,
// authentication, the guards, authz.Can, then the handler with the
// principal on the context; and the request counter.
func (rt *Router) wrap(route Route) http.Handler {
	label := route.Method + " " + route.Pattern
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &countingWriter{ResponseWriter: w}
		defer func() { rt.metrics.request(label, cw.status()) }()
		w = cw.writer()
		if r.URL.Query().Has("token") {
			// Never read, in any setup, whatever it holds — and never
			// echoed: a URL lands in logs, history and Referer headers.
			writeError(w, r, http.StatusUnauthorized, "unauthorized",
				"a credential in the URL is never accepted: send Authorization: Bearer <key>")
			return
		}
		if route.Action == authz.Public {
			route.Handler.ServeHTTP(w, r)
			return
		}
		p, sc, err := rt.auth.Authenticate(r.Context(), r)
		switch {
		case errors.Is(err, authz.ErrNoCredential):
			w.Header().Set("WWW-Authenticate", `Bearer realm="weftbase"`)
			writeError(w, r, http.StatusUnauthorized, "unauthorized",
				"this route needs a credential: Authorization: Bearer <key>, or a Console session")
			return
		case errors.Is(err, authz.ErrInvalid):
			w.Header().Set("WWW-Authenticate", `Bearer realm="weftbase", error="invalid_token"`)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "the credential is invalid, revoked or expired")
			return
		case err != nil:
			reqlog.Cause(r.Context(), "authenticate: "+err.Error())
			writeError(w, r, http.StatusInternalServerError, "internal", "the credential could not be checked")
			return
		}
		if !rt.guard(w, r, p) {
			return
		}
		if d := authz.Can(p, route.Action); !d.Allow {
			writeError(w, r, http.StatusForbidden, "forbidden",
				"not allowed to "+string(route.Action)+": "+d.Reason)
			return
		}
		route.Handler.ServeHTTP(w, r.WithContext(authz.With(r.Context(), p, sc)))
	})
}

// writeError is the API's error shape (internal/httpx).
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httpx.WriteError(w, r, status, code, msg)
}
