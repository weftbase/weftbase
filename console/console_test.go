package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve answers one request against h.
func serve(t *testing.T, h http.Handler, method, path string) (int, http.Header, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	b, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return w.Code, w.Header(), string(b)
}

// TestShellAndFallback: every GET that names no build file is the
// shell — deep links survive reload — with its <base href> the mount
// base, the security headers and a self-only CSP.
func TestShellAndFallback(t *testing.T) {
	h := Handler(Options{Base: "/weftbase/"})
	code, hdr, body := serve(t, h, http.MethodGet, "/")
	if code != http.StatusOK {
		t.Fatalf("shell: %d", code)
	}
	if !strings.Contains(body, `<base href="/weftbase/">`) {
		t.Errorf("shell missing base rewrite: %s", body)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("shell content-type %q", ct)
	}
	if cc := hdr.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("shell cache-control %q", cc)
	}
	csp := hdr.Get("Content-Security-Policy")
	_, script, _ := strings.Cut(csp, "script-src")
	if !strings.HasPrefix(csp, "default-src 'self'") || script == "" || strings.Contains(script, "unsafe-inline") {
		t.Errorf("CSP %q, want self-only scripts", csp)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("security headers missing: %v", hdr)
	}
	for _, p := range []string{"/runs", "/runs/r_ok", "/agents", "/index.html", "/csp.json"} {
		if _, _, b := serve(t, h, http.MethodGet, p); !strings.Contains(b, `<base href="/weftbase/">`) {
			t.Errorf("%s is not the shell", p)
		}
	}
	// HEAD answers the headers alone.
	if code, _, b := serve(t, h, http.MethodHead, "/runs"); code != http.StatusOK || b != "" {
		t.Errorf("HEAD = %d %q, want 200 and no body", code, b)
	}
}

// TestBaseAndTitle: Base is normalized to a leading and trailing slash
// ("/" when empty); Title replaces the shell's <title>, escaped.
func TestBaseAndTitle(t *testing.T) {
	for base, want := range map[string]string{"": "/", "/x/": "/x/", "x": "/x/", "/x": "/x/"} {
		if _, _, b := serve(t, Handler(Options{Base: base}), http.MethodGet, "/"); !strings.Contains(b, `<base href="`+want+`">`) {
			t.Errorf("Base(%q): want %s in %s", base, want, b)
		}
	}
	if _, _, b := serve(t, Handler(Options{Title: "dev server"}), http.MethodGet, "/"); !strings.Contains(b, "<title>dev server</title>") {
		t.Errorf("Title not rewritten: %s", b)
	}
	if _, _, b := serve(t, Handler(Options{Title: "a <b> & c"}), http.MethodGet, "/"); !strings.Contains(b, "<title>a &lt;b&gt; &amp; c</title>") {
		t.Errorf("Title not escaped: %s", b)
	}
}

// TestMissingAssetAndMethods: a missing hashed asset (a stale tab
// after an upgrade) is a 404, never the HTML shell under a script's
// name; the Console is static, so any method but GET and HEAD is 405.
func TestMissingAssetAndMethods(t *testing.T) {
	h := Handler(Options{})
	if code, hdr, _ := serve(t, h, http.MethodGet, "/assets/missing-0000.js"); code != http.StatusNotFound ||
		strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("missing asset: %d %q, want a non-HTML 404", code, hdr.Get("Content-Type"))
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		code, hdr, _ := serve(t, h, m, "/runs")
		if code != http.StatusMethodNotAllowed || hdr.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s = %d (Allow %q), want 405 GET, HEAD", m, code, hdr.Get("Allow"))
		}
	}
}

// TestAssetHeaders: hashed assets are immutable, other build files
// revalidate. Meaningful once a build is committed; the placeholder
// dist has the shell alone.
func TestAssetHeaders(t *testing.T) {
	h := Handler(Options{})
	for _, name := range Files() {
		code, hdr, _ := serve(t, h, http.MethodGet, "/"+name)
		if code != http.StatusOK {
			t.Errorf("%s = %d", name, code)
		}
		cc := hdr.Get("Cache-Control")
		if strings.HasPrefix(name, "assets/") {
			if cc != "public, max-age=31536000, immutable" {
				t.Errorf("%s cache-control %q", name, cc)
			}
		} else if cc != "no-cache" {
			t.Errorf("%s cache-control %q", name, cc)
		}
	}
}

// TestCSPHashMatchesParsedText: the CSP hashes must match what the
// browser computes over the PARSED script text — the HTML parser
// replaces NUL bytes with U+FFFD — so a NUL in an inline script hashes
// identically to its already-replaced form; an external script and an
// empty one add no hash.
func TestCSPHashMatchesParsedText(t *testing.T) {
	withNUL := CSPFor([]byte(`<script>x("a` + "\x00" + `");</script>`))
	withReplacement := CSPFor([]byte(`<script>x("a` + "�" + `");</script>`))
	if withNUL != withReplacement {
		t.Errorf("NUL script hashed differently than its parsed form:\n%s\n%s", withNUL, withReplacement)
	}
	if !strings.Contains(withNUL, "'sha256-") {
		t.Errorf("no hash emitted: %s", withNUL)
	}
	if got := CSPFor([]byte(`<script src="/assets/a.js"></script><script> </script>`)); strings.Contains(got, "sha256") {
		t.Errorf("an external or empty script hashed: %s", got)
	}
}
