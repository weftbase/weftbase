// Package console serves the Weftbase Console: the single-page app
// built from console/app (Bun, TanStack Start in SPA mode, shadcn) and
// committed to console/dist, so `go install` needs no Bun.
//
// The package only embeds and serves. It imports the standard library
// and nothing of weftbase; the API it talks to is the public /v1
// surface, mounted by api/http beside it.
package console

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// dist is the committed Console build. console/app's build writes it
// (index.html, assets/<hash>.js|css, fonts/, csp.json); the freshness
// gate (make console-check) keeps the two in sync.
//
//go:embed all:dist
var dist embed.FS

// Options configures Handler.
type Options struct {
	// Base is the URL path the Console is mounted at, with leading and
	// trailing slash ("/" when empty). It is written into the shell's
	// <base href> per request, and the router reads its basepath from
	// document.baseURI, so one build mounts anywhere.
	Base string
	// Title overrides the shell's <title> when not empty.
	Title string
}

var (
	// inlineScript finds the shell's inline <script> blocks: each needs
	// its sha256 in the CSP when the build did not write csp.json.
	inlineScript = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

	titleTag = regexp.MustCompile(`(?is)<title[^>]*>.*?</title>`)

	// baseTag is the rewrite target the shell carries (<base href="/">);
	// the handler replaces it with the mount base.
	baseTag = []byte(`<base href="/">`)
)

// handler serves the embedded build.
type handler struct {
	base  string
	shell []byte
	csp   string
}

// Handler returns the Console's handler: a build file when the path
// names one (hashed assets under assets/ are immutable), a 404 for a
// missing asset, and the shell with its <base href> rewritten for
// every other GET (the history fallback that keeps deep links alive on
// reload). It sets the Content-Security-Policy on its own responses
// only. Methods other than GET and HEAD are 405.
func Handler(o Options) http.Handler {
	shell, err := dist.ReadFile("dist/index.html")
	if err != nil {
		panic("console: dist/index.html missing: the committed Console build is required")
	}
	if o.Title != "" {
		if t := titleTag.FindIndex(shell); t != nil {
			shell = bytes.Replace(shell, shell[t[0]:t[1]],
				[]byte("<title>"+html.EscapeString(o.Title)+"</title>"), 1)
		}
	}
	return &handler{base: normBase(o.Base), shell: shell, csp: cspOf(shell)}
}

// normBase gives a base path a leading and a trailing slash.
func normBase(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// cspOf is the Content-Security-Policy for the shell: the one the build
// computed (dist/csp.json, {"script_hashes": ["sha256-…"]}) when
// present, else hashes of the shell's inline scripts. Self-only
// everywhere, data: images, inline styles, and scripts from self plus
// a sha256 per inline block, never 'unsafe-inline' for scripts.
func cspOf(shell []byte) string {
	csp := "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'"
	if b, err := dist.ReadFile("dist/csp.json"); err == nil {
		var doc struct {
			ScriptHashes []string `json:"script_hashes"`
		}
		if json.Unmarshal(b, &doc) == nil {
			for _, h := range doc.ScriptHashes {
				csp += " '" + h + "'"
			}
			return csp
		}
	}
	return CSPFor(shell)
}

// CSPFor computes the policy from the shell's inline scripts. The HTML
// parser replaces NUL bytes in script text with U+FFFD and the browser
// hashes the parsed text, so NULs are normalized the same way first.
func CSPFor(shell []byte) string {
	csp := "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'"
	for _, m := range inlineScript.FindAllSubmatch(shell, -1) {
		attrs, body := m[1], m[2]
		if strings.Contains(strings.ToLower(string(attrs)), "src") {
			continue // external script, covered by 'self'
		}
		if len(bytes.TrimSpace(body)) == 0 {
			continue
		}
		body = bytes.ReplaceAll(body, []byte{0}, []byte("�"))
		sum := sha256.Sum256(body)
		csp += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return csp
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("Content-Security-Policy", h.csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hd.Set("Allow", "GET, HEAD")
		http.Error(w, "the Console is static: GET only", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	if p == "" {
		p = "/"
	}
	if h.serveFile(w, r, p) {
		return
	}
	if strings.HasPrefix(p, "/assets/") {
		// A missing hashed asset (a stale tab after an upgrade) is a
		// 404, never the HTML shell under a script's name.
		http.NotFound(w, r)
		return
	}
	h.serveShell(w, r)
}

// serveFile answers with an embedded build file when the path names
// one other than the shell. Returns false when it names none.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, urlPath string) bool {
	name := strings.TrimPrefix(path.Clean(urlPath), "/")
	if name == "" || name == "index.html" || name == "csp.json" {
		return false
	}
	f, err := dist.Open("dist/" + name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	if strings.HasPrefix(urlPath, "/assets/") {
		// Content-hashed filenames: the bytes at a name never change.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, dist, "dist/"+name)
	return true
}

// serveShell answers with the SPA shell, its <base href> rewritten to
// the mount base.
func (h *handler) serveShell(w http.ResponseWriter, r *http.Request) {
	body := h.shell
	tag := []byte(`<base href="` + html.EscapeString(h.base) + `">`)
	if bytes.Contains(body, baseTag) {
		body = bytes.Replace(body, baseTag, tag, 1)
	} else if i := bytes.Index(body, []byte("<head>")); i >= 0 {
		// The build dropped the marker: inject rather than misroute.
		i += len("<head>")
		body = append(append(append([]byte{}, body[:i]...), tag...), body[i:]...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// Files lists every path in the build other than the shell and its
// csp.json.
func Files() []string {
	var out []string
	_ = fs.WalkDir(dist, "dist", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != "dist/index.html" && p != "dist/csp.json" {
			out = append(out, strings.TrimPrefix(p, "dist/"))
		}
		return nil
	})
	return out
}
