package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/weftbase/weft/version"
	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/internal/release"
)

// tok is the tests' API key: a secret key of the default project's test
// environment, imported as the bootstrap key.
const tok = "wb_sk_test_tok"

// syncBuffer is a stdout two goroutines may share.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// freeBase is a loopback port whose n-1 successors were free too.
func freeBase(t *testing.T, n int) int {
	t.Helper()
	for range 50 {
		var held []net.Listener
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, ln)
		p := ln.Addr().(*net.TCPAddr).Port
		ok := p+n-1 <= 65535
		for i := 1; ok && i < n; i++ {
			l, err := net.Listen("tcp", loop(p+i))
			if err != nil {
				ok = false
				break
			}
			held = append(held, l)
		}
		for _, l := range held {
			_ = l.Close()
		}
		if ok {
			return p
		}
	}
	t.Fatalf("no run of %d free ports", n)
	return 0
}

func loop(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

// discoveryDir points discovery's locations at a fresh directory.
func discoveryDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := discovery.Dirs
	discovery.Dirs = func() []string { return []string{dir} }
	t.Cleanup(func() { discovery.Dirs = old })
	return dir
}

// testServer is a kernel node under httptest for the API commands.
type testServer struct{ app *weftbase.App }

func (s testServer) Handler() http.Handler { return s.app.Handler() }
func (s testServer) Close() error          { return s.app.Shutdown(context.Background()) }

// newServer is the kernel over a temp data dir with key imported.
func newServer(t *testing.T, key string, opts ...weftbase.Option) testServer {
	t.Helper()
	app, err := weftbase.New(append([]weftbase.Option{weftbase.DataDir(t.TempDir()), weftbase.BootstrapKey(key),
		weftbase.Logger(slog.New(slog.DiscardHandler))}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return testServer{app}
}

// get is GET url with key as the bearer: the status and body.
func get(t *testing.T, url, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// waitServing waits until something answers /healthz at addr.
func waitServing(t *testing.T, addr string, done <-chan int, out *syncBuffer) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = resp.Body.Close()
			return
		}
		select {
		case code := <-done:
			t.Fatalf("weftbase exited %d before serving on %s (stdout %q)", code, addr, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("weftbase never served on %s (stdout %q)", addr, out.String())
		}
	}
}

// stopServer sends SIGTERM and waits for the command's exit code.
func stopServer(t *testing.T, done <-chan int) int {
	t.Helper()
	signalSelf(t, syscall.SIGTERM)
	select {
	case code := <-done:
		return code
	case <-time.After(15 * time.Second):
		t.Fatal("weftbase never returned after SIGTERM")
		return -1
	}
}

// startCmd runs the command line in the background until it serves at
// addr; stop is SIGTERM and the exit code.
func startCmd(t *testing.T, args []string, addr string, out *syncBuffer) (stop func() int) {
	t.Helper()
	skipWithoutSelfSignal(t)
	done := make(chan int, 1)
	go func() { done <- run(args, out, out) }()
	waitServing(t, addr, done, out)
	stopped := false
	stop = func() int {
		if stopped {
			return 0
		}
		stopped = true
		return stopServer(t, done)
	}
	t.Cleanup(func() { stop() })
	return stop
}

// TestVersion: weftbase's version and the weft it embeds on one line.
func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out, errb strings.Builder
		if code := run(args, &out, &errb); code != 0 {
			t.Fatalf("weftbase %v = exit %d: %s", args, code, errb.String())
		}
		if want := "weftbase " + release.Version + " (weft " + version.Runtime() + ")\n"; out.String() != want {
			t.Errorf("weftbase %v printed %q, want %q", args, out.String(), want)
		}
	}
}

// TestDevServer pins `weftbase dev` without an app: the banner (no key
// printed), the bootstrap key created once in <data>/bootstrap.key
// (0600) and kept across restarts, the discovery file (url and key,
// 0600), the manifest found upward, a one-time sign-in link that
// exchanges once for the Console cookie, every /v1 route behind the
// key, a second start on the data dir refused, and the discovery file
// removed on a clean exit.
func TestDevServer(t *testing.T) {
	disc := discoveryDir(t)
	tree := t.TempDir()
	wd := filepath.Join(tree, "app")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(tree, "weft.json")
	if err := os.WriteFile(manifest, []byte(`{"weft":1,"agents":[{"name":"found-upward"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wd)
	for _, k := range []string{"WEFT_MANIFEST", "WEFTBASE_ADDR", "WEFTBASE_DATA", "WEFTBASE_TOKEN", "WEFTBASE_URL"} {
		t.Setenv(k, "")
	}
	data := filepath.Join(t.TempDir(), "data")
	addr := loop(freeBase(t, 1))
	var out syncBuffer
	stop := startCmd(t, []string{"dev", "--addr", addr, "--data", data}, addr, &out)

	keyPath := filepath.Join(data, "bootstrap.key")
	b, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSpace(string(b))
	if !strings.HasPrefix(key, "wb_sk_test_") {
		t.Fatalf("bootstrap key %q, want wb_sk_test_…", key)
	}
	if fi, _ := os.Stat(keyPath); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("bootstrap.key mode %v, want 0600", fi.Mode().Perm())
	}
	if strings.Contains(out.String(), key) {
		t.Errorf("the banner printed the key:\n%s", out.String())
	}
	for _, want := range []string{
		"weftbase: http://" + addr + "/\n",
		"weftbase: bootstrap key created in " + keyPath,
		"weftbase: manifest " + manifest + " (found upward)",
		"weftbase: apps find this server through ",
		"weftbase: sign in to the Console: http://" + addr + "/#signin=",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("banner lacks %q:\n%s", want, out.String())
		}
	}

	// The discovery file: this server, its key, 0600.
	dp := filepath.Join(disc, discovery.FileName)
	info, err := discovery.Read(dp)
	if err != nil || info.URL != "http://"+addr || info.Token != key {
		t.Errorf("discovery file = %+v (%v), want the url and the key", info, err)
	}
	if fi, _ := os.Stat(dp); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("discovery file mode %v, want 0600", fi.Mode().Perm())
	}

	// The key opens /v1; no key is a 401.
	if code, _ := get(t, "http://"+addr+"/v1/runs", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /v1/runs without a key = %d, want 401", code)
	}
	if code, body := get(t, "http://"+addr+"/v1/manifest", key); code != http.StatusOK || !strings.Contains(body, "found-upward") {
		t.Errorf("GET /v1/manifest = %d %s, want the weft.json found upward", code, body)
	}

	// The sign-in link: one exchange for the cookie, never two.
	i := strings.Index(out.String(), "#signin=")
	link := out.String()[i+len("#signin="):]
	link = link[:strings.IndexAny(link, " \n")]
	exchange := func() *http.Response {
		resp, err := http.Post("http://"+addr+"/v1/auth/sessions", "application/json", strings.NewReader(`{"token":"`+link+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp := exchange(); resp.StatusCode != http.StatusCreated || len(resp.Cookies()) != 1 || resp.Cookies()[0].Name != "wb_session" {
		t.Errorf("sign in = %d %v, want 201 and the wb_session cookie", resp.StatusCode, resp.Cookies())
	} else {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/meta", nil)
		req.AddCookie(resp.Cookies()[0])
		r2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r2.Body.Close()
		if r2.StatusCode != http.StatusOK {
			t.Errorf("GET /v1/meta with the session cookie = %d", r2.StatusCode)
		}
	}
	if resp := exchange(); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a second exchange of the link = %d, want 401", resp.StatusCode)
	}

	// A second dev on the same data dir is refused, naming the first.
	_, errb, code := cli("dev", "--data", data)
	if want := "weftbase: weftbase already running at http://" + addr + " (pid " + strconv.Itoa(os.Getpid()) + ") on " + data; code != 1 || !strings.HasPrefix(errb, want) {
		t.Errorf("second dev = exit %d %q, want %q…", code, errb, want)
	}

	if code := stop(); code != 0 {
		t.Errorf("exit %d after SIGTERM", code)
	}
	if _, err := os.Stat(dp); !os.IsNotExist(err) {
		t.Errorf("the discovery file outlived the server: %v", err)
	}

	// A restart keeps the key and says so.
	var again syncBuffer
	stop = startCmd(t, []string{"dev", "--addr", addr, "--data", data}, addr, &again)
	if !strings.Contains(again.String(), "weftbase: bootstrap key in "+keyPath+"\n") {
		t.Errorf("restart banner:\n%s", again.String())
	}
	if code, _ := get(t, "http://"+addr+"/v1/meta", key); code != http.StatusOK {
		t.Errorf("the kept key after a restart = %d", code)
	}
	stop()
}

// TestBootstrapKeyFile: created 0600 once, read back after, and a file
// holding anything but a wb_sk_test_ key is an error naming it.
func TestBootstrapKeyFile(t *testing.T) {
	data := t.TempDir()
	k1, path, created, err := bootstrapKey(data)
	if err != nil || !created || !strings.HasPrefix(k1, "wb_sk_test_") {
		t.Fatalf("first = %q %v %v", k1, created, err)
	}
	k2, _, created, err := bootstrapKey(data)
	if err != nil || created || k2 != k1 {
		t.Errorf("second = %q created %v %v, want the same key", k2, created, err)
	}
	if err := os.WriteFile(path, []byte("wb_pk_live_nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := bootstrapKey(data); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("a bad key file = %v, want an error naming it", err)
	}
}

// TestServeRoles pins `weftbase serve --roles`: an ingest-only node
// serves OTLP (behind a key) and no /v1; an api-only node serves /v1 and
// no OTLP; WEFTBASE_BOOTSTRAP_KEY is imported; serve writes no discovery
// file; an unknown role is a usage error.
func TestServeRoles(t *testing.T) {
	disc := discoveryDir(t)
	t.Setenv("WEFTBASE_BOOTSTRAP_KEY", tok)
	t.Setenv("WEFTBASE_ADDR", "")
	t.Chdir(t.TempDir())

	addr := loop(freeBase(t, 1))
	var out syncBuffer
	stop := startCmd(t, []string{"serve", "--roles", "ingest", "--addr", addr, "--data", t.TempDir()}, addr, &out)
	if code, _ := get(t, "http://"+addr+"/v1/meta", tok); code != http.StatusNotFound {
		t.Errorf("ingest-only GET /v1/meta = %d, want 404", code)
	}
	resp, err := http.Post("http://"+addr+"/otlp/v1/traces", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ingest without a key = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(out.String(), "(roles ingest)") || !strings.Contains(out.String(), "bootstrap key from WEFTBASE_BOOTSTRAP_KEY") {
		t.Errorf("serve banner:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(disc, discovery.FileName)); !os.IsNotExist(err) {
		t.Errorf("serve wrote a discovery file: %v", err)
	}
	stop()

	addr = loop(freeBase(t, 1))
	out = syncBuffer{}
	stop = startCmd(t, []string{"serve", "--roles", "api", "--console=false", "--addr", addr, "--data", t.TempDir()}, addr, &out)
	code, body := get(t, "http://"+addr+"/v1/meta", tok)
	var meta struct {
		Capabilities []string `json:"capabilities"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &meta) != nil || len(meta.Capabilities) != 0 {
		t.Errorf("api-only meta = %d %s, want no ingest capability", code, body)
	}
	resp, err = http.Post("http://"+addr+"/otlp/v1/traces", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("api-only OTLP = %d, want 404", resp.StatusCode)
	}
	if strings.Contains(out.String(), "#signin=") {
		t.Errorf("--console=false printed a sign-in link:\n%s", out.String())
	}
	stop()

	if _, errb, code := cli("serve", "--roles", "api,bogus"); code != 2 || !strings.Contains(errb, `unknown role "bogus"`) {
		t.Errorf("--roles bogus = exit %d %q", code, errb)
	}
	if _, errb, code := cli("serve", "--control-db", "postgres://x", "--addr", loop(freeBase(t, 1))); code != 1 || !strings.Contains(errb, "not supported yet") {
		t.Errorf("--control-db postgres:// = exit %d %q", code, errb)
	}
}

// TestPortPolicy: a foreign listener on the wanted port moves the start
// to the next free one (said in one line); a pinned busy address fails.
func TestPortPolicy(t *testing.T) {
	discoveryDir(t)
	t.Setenv("WEFTBASE_ADDR", "")
	base := freeBase(t, 2)
	foreign, err := net.Listen("tcp", loop(base))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	go func() {
		for {
			c, err := foreign.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	setVar(t, &devDefaultAddr, loop(base))
	setVar(t, &devSpan, 2)
	var out syncBuffer
	stop := startCmd(t, []string{"dev", "--data", t.TempDir()}, loop(base+1), &out)
	if !strings.Contains(out.String(), "listening on "+loop(base+1)+" instead") {
		t.Errorf("no move line:\n%s", out.String())
	}
	stop()

	_, errb, code := cli("dev", "--addr", loop(base), "--data", t.TempDir())
	if code != 1 || !strings.Contains(errb, loop(base)) {
		t.Errorf("pinned busy = exit %d %q, want the address in the error", code, errb)
	}
}

// TestBareDoctorFindsDevServer: a dev server on a free port is found by
// bare `weftbase doctor` and `weftbase runs` through the discovery file,
// its key from the file, said on stderr.
func TestBareDoctorFindsDevServer(t *testing.T) {
	dir := discoveryDir(t)
	for _, k := range []string{"WEFTBASE_URL", "WEFTBASE_TOKEN", "WEFTBASE_DISCOVERY", "WEFTBASE_ADDR", "WEFTBASE_DATA"} {
		t.Setenv(k, "")
	}
	t.Chdir(t.TempDir())
	addr := loop(freeBase(t, 1))
	url := "http://" + addr
	var banner syncBuffer
	startCmd(t, []string{"dev", "--addr", addr, "--data", t.TempDir()}, addr, &banner)
	file := filepath.Join(dir, discovery.FileName)
	from := "weftbase: server " + url + " (discovery file " + file + ")\n"
	out, errb, code := cli("doctor")
	if code != 0 || errb != from {
		t.Errorf("bare doctor = exit %d, stderr %q, want %q\n%s", code, errb, from, out)
	}
	for _, want := range []string{
		"ok   server    reachable at " + url + " (weft ",
		"ok   key       accepted: key sk ",
		"ok   ready     ",
		"ok   discovery " + file + " -> " + url + " (this server)",
		"ok   otlp      OTLP at " + url + "/otlp/v1/traces",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if _, errb, code := cli("runs"); code != 0 || !strings.HasPrefix(errb, from) || !strings.Contains(errb, "no runs match") {
		t.Errorf("bare runs = exit %d, stderr %q", code, errb)
	}
	if out, errb, code := cli("open"); code != 0 || out != url+"/\n" || errb != from {
		t.Errorf("bare open = exit %d %q %q, want the root, no credential", code, out, errb)
	}
}

// TestManifestFlagErrors: a manifest that does not parse or cannot be
// read is a start error; none found is a note.
func TestManifestFlagErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manifestOptions(bad, dir); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("a bad manifest = %v", err)
	}
	if _, _, err := manifestOptions(filepath.Join(dir, "missing.json"), dir); err == nil || !strings.Contains(err.Error(), "--manifest") {
		t.Errorf("a missing --manifest = %v", err)
	}
	t.Setenv("WEFT_MANIFEST", "")
	if opts, note, err := manifestOptions("", dir); err != nil || opts != nil || !strings.Contains(note, "no weft.json") {
		t.Errorf("none found = %v %q %v", opts, note, err)
	}
}

// TestHTTPServerTimeouts: the header read and idle keep-alives are
// bounded; the write side is not (live streams).
func TestHTTPServerTimeouts(t *testing.T) {
	s := httpServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout == 0 || s.IdleTimeout == 0 || s.WriteTimeout != 0 {
		t.Errorf("timeouts: header %v idle %v write %v", s.ReadHeaderTimeout, s.IdleTimeout, s.WriteTimeout)
	}
}

// TestMaskDSN: a DSN's password is never echoed.
func TestMaskDSN(t *testing.T) {
	for in, want := range map[string]string{
		"clickhouse://u:p@ss@h:9000/db": "clickhouse://u:***@h:9000/db",
		"clickhouse://u@h/db":           "clickhouse://u@h/db",
		"sqlite://x.db":                 "sqlite://x.db",
	} {
		if got := maskDSN(in); got != want {
			t.Errorf("maskDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

var _ = httptest.NewServer
