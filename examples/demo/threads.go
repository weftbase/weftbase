package main

// A conversation kept by weft's thread package, the way an app that
// owns its sessions runs one: in this process, outside the node's run
// ledger, its traces and session records exported to the node like any
// app's. It walks through what a session can do, so the Console's
// conversation view has each of them to show: an approval decided by a
// person, a grant given and then used, a message steered into a running
// turn and one queued behind it, a compaction, a turn interrupted by
// the next message, a turn rolled back, and a fork.

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/thread"

	"github.com/weftbase/weftbase/examples/demo/shop"
)

const (
	deskHello     = "Hi, has order #1042 shipped yet?"
	deskWrap      = "Add gift wrap to order #1042."
	deskWrapAgain = "Add gift wrap to order #1077 as well."
	deskSummary   = "Summarise my last three orders."
	deskSteer     = "Only this month's orders, please."
	deskThanks    = "Thanks, that's all for now."
	deskStock     = "Is the red kettle back in stock?"
	deskNevermind = "Never mind the kettle, I found it."
	deskShip      = "Ship order #1042 to my office instead."
	deskKeep      = "Actually, keep the home address."
	deskReturn    = "What if I return the kettle instead?"
)

// hold is a tool call the script keeps open until it has sent the next
// message, so "while the turn is running" is exact at any speed.
type hold struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (h *hold) arm() (entered <-chan struct{}, release func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entered, h.release = make(chan struct{}), make(chan struct{})
	rel := h.release
	return h.entered, sync.OnceFunc(func() { close(rel) })
}

func (h *hold) wait(ctx context.Context) error {
	h.mu.Lock()
	entered, release := h.entered, h.release
	h.entered = nil
	h.mu.Unlock()
	if entered == nil {
		return nil
	}
	close(entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
		return nil
	}
}

// summarizer is the model the session compacts with: it always answers
// with the same summary.
type summarizer struct{ speed shop.Speed }

func (summarizer) Info() core.ModelInfo {
	return core.ModelInfo{Provider: "openai", Name: "gpt-4o-mini"}
}

func (s summarizer) Stream(ctx context.Context, req core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	turn := wefttest.Say("The customer added gift wrap to orders #1042 and #1077 (approved by an operator, then under a standing grant) " +
		"and asked for this month's orders: #1042 Blue Kettle $64.00 and #1077 Cast Iron Pan $129.00.").
		WithUsage(core.Usage{InputTokens: 1840, OutputTokens: 58})
	return func(yield func(core.ModelEvent, error) bool) {
		if err := s.speed.Sleep(ctx, 900*time.Millisecond); err != nil {
			yield(nil, err)
			return
		}
		for ev, err := range wefttest.Script(turn).Stream(ctx, req) {
			if !yield(ev, err) {
				return
			}
		}
	}
}

func deskAgent(speed shop.Speed, h *hold) *core.Agent {
	type orderIn struct {
		OrderID string `json:"order_id"`
	}
	held := func(name, desc, result string) *core.ToolDef {
		return shop.Slow(speed, 600*time.Millisecond, name, desc, func(ctx context.Context, _ struct {
			Query string `json:"query"`
		}) (string, error) {
			if err := h.wait(ctx); err != nil {
				return "", err
			}
			return result, nil
		}, core.Replay(core.ReplaySafe)) // a lookup: cut off by an interrupt, it is simply dropped
	}
	return core.New(shop.NewModel(core.ModelInfo{Provider: "anthropic", Name: "claude-haiku-4-5"}, speed, map[string][]shop.Beat{
		deskHello: {shop.Say(700*time.Millisecond, 240, 26, "Not yet: order #1042 (Blue Kettle, $64.00) is being packed and ships tomorrow.")},
		deskWrap: {
			shop.Calls(700*time.Millisecond, 310, 28, shop.Tool("add_gift_wrap", `{"order_id":"1042"}`)),
			shop.Say(800*time.Millisecond, 402, 22, "Gift wrap is added to order #1042.").OrDenied("I could not add gift wrap: it was not approved."),
		},
		deskWrapAgain: {
			shop.Calls(700*time.Millisecond, 468, 28, shop.Tool("add_gift_wrap", `{"order_id":"1077"}`)),
			shop.Say(800*time.Millisecond, 560, 22, "Done, order #1077 is gift wrapped too."),
		},
		deskSummary: {
			shop.Calls(700*time.Millisecond, 640, 24, shop.Tool("order_history", `{"query":"last 3 orders"}`)),
			shop.Say(900*time.Millisecond, 820, 52, "Your last three orders: #1042 Blue Kettle, #1077 Cast Iron Pan and #1090 Espresso Machine."),
		},
		deskSteer: {
			shop.Say(900*time.Millisecond, 860, 44, "This month: #1042 Blue Kettle ($64.00) and #1077 Cast Iron Pan ($129.00)."),
		},
		deskThanks: {shop.Say(500*time.Millisecond, 910, 10, "You're welcome!")},
		deskStock: {
			shop.Calls(700*time.Millisecond, 300, 24, shop.Tool("warehouse_stock", `{"query":"red kettle"}`)),
			shop.Say(700*time.Millisecond, 380, 18, "The red kettle is back: 6 in stock."),
		},
		deskNevermind: {shop.Say(500*time.Millisecond, 330, 12, "No problem. Anything else?")},
		deskShip: {
			shop.Calls(700*time.Millisecond, 350, 30, shop.Tool("shipping_options", `{"query":"order 1042 to office"}`)),
			shop.Say(700*time.Millisecond, 430, 20, "Order #1042 can ship to your office at no extra cost. Shall I change it?"),
		},
		deskKeep:   {shop.Say(500*time.Millisecond, 360, 14, "Understood: order #1042 still ships to your home address.")},
		deskReturn: {shop.Say(800*time.Millisecond, 480, 30, "Returns are free within 30 days; I can email you a label for order #1042.")},
	}),
		core.Name("desk"),
		core.Instructions("You are the order desk of an online kitchenware store. Prices are in USD."),
		shop.Slow(speed, 400*time.Millisecond, "add_gift_wrap", "Add gift wrap to an order.", func(_ context.Context, in orderIn) (string, error) {
			return `{"order_id":"` + in.OrderID + `","gift_wrap":true}`, nil
		}, core.RequireApproval()),
		held("order_history", "A customer's past orders.", `[{"order_id":"1042","month":"this"},{"order_id":"1077","month":"this"},{"order_id":"1090","month":"last"}]`),
		held("warehouse_stock", "Stock in the warehouse.", `{"in_stock":6}`),
		held("shipping_options", "Where an order can ship, and what it costs.", `{"office":{"usd":0},"home":{"usd":0}}`),
	)
}

// deskSession plays the conversation and its fork.
func deskSession(ctx context.Context, speed shop.Speed) error {
	h := &hold{}
	agent := deskAgent(speed, h)
	s, err := thread.Create(ctx, thread.Memory(), agent,
		thread.PublicID("pub_desk_"+newID()[:8]),
		thread.SummaryModel(summarizer{speed}), thread.ContextWindow(200_000), thread.KeepRecent(1))
	if err != nil {
		return err
	}
	defer func() { _ = s.Close(context.WithoutCancel(ctx)) }()
	if err := s.SetInfo(ctx, "Gift wrap, this month's orders", nil); err != nil {
		return err
	}
	send := func(text string, opts ...thread.SendOption) (*thread.Turn, error) {
		return s.Send(ctx, core.User(text), opts...)
	}
	answer := func(text string) error {
		t, err := send(text)
		if err != nil {
			return err
		}
		_, err = t.Wait()
		return err
	}
	// while runs first, waits until its tool call is open, runs during,
	// then lets the call finish.
	while := func(first string, during func() error) (*thread.Turn, error) {
		entered, release := h.arm()
		defer release()
		t, err := send(first)
		if err != nil {
			return nil, err
		}
		select {
		case <-entered:
		case <-t.Done():
			return t, errors.New("the turn ended before its tool call")
		case <-ctx.Done():
			return t, ctx.Err()
		}
		return t, during()
	}

	// 0. An ordinary turn. The fork at the end starts from here.
	if err := answer(deskHello); err != nil {
		return err
	}
	forkAt := s.Leaf()

	// 1. A call that needs a person: the turn parks, an operator
	// approves, the turn resumes.
	t, err := send(deskWrap)
	if err != nil {
		return err
	}
	if res, err := t.Wait(); err != nil || len(res.Pending) != 1 {
		return fmt.Errorf("desk: turn 1 did not park: %v", err)
	}
	pause(ctx, speed, 3*time.Second)
	d := thread.Approve(s.Pending()[0].CallID).Bind(s.Pending()[0])
	d.Who = "op_maria"
	resumed, err := s.Decide(ctx, d)
	if err != nil {
		return err
	}
	if _, err := resumed.Wait(); err != nil {
		return err
	}

	// 2. A standing grant: the operator allows gift wrap for the rest of
	// the conversation, so the next call does not park.
	if err := s.Grant(ctx, thread.Grant{Tool: "add_gift_wrap", Who: "op_maria", MaxUses: 5, Reason: "gift wrap is free this month"}); err != nil {
		return err
	}
	if err := answer(deskWrapAgain); err != nil {
		return err
	}

	// 3. While a turn runs, one message is steered into it and another
	// queues behind it.
	var queued *thread.Turn
	t, err = while(deskSummary, func() error {
		if _, err := send(deskSteer, thread.As(thread.Steer)); err != nil {
			return err
		}
		queued, err = send(deskThanks, thread.As(thread.Queue))
		return err
	})
	if err != nil {
		return err
	}
	if _, err := t.Wait(); err != nil {
		return err
	}
	if _, err := queued.Wait(); err != nil {
		return err
	}

	// 4. The context is compacted: a summary stands in for the turns so
	// far.
	if err := s.WaitIdle(ctx); err != nil {
		return err
	}
	if err := s.Compact(ctx); err != nil {
		return fmt.Errorf("desk: compact: %w", err)
	}

	// 5. A turn interrupted by the next message.
	var next *thread.Turn
	if _, err = while(deskStock, func() error {
		next, err = send(deskNevermind, thread.As(thread.Interrupt))
		return err
	}); err != nil {
		return err
	}
	if _, err := next.Wait(); err != nil {
		return err
	}

	// 6. A turn rolled back: the next message also takes the
	// conversation back to before it.
	if _, err = while(deskShip, func() error {
		next, err = send(deskKeep, thread.As(thread.Rollback))
		return err
	}); err != nil {
		return err
	}
	if _, err := next.Wait(); err != nil {
		return err
	}
	if err := s.WaitIdle(ctx); err != nil {
		return err
	}

	// 7. A fork: another conversation that starts from the first turn.
	fork, err := s.Fork(ctx, forkAt)
	if err != nil {
		return fmt.Errorf("desk: fork: %w", err)
	}
	defer func() { _ = fork.Close(context.WithoutCancel(ctx)) }()
	ft, err := fork.Send(ctx, core.User(deskReturn))
	if err != nil {
		return err
	}
	_, err = ft.Wait()
	return err
}
