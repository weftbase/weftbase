package v1

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

// The tests' mount: the handler set on a plain mux as the Local
// principal in the default scope — api/http's authentication, guards
// and route table are its own tests' business (api/http). Unknown
// paths answer the API's JSON 404 (and 405), as the router does.

// opened are the databases Open opened, which Close closes.
var opened sync.Map

// Open is DB(sqlite.Open(path)) for the tests; Close closes it.
func Open(path string) Option {
	db, err := sqlite.Open(path)
	if err != nil {
		panic("open " + path + ": " + err.Error())
	}
	opened.Store(db, true)
	return DB(db)
}

// Close closes the database Open opened (a DB passed in stays its
// owner's).
func (s *Server) Close() error {
	if !reflect.TypeOf(s.db).Comparable() {
		return nil
	}
	if _, ok := opened.LoadAndDelete(s.db); ok {
		return s.db.Close()
	}
	return nil
}

// localPrincipal is the caller every test request runs as.
var localPrincipal = authz.Principal{Kind: authz.Local, ID: "local", Assurance: "local", AllTenants: true}

// Handler mounts every route (ingest included) on a mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range append(s.Routes(), s.IngestRoutes()...) {
		mux.Handle(r.Method+" "+r.Pattern, r.Handler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p := mux.Handler(r); p == "" {
			r2 := r.Clone(r.Context())
			for _, m := range []string{"GET", "POST", "DELETE"} {
				r2.Method = m
				if _, p := mux.Handler(r2); p != "" {
					writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" not allowed")
					return
				}
			}
			notFound(w, r, "no such route "+r.URL.Path)
			return
		}
		mux.ServeHTTP(w, r.WithContext(authz.With(r.Context(), localPrincipal, tenant.Default())))
	})
}

// Handler is New(opts...).Handler().
func Handler(opts ...Option) http.Handler { return New(opts...).Handler() }

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

// searchPath is GET /v1/runs as a search with query q: q as it is when
// it carries a search parameter, else with sort=started (the default
// order) so the route answers the search page.
func searchPath(prefix, q string) string {
	vs, _ := url.ParseQuery(q)
	for _, k := range searchParams {
		if vs.Has(k) {
			return prefix + "/v1/runs?" + q
		}
	}
	if q == "" {
		return prefix + "/v1/runs?sort=started"
	}
	return prefix + "/v1/runs?sort=started&" + q
}

// esc percent-encodes a path id: a child run id carries "/".
func esc(id string) string { return url.PathEscape(id) }
