package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/weftbase/weft/core"
	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/client/rest"
)

// newServer is the read server over the sqlite file dbFlag names
// (sqlite://path) with the token wall on: what `weftbase dev` serves.
func newServer(dbFlag, token string) (*v1.Server, error) {
	path := strings.TrimPrefix(dbFlag, "sqlite://")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return v1.New(v1.Base("/"), v1.Token(token), v1.Open(path)), nil
}

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
