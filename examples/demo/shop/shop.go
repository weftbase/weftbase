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
	"sync"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
)

// Speed scales every artificial delay: 1 is realistic (hundreds of
// milliseconds to a few seconds per model call and tool), 0 is none.
type Speed float64

// Sleep waits d scaled by the speed, or until ctx ends.
func (s Speed) Sleep(ctx context.Context, d time.Duration) error {
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
	AskClose    = "Refund order #1077 and close my account."
	AskInvoice  = "Can you resend the invoice for order #1090?"
)

// Reply is the final answer the scripted model gives to prompt: what a
// caller puts in the history of a conversation's next turn.
func Reply(prompt string) string {
	for _, plays := range []map[string][]Beat{supportPlays, refundsPlays} {
		if beats := plays[prompt]; len(beats) > 0 {
			return beats[len(beats)-1].text
		}
	}
	return fallback
}

const fallback = "Is there anything else I can help you with?"

// Beat is one scripted model call: how long the "model" takes, and what
// it answers.
type Beat struct {
	wait   time.Duration
	turn   wefttest.Turn
	text   string // the turn's text, when it is a final answer
	denied string // said instead when the last tool result is an error
	flaky  bool   // the call's first attempt fails with a 503 (a retry answers)
}

// Fail is a model call that errors.
func Fail(wait time.Duration, err error) Beat {
	return Beat{wait: wait, turn: wefttest.Fail(err)}
}

// Thinking gives the call a reasoning part before its answer.
func (b Beat) Thinking(text string) Beat {
	b.turn = wefttest.Think(text, b.turn)
	return b
}

// Flaky makes every other attempt of the call fail with a retryable
// provider error: under mw.Retry the run shows a retried attempt.
func (b Beat) Flaky() Beat {
	b.flaky = true
	return b
}

// OrDenied is what the model says instead when the tool call before it
// was refused.
func (b Beat) OrDenied(text string) Beat {
	b.denied = text
	return b
}

// APIError is a provider's HTTP error, as an adapter would surface it.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s", e.StatusCode, e.Message) }

func Say(wait time.Duration, in, out int64, text string) Beat {
	return Beat{wait: wait, text: text, turn: wefttest.Say(text).WithUsage(core.Usage{InputTokens: in, OutputTokens: out})}
}

func Calls(wait time.Duration, in, out int64, calls ...wefttest.Call) Beat {
	return Beat{wait: wait, turn: wefttest.ToolCalls(calls...).WithUsage(core.Usage{InputTokens: in, OutputTokens: out})}
}

// tool is one scripted tool call. Every call gets its own id, as a
// provider's would have.
func Tool(name, args string) wefttest.Call {
	calls++
	return wefttest.Call{ID: fmt.Sprintf("call_%s_%02d", name, calls), Name: name, Args: args}
}

var calls int

// model plays one script per user message: the n-th model call after
// the last user message is the script's n-th Beat. It is stateless, so
// one agent serves any number of runs, and a run parked in one process
// resumes in another.
type model struct {
	info  core.ModelInfo
	speed Speed
	plays map[string][]Beat

	mu       *sync.Mutex
	attempts map[string]int // per flaky call
}

// NewModel is a scripted model: plays maps a user's message to the
// model calls that answer it, in order.
func NewModel(info core.ModelInfo, speed Speed, plays map[string][]Beat) core.Model {
	return model{info: info, speed: speed, plays: plays, mu: &sync.Mutex{}, attempts: map[string]int{}}
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
	b := Say(400*time.Millisecond, 180, 14, fallback)
	if beats := m.plays[prompt]; n < len(beats) {
		b = beats[n]
		if toolFailed && b.denied != "" {
			b = Say(b.wait, 420, 24, b.denied)
		}
	}
	flake := false
	if b.flaky {
		m.mu.Lock()
		key := fmt.Sprintf("%s#%d", prompt, n)
		m.attempts[key]++
		flake = m.attempts[key]%2 == 1
		m.mu.Unlock()
	}
	return func(yield func(core.ModelEvent, error) bool) {
		if err := m.speed.Sleep(ctx, b.wait); err != nil {
			yield(nil, err)
			return
		}
		if flake {
			yield(nil, &APIError{StatusCode: 503, Message: "upstream connect error or disconnect/reset before headers"})
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
		NewModel(core.ModelInfo{Provider: "acme", Name: "acme-billing-ft-1"}, speed, billingPlays),
		core.Name("billing"),
		core.Instructions("You audit charges on an order and explain them."),
		Slow(speed, 700*time.Millisecond, "list_charges", "List the charges on an order.", listCharges),
	)
	opts := []core.Option{
		core.Name("support"),
		core.Instructions("You are the support agent of an online kitchenware store. Prices are in USD."),
		core.MaxSteps(4),
		Slow(speed, 450*time.Millisecond, "lookup_order", "Look up an order by id.", lookupOrder),
		Slow(speed, 1200*time.Millisecond, "track_shipment", "Ask the carrier where a shipment is.", trackShipment),
		Slow(speed, 600*time.Millisecond, "search_catalog", "Search the product catalog.", searchCatalog),
		Slow(speed, 300*time.Millisecond, "check_inventory", "Stock of a product by SKU.", checkInventory),
		Slow(speed, 350*time.Millisecond, "shipping_quote", "Shipping price to a city.", shippingQuote),
		Slow(speed, 900*time.Millisecond, "update_address", "Change an order's delivery address.", updateAddress),
		Slow(speed, 200*time.Millisecond, "export_orders", "Export a customer's orders as CSV.", exportOrders),
		core.Subagent("billing", "Ask the billing specialist about charges on an order.", billing),
	}
	if release >= 2 {
		opts = append(opts, Slow(speed, 250*time.Millisecond, "handoff_to_human", "Open a ticket for a human agent.", handoff))
	}
	return core.New(NewModel(core.ModelInfo{Provider: "openai", Name: "gpt-4o-mini"}, speed, supportPlays), opts...)
}

// Refunds is the refunds agent: it looks an order up and issues the
// refund, which needs a person's approval. Release 2 can also offer
// store credit.
func Refunds(release int, speed Speed) *core.Agent {
	opts := []core.Option{
		core.Name("refunds"),
		core.Instructions("You process refund requests for an online kitchenware store. Amounts are in USD."),
		Slow(speed, 450*time.Millisecond, "lookup_order", "Look up an order by id.", lookupOrder),
		Slow(speed, 800*time.Millisecond, "issue_refund", "Refund an order to its payment method.", issueRefund, core.RequireApproval()),
		Slow(speed, 300*time.Millisecond, "notify_customer", "Email the customer.", notifyCustomer),
		Slow(speed, 500*time.Millisecond, "close_account", "Close a customer's account for good.", closeAccount, core.RequireApproval()),
	}
	if release >= 2 {
		opts = append(opts, Slow(speed, 300*time.Millisecond, "offer_store_credit", "Offer store credit instead of a refund.", storeCredit))
	}
	return core.New(NewModel(core.ModelInfo{Provider: "anthropic", Name: "claude-sonnet-4-6"}, speed, refundsPlays), opts...)
}

var supportPlays = map[string][]Beat{
	AskTracking: {
		Calls(900*time.Millisecond, 412, 46, Tool("lookup_order", `{"order_id":"1042"}`), Tool("track_shipment", `{"order_id":"1042"}`)),
		Say(1400*time.Millisecond, 688, 71, "Order #1042 (1 × Blue Kettle, $64.00) shipped on Monday with UPS and is out for delivery today."),
	},
	AskStock: {
		Calls(700*time.Millisecond, 398, 31, Tool("search_catalog", `{"query":"blue kettle"}`)),
		Calls(800*time.Millisecond, 571, 44, Tool("check_inventory", `{"sku":"KET-BLU-17"}`), Tool("shipping_quote", `{"city":"Austin"}`)),
		Say(1100*time.Millisecond, 742, 58, "Yes, the Blue Kettle 1.7 L is in stock (23 left) at $64.00. Standard shipping to Austin is $6.50."),
	},
	AskCharge: {
		Calls(800*time.Millisecond, 405, 38, Tool("lookup_order", `{"order_id":"1077"}`)),
		Calls(600*time.Millisecond, 590, 52, Tool("billing", `{"prompt":"`+askBilling+`"}`)),
		Say(1300*time.Millisecond, 803, 66, "You were right: order #1077 was charged twice ($129.00 each). The duplicate will be refunded by our refunds team."),
	},
	// The address service is down and the model keeps retrying: the run
	// fails at the agent's step limit.
	AskAddress: {
		Calls(700*time.Millisecond, 420, 40, Tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		Calls(700*time.Millisecond, 505, 40, Tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		Calls(700*time.Millisecond, 590, 40, Tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
		Calls(700*time.Millisecond, 675, 40, Tool("update_address", `{"order_id":"1090","address":"12 Elm St, Austin"}`)),
	},
	AskExport: {
		Calls(600*time.Millisecond, 401, 29, Tool("export_orders", `{"year":2023}`)),
		Say(900*time.Millisecond, 520, 33, "Your 2023 orders are on their way to your inbox."),
	},
	// The provider is overloaded: the model call itself fails.
	AskOutage: {
		{wait: 2200 * time.Millisecond, turn: wefttest.Fail(errors.New("openai: 529 the engine is currently overloaded, please try again later"))},
	},
	// The billing subagent fails; the parent carries on and succeeds.
	AskInvoice: {
		Calls(600*time.Millisecond, 396, 34, Tool("billing", `{"prompt":"`+askBillingInvoice+`"}`)),
		Say(1000*time.Millisecond, 520, 44, "I could not reach billing just now. I have asked a colleague to email you the invoice for order #1090 today."),
	},
	AskReturn: {
		Calls(600*time.Millisecond, 934, 30, Tool("lookup_order", `{"order_id":"1042"}`)),
		Say(1000*time.Millisecond, 1120, 49, "Yes. Order #1042 can be returned free of charge until June 30."),
	},
	AskThanks: {
		Say(500*time.Millisecond, 1210, 16, "You're welcome. Enjoy the kettle!"),
	},
}

const (
	askBilling        = "List the charges on order 1077 and say whether any is a duplicate."
	askBillingInvoice = "Resend the invoice of order 1090 to the customer."
)

var billingPlays = map[string][]Beat{
	askBilling: {
		Calls(500*time.Millisecond, 216, 22, Tool("list_charges", `{"order_id":"1077"}`)),
		Say(900*time.Millisecond, 344, 41, "Order 1077 has two identical card charges of $129.00, 4 seconds apart. The second is a duplicate."),
	},
	// The billing model is not deployed for invoices: the child run
	// fails, and the support agent reads that as a tool error.
	askBillingInvoice: {
		Fail(600*time.Millisecond, &APIError{StatusCode: 404, Message: "acme: model acme-billing-ft-1 has no deployment in region us-east"}),
	},
}

func refundPlay(order, amount, reason, done string) []Beat {
	issue := Calls(1100*time.Millisecond, 655, 57, Tool("issue_refund",
		fmt.Sprintf(`{"order_id":%q,"amount_usd":%s,"reason":%q}`, order, amount, reason)))
	notify := Calls(800*time.Millisecond, 790, 48, Tool("notify_customer",
		fmt.Sprintf(`{"order_id":%q,"message":"Your refund of $%s is on its way."}`, order, amount)))
	notify.denied = "I could not refund order #" + order + ": a reviewer declined the request."
	return []Beat{
		Calls(800*time.Millisecond, 380, 35, Tool("lookup_order", fmt.Sprintf(`{"order_id":%q}`, order))),
		issue,
		notify,
		Say(1200*time.Millisecond, 905, 52, done),
	}
}

var refundsPlays = map[string][]Beat{
	AskRefund:   refundPlay("1042", "64.00", "arrived damaged", "Done: $64.00 was refunded for order #1042 and the customer was notified."),
	AskRefund77: refundPlay("1077", "129.00", "duplicate charge", "Done: the duplicate $129.00 charge on order #1077 was refunded."),
	AskRefund90: refundPlay("1090", "212.50", "changed mind", "Done: $212.50 was refunded for order #1090."),
	AskThanks:   {Say(500*time.Millisecond, 1040, 12, "You're welcome.")},
	// Two calls that each need a person, one after the other: the run
	// parks twice.
	AskClose: {
		Calls(800*time.Millisecond, 392, 36, Tool("lookup_order", `{"order_id":"1077"}`)),
		Calls(1000*time.Millisecond, 660, 58, Tool("issue_refund", `{"order_id":"1077","amount_usd":129.00,"reason":"account closure"}`)),
		Calls(900*time.Millisecond, 812, 40, Tool("close_account", `{"customer":"cus_8841","confirm":true}`)).
			OrDenied("The refund was not approved, so I left the account open."),
		Say(1200*time.Millisecond, 940, 46, "Done: $129.00 was refunded for order #1077 and the account is closed.").
			OrDenied("Order #1077 was refunded, but closing the account was not approved; it stays open."),
	},
}

// ── Tools ────────────────────────────────────────────────────────────

// slow is a tool that takes wait (scaled by speed) before it answers.
func Slow[In any](speed Speed, wait time.Duration, name, desc string, fn func(context.Context, In) (string, error), opts ...core.ToolOption) *core.ToolDef {
	return core.Tool(name, desc, func(ctx context.Context, in In) (string, error) {
		if err := speed.Sleep(ctx, wait); err != nil {
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

func closeAccount(ctx context.Context, in struct {
	Customer string `json:"customer"`
	Confirm  bool   `json:"confirm"`
}) (string, error) {
	slog.WarnContext(ctx, "account closed", "customer", in.Customer)
	return `{"customer":"` + in.Customer + `","state":"closed"}`, nil
}

func storeCredit(_ context.Context, in struct {
	OrderID   string  `json:"order_id"`
	AmountUSD float64 `json:"amount_usd"`
}) (string, error) {
	return `{"credit_id":"sc_` + in.OrderID + `"}`, nil
}
