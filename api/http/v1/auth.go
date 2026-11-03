package v1

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weftbase/weftbase/internal/reqlog"
)

// Auth and CORS. Two setups, one handler:
//
//   - embedded: no Token configured — the API is open to the page's
//     own origin (same origin, in-process), ingest answers loopback.
//     "Same origin" is the browser's word, and DNS rebinding makes any
//     site same-origin to a loopback server (http://evil.example:7331
//     resolving to 127.0.0.1), so the open API answers only a loopback
//     Host or one AllowOrigins names (hostAllowed below).
//   - local binary: a server token (WEFTBASE_TOKEN fixes it) protects
//     the API; AllowOrigins defaults to localhost and 127.0.0.1 on any
//     port.
//
// A token rides the Authorization header only: a ?token= query
// parameter is refused on every /api route, in every setup.

// identityCtxKey carries the request's identity from the middleware to
// the handlers.
type identityCtxKey struct{}

// identity is who is asking.
type identity struct {
	server bool // the server token (Token's value)
}

// idFrom returns the request's identity; the zero identity when no
// token is configured (open, loopback).
func idFrom(r *http.Request) identity {
	id, _ := r.Context().Value(identityCtxKey{}).(identity)
	return id
}

// bearerToken reads the request's token from the Authorization header
// — never from the URL: a URL lands in logs, history and Referer
// headers.
func bearerToken(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return tok
	}
	return ""
}

// auth wraps the API tree with the token check. Without a configured
// Token every request whose Host passes hostAllowed goes through, and
// any other Host is a 403 that names what to configure; an unsafe
// method from another origin is refused (crossOriginWrite). With a
// Token the bearer must be the server token, and the Host does not
// matter. Ingest carries its own rule and is not wrapped; neither is
// the Console, which carries no data.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identify := func(id identity) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityCtxKey{}, id)))
		}
		if r.URL.Query().Has("token") {
			// Never accepted on the /api tree, in any setup, whatever it
			// holds — and never echoed.
			writeError(w, r, http.StatusUnauthorized, "unauthorized",
				"a token in the URL is never accepted: send Authorization: Bearer <token>")
			return
		}
		if s.token == "" {
			if !hostAllowed(s.origins, r) {
				writeError(w, r, http.StatusForbidden, "forbidden",
					"no server token is configured, so the API answers only a loopback Host "+
						"(localhost, *.localhost, 127.0.0.1, [::1]); this request's Host is "+
						strconv.Quote(r.Host)+": list its origin in AllowOrigins "+
						"(e.g. \"http://"+r.Host+"\") or configure Token")
				return
			}
			if crossOriginWrite(s.origins, r) {
				writeError(w, r, http.StatusForbidden, "forbidden",
					"cross-origin write refused: with no server token the API takes an unsafe "+
						"method only from its own origin; this request's Origin is "+strconv.Quote(r.Header.Get("Origin"))+
						" (Sec-Fetch-Site "+strconv.Quote(r.Header.Get("Sec-Fetch-Site"))+"): list the origin in "+
						"AllowOrigins or configure Token")
				return
			}
			identify(identity{})
			return
		}
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthorized",
				"the API requires a token: Authorization: Bearer <token>")
			return
		}
		if !tokenEqual(tok, s.token) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "bad token")
			return
		}
		identify(identity{server: true})
	})
}

// tokenEqual compares a presented token with the configured one in
// constant time (the length aside): the comparison must not tell a
// remote caller how much of its guess was right.
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ── CORS (S4.6) ────────────────────────────────────────────────────

// cors wraps the API and ingest trees: a client served from another
// origin needs the headers, and an EventSource needs them on the
// stream. Without a configured Token (same origin) no CORS headers are
// sent; explicit AllowOrigins always apply. The allowed origin is
// echoed, never with credentials: the bearer rides a header.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.token != "" || len(s.origins) > 0 {
			// The answer depends on the Origin whenever CORS is in play
			// — the refusals too: a shared cache must not hand an answer
			// made for a refused origin to an allowed one.
			w.Header().Add("Vary", "Origin")
		}
		if origin != "" && originAllowed(s.origins, s.token != "", origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, Last-Event-ID")
			// The request id (reqlog) is readable cross-origin: a client's
			// error report quotes it to meet its server log line.
			h.Set("Access-Control-Expose-Headers", reqlog.Header)
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed matches an origin against the configured list; with
// none configured and a token in play, the default allows localhost
// and 127.0.0.1 on any port, http or https.
func originAllowed(origins []string, defaults bool, origin string) bool {
	if len(origins) > 0 {
		for _, o := range origins {
			if o == "*" || o == origin {
				return true
			}
		}
		return false
	}
	if !defaults {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1"
}

// crossOriginWrite is the open API's CSRF guard: the loopback Host proves
// the request reached this machine, not that this origin's page sent it
// — any site's page can POST text/plain with mode "no-cors" and no
// preflight. An unsafe method (anything but GET, HEAD, OPTIONS) is
// cross-origin when its Origin is neither scheme://Host nor an
// AllowOrigins entry, or, with no Origin, when Sec-Fetch-Site says
// cross-site. A client sending neither header (curl, an SDK)
// is not a browser page and passes.
func crossOriginWrite(origins []string, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
	}
	for _, o := range origins {
		if o == "*" || o == origin {
			return false
		}
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return true // "null" (a sandboxed frame, file://) included
	}
	return !strings.EqualFold(u.Host, r.Host)
}

// hostAllowed is the open API's DNS-rebinding guard: the request's Host
// (the browser's idea of the origin's host, which a rebound page
// cannot fake) must be a loopback name — localhost, *.localhost
// (browsers resolve those to loopback themselves), 127.0.0.0/8 or
// [::1], any port — or the host[:port] of an AllowOrigins entry
// ("*" lets every Host through, as it lets every origin). Proxy
// headers (X-Forwarded-Host, Forwarded) are never read: anyone can
// send them. No request is exempt.
func hostAllowed(origins []string, r *http.Request) bool {
	if isLoopbackName(hostOnly(r.Host)) {
		return true
	}
	for _, o := range origins {
		if o == "*" {
			return true
		}
		if u, err := url.Parse(o); err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) {
			return true
		}
	}
	return false
}

// hostOnly strips the port (and an IPv6 literal's brackets) from a
// host[:port].
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// isLoopbackName reports whether host names this machine: localhost,
// a *.localhost name, or a loopback IP (127.0.0.0/8, ::1, the
// IPv4-mapped form).
func isLoopbackName(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DevToken generates a random server token for the local binary,
// fixed by WEFTBASE_TOKEN. Exported because the binary
// lives in another package (cmd/weftbase).
func DevToken() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// Crypto randomness refusing is not survivable; a fixed token
		// would be worse than loud.
		panic("weftbase: dev token: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
