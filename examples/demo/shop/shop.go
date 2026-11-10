// Package shop is the demo's make-believe online store: two agents
// (support and refunds), their tools, and the scripted models that play
// them. Nothing here calls a model provider: every model is a
// wefttest script chosen by the user's message, with artificial
// latency so a run's timeline has shape.
//
// The package imports weft only, so the agent build under ../build
// never compiles the server.
package shop

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
)

// Speed scales every artificial delay: 1 is realistic (hundreds of
// milliseconds to a few seconds per model call and tool), 0 is none.
type Speed float64

func (s Speed) sleep(ctx context.Context, d time.Duration) error {
	d = time.Duration(float64(d) * float64(s))
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// The prompts the scripted models know. Any other message gets a short
// generic reply.
const (
	AskTracking = "Where is my order #1042?"
	AskStock    = "Do you have the blue kettle in stock, and how much is shipping to Austin?"
	AskCharge   = "I was charged twice for order #1077. Can you check?"
	AskAddress  = "Please change the delivery address on order #1090 to 12 Elm St, Austin."
	AskExport   = "Send me a CSV of all my orders from 2023."
	AskOutage   = "Is the summer sale still on?"
	AskReturn   = "And can I still return it?"
	AskThanks   = "Great, thanks!"
	AskRefund   = "Refund order #1042, the kettle arrived cracked."
	AskRefund77 = "Refund the duplicate charge on order #1077."
	AskRefund90 = "Refund order #1090 in full, I changed my mind."
)

// Reply is the final answer the scripted model gives to prompt: what a
// caller puts in the history of a conversation's next turn.
func Reply(prompt string) string {
	for _, plays := range []map[string][]beat{supportPlays, refundsPlays} {
		if beats := plays[prompt]; len(beats) > 0 {
			return beats[len(beats)-1].text
		}
	}
	return fallback
}

const fallback = "Is there anything else I can help you with?"

// beat is one scripted model call: how long the "model" takes, and what
// it answers.
type beat struct {
	wait   time.Duration
	turn   wefttest.Turn
	text   string // the turn's text, when it is a final answer
	denied string // said instead when the last tool result is an error
}

func say(wait time.Duration, in, out int64, text string) beat {
	return beat{wait: wait, text: text, turn: wefttest.Say(text).WithUsage(core.Usage{InputTokens: in, OutputTokens: out})}
}

func call(wait time.Duration, in, out int64, calls ...wefttest.Call) beat {
	return beat{wait: wait, turn: wefttest.ToolCalls(calls...).WithUsage(core.Usage{InputTokens: in, OutputTokens: out})}
}

// tool is one scripted tool call. Every call gets its own id, as a
// provider's would have.
func tool(name, args string) wefttest.Call {
	calls++
	return wefttest.Call{ID: fmt.Sprintf("call_%s_%02d", name, calls), Name: name, Args: args}
}

var calls int

// model plays one script per user message: the n-th model call after
// the last user message is the script's n-th beat. It is stateless, so
// one agent serves any number of runs, and a run parked in one process
// resumes in another.
type model struct {
	info  core.ModelInfo
	speed Speed
	plays map[string][]beat
}

func (m model) Info() core.ModelInfo { return m.info }

func (m model) Stream(ctx context.Context, req core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	var prompt string
	var n int
	var toolFailed bool
	for _, msg := range req.Messages {
		switch msg.Role {
		case core.RoleUser:
			prompt, n = msg.Text(), 0
		case core.RoleAssistant:
			n++
		}
		toolFailed = false
		for _, p := range msg.Content {
			if r, ok := p.(core.ToolResultPart); ok && r.IsError {
				toolFailed = true
			}
		}
	}
	b := say(400*time.Millisecond, 180, 14, fallback)
	if beats := m.plays[prompt]; n < len(beats) {
		b = beats[n]
		if toolFailed && b.denied != "" {
			b = say(b.wait, 420, 24, b.denied)
		}
	}
	return func(yield func(core.ModelEvent, error) bool) {
		if err := m.speed.sleep(ctx, b.wait); err != nil {
			yield(nil, err)
			return
		}
		for ev, err := range wefttest.Script(b.turn).Stream(ctx, req) {
			if !yield(ev, err) {
				return
			}
		}
	}
}

// Support is the support agent: order questions, stock, and a billing
// subagent. Release 2 adds a hand-off tool.
func Support(release int, speed Speed) *core.Agent {
	billing := core.New(
		// A fine-tuned model no pricing table knows: its cost is unknown.
		model{info: core.ModelInfo{Provider: "acme", Name: "acme-billing-ft-1"}, speed: speed, plays: billingPlays},
		core.Name("billing"),
		core.Instructions("You audit charges on an order and explain them."),
		slow(speed, 700*time.Millisecond, "list_charges", "List the charges on an order.", listCharges),
	)
	opts := []core.Option{
		core.Name("support"),
		core.Instructions("You are the support agent of an online kitchenware store. Prices are in USD."),
		core.MaxSteps(4),
		slow(speed, 450*time.Millisecond, "lookup_order", "Look up an order by id.", lookupOrder),
		slow(speed, 1200*time.Millisecond, "track_shipment", "Ask the carrier where a shipment is.", trackShipment),
		slow(speed, 600*time.Millisecond, "search_catalog", "Search the product catalog.", searchCatalog),
		slow(speed, 300*time.Millisecond, "check_inventory", "Stock of a product by SKU.", checkInventory),
		slow(speed, 350*time.Millisecond, "shipping_quote", "Shipping price to a city.", shippingQuote),
		slow(speed, 900*time.Millisecond, "update_address", "Change an order's delivery address.", updateAddress),
		slow(speed, 200*time.Millisecond, "export_orders", "Export a customer's orders as CSV.", exportOrders),
		core.Subagent("billing", "Ask the billing specialist about charges on an order.", billing),
	}
	if release >= 2 {
		opts = append(opts, slow(speed, 250*time.Millisecond, "handoff_to_human", "Open a ticket for a human agent.", handoff))
	}
	return core.New(model{info: core.ModelInfo{Provider: "openai", Name: "gpt-4o-mini"}, speed: speed, plays: supportPlays}, opts...)
}

// Refunds is the refunds agent: it looks an order up and issues the
// refund, which needs a person's approval. Release 2 can also offer
// store credit.
func Refunds(release int, speed Speed) *core.Agent {
	opts := []core.Option{
		core.Name("refunds"),
		core.Instructions("You process refund requests for an online kitchenware store. Amounts are in USD."),
		slow(speed, 450*time.Millisecond, "lookup_order", "Look up an order by id.", lookupOrder),
		slow(speed, 800*time.Millisecond, "issue_refund", "Refund an order to its payment method.", issueRefund, core.RequireApproval()),
		slow(speed, 300*time.Millisecond, "notify_customer", "Email the customer.", notifyCustomer),
	}
	if release >= 2 {
		opts = append(opts, slow(speed, 300*time.Millisecond, "offer_store_credit", "Offer store credit instead of a refund.", storeCredit))
	}
	return core.New(model{info: core.ModelInfo{Provider: "anthropic", Name: "claude-sonnet-4-6"}, speed: speed, plays: refundsPlays}, opts...)
}

var supportPlays = map[string][]beat{
	AskTracking: {
		call(900*time.Millisecond, 412, 46, tool("lookup_order", `{"order_id":"1042"}`), tool("track_shipment", `{"order_id":"1042"}`)),
		say(1400*time.Millisecond, 688, 71, "Order #1042 (1 × Blue Kettle, $64.00) shipped on Monday with UPS and is out for delivery today."),
	},
	AskStock: {
		call(700*time.Millisecond, 398, 31, tool("search_catalog", `{"query":"blue kettle"}`)),
		call(800*time.Millisecond, 571, 44, tool("check_inventory", `{"sku":"KET-BLU-17"}`), tool("shipping_quote", `{"city":"Austin"}`)),
		say(1100*time.Millisecond, 742, 58, "Yes, the Blue Kettle 1.7 L is in stock (23 left) at $64.00. Standard shipping to Austin is $6.50."),
	},
	AskCharge: {
		call(800*time.Millisecond, 405, 38, tool("lookup_order", `{"order_id":"1077"}`)),
		call(600*time.Millisecond, 590, 52, tool("billing", `{"prompt":"`+askBilling+`"}`)),
		say(1300*time.Millisecond, 803, 66, "You were right: order #1077 was charged twice ($129.00 each). The duplicate will be refunded by our refunds team."),
	},
	// The address service is down and the model keeps retrying: the run
	// fails at the agent's step limit.
	AskAddress: {
		call(700*time.Millisecond, 420, 40, tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		call(700*time.Millisecond, 505, 40, tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		call(700*time.Millisecond, 590, 40, tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		call(700*time.Millisecond, 675, 40, tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
	},
	AskExport: {
		call(600*time.Millisecond, 401, 29, tool("export_orders", `{"year":2023}`)),
		say(900*time.Millisecond, 520, 33, "Your 2023 orders are on their way to your inbox."),
	},
	// The provider is overloaded: the model call itself fails.
	AskOutage: {
		{wait: 2200 * time.Millisecond, turn: wefttest.Fail(errors.New("openai: 529 the engine is currently overloaded, please try again later"))},
	},
	AskReturn: {
		call(600*time.Millisecond, 934, 30, tool("lookup_order", `{"order_id":"1042"}`)),
		say(1000*time.Millisecond, 1120, 49, "Yes. Order #1042 can be returned free of charge until June 30."),
	},
	AskThanks: {
		say(500*time.Millisecond, 1210, 16, "You're welcome. Enjoy the kettle!"),
	},
}

const askBilling = "List the charges on order 1077 and say whether any is a duplicate."

var billingPlays = map[string][]beat{
	askBilling: {
		call(500*time.Millisecond, 216, 22, tool("list_charges", `{"order_id":"1077"}`)),
		say(900*time.Millisecond, 344, 41, "Order 1077 has two identical card charges of $129.00, 4 seconds apart. The second is a duplicate."),
	},
}

func refundPlay(order, amount, reason, done string) []beat {
	issue := call(1100*time.Millisecond, 655, 57, tool("issue_refund",
		fmt.Sprintf(`{"order_id":%q,"amount_usd":%s,"reason":%q}`, order, amount, reason)))
	notify := call(800*time.Millisecond, 790, 48, tool("notify_customer",
		fmt.Sprintf(`{"order_id":%q,"message":"Your refund of $%s is on its way."}`, order, amount)))
	notify.denied = "I could not refund order #" + order + ": a reviewer declined the request."
	return []beat{
		call(800*time.Millisecond, 380, 35, tool("lookup_order", fmt.Sprintf(`{"order_id":%q}`, order))),
		issue,
		notify,
		say(1200*time.Millisecond, 905, 52, done),
	}
}

var refundsPlays = map[string][]beat{
	AskRefund:   refundPlay("1042", "64.00", "arrived damaged", "Done: $64.00 was refunded for order #1042 and the customer was notified."),
	AskRefund77: refundPlay("1077", "129.00", "duplicate charge", "Done: the duplicate $129.00 charge on order #1077 was refunded."),
	AskRefund90: refundPlay("1090", "212.50", "changed mind", "Done: $212.50 was refunded for order #1090."),
	AskThanks:   {say(500*time.Millisecond, 1040, 12, "You're welcome.")},
}

// ── Tools ────────────────────────────────────────────────────────────

// slow is a tool that takes wait (scaled by speed) before it answers.
func slow[In any](speed Speed, wait time.Duration, name, desc string, fn func(context.Context, In) (string, error), opts ...core.ToolOption) *core.ToolDef {
	return core.Tool(name, desc, func(ctx context.Context, in In) (string, error) {
		if err := speed.sleep(ctx, wait); err != nil {
			return "", err
		}
		return fn(ctx, in)
	}, opts...)
}

type orderIn struct {
	OrderID string `json:"order_id"`
}

type order struct {
	item, status string
	totalUSD     float64
}

var orders = map[string]order{
	"1042": {"1 × Blue Kettle 1.7 L", "shipped", 64.00},
	"1077": {"1 × Cast Iron Pan 28 cm", "delivered", 129.00},
	"1090": {"1 × Espresso Machine", "processing", 212.50},
}

func lookupOrder(ctx context.Context, in orderIn) (string, error) {
	o, ok := orders[in.OrderID]
	if !ok {
		slog.WarnContext(ctx, "order not found", "order_id", in.OrderID)
		return "", &core.ToolError{Code: "NOT_FOUND", Message: "no order " + in.OrderID}
	}
	slog.InfoContext(ctx, "order loaded", "order_id", in.OrderID, "status", o.status, "total_usd", o.totalUSD)
	return fmt.Sprintf(`{"order_id":%q,"items":%q,"status":%q,"total_usd":%.2f}`, in.OrderID, o.item, o.status, o.totalUSD), nil
}

func trackShipment(ctx context.Context, in orderIn) (string, error) {
	slog.InfoContext(ctx, "carrier answered", "carrier", "UPS", "order_id", in.OrderID)
	return `{"carrier":"UPS","tracking":"1Z999AA10123456784","state":"out for delivery","eta":"today"}`, nil
}

func searchCatalog(_ context.Context, in struct {
	Query string `json:"query"`
}) (string, error) {
	return `[{"sku":"KET-BLU-17","name":"Blue Kettle 1.7 L","price_usd":64.00},{"sku":"KET-RED-17","name":"Red Kettle 1.7 L","price_usd":64.00}]`, nil
}

func checkInventory(ctx context.Context, in struct {
	SKU string `json:"sku"`
}) (string, error) {
	slog.InfoContext(ctx, "inventory read", "sku", in.SKU, "in_stock", 23)
	return `{"sku":"` + in.SKU + `","in_stock":23,"warehouse":"Dallas"}`, nil
}

func shippingQuote(_ context.Context, in struct {
	City string `json:"city"`
}) (string, error) {
	return `{"city":"` + in.City + `","standard_usd":6.50,"express_usd":14.00}`, nil
}

func updateAddress(ctx context.Context, in struct {
	OrderID string `json:"order_id"`
	Address string `json:"address"`
}) (string, error) {
	slog.ErrorContext(ctx, "address service unavailable", "order_id", in.OrderID, "status", 503)
	return "", &core.ToolError{Code: "UPSTREAM_UNAVAILABLE", Message: "address service answered 503"}
}

// exportOrders is the slow one: it takes half a minute whatever the
// speed, so there is always time to cancel it.
func exportOrders(ctx context.Context, _ struct {
	Year int `json:"year"`
}) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(30 * time.Second):
		return `{"rows":318,"sent_to":"customer inbox"}`, nil
	}
}

func handoff(context.Context, struct {
	Summary string `json:"summary"`
}) (string, error) {
	return `{"ticket":"T-5521"}`, nil
}

func listCharges(ctx context.Context, in orderIn) (string, error) {
	slog.InfoContext(ctx, "charges listed", "order_id", in.OrderID, "count", 2)
	return `[{"id":"ch_1","amount_usd":129.00,"at":"10:14:02"},{"id":"ch_2","amount_usd":129.00,"at":"10:14:06"}]`, nil
}

func issueRefund(ctx context.Context, in struct {
	OrderID   string  `json:"order_id"`
	AmountUSD float64 `json:"amount_usd"`
	Reason    string  `json:"reason"`
}) (string, error) {
	slog.InfoContext(ctx, "refund issued", "order_id", in.OrderID, "amount_usd", in.AmountUSD, "reason", in.Reason)
	return fmt.Sprintf(`{"refund_id":"re_%s","amount_usd":%.2f,"state":"settled"}`, in.OrderID, in.AmountUSD), nil
}

func notifyCustomer(_ context.Context, in struct {
	OrderID string `json:"order_id"`
	Message string `json:"message"`
}) (string, error) {
	return `{"sent":true}`, nil
}

func storeCredit(_ context.Context, in struct {
	OrderID   string  `json:"order_id"`
	AmountUSD float64 `json:"amount_usd"`
}) (string, error) {
	return `{"credit_id":"sc_` + in.OrderID + `"}`, nil
}
