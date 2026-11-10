// Command refunds is the local placement's test build: a weft agent
// over wefttest scripted models, served with client/worker. Built twice
// with a different -X main.build, it is two builds (two digests) of one
// agent definition (one manifest).
//
// The model answers by the transcript, so a fresh process can resume a
// run another one parked:
//   - a prompt "refund <n>" calls refund (it needs approval: the run
//     parks); once the call has its result, it says "refunded on
//     <build>";
//   - "wait" calls wait, which holds until its context ends or 300 ms
//     pass; afterwards the last user message is echoed back (a steer
//     lands there);
//   - "crash" calls crash, which exits the process.
//
// The refund tool appends "<build> <order>" to $REFUND_LOG.
package main

import (
	"context"
	"fmt"
	"iter"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/worker"
)

var build = "dev"

type orderIn struct {
	Order int `json:"order"`
}

type model struct{}

func (model) Info() core.ModelInfo { return wefttest.Script().Info() }

func (model) Stream(ctx context.Context, req core.ModelRequest) iter.Seq2[core.ModelEvent, error] {
	last := req.Messages[len(req.Messages)-1]
	var lastUser string
	for _, m := range req.Messages {
		if m.Role == core.RoleUser {
			lastUser = m.Text()
		}
	}
	first := req.Messages[0].Text()
	var turn wefttest.Turn
	switch {
	case last.Role == core.RoleTool && strings.HasPrefix(first, "refund"):
		turn = wefttest.Say("refunded on " + build)
	case last.Role == core.RoleTool:
		turn = wefttest.Say("last user message: " + lastUser)
	case strings.HasPrefix(first, "refund"):
		var n int
		_, _ = fmt.Sscanf(first, "refund %d", &n)
		turn = wefttest.ToolCalls(wefttest.Call{Name: "refund", Args: fmt.Sprintf(`{"order":%d}`, n)})
	case first == "wait":
		turn = wefttest.ToolCalls(wefttest.Call{Name: "wait"})
	case first == "crash":
		turn = wefttest.ToolCalls(wefttest.Call{Name: "crash"})
	default:
		turn = wefttest.Say("hello from " + build)
	}
	return wefttest.Script(turn).Stream(ctx, req)
}

func main() {
	refund := core.Tool("refund", "Refund an order.", func(_ context.Context, in orderIn) (string, error) {
		if p := os.Getenv("REFUND_LOG"); p != "" {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return "", err
			}
			defer func() { _ = f.Close() }()
			if _, err := fmt.Fprintf(f, "%s %d\n", build, in.Order); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("refunded %d", in.Order), nil
	}, core.RequireApproval())
	wait := core.Tool("wait", "Wait a little.", func(ctx context.Context, _ struct{}) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
			return "waited", nil
		}
	})
	crash := core.Tool("crash", "Exit the process.", func(context.Context, struct{}) (string, error) {
		os.Exit(3)
		return "", nil
	})
	agent := core.New(model{}, core.Name("refunds"), core.Instructions("Refund orders."), refund, wait, crash)
	// What the link does and why the process ends goes to stderr: the
	// tests show it when one fails.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			fmt.Fprintf(os.Stderr, "refunds %s (pid %d, ppid %d): got %v\n", build, os.Getpid(), os.Getppid(), s)
		}
	}()
	err := worker.Serve(worker.FromEnv(), worker.Agent(agent), worker.Heartbeat(200*time.Millisecond))
	fmt.Fprintf(os.Stderr, "refunds %s (pid %d): serve returned %v\n", build, os.Getpid(), err)
	if err != nil {
		log.Fatal(err)
	}
}
