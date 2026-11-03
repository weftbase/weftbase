package studio

import (
	"net/http"
	"strings"
	"testing"
)

// TestLeakedCredentialsNeverServer pins that one Studio secret
// (studio.Token: the server bearer, the panel tokens' HMAC key, the
// live grants' root) cannot be reached from what it signs. A leaked
// panel token — even a playground-scoped one — or a live grant minted
// by the server token itself is refused on every server-only route:
// the mint, the runtime link, deletes, breakpoints and the hand-off's
// server branch (which would answer with the token in its fragment).
// A grant is a credential on GET /api/live alone, never as a bearer
// and never as ?sig= on another route.
func TestLeakedCredentialsNeverServer(t *testing.T) {
	pt := newPlaygroundServer(t, "srv-token")
	pgTok := pt.panelTok(t, "pub_mine", true)
	// A grant minted by the server token: the most powerful one there is.
	serverGrant := mustGrant(t, pt.Handler(), pt.token, "all=1")

	serverOnly := []struct{ method, path, body string }{
		{http.MethodPost, "/api/panel-tokens", `{"public_id":"pub_other","ttl":"1h","playground":true}`},
		{http.MethodPost, "/api/runtime/register", `{"runtime_id":"rt_evil"}`},
		{http.MethodGet, "/api/runtime/commands?runtime_id=rt_test", ""},
		{http.MethodPost, "/api/runtime/acks", `{"command_id":"x","state":"accepted"}`},
		{http.MethodDelete, "/api/runs/run_x", ""},
		{http.MethodDelete, "/api/sessions/s_x", ""},
		{http.MethodPut, "/api/runtimes/rt_test/breakpoints", `{"tools":["refund"]}`},
		{http.MethodDelete, "/api/runtimes/rt_test", ""},
	}
	for _, c := range serverOnly {
		if code, body := pt.authed(t, c.method, c.path, pgTok, c.body); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("%s %s with a playground panel token = %d (%s), want 401/403", c.method, c.path, code, body)
		}
		// The grant as a bearer is no token at all.
		if code, body := pt.authed(t, c.method, c.path, serverGrant, c.body); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a live grant as bearer = %d (%s), want 401", c.method, c.path, code, body)
		}
	}
	// The hand-off never hands a panel token the server token.
	req, err := http.NewRequest(http.MethodGet, pt.ts.URL+"/api/panel-handoff?to=/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+pgTok)
	req.Header.Set("Accept", "application/json")
	if code, body := pt.do(t, req); code != http.StatusOK || !strings.Contains(body, "#token="+panelTokenPrefix) ||
		strings.Contains(body, pt.token) {
		t.Errorf("hand-off with a panel token = %d (%s): want a minted read token, never the server token", code, body)
	}
	// A grant rides ?sig= on the live stream alone: elsewhere the query
	// credential is no credential (401, no bearer).
	for _, path := range []string{"/api/runs?sig=" + serverGrant, "/api/meta?sig=" + serverGrant, "/api/panel-tokens?sig=" + serverGrant} {
		method := http.MethodGet
		if strings.HasPrefix(path, "/api/panel-tokens") {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, pt.ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if code, body := pt.do(t, req); code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d (%s), want 401", method, path, code, body)
		}
	}
	// The server token itself is not a panel token's signature key
	// spelled another way: a "panel token" whose payload is anything
	// and whose MAC is the server token verifies nowhere.
	if code, _ := pt.authed(t, http.MethodGet, "/api/runs", panelTokenPrefix+"e30."+pt.token, ""); code != http.StatusUnauthorized {
		t.Errorf("a forged wb_pt carrying the server token = %d, want 401", code)
	}
}
