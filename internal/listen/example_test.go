package listen_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	v1 "github.com/weftbase/weftbase/api/http/v1"
	"github.com/weftbase/weftbase/internal/listen"
)

// ExampleChoose starts a server the way the command does — Choose,
// then serve on the Choice's listener — and starts it again on the
// same database: the second Choose binds nothing and names the running
// one to reuse. (The command wants listen.DefaultAddr; the example
// takes an ephemeral port so it runs anywhere.)
func ExampleChoose() {
	dir, _ := os.MkdirTemp("", "listen")
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "weft.db")
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	want := free.Addr().String()
	_ = free.Close()
	req := listen.Request{Addr: want, Span: 2, DBPath: path, Token: "dev-token"}

	first, err := listen.Choose(context.Background(), req)
	if err != nil {
		fmt.Println(err)
		return
	}
	srv := v1.New(v1.Base("/"), v1.Open(path), v1.Token("dev-token"))
	defer func() { _ = srv.Close() }()
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: time.Second}
	go func() { _ = hs.Serve(first.Listener) }()
	defer func() { _ = hs.Close() }()
	fmt.Println("first: listening on the wanted address:", first.Addr == want)

	second, err := listen.Choose(context.Background(), req)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("second: reuse:", second.Reuse, "bound:", second.Listener != nil, "same pid:", second.PID == os.Getpid())
	// Output:
	// first: listening on the wanted address: true
	// second: reuse: true bound: false same pid: true
}
