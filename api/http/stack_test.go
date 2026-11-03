package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/console"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/internal/reqlog"
	"github.com/weftbase/weftbase/obsdb"
	obsqlite "github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// stack is the router as the kernel wires it for every role, over a
// fresh trace store and an in-memory control store.
type stack struct {
	router   *Router
	ts       *httptest.Server
	key      string // a secret key of the default scope
	keys     *keys.Service
	sessions *authz.Sessions
	log      *events.Log
	traces   obsdb.DB
}

type stackOpts struct {
	open    bool
	noUI    bool
	origins []string
	db      func(obsdb.DB) obsdb.DB // wraps the trace store
}

func newStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	ctx := context.Background()
	control, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	traces, err := obsqlite.Open(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = traces.Close() })
	s := &stack{log: events.New(control), traces: traces}
	s.keys = keys.New(control, s.log)
	s.sessions = authz.NewSessions(control)
	if _, s.key, err = s.keys.Mint(ctx, tenant.Default(), keys.Secret, "test", events.System); err != nil {
		t.Fatal(err)
	}
	var aopts []authz.Option
	if o.open {
		aopts = append(aopts, authz.Open())
	}
	db := obsdb.DB(traces)
	if o.db != nil {
		db = o.db(traces)
	}
	reqs := reqlog.New()
	api := v1.New(v1.DB(db), v1.Events(s.log), v1.Capabilities("ingest"), v1.IngestCounts(reqs.Ingest))
	var routes []Route
	for _, r := range append(api.Routes(), api.IngestRoutes()...) {
		routes = append(routes, Route{Method: r.Method, Pattern: r.Pattern, Action: r.Action, Handler: r.Handler})
	}
	c := Config{
		Auth: authz.NewAuthenticator(s.keys, s.sessions, aopts...), Sessions: s.sessions,
		Routes: routes, AllowOrigins: o.origins, RequestLog: reqs,
	}
	if !o.noUI {
		c.Console = console.Handler(console.Options{})
	}
	s.router = New(c)
	s.ts = httptest.NewServer(s.router)
	t.Cleanup(s.ts.Close)
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = s.log.Hub().Run(hctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

// req is one request against the stack.
type req struct {
	method, path, body string
	key                string // bearer
	host               string
	header             map[string]string
}

func (s *stack) do(t *testing.T, r req) (*http.Response, string) {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	hr, err := http.NewRequest(r.method, s.ts.URL+r.path, strings.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	if r.host != "" {
		hr.Host = r.host
	}
	if r.key != "" {
		hr.Header.Set("Authorization", "Bearer "+r.key)
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, ""
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}
