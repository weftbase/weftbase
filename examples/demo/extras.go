package main

// The less common things a run can be, each once, so every view of the
// Console has something real to show: an agent with retries, a model
// fallback, reasoning, a typed answer and an MCP tool (concierge); an
// agent that takes one run at a time, so a second one queues (reports);
// an agent whose approvals expire, registered with a stale manifest
// (payouts); and traces that are not weft runs at all, sent the way any
// OpenTelemetry app would send them.
//
// Nothing here is needed to run agents on Weftbase: main.go is the tour.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/mw"
	"github.com/weftbase/weft/mcp"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/examples/demo/shop"
	"github.com/weftbase/weftbase/worker"
)

const (
	askGift   = "I need a gift for a friend who loves pour-over coffee, under $80."
	askReport = "Monthly sales report for October."
	askPayout = "Pay out $1,250.00 to supplier Kiln & Co."
)

// Recommendation is the concierge's typed answer.
type Recommendation struct {
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	PriceUSD float64 `json:"price_usd"`
	Why      string  `json:"why" jsonschema:"one sentence"`
}

// concierge recommends a product and answers with a Recommendation. Its
// run shows: a model attempt that fails and is retried, a reasoning
// part, tool arguments that arrive fenced and are repaired, a tool
// imported from an MCP server, a result cut at the tool's cap, WARN and
// ERROR log lines, and a last step the backup model answers because the
// first model is down.
func concierge(ctx context.Context, speed shop.Speed) (*core.Agent, error) {
	inventory, err := inventoryTools(ctx, speed)
	if err != nil {
		return nil, err
	}
	submit := shop.Calls(900*time.Millisecond, 1480, 96, shop.Tool("submit_output",
		`{"sku":"DRP-02","name":"Ceramic Dripper Set","price_usd":58.00,"why":"A hand-glazed dripper with a matching carafe, well reviewed and under budget."}`))
	steps := []shop.Beat{
		shop.Calls(800*time.Millisecond, 512, 61, shop.Tool("inventory_search", "```json\n{\"query\":\"pour-over\",\"max_price_usd\":80}\n```")).
			Thinking("Pour-over means drippers, kettles and filters. The budget rules out the grinder bundles; search the catalog first.").
			Flaky(),
		shop.Calls(700*time.Millisecond, 884, 38, shop.Tool("product_reviews", `{"sku":"DRP-02"}`)),
	}
	primary := shop.NewModel(core.ModelInfo{Provider: "openai", Name: "gpt-4o-mini"}, speed, map[string][]shop.Beat{
		askGift: append(steps[:2:2], shop.Fail(1500*time.Millisecond,
			&shop.APIError{StatusCode: 400, Message: "openai: this model's rate limit tier does not allow structured output"})),
	})
	backup := shop.NewModel(core.ModelInfo{Provider: "openai", Name: "gpt-4o"}, speed, map[string][]shop.Beat{
		askGift: append(steps[:2:2], submit),
	})
	opts := []core.Option{
		core.Name("concierge"),
		core.Instructions("You recommend one product from the catalog and answer with a Recommendation."),
		core.Output[Recommendation](),
		// The first model's failed attempts are retried once; what still
		// fails goes to the backup model. Fenced or cut arguments are
		// repaired before a tool sees them.
		core.WrapModel(mw.Fallback(backup), mw.Retry(mw.MaxRetries(1), mw.BaseDelay(0)), mw.RepairJSON()),
		shop.Slow(speed, 500*time.Millisecond, "product_reviews", "Customer reviews of a product.", productReviews, core.MaxResultBytes(240)),
	}
	for _, t := range inventory {
		opts = append(opts, t)
	}
	return core.New(primary, opts...), nil
}

func productReviews(ctx context.Context, in struct {
	SKU string `json:"sku"`
}) (string, error) {
	slog.WarnContext(ctx, "reviews service answered slowly", "sku", in.SKU, "took_ms", 2140)
	slog.ErrorContext(ctx, "review translation failed; returning the originals", "sku", in.SKU, "lang", "pt")
	review := `{"stars":5,"text":"Bought it as a gift. The glaze is beautiful, the carafe pours cleanly and the filters are a standard size."}`
	return "[" + strings.Repeat(review+",", 11) + review + "]", nil
}

// inventoryTools imports the tools of the store's inventory service: an
// MCP server, here in process.
func inventoryTools(ctx context.Context, speed shop.Speed) ([]*core.ToolDef, error) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "inventory-service", Version: "1.4.0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "search", Description: "Search the catalog by text and price.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"max_price_usd":{"type":"number"}},"required":["query"]}`)},
		func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			if err := speed.Sleep(ctx, 650*time.Millisecond); err != nil {
				return nil, err
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{
				Text: `[{"sku":"DRP-02","name":"Ceramic Dripper Set","price_usd":58.00},{"sku":"KET-GSN-09","name":"Gooseneck Kettle 0.9 L","price_usd":74.00}]`}}}, nil
		})
	st, ct := sdk.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	sess, err := sdk.NewClient(&sdk.Implementation{Name: "weftbase-demo", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	return mcp.Tools(ctx, sess, mcp.Server("inventory"), mcp.Transport("memory"), mcp.Prefix("inventory_"))
}

// reports builds a report slowly. The demo serves it one run at a time,
// so a second request waits in the queue.
func reports(speed shop.Speed) *core.Agent {
	build := core.Tool("build_report", "Build the monthly sales report.", func(ctx context.Context, _ struct {
		Month string `json:"month"`
	}) (string, error) {
		// At least a second whatever the speed: long enough to see the
		// run behind it queued.
		if err := shop.Speed(1).Sleep(ctx, max(time.Duration(float64(3*time.Second)*float64(speed)), 1200*time.Millisecond)); err != nil {
			return "", err
		}
		return `{"orders":1284,"revenue_usd":96412.50,"refunds_usd":1873.00}`, nil
	})
	return core.New(shop.NewModel(core.ModelInfo{Provider: "google", Name: "gemini-2.5-flash"}, speed, map[string][]shop.Beat{
		askReport: {
			shop.Calls(500*time.Millisecond, 240, 22, shop.Tool("build_report", `{"month":"2026-10"}`)),
			shop.Say(900*time.Millisecond, 410, 64, "October: 1,284 orders, $96,412.50 in revenue, $1,873.00 refunded."),
		},
	}), core.Name("reports"), core.Instructions("You report on the store's sales. Amounts are in USD."), build)
}

// payouts pays suppliers; every payout needs a person. The demo gives
// its approvals a short expiry (weftbase.AgentApprovalExpiry).
func payouts(speed shop.Speed, instructions string) *core.Agent {
	send := shop.Slow(speed, 700*time.Millisecond, "send_payout", "Send money to a supplier.", func(context.Context, struct {
		Supplier  string  `json:"supplier"`
		AmountUSD float64 `json:"amount_usd"`
	}) (string, error) {
		return `{"state":"sent"}`, nil
	}, core.RequireApproval())
	return core.New(shop.NewModel(core.ModelInfo{Provider: "anthropic", Name: "claude-haiku-4-5"}, speed, map[string][]shop.Beat{
		askPayout: {
			shop.Calls(700*time.Millisecond, 310, 41, shop.Tool("send_payout", `{"supplier":"Kiln & Co.","amount_usd":1250.00}`)),
			shop.Say(800*time.Millisecond, 450, 30, "Sent: $1,250.00 to Kiln & Co.").
				OrDenied("Nobody approved the payout in time, so nothing was sent to Kiln & Co."),
		},
	}), core.Name("payouts"), core.Instructions(instructions), send)
}

// staleManifest makes an executor register one of its agents with an
// older manifest than the code it runs: what a build looks like when it
// was registered from a manifest file and then rebuilt without one. The
// runs report the manifest they really ran with, so the version reads
// "manifest differs".
type staleManifest struct {
	worker.Executor
	agent    string
	from, to string // the instructions the agent has, and the ones the registry is told
	running  string // the hash of the manifest the agent has
}

func (s *staleManifest) Register(ctx context.Context) (wire.Registration, error) {
	reg, err := s.Executor.Register(ctx)
	for i, a := range reg.Agents {
		if a.Name == s.agent {
			s.running = a.ManifestHash
			old := bytes.Replace(a.Manifest, []byte(s.from), []byte(s.to), 1)
			sum := sha256.Sum256(old)
			reg.Agents[i].Manifest, reg.Agents[i].ManifestHash = old, hex.EncodeToString(sum[:])
		}
	}
	return reg, err
}

func (s *staleManifest) Dispatch(ctx context.Context, cmd wire.Command) (wire.Ack, error) {
	if cmd.Agent == s.agent {
		cmd.ManifestHash = s.running
	}
	return s.Executor.Dispatch(ctx, cmd)
}

// sendStockTraces posts three traces as a plain OpenTelemetry app
// would, OTLP/JSON over HTTP with no weft attribute anywhere: a GenAI
// agent of another framework (Weftbase derives a run from it), a bare
// model call from a batch job (a GenAI trace that is no run) and an
// ordinary web request.
func sendStockTraces(ctx context.Context, base, key string) error {
	now := time.Now()
	at := func(ms int) string { return fmt.Sprint(now.Add(time.Duration(ms-4000) * time.Millisecond).UnixNano()) }
	str := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	num := func(k string, v int) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"intValue": fmt.Sprint(v)}}
	}
	span := func(trace, id, parent, name string, kind, from, to int, attrs ...map[string]any) map[string]any {
		s := map[string]any{"traceId": trace, "spanId": id, "name": name, "kind": kind,
			"startTimeUnixNano": at(from), "endTimeUnixNano": at(to), "attributes": attrs}
		if parent != "" {
			s["parentSpanId"] = parent
		}
		return s
	}
	id := func(seed string, n int) string {
		sum := sha256.Sum256([]byte(seed + now.String()))
		return hex.EncodeToString(sum[:])[:n]
	}
	agentTrace, webTrace, callTrace := id("agent", 32), id("web", 32), id("call", 32)
	resource := func(service string, spans ...map[string]any) map[string]any {
		return map[string]any{
			"resource":   map[string]any{"attributes": []any{str("service.name", service)}},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "opentelemetry.instrumentation"}, "spans": spans}},
		}
	}
	body := map[string]any{"resourceSpans": []any{
		resource("recommender-py",
			span(agentTrace, id("a1", 16), "", "invoke_agent recommender", 1, 0, 2600,
				str("gen_ai.operation.name", "invoke_agent"), str("gen_ai.agent.name", "recommender"), str("gen_ai.provider.name", "openai")),
			span(agentTrace, id("a2", 16), id("a1", 16), "chat gpt-4o-mini", 3, 40, 1100,
				str("gen_ai.operation.name", "chat"), str("gen_ai.provider.name", "openai"), str("gen_ai.request.model", "gpt-4o-mini"),
				str("gen_ai.response.model", "gpt-4o-mini"), num("gen_ai.usage.input_tokens", 820), num("gen_ai.usage.output_tokens", 64)),
			span(agentTrace, id("a3", 16), id("a1", 16), "execute_tool similar_products", 1, 1120, 1700,
				str("gen_ai.operation.name", "execute_tool"), str("gen_ai.tool.name", "similar_products")),
			span(agentTrace, id("a4", 16), id("a1", 16), "chat gpt-4o-mini", 3, 1720, 2580,
				str("gen_ai.operation.name", "chat"), str("gen_ai.provider.name", "openai"), str("gen_ai.request.model", "gpt-4o-mini"),
				str("gen_ai.response.model", "gpt-4o-mini"), num("gen_ai.usage.input_tokens", 1140), num("gen_ai.usage.output_tokens", 92)),
		),
		// A bare model call, under no agent: a GenAI trace that is no run.
		resource("review-digest-job",
			span(callTrace, id("c1", 16), "", "nightly digest", 1, 3500, 3990, str("job.name", "review-digest")),
			span(callTrace, id("c2", 16), id("c1", 16), "chat gpt-4o-mini", 3, 3520, 3980,
				str("gen_ai.operation.name", "chat"), str("gen_ai.provider.name", "openai"), str("gen_ai.request.model", "gpt-4o-mini"),
				num("gen_ai.usage.input_tokens", 2210), num("gen_ai.usage.output_tokens", 180)),
		),
		resource("checkout-web",
			span(webTrace, id("w1", 16), "", "POST /cart/checkout", 2, 2800, 3400,
				str("http.request.method", "POST"), str("url.path", "/cart/checkout"), num("http.response.status_code", 200)),
			span(webTrace, id("w2", 16), id("w1", 16), "SELECT orders", 3, 2840, 2910, str("db.system.name", "postgresql")),
			span(webTrace, id("w3", 16), id("w1", 16), "POST https://payments.example/charges", 3, 2930, 3380,
				str("http.request.method", "POST"), num("http.response.status_code", 201)),
		),
	}}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/otlp/v1/traces", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST /otlp/v1/traces: %s", resp.Status)
	}
	return nil
}
