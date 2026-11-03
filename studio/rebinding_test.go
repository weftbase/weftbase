package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rebindingServer is a server over a temp database, AllowOrigins
// naming one real hostname, and token as the server token ("" is the
// open API).
func rebindingServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	opts := []Option{
		Open(t.TempDir() + "/rebind.db"),
		AllowOrigins("http://myapp.internal:8080"),
	}
	if token != "" {
		opts = append(opts, Token(token))
	}
	srv := New(opts...)
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestDNSRebindingGuard pins the Host rule for the open API (no Token):
// a DNS-rebound page (http://evil.example:7331 resolving to 127.0.0.1)
// is same-origin to the browser, so without the rule it could read
// transcripts, delete runs and hold the live stream. The whole /api
// tree answers only a loopback Host or one AllowOrigins names; the
// Console carries no data and stays open. With a Token the bearer is
// the defence and the Host does not matter.
func TestDNSRebindingGuard(t *testing.T) {
	const tok = "tok_rebind"
	hosts := []string{"127.0.0.1:7331", "localhost:3000", "[::1]:7331", "studio.localhost", "evil.example:7331", "myapp.internal:8080"}
	routes := []struct {
		name, method, path, body string
		open                     int // the status when the request is let through
	}{
		{"runs", http.MethodGet, "/api/runs", "", http.StatusOK},
		{"search", http.MethodGet, "/api/search?q=hi", "", http.StatusOK},
		{"traces", http.MethodGet, "/api/traces", "", http.StatusOK},
		{"live", http.MethodGet, "/api/live?session=s_1", "", http.StatusOK},
		{"shell", http.MethodGet, "/", "", http.StatusOK},
	}
	for _, token := range []string{"", tok} {
		ts := rebindingServer(t, token)
		for _, host := range hosts {
			for _, rt := range routes {
				want := rt.open
				refused := token == "" && host == "evil.example:7331" && strings.HasPrefix(rt.path, "/api/")
				if refused {
					want = http.StatusForbidden
				}
				name := "token=" + token + "/" + host + "/" + rt.name
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					req, err := http.NewRequestWithContext(ctx, rt.method, ts.URL+rt.path, strings.NewReader(rt.body))
					if err != nil {
						t.Fatal(err)
					}
					req.Host = host
					if rt.body != "" {
						req.Header.Set("Content-Type", "application/json")
					}
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode != want {
						b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
						t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, b)
					}
					if refused {
						var e struct {
							Error struct{ Code, Message string } `json:"error"`
						}
						b, _ := io.ReadAll(resp.Body)
						if err := json.Unmarshal(b, &e); err != nil {
							t.Fatalf("refusal body is not the error shape: %s", b)
						}
						for _, frag := range []string{"evil.example:7331", "AllowOrigins", "Token"} {
							if !strings.Contains(e.Error.Message, frag) {
								t.Errorf("refusal message %q does not name %q", e.Error.Message, frag)
							}
						}
					}
				})
			}
		}
	}
}

// TestDNSRebindingForwardedHost: proxy headers never vouch for a Host
// — anyone can send them, a rebound page included.
func TestDNSRebindingForwardedHost(t *testing.T) {
	ts := rebindingServer(t, "")
	for _, hdr := range [][2]string{
		{"X-Forwarded-Host", "localhost"},
		{"Forwarded", "host=localhost"},
		{"X-Forwarded-Host", "myapp.internal:8080"},
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/runs", nil)
		req.Host = "evil.example:7331"
		req.Header.Set(hdr[0], hdr[1])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %s = %d, want 403", hdr[0], hdr[1], resp.StatusCode)
		}
	}
}

// TestDNSRebindingInProcess: a request that never crossed a socket (no
// RemoteAddr, as an adapter may hand one over) gets no exemption — only
// its Host decides: localhost passes; any other Host is refused.
func TestDNSRebindingInProcess(t *testing.T) {
	h := New(Open(t.TempDir() + "/inproc.db")).Handler()
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"http://localhost/api/runs", http.StatusOK},
		{"http://weft.studio.local/api/runs", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		req.RemoteAddr = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s with no RemoteAddr: %d, want %d (%s)", tc.url, w.Code, tc.want, w.Body)
		}
	}
}
