package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weftbase/weftbase/tenant"
)

// TestSignOutAll: `weftbase signout --all` ends every Console session
// (DELETE /v1/auth/sessions); without --all it is a usage error, and a
// key that may not is refused with the server's word.
func TestSignOutAll(t *testing.T) {
	srv := newServer(t, tok)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Setenv("WEFTBASE_DISCOVERY", "off")
	ctx := context.Background()
	var ids []string
	for range 2 {
		link, err := srv.app.Sessions().CreateLink(ctx, tenant.Default())
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := srv.app.Sessions().Exchange(ctx, link, "operator")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, errb, code := cli("signout", "--url", ts.URL, "--token", tok); code != 2 || !strings.Contains(errb, "--all") {
		t.Errorf("signout without --all = %d %q", code, errb)
	}
	out, errb, code := cli("signout", "--all", "--url", ts.URL, "--token", tok)
	if code != 0 || !strings.Contains(out, "2 Console session(s) ended") {
		t.Fatalf("signout --all = %d %q %q", code, out, errb)
	}
	for _, id := range ids {
		if _, err := srv.app.Sessions().Lookup(ctx, id); err == nil {
			t.Error("a session outlived signout --all")
		}
	}
	if _, errb, code := cli("signout", "--all", "--url", ts.URL, "--token", "wb_sk_test_nope"); code != 1 || !strings.Contains(errb, "401") {
		t.Errorf("a bad key = %d %q", code, errb)
	}
}
