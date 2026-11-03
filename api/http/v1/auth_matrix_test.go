package v1

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb"
)

// TestAuthMatrix pins the API's auth as one table: every registered
// route × every identity. The rules it spells out:
//
//   - the Console ("/") is open; everything under /api needs the
//     server token once one is configured — as a bearer: 401 without
//     it, with the ingest token, or with any other token; a token in
//     the URL (?token=) is refused on every route, the right one
//     included, and with a good bearer beside it;
//   - the server token reaches every /api route;
//   - ingest (/otlp/v1/traces, /otlp/v1/logs) takes the ingest token and nothing
//     else.
func TestAuthMatrix(t *testing.T) {
	const serverTok, ingestTok = "srv-token", "ingest-token"
	manifest := []byte(`{"weft":1,"agents":[{"name":"acme-support"}]}`)
	srv := New(Open(t.TempDir()+"/matrix.db"), Token(serverTok), IngestToken(ingestTok), Manifest(manifest))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	hashes, err := manifestAgentHashes(manifest)
	if err != nil || len(hashes) != 1 {
		t.Fatalf("the matrix manifest's agent hashes = %v, %v", hashes, err)
	}
	seedRun(t, srv.db, "run_a", "pub_a", "s_a", "", nil)
	span := obsdb.Span{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "0102030405060708", Name: "invoke_agent acme-support", Kind: 1,
		Start: time.Now().UTC(), End: time.Now().UTC().Add(time.Second), StatusCode: 1, Service: "svc",
		Attrs:    map[string]any{"gen_ai.operation.name": "invoke_agent", "weft.run.id": "run_a", "weft.public_id": "pub_a"},
		Resource: map[string]any{"service.name": "svc"},
	}
	if err := srv.db.Write(context.Background(), obsdb.Batch{Spans: []obsdb.Span{span}}); err != nil {
		t.Fatal(err)
	}
	seedRun(t, srv.db, "del_run", "pub_a", "", "", nil)
	seedRun(t, srv.db, "del_turn", "pub_a", "del_s", "", nil)

	type identity struct {
		name, token string
		query       bool // ?token= instead of the bearer
		both        bool // ?token= beside the bearer
		server      bool // the server token as a bearer
		ingest      bool // the ingest token as a bearer
	}
	identities := []identity{
		{name: "anonymous"},
		{name: "server token (bearer)", token: serverTok, server: true},
		{name: "server token (?token=)", token: serverTok, query: true},
		{name: "server token (bearer and ?token=)", token: serverTok, query: true, both: true},
		{name: "ingest token", token: ingestTok, ingest: true},
		{name: "garbage token", token: "not-a-token"},
	}
	do := func(method, path, body string, id identity) int {
		t.Helper()
		if id.query {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			path += sep + "token=" + id.token
		}
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if (!id.query || id.both) && id.token != "" {
			req.Header.Set("Authorization", "Bearer "+id.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		return resp.StatusCode
	}

	type row struct {
		name, method, path, body string
		open                     bool // the Console: every identity reads it
		ingest                   bool // the ingest token's alone
		noRoute                  bool // the API's 404 for every token-holder
	}
	rows := []row{
		{name: "/", method: http.MethodGet, path: "/runs/run_a", open: true},
		{name: "GET /api/meta", method: http.MethodGet, path: "/api/meta"},
		{name: "GET /api/manifest", method: http.MethodGet, path: "/api/manifest"},
		{name: "GET /api/manifests/{hash}", method: http.MethodGet, path: "/api/manifests/" + hashes[0].hash},
		{name: "GET /api/runs", method: http.MethodGet, path: "/api/runs"},
		{name: "GET /api/runs/{id}", method: http.MethodGet, path: "/api/runs/run_a"},
		{name: "GET /api/runs/{id}/events", method: http.MethodGet, path: "/api/runs/run_a/events"},
		{name: "GET /api/runs/{id}/transcript", method: http.MethodGet, path: "/api/runs/run_a/transcript"},
		{name: "GET /api/runs/{id}/spans", method: http.MethodGet, path: "/api/runs/run_a/spans"},
		{name: "GET /api/runs/{id}/requests", method: http.MethodGet, path: "/api/runs/run_a/requests"},
		{name: "GET /api/runs/{id}/tools", method: http.MethodGet, path: "/api/runs/run_a/tools"},
		{name: "GET /api/runs/{id}/steps/{item}", method: http.MethodGet, path: "/api/runs/run_a/steps/0"},
		{name: "GET /api/runs/{id}/export", method: http.MethodGet, path: "/api/runs/run_a/export?format=json"},
		{name: "GET /api/runs/{id}/logs", method: http.MethodGet, path: "/api/runs/run_a/logs"},
		{name: "GET /api/diff", method: http.MethodGet, path: "/api/diff?a=run_a&b=run_a"},
		{name: "GET /api/traces", method: http.MethodGet, path: "/api/traces"},
		{name: "GET /api/traces/{id}", method: http.MethodGet, path: "/api/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "GET /api/search", method: http.MethodGet, path: "/api/search?q=x"},
		{name: "GET /api/stats", method: http.MethodGet, path: "/api/stats?by=agent"},
		{name: "GET /api/settings/pricing", method: http.MethodGet, path: "/api/settings/pricing"},
		{name: "GET /api/sessions", method: http.MethodGet, path: "/api/sessions"},
		{name: "GET /api/sessions/{id}", method: http.MethodGet, path: "/api/sessions/s_a"},
		{name: "GET /api/sessions/{id}/public_id", method: http.MethodGet, path: "/api/sessions/s_a/public_id"},
		{name: "GET /api/sessions/{id}/timeline", method: http.MethodGet, path: "/api/sessions/s_a/timeline"},
		{name: "GET /api/sessions/{id}/entries", method: http.MethodGet, path: "/api/sessions/s_a/entries"},
		{name: "GET /api/sessions/{id}/export", method: http.MethodGet, path: "/api/sessions/s_a/export"},
		{name: "GET /api/public/{id}", method: http.MethodGet, path: "/api/public/pub_a"},
		{name: "GET /api/live", method: http.MethodGet, path: "/api/live?run=run_a"},
		{name: "DELETE /api/runs/{id}", method: http.MethodDelete, path: "/api/runs/del_run"},
		{name: "DELETE /api/sessions/{id}", method: http.MethodDelete, path: "/api/sessions/del_s"},
		{name: "POST /otlp/v1/traces", method: http.MethodPost, path: "/otlp/v1/traces", body: "{}", ingest: true},
		{name: "POST /otlp/v1/logs", method: http.MethodPost, path: "/otlp/v1/logs", body: "{}", ingest: true},
		{name: "GET /api/nope", method: http.MethodGet, path: "/api/nope", noRoute: true},
	}

	// The completeness check: a row per registered route.
	routes, problems := registeredRoutes(t, srv)
	for _, p := range problems {
		t.Error(p)
	}
	mrows := make([]matrixRow, 0, len(rows))
	for _, r := range rows {
		mrows = append(mrows, matrixRow{name: r.name, method: r.method, path: r.path, noRoute: r.noRoute})
	}
	for _, g := range authMatrixGaps(srv, routes, mrows) {
		t.Error(g)
	}

	for _, r := range rows {
		for _, id := range identities {
			got := do(r.method, r.path, r.body, id)
			switch {
			case r.open:
				if got != http.StatusOK {
					t.Errorf("%s as %s = %d, want 200 (the Console is open)", r.name, id.name, got)
				}
			case r.ingest:
				if id.ingest && !id.query == (got == http.StatusUnauthorized) {
					t.Errorf("%s as %s = %d, want it accepted", r.name, id.name, got)
				}
				if !id.ingest && got != http.StatusUnauthorized {
					t.Errorf("%s as %s = %d, want 401 (ingest takes the ingest token alone)", r.name, id.name, got)
				}
			case !id.server || id.query:
				if got != http.StatusUnauthorized {
					t.Errorf("%s as %s = %d, want 401", r.name, id.name, got)
				}
			case r.noRoute:
				if got != http.StatusNotFound {
					t.Errorf("%s as %s = %d, want 404", r.name, id.name, got)
				}
			default:
				if got == http.StatusUnauthorized || got == http.StatusForbidden || got == http.StatusNotFound || got >= 500 {
					t.Errorf("%s as %s = %d, want the route's answer", r.name, id.name, got)
				}
			}
		}
	}
}

// TestSetupACrossOriginWrites pins the open API's CSRF guard: with no
// Token, an unsafe method from another origin (or cross-site with no
// Origin) is 403; a read, the same origin and an AllowOrigins entry
// pass.
func TestSetupACrossOriginWrites(t *testing.T) {
	plainSrv := New(Open(t.TempDir() + "/a.db"))
	t.Cleanup(func() { _ = plainSrv.Close() })
	plain := httptest.NewServer(plainSrv.Handler())
	t.Cleanup(plain.Close)
	allowedSrv := New(Open(t.TempDir()+"/b.db"), AllowOrigins("http://myapp.internal:8080"))
	t.Cleanup(func() { _ = allowedSrv.Close() })
	allowed := httptest.NewServer(allowedSrv.Handler())
	t.Cleanup(allowed.Close)

	routes := []struct{ name, method, path string }{
		{"delete run", http.MethodDelete, "/api/runs/run_x"},
		{"read runs", http.MethodGet, "/api/runs"},
	}
	do := func(ts *httptest.Server, method, path string, hdr map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, rt := range routes {
		for _, ts := range []*httptest.Server{plain, allowed} {
			// The usual answer: no Origin, no Sec-Fetch-Site (curl).
			base, body := do(ts, rt.method, rt.path, nil)
			if base == http.StatusForbidden {
				t.Fatalf("%s: baseline = 403 %s", rt.name, body)
			}
			self := "http://" + strings.TrimPrefix(ts.URL, "http://")
			cases := []struct {
				name    string
				hdr     map[string]string
				refused bool
			}{
				{"foreign origin", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, true},
				{"foreign origin alone", map[string]string{"Origin": "https://evil.example"}, true},
				{"null origin", map[string]string{"Origin": "null"}, true},
				{"cross-site without origin", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
				{"same origin", map[string]string{"Origin": self, "Sec-Fetch-Site": "same-origin"}, false},
				{"same-origin fetch site", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
				{"user-initiated", map[string]string{"Sec-Fetch-Site": "none"}, false},
				{"AllowOrigins origin", map[string]string{"Origin": "http://myapp.internal:8080", "Sec-Fetch-Site": "cross-site"}, ts == plain},
			}
			for _, c := range cases {
				want, refused := base, c.refused && rt.method != http.MethodGet
				if refused {
					want = http.StatusForbidden
				}
				code, body := do(ts, rt.method, rt.path, c.hdr)
				if code != want {
					t.Errorf("%s / %s (allow=%v): status = %d, want %d: %s", rt.name, c.name, ts == allowed, code, want, body)
					continue
				}
				if refused && !strings.Contains(body, "cross-origin write refused") {
					t.Errorf("%s / %s: refusal body does not name the reason: %s", rt.name, c.name, body)
				}
			}
		}
	}
}
