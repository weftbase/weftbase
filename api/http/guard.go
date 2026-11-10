package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/internal/reqlog"
)

// The guards. Three kinds of caller reach the API:
//
//   - an API key (Authorization: Bearer wb_…): the key is the defence;
//     the Host and the Origin do not matter.
//   - a Console session (the wb_session cookie): the browser sends it
//     on its own, so an unsafe method also needs the Weftbase-CSRF: 1
//     header (a cross-site form cannot set it, and a cross-site fetch
//     that sets it is preflighted and refused by CORS) and must not come
//     from another origin (crossOriginWrite).
//   - the Local principal (an Open authenticator: the embedded
//     placement, tests): no credential at all, so the API answers only
//     a loopback Host or one AllowOrigins names (hostAllowed: DNS
//     rebinding makes any site same-origin to a loopback server), and
//     an unsafe method only from its own origin.
//
// A credential rides a header or the cookie, never the URL: a ?token=
// query parameter is refused everywhere.

// CSRFHeader is the header an unsafe method with a cookie session
// carries.
const CSRFHeader = "Weftbase-CSRF"

// guard refuses what the principal's kind requires refused; it reports
// false after writing the refusal.
func (rt *Router) guard(w http.ResponseWriter, r *http.Request, p authz.Principal) bool {
	switch p.Kind {
	case authz.Local:
		if !hostAllowed(rt.origins, r) {
			writeError(w, r, http.StatusForbidden, "forbidden",
				"no credential was sent, so the API answers only a loopback Host "+
					"(localhost, *.localhost, 127.0.0.1, [::1]); this request's Host is "+
					strconv.Quote(r.Host)+": send Authorization: Bearer <key>")
			return false
		}
		if crossOriginWrite(rt.origins, r) {
			writeError(w, r, http.StatusForbidden, "forbidden", crossOriginMsg(r))
			return false
		}
	case authz.Operator:
		if unsafe(r.Method) {
			if r.Header.Get(CSRFHeader) != "1" {
				writeError(w, r, http.StatusForbidden, "forbidden",
					"a Console session's "+r.Method+" needs the "+CSRFHeader+": 1 header")
				return false
			}
			if crossOriginWrite(rt.origins, r) {
				writeError(w, r, http.StatusForbidden, "forbidden", crossOriginMsg(r))
				return false
			}
		}
	}
	return true
}

func crossOriginMsg(r *http.Request) string {
	return "cross-origin write refused: without an API key the API takes an unsafe method only from its own origin; " +
		"this request's Origin is " + strconv.Quote(r.Header.Get("Origin")) +
		" (Sec-Fetch-Site " + strconv.Quote(r.Header.Get("Sec-Fetch-Site")) + ")"
}

// unsafe reports whether method changes state.
func unsafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// ── CORS (S4.6) ────────────────────────────────────────────────────

// cors answers the API and ingest trees for a client served from
// another origin, and only for the origins AllowOrigins names: with
// none configured no CORS header is ever sent (a page on another origin
// cannot read an answer, whoever it is). The allowed origin is echoed,
// never with credentials: a key rides a header, and the session cookie
// is SameSite=Strict, so a cross-origin page is never a cookie caller.
// The Local principal never gets them either (wrap strips them once the
// caller is known): a request with no credential at all must not be
// readable by another origin's page, allowed or not.
func (rt *Router) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// The answer depends on the Origin — the refusals too: a shared
		// cache must not hand an answer made for a refused origin to an
		// allowed one.
		w.Header().Add("Vary", "Origin")
		if origin != "" && originAllowed(rt.origins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers",
				"Authorization, Content-Type, Last-Event-ID, "+reqlog.Header)
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

// stripCORS removes the CORS answer cors set (the Local principal's).
func stripCORS(h http.Header) {
	for _, k := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers", "Access-Control-Expose-Headers"} {
		h.Del(k)
	}
}

// originAllowed matches an origin against the configured list ("*"
// matches any); with none configured, nothing is allowed.
func originAllowed(origins []string, origin string) bool {
	for _, o := range origins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// crossOriginWrite is the CSRF guard of the callers without a key: the
// loopback Host proves the request reached this machine, not that this
// origin's page sent it — any site's page can POST text/plain with mode
// "no-cors" and no preflight. An unsafe method (anything but GET, HEAD,
// OPTIONS) is cross-origin when its Origin is neither scheme://Host nor
// an AllowOrigins entry, or, with no Origin, when Sec-Fetch-Site says
// cross-site. A client sending neither header (curl, an SDK) is not a
// browser page and passes.
func crossOriginWrite(origins []string, r *http.Request) bool {
	if !unsafe(r.Method) {
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

// hostAllowed is the DNS-rebinding guard of the Local principal: the
// request's Host (the browser's idea of the origin's host, which a
// rebound page cannot fake) must be a loopback name — localhost,
// *.localhost (browsers resolve those to loopback themselves),
// 127.0.0.0/8 or [::1], any port — or the host[:port] of an
// AllowOrigins entry ("*" lets every Host through). Proxy headers
// (X-Forwarded-Host, Forwarded) are never read: anyone can send them.
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
