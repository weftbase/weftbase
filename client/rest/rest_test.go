package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGet pins the client's contract: the bearer in the header, the
// body on a 2xx, the server's own message (made printable) in a
// *StatusError on a non-2xx, a redirect refused, and an unreachable
// server named with its password redacted.
func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/meta":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no run \u001b[2Jx"}}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c := New(srv.URL+"/", "tok")
	if c.Base() != srv.URL || c.Token() != "tok" {
		t.Fatalf("Base %q Token %q", c.Base(), c.Token())
	}
	if b, err := c.Get(ctx, "/v1/meta", nil); err != nil || string(b) != `{"ok":true}` {
		t.Fatalf("Get meta = %q %v", b, err)
	}
	_, err := c.With(srv.URL, "wrong").Get(ctx, "/v1/meta", nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized || !strings.Contains(err.Error(), "bad token") ||
		!strings.Contains(err.Error(), "--token or WEFTBASE_TOKEN") {
		t.Errorf("a 401 = %v", err)
	}
	_, err = c.Get(ctx, RunPath("p/1/c"), nil)
	if !errors.As(err, &se) || se.Status != http.StatusNotFound || strings.Contains(err.Error(), "\x1b") ||
		!strings.Contains(err.Error(), "GET /v1/runs/p%2F1%2Fc: 404 no run") {
		t.Errorf("a 404 = %v", err)
	}
	if _, err := c.Get(ctx, "/v1/moved", nil); err == nil || !strings.Contains(err.Error(), "not followed") {
		t.Errorf("a redirect = %v, want refused", err)
	}
	if _, err := New("http://user:pw@127.0.0.1:1", "").Get(ctx, "/v1/meta", nil); err == nil ||
		!strings.Contains(err.Error(), "weftbase not reachable at http://user:xxxxx@127.0.0.1:1") {
		t.Errorf("unreachable = %v", err)
	}
	if got := RunLink("http://h", "p/1/c"); got != "http://h/runs/p%2F1%2Fc" {
		t.Errorf("RunLink = %q", got)
	}
}
