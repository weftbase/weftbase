package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// TestAuthentication: no credential is 401 with a challenge, a bad key
// 401, a good key through; a credential in the URL is refused even
// beside a good header.
func TestAuthentication(t *testing.T) {
	s := newStack(t, stackOpts{})
	resp, body := s.do(t, req{path: "/v1/runs"})
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") ||
		!strings.Contains(body, `"code":"unauthorized"`) {
		t.Errorf("no credential: %d %q %s", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), body)
	}
	if resp, body := s.do(t, req{path: "/v1/runs", key: "wb_sk_test_bogus"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad key: %d %s", resp.StatusCode, body)
	}
	if resp, body := s.do(t, req{path: "/v1/runs", key: s.key}); resp.StatusCode != http.StatusOK {
		t.Errorf("good key: %d %s", resp.StatusCode, body)
	}
	resp, body = s.do(t, req{path: "/v1/runs?token=" + s.key, key: s.key})
	if resp.StatusCode != http.StatusUnauthorized || strings.Contains(body, s.key) {
		t.Errorf("token in the URL: %d %s", resp.StatusCode, body)
	}
	// The ingest routes authenticate too.
	if resp, _ := s.do(t, req{method: "POST", path: "/otlp/v1/traces", body: "{}", header: map[string]string{"Content-Type": "application/json"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ingest without a key: %d", resp.StatusCode)
	}
	if resp, body := s.do(t, req{method: "POST", path: "/otlp/v1/traces", body: "{}", key: s.key, header: map[string]string{"Content-Type": "application/json"}}); resp.StatusCode != http.StatusOK {
		t.Errorf("ingest with a key: %d %s", resp.StatusCode, body)
	}
	// Public routes need nothing.
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		if resp, body := s.do(t, req{path: p}); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
}

// TestCanPerRoute: a key whose kind may not perform a route's action is
// 403 naming the action; the routes it may use pass.
func TestCanPerRoute(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	_, pk, err := s.keys.Mint(ctx, tenant.Default(), keys.Publishable, "web", events.System)
	if err != nil {
		t.Fatal(err)
	}
	_, wk, err := s.keys.Mint(ctx, tenant.Default(), keys.Worker, "worker", events.System)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path, key string
		want              int
	}{
		{"GET", "/v1/meta", pk, http.StatusOK},
		{"GET", "/v1/runs", pk, http.StatusForbidden},
		{"DELETE", "/v1/runs/r1", pk, http.StatusForbidden},
		{"GET", "/v1/runs", wk, http.StatusForbidden},
		{"POST", "/otlp/v1/traces", wk, http.StatusOK},
		{"POST", "/otlp/v1/logs", pk, http.StatusForbidden},
	} {
		resp, body := s.do(t, req{method: c.method, path: c.path, key: c.key, body: "{}", header: map[string]string{"Content-Type": "application/json"}})
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, resp.StatusCode, body, c.want)
		}
		if c.want == http.StatusForbidden && !strings.Contains(body, "not allowed to") {
			t.Errorf("%s %s: the refusal names no action: %s", c.method, c.path, body)
		}
	}
}

// TestLocalPrincipalGuards (the DNS-rebinding guard, carried from the
// old open API): without a credential the API answers only a loopback
// Host or an AllowOrigins one, and an unsafe method only from its own
// origin. The Console carries no data and answers any Host; a key's
// request is never Host-checked.
func TestLocalPrincipalGuards(t *testing.T) {
	s := newStack(t, stackOpts{open: true, origins: []string{"http://myapp.internal:8080"}})
	for _, c := range []struct {
		host, path, key string
		want            int
	}{
		{"127.0.0.1:7331", "/v1/runs", "", http.StatusOK},
		{"localhost:3000", "/v1/runs", "", http.StatusOK},
		{"[::1]:7331", "/v1/runs", "", http.StatusOK},
		{"wb.localhost", "/v1/runs", "", http.StatusOK},
		{"myapp.internal:8080", "/v1/runs", "", http.StatusOK},
		{"evil.example:7331", "/v1/runs", "", http.StatusForbidden},
		{"evil.example:7331", "/v1/runs/stream?all=1", "", http.StatusForbidden},
		{"evil.example:7331", "/v1/events", "", http.StatusForbidden},
		{"evil.example:7331", "/", "", http.StatusOK},
		{"evil.example:7331", "/v1/runs", s.key, http.StatusOK},
	} {
		resp, body := s.do(t, req{path: c.path, host: c.host, key: c.key})
		if resp.StatusCode != c.want {
			t.Errorf("Host %s %s (key %v): %d %s, want %d", c.host, c.path, c.key != "", resp.StatusCode, body, c.want)
		}
	}
	// Cross-origin writes.
	for _, c := range []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"no origin (curl)", nil, http.StatusNotFound},
		{"same origin", map[string]string{"Origin": s.ts.URL}, http.StatusNotFound},
		{"other origin", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"null origin", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"allowed origin", map[string]string{"Origin": "http://myapp.internal:8080"}, http.StatusNotFound},
	} {
		resp, body := s.do(t, req{method: "DELETE", path: "/v1/runs/nope", header: c.header})
		if resp.StatusCode != c.want {
			t.Errorf("DELETE %s: %d %s, want %d", c.name, resp.StatusCode, body, c.want)
		}
	}
	// An Open authenticator never downgrades a bad key to Local.
	if resp, _ := s.do(t, req{path: "/v1/runs", key: "wb_sk_test_bogus"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("open with a bad key: %d", resp.StatusCode)
	}
}

// TestCORS: with no AllowOrigins no CORS header is sent at all; a
// configured origin is echoed, never with credentials; a preflight
// answers 204; every answer varies on Origin.
func TestCORS(t *testing.T) {
	bare := newStack(t, stackOpts{})
	resp, _ := bare.do(t, req{path: "/v1/runs", key: bare.key, header: map[string]string{"Origin": "http://localhost:5173"}})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" || !strings.Contains(resp.Header.Get("Vary"), "Origin") {
		t.Errorf("no AllowOrigins: CORS headers %v", resp.Header)
	}
	resp, _ = bare.do(t, req{method: "OPTIONS", path: "/v1/runs", header: map[string]string{"Origin": "http://127.0.0.1:3000", "Access-Control-Request-Method": "DELETE"}})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("no AllowOrigins: preflight answered %v", resp.Header)
	}

	s := newStack(t, stackOpts{origins: []string{"http://localhost:5173", "http://127.0.0.1:3000"}})
	resp, _ = s.do(t, req{path: "/v1/runs", key: s.key, header: map[string]string{"Origin": "http://localhost:5173"}})
	h := resp.Header
	if h.Get("Access-Control-Allow-Origin") != "http://localhost:5173" || h.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(h.Get("Access-Control-Expose-Headers"), "X-Request-Id") {
		t.Errorf("allowed origin headers: %v", h)
	}
	if strings.Contains(h.Get("Access-Control-Allow-Headers"), CSRFHeader) {
		t.Error("CORS admits the CSRF header: a cross-origin page could then send cookie writes")
	}
	resp, _ = s.do(t, req{path: "/v1/runs", key: s.key, header: map[string]string{"Origin": "http://evil.example"}})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" || !strings.Contains(resp.Header.Get("Vary"), "Origin") {
		t.Errorf("refused origin headers: %v", resp.Header)
	}
	resp, _ = s.do(t, req{method: "OPTIONS", path: "/v1/runs", header: map[string]string{"Origin": "http://127.0.0.1:3000", "Access-Control-Request-Method": "DELETE"}})
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Max-Age") == "" {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
}

// TestOpenModeNoCORS: a request with no credential (the Local
// principal) is never readable cross-origin, not even from an origin
// AllowOrigins names; a key from that origin is.
func TestOpenModeNoCORS(t *testing.T) {
	for _, origins := range [][]string{nil, {"http://localhost:9999"}} {
		s := newStack(t, stackOpts{open: true, origins: origins})
		for _, p := range []string{"/v1/meta", "/v1/runs", "/v1/approvals"} {
			resp, body := s.do(t, req{path: p, host: "127.0.0.1:7331", header: map[string]string{"Origin": "http://localhost:9999", "Sec-Fetch-Site": "same-site"}})
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("origins %v: Local %s -> %d with Access-Control-Allow-Origin %q: %.80s", origins, p, resp.StatusCode, got, body)
			}
		}
		if origins != nil {
			resp, _ := s.do(t, req{path: "/v1/runs", key: s.key, header: map[string]string{"Origin": "http://localhost:9999"}})
			if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:9999" {
				t.Errorf("a key from an allowed origin: %v", resp.Header)
			}
		}
	}
}

// TestConsoleSessionCookie: a one-time link signs in once for an
// HttpOnly, SameSite=Strict cookie; reads pass on the cookie alone;
// writes also need Weftbase-CSRF: 1 and a same-origin request;
// sign-out ends the session.
func TestConsoleSessionCookie(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	link, err := s.sessions.CreateLink(ctx, tenant.Default())
	if err != nil {
		t.Fatal(err)
	}
	resp, body := s.do(t, req{method: "POST", path: "/v1/auth/sessions", body: `{"token":"` + link + `"}`})
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"operator":"operator"`) {
		t.Fatalf("sign in: %d %s", resp.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "wb_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
		t.Fatalf("cookie = %+v (loopback: not Secure)", cookie)
	}
	if resp, _ := s.do(t, req{method: "POST", path: "/v1/auth/sessions", body: `{"token":"` + link + `"}`}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a used link: %d, want 401", resp.StatusCode)
	}
	if resp, _ := s.do(t, req{method: "POST", path: "/v1/auth/sessions", body: `{}`}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no token: %d, want 400", resp.StatusCode)
	}
	jar := map[string]string{"Cookie": "wb_session=" + cookie.Value}
	if resp, body := s.do(t, req{path: "/v1/meta", header: jar}); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"kind":"operator"`) {
		t.Errorf("cookie read: %d %s", resp.StatusCode, body)
	}
	with := func(extra map[string]string) map[string]string {
		h := map[string]string{"Cookie": jar["Cookie"]}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	for _, c := range []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"no CSRF header", jar, http.StatusForbidden},
		{"CSRF header", with(map[string]string{CSRFHeader: "1"}), http.StatusNotFound},
		{"CSRF header, other origin", with(map[string]string{CSRFHeader: "1", "Origin": "http://evil.example"}), http.StatusForbidden},
		{"CSRF header, same origin", with(map[string]string{CSRFHeader: "1", "Origin": s.ts.URL}), http.StatusNotFound},
	} {
		resp, body := s.do(t, req{method: "DELETE", path: "/v1/runs/nope", header: c.header})
		if resp.StatusCode != c.want {
			t.Errorf("cookie DELETE, %s: %d %s, want %d", c.name, resp.StatusCode, body, c.want)
		}
	}
	// A key needs no CSRF header.
	if resp, _ := s.do(t, req{method: "DELETE", path: "/v1/runs/nope", key: s.key}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("key DELETE: %d", resp.StatusCode)
	}
	// Sign-out: the CSRF rule applies, then the session is gone.
	if resp, _ := s.do(t, req{method: "DELETE", path: "/v1/auth/sessions/current", header: jar}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("sign out without CSRF: %d", resp.StatusCode)
	}
	resp, _ = s.do(t, req{method: "DELETE", path: "/v1/auth/sessions/current", header: with(map[string]string{CSRFHeader: "1"})})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("sign out: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, req{path: "/v1/meta", header: jar}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after sign-out: %d", resp.StatusCode)
	}

	// Off loopback the cookie is Secure.
	link, _ = s.sessions.CreateLink(ctx, tenant.Default())
	resp, _ = s.do(t, req{method: "POST", path: "/v1/auth/sessions", host: "weftbase.example.com", body: `{"token":"` + link + `"}`})
	if cs := resp.Cookies(); resp.StatusCode != http.StatusCreated || len(cs) != 1 || !cs[0].Secure {
		t.Errorf("sign in off loopback: %d %+v", resp.StatusCode, cs)
	}
}

// TestSessionChaining: a link a signed-in session mints joins its
// family: the session traded for it does not outlive the parent's
// sign-out (it used to be independent, with a fresh 30-day cap).
// DELETE /v1/auth/sessions ends every session (a secret key or an
// operator); sign-in and sign-out are audited with the session's ref.
func TestSessionChaining(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	signIn := func(link string) map[string]string {
		t.Helper()
		resp, body := s.do(t, req{method: "POST", path: "/v1/auth/sessions", body: `{"token":"` + link + `"}`})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("sign in: %d %s", resp.StatusCode, body)
		}
		for _, c := range resp.Cookies() {
			if c.Name == "wb_session" {
				return map[string]string{"Cookie": "wb_session=" + c.Value, CSRFHeader: "1"}
			}
		}
		t.Fatal("no cookie")
		return nil
	}
	link, _ := s.sessions.CreateLink(ctx, tenant.Default())
	parent := signIn(link)
	resp, body := s.do(t, req{method: "POST", path: "/v1/auth/links", header: parent})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link from a session: %d %s", resp.StatusCode, body)
	}
	var doc struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &doc)
	child := signIn(doc.Token)
	if resp, _ := s.do(t, req{path: "/v1/meta", header: child}); resp.StatusCode != http.StatusOK {
		t.Fatalf("child session: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, req{method: "DELETE", path: "/v1/auth/sessions/current", header: parent}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sign out: %d", resp.StatusCode)
	}
	if resp, _ := s.do(t, req{path: "/v1/meta", header: child}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the child session outlived its parent's sign-out: %d", resp.StatusCode)
	}

	// End all: a key may, a worker key may not.
	var cookies []map[string]string
	for range 2 {
		l, _ := s.sessions.CreateLink(ctx, tenant.Default())
		cookies = append(cookies, signIn(l))
	}
	_, wk, err := s.keys.Mint(ctx, tenant.Default(), keys.Worker, "w", events.System)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := s.do(t, req{method: "DELETE", path: "/v1/auth/sessions", key: wk}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("end all with a worker key: %d", resp.StatusCode)
	}
	resp, body = s.do(t, req{method: "DELETE", path: "/v1/auth/sessions", key: s.key})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"sessions_ended":2`) {
		t.Fatalf("end all: %d %s", resp.StatusCode, body)
	}
	for _, c := range cookies {
		if resp, _ := s.do(t, req{path: "/v1/meta", header: c}); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("a session outlived end all: %d", resp.StatusCode)
		}
	}

	// Audit: who signed in and out, by session ref.
	evs, err := s.log.List(ctx, events.ForScope(tenant.Default(), true), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var created, ended int
	var linkByRef bool
	for _, e := range evs {
		switch e.Type {
		case events.ConsoleSessionCreated:
			created++
		case events.ConsoleSessionEnded:
			ended++
		case events.ConsoleLinkCreated:
			linkByRef = linkByRef || strings.HasPrefix(e.Actor.Credential, "ses_")
		}
	}
	if created != 4 || ended != 2 || !linkByRef {
		t.Errorf("audit: %d created, %d ended, link by session ref %v; want 4, 2, true", created, ended, linkByRef)
	}
}

// TestConsoleFallthrough: the Console answers every path outside the
// API trees; /v1/*, /otlp/*, /healthz, /readyz and /metrics never fall
// through to it — an unknown one is a JSON 404, a wrong method a 405
// with Allow.
func TestConsoleFallthrough(t *testing.T) {
	s := newStack(t, stackOpts{})
	for _, p := range []string{"/", "/runs/r_1", "/settings"} {
		resp, body := s.do(t, req{path: p})
		if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(body), "<!doctype html") {
			t.Errorf("Console %s: %d %.80s", p, resp.StatusCode, body)
		}
	}
	for _, p := range []string{"/v1/nope", "/v1", "/otlp/v1/nope", "/otlp", "/v1/runs/a/1/c"} {
		resp, body := s.do(t, req{path: p, key: s.key})
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) {
			t.Errorf("%s: %d %s, want the JSON 404", p, resp.StatusCode, body)
		}
	}
	resp, body := s.do(t, req{method: "PUT", path: "/v1/runs", key: s.key})
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD, POST" || !strings.Contains(body, `"error"`) {
		t.Errorf("PUT /v1/runs: %d Allow %q %s", resp.StatusCode, resp.Header.Get("Allow"), body)
	}
	resp, _ = s.do(t, req{method: "GET", path: "/otlp/v1/traces", key: s.key})
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Errorf("GET /otlp/v1/traces: %d Allow %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	// No /api tree remains.
	if resp, body := s.do(t, req{path: "/api/runs", key: s.key}); resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(body), "<!doctype html") {
		t.Errorf("/api/runs: %d, want the Console's shell (no API there)", resp.StatusCode)
	}
	// With the Console off, the root is a JSON 404.
	off := newStack(t, stackOpts{noUI: true})
	if resp, body := off.do(t, req{path: "/"}); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Errorf("Console off: %d %s", resp.StatusCode, body)
	}
}

// TestChildIDEncoding: ids are percent-encoded in paths and decoded
// once: a child run id's "/" travels as %2F, a literal "%2F" in an id
// as %252F.
func TestChildIDEncoding(t *testing.T) {
	s := newStack(t, stackOpts{})
	for _, c := range []struct{ path, id string }{
		{"/v1/runs/r_p%2F0%2Fcall_1", "r_p/0/call_1"},
		{"/v1/runs/r_p%2F0%2Fcall_1/events", "r_p/0/call_1"},
		{"/v1/runs/a%252Fb", "a%2Fb"},
	} {
		resp, body := s.do(t, req{path: c.path, key: s.key})
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "run "+c.id) {
			t.Errorf("%s: %d %s, want a 404 naming run %s", c.path, resp.StatusCode, body, c.id)
		}
	}
}

// TestRequestIDAndMetrics: every answer carries X-Request-Id (the
// client's well-formed one echoed); /metrics counts routes and ingest
// batches.
func TestRequestIDAndMetrics(t *testing.T) {
	s := newStack(t, stackOpts{})
	resp, _ := s.do(t, req{path: "/healthz", header: map[string]string{"X-Request-Id": "abc-1"}})
	if resp.Header.Get("X-Request-Id") != "abc-1" {
		t.Errorf("echoed id = %q", resp.Header.Get("X-Request-Id"))
	}
	s.do(t, req{path: "/v1/runs", key: s.key})
	s.do(t, req{method: "POST", path: "/otlp/v1/traces", key: s.key, body: "{not otlp", header: map[string]string{"Content-Type": "application/json"}})
	_, body := s.do(t, req{path: "/metrics"})
	for _, want := range []string{
		`weftbase_http_requests_total{route="GET /v1/runs",code="200"} 1`,
		`weftbase_http_requests_total{route="GET /healthz",code="200"} 1`,
		`weftbase_ingest_batches_total{outcome="refused"} 1`,
		`# TYPE weftbase_http_requests_total counter`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
}

// POST /v1/auth/links mints a fresh one-time link on a running node
// for a secret key (never a worker or publishable key, never without a
// credential), and the link signs in.
func TestSignInLinkFromARunningNode(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	if resp, _ := s.do(t, req{method: "POST", path: "/v1/auth/links"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", resp.StatusCode)
	}
	_, wk, err := s.keys.Mint(ctx, tenant.Default(), keys.Worker, "w", events.System)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := s.do(t, req{method: "POST", path: "/v1/auth/links", key: wk}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a worker key: %d, want 403", resp.StatusCode)
	}
	resp, body := s.do(t, req{method: "POST", path: "/v1/auth/links", key: s.key, body: `{"origin":"http://localhost:3000/"}`})
	var doc struct {
		Link, Token string
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(body), &doc); resp.StatusCode != http.StatusCreated || err != nil ||
		doc.Link != "http://localhost:3000/#signin="+doc.Token || doc.Token == "" || time.Until(doc.ExpiresAt) < 14*time.Minute {
		t.Fatalf("mint: %d %s", resp.StatusCode, body)
	}
	if resp, body := s.do(t, req{method: "POST", path: "/v1/auth/links", key: s.key}); resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"link":"http://127.0.0.1:`) {
		t.Errorf("default origin: %d %s", resp.StatusCode, body)
	}
	if resp, _ := s.do(t, req{method: "POST", path: "/v1/auth/links", key: s.key, body: `{"origin":"javascript:alert(1)"}`}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad origin: %d, want 400", resp.StatusCode)
	}
	if resp, body := s.do(t, req{method: "POST", path: "/v1/auth/sessions", body: `{"token":"` + doc.Token + `"}`}); resp.StatusCode != http.StatusCreated {
		t.Errorf("signing in with the minted link: %d %s", resp.StatusCode, body)
	}
}
