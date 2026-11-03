package studio

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// getWith issues a GET with a bearer token and optional Origin. It
// never drains the body of an SSE response: those stream forever, so
// the status is read and the connection closed.
func getWith(t *testing.T, h http.Handler, path, bearer, origin string) (int, http.Header, string) {
	t.Helper()
	srv := httptest.NewServer(mounted(h))
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp.StatusCode, resp.Header, ""
	}
	return resp.StatusCode, resp.Header, readAll(t, resp)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestTokenAuth pins the token: with a Token configured the API
// refuses anonymous and wrong bearers; without one it is open.
func TestTokenAuth(t *testing.T) {
	closed := Handler(DB(fixtureDB(t)), Token("dev-secret"))
	if code, _, body := getWith(t, closed, "/studio/api/runs", "", ""); code != http.StatusUnauthorized ||
		!strings.Contains(body, `"unauthorized"`) {
		t.Errorf("anonymous: %d %s", code, body)
	}
	if code, _, _ := getWith(t, closed, "/studio/api/runs", "wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("wrong bearer: %d", code)
	}
	if code, _, _ := getWith(t, closed, "/studio/api/meta", "", ""); code != http.StatusUnauthorized {
		t.Errorf("meta anonymous: %d", code)
	}
	if code, _, body := getWith(t, closed, "/studio/api/runs", "dev-secret", ""); code != http.StatusOK ||
		!strings.Contains(body, `"total":4`) {
		t.Errorf("right bearer: %d %s", code, body)
	}
	// A token in the URL is never accepted — not even the right one,
	// not even beside a good bearer, and the refusal does not echo it.
	for _, bearer := range []string{"", "dev-secret"} {
		code, _, body := getWith(t, closed, "/studio/api/live?run=r_ok&token=dev-secret", bearer, "")
		if code != http.StatusUnauthorized || strings.Contains(body, "dev-secret") {
			t.Errorf("token query parameter (bearer %q): %d %s, want 401", bearer, code, body)
		}
	}
	if code, _, _ := getWith(t, closed, "/studio/api/live?run=r_ok", "dev-secret", ""); code != http.StatusOK {
		t.Errorf("live with the bearer: %d", code)
	}
	// Setup A: open.
	if code, _, _ := getWith(t, Handler(DB(fixtureDB(t))), "/studio/api/runs", "", ""); code != http.StatusOK {
		t.Errorf("open setup: %d", code)
	}
}

// TestCORS pins S4.6's origins: the localhost default when a token is
// configured, explicit AllowOrigins, nothing same-origin (setup A),
// and preflights answered before routing.
func TestCORS(t *testing.T) {
	h := Handler(DB(fixtureDB(t)), Token("dev"))
	preflight := func(h http.Handler, path, origin string) (int, http.Header) {
		srv := httptest.NewServer(mounted(h))
		t.Cleanup(srv.Close)
		req, err := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, resp.Header
	}

	// Any localhost port, http or https.
	for _, origin := range []string{"http://localhost:3000", "http://127.0.0.1:5173", "https://localhost"} {
		code, hdr := preflight(h, "/studio/api/runs", origin)
		if code != http.StatusNoContent || hdr.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("%s: %d %v", origin, code, hdr)
		}
		if vary := hdr.Get("Vary"); !strings.Contains(vary, "Origin") {
			t.Errorf("Vary: %q", vary)
		}
		// Never with credentials: the bearer rides a header.
		if c := hdr.Get("Access-Control-Allow-Credentials"); c != "" {
			t.Errorf("%s: Allow-Credentials = %q, want none", origin, c)
		}
		// DELETE too: the delete capability's two verbs (plan A7).
		if m := hdr.Get("Access-Control-Allow-Methods"); !strings.Contains(m, "DELETE") {
			t.Errorf("%s: Allow-Methods = %q, want DELETE among them", origin, m)
		}
	}
	// A foreign origin gets no CORS headers; the preflight falls
	// through to the API's own answer (here the token check's 401),
	// which fails the browser's preflight exactly as intended.
	code, hdr := preflight(h, "/studio/api/runs", "https://evil.example")
	if hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin allowed: %v", hdr)
	}
	if code == http.StatusNoContent {
		t.Errorf("foreign preflight answered 204: %v", hdr)
	}
	// An actual GET with an allowed origin carries the header; the
	// bearer still applies.
	code, hdr, _ = getWith(t, h, "/studio/api/meta", "dev", "http://localhost:3000")
	if code != http.StatusOK || hdr.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("GET with origin: %d %v", code, hdr)
	}
	// The request id is readable cross-origin (P8-fix-a 9): a client on
	// another origin can quote it in an error report.
	if got := hdr.Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Request-Id") || hdr.Get("X-Request-Id") == "" {
		t.Errorf("cross-origin GET: Expose-Headers %q, X-Request-Id %q", got, hdr.Get("X-Request-Id"))
	}
	// Explicit AllowOrigins replace the default.
	custom := Handler(DB(fixtureDB(t)), Token("dev"), AllowOrigins("https://app.example"))
	if code, hdr := preflight(custom, "/studio/api/runs", "https://app.example"); code != http.StatusNoContent ||
		hdr.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("explicit origin: %d %v", code, hdr)
	}
	if _, hdr := preflight(custom, "/studio/api/runs", "http://localhost:3000"); hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("default leaked past explicit list: %v", hdr)
	}
	// No token: same origin, no CORS at all.
	open := Handler(DB(fixtureDB(t)))
	if _, hdr := preflight(open, "/studio/api/runs", "http://localhost:3000"); hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("open handler sent CORS: %v", hdr)
	}
}
