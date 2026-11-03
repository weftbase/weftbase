package mcp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/client/rest"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// testServer is a weftbase node for the tests: the kernel over a data
// dir, served by an httptest server the caller starts.
type testServer struct{ app *weftbase.App }

func (s testServer) Handler() http.Handler { return s.app.Handler() }

func (s testServer) Close() error { return s.app.Shutdown(context.Background()) }

// newServer is the kernel over the trace file dbFlag names
// (sqlite://path), its control store beside it, with token imported as
// the bootstrap secret key and every other key revoked (a restart with
// a new token is a rotation): what `weftbase dev` serves.
func newServer(dbFlag, token string) (testServer, error) {
	path := strings.TrimPrefix(dbFlag, "sqlite://")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return testServer{}, err
	}
	app, err := weftbase.New(weftbase.DataDir(filepath.Dir(path)), weftbase.TraceDB(dbFlag),
		weftbase.BootstrapKey(token), weftbase.Logger(slog.New(slog.DiscardHandler)))
	if err != nil {
		return testServer{}, err
	}
	ctx := context.Background()
	k, err := app.Keys().Verify(ctx, token)
	if err == nil {
		var all []keys.Key
		if all, err = app.Keys().List(ctx, tenant.Default()); err == nil {
			for _, o := range all {
				if o.ID != k.ID && o.RevokedAt == nil {
					if err = app.Keys().Revoke(ctx, tenant.Default(), o.ID, events.System); err != nil {
						break
					}
				}
			}
		}
	}
	if err != nil {
		_ = app.Shutdown(ctx)
		return testServer{}, err
	}
	return testServer{app}, nil
}

// The tests' keys: secret keys of the default project's test env.
const (
	tok  = "wb_sk_test_tok"
	tok2 = "wb_sk_test_tok2"
)

// startMCP is New over the server the flags, the environment and the
// discovery file name — the resolution `weftbase mcp` makes
// (cmd/weftbase owns the real one): --url, else WEFTBASE_URL, else the
// discovery file; the token --token, else WEFTBASE_TOKEN, else the
// file's when it names that server. Without --url every look re-reads
// the environment and the file.
func startMCP(ctx context.Context, urlFlag, tokenFlag string, stderr io.Writer) (*sdk.Server, error) {
	resolve := func() (string, string) {
		info, _, found := discovery.Lookup(os.Getenv, slog.New(slog.DiscardHandler))
		base := urlFlag
		if base == "" {
			base = os.Getenv("WEFTBASE_URL")
		}
		if base == "" && found {
			base = info.URL
		}
		tok := tokenFlag
		if tok == "" {
			tok = os.Getenv("WEFTBASE_TOKEN")
		}
		if tok == "" && found && strings.TrimRight(info.URL, "/") == strings.TrimRight(base, "/") {
			tok = info.Token
		}
		return base, tok
	}
	u, tok := resolve()
	o := Options{Client: rest.New(u, tok), TokenFrom: "from the test", Stderr: stderr}
	if urlFlag == "" {
		o.Resolve = resolve
		o.RereadToken = tokenFlag == "" && os.Getenv("WEFTBASE_TOKEN") == ""
	}
	return New(ctx, o)
}

type orderIn struct {
	OrderID string `json:"order_id" jsonschema:"the order"`
}

// ordersAgent is the orders agent the seeded runs share.
func ordersAgent(m core.Model, opts ...core.Option) *core.Agent {
	lookup := core.Tool("lookup_order", "Look up an order.", func(context.Context, orderIn) (string, error) {
		return "order 42 shipped", nil
	})
	return core.New(m, append([]core.Option{core.Name("orders"), core.Instructions("You are a support agent."), lookup}, opts...)...)
}

// syncBuffer is a strings.Builder safe for concurrent writes.
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
