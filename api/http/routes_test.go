package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"

	httpapi "github.com/weftbase/weftbase/api/http"
	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/events"
	obsqlite "github.com/weftbase/weftbase/obsdb/sqlite"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant/keys"
)

// fullRouter builds the route table the kernel mounts with every role:
// the router's own rows, v1's read surface and the OTLP receiver.
func fullRouter(t *testing.T) *httpapi.Router {
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
	log := events.New(control)
	sessions := authz.NewSessions(control)
	api := v1.New(v1.DB(traces), v1.Events(log))
	var routes []httpapi.Route
	for _, r := range append(api.Routes(), api.IngestRoutes()...) {
		routes = append(routes, httpapi.Route{Method: r.Method, Pattern: r.Pattern, Action: r.Action, Handler: r.Handler})
	}
	return httpapi.New(httpapi.Config{
		Auth:     authz.NewAuthenticator(keys.New(control, log), sessions),
		Sessions: sessions,
		Routes:   routes,
	})
}

// TestRouteActions: every registered route names an authz action, and
// that action is one authz knows.
func TestRouteActions(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range fullRouter(t).Routes() {
		key := r.Method + " " + r.Pattern
		if seen[key] {
			t.Errorf("%s registered twice", key)
		}
		seen[key] = true
		if r.Action == "" {
			t.Errorf("%s has no authz action", key)
			continue
		}
		if !slices.Contains(authz.Actions, r.Action) {
			t.Errorf("%s names action %q, which authz.Actions does not list", key, r.Action)
		}
	}
}

// openAPIDoc is the part of api/openapi/v1.yaml the checks read.
type openAPIDoc struct {
	OpenAPI string                    `yaml:"openapi"`
	Paths   map[string]map[string]any `yaml:"paths"`
}

var httpMethods = []string{"get", "put", "post", "delete", "patch", "options", "head", "trace"}

func readSpec(t *testing.T) openAPIDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "openapi", "v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("api/openapi/v1.yaml does not parse: %v", err)
	}
	return doc
}

// specOps maps "METHOD /path" to the operation's x-weftbase-action.
func specOps(t *testing.T, doc openAPIDoc) map[string]string {
	t.Helper()
	out := map[string]string{}
	for path, item := range doc.Paths {
		for key, v := range item {
			if !slices.Contains(httpMethods, key) {
				continue // parameters, summary, extensions of the path item
			}
			op, _ := v.(map[string]any)
			action, _ := op["x-weftbase-action"].(string)
			out[strings.ToUpper(key)+" "+path] = action
		}
	}
	return out
}

// TestOpenAPISpecParses: the contract is OpenAPI 3.1 and has paths.
func TestOpenAPISpecParses(t *testing.T) {
	doc := readSpec(t)
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") {
		t.Errorf("openapi = %q, want 3.1.x", doc.OpenAPI)
	}
	if len(doc.Paths) == 0 {
		t.Error("the spec has no paths")
	}
}

// TestOpenAPIMatchesRoutes: every route the router serves is an
// operation of api/openapi/v1.yaml at the same path (the mux pattern's
// wildcards are the spec's path parameters, by name) and the same
// method, and vice versa; each operation's x-weftbase-action is the
// route's action. HEAD is implied by GET on both sides.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	ops := specOps(t, readSpec(t))
	routes := map[string]authz.Action{}
	for _, r := range fullRouter(t).Routes() {
		routes[r.Method+" "+r.Pattern] = r.Action
	}
	var missing, extra []string
	for k, a := range routes {
		specAction, ok := ops[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if specAction != string(a) {
			t.Errorf("%s: spec x-weftbase-action %q, route action %q", k, specAction, a)
		}
	}
	for k := range ops {
		if strings.HasPrefix(k, http.MethodHead+" ") {
			continue
		}
		if _, ok := routes[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	for _, k := range missing {
		t.Errorf("route %s is not in api/openapi/v1.yaml", k)
	}
	for _, k := range extra {
		t.Errorf("api/openapi/v1.yaml operation %s has no route", k)
	}
}
