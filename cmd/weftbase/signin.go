package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// `weftbase signin`: a fresh one-time Console sign-in link from a
// running server (POST /v1/auth/links). The links `weftbase dev` prints
// at start are spent once and lapse after 15 minutes; this is how to
// get in afterwards, without a restart. It needs a secret key: --token,
// WEFTBASE_TOKEN, or the discovery file of a `weftbase dev` on this
// machine (so beside a dev server it needs no flag at all).
func runSignIn(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("signin", stderr)
	base, token := clientFlags(fs)
	origin := fs.String("origin", "", "where the Console is opened, when not the server itself (a dev server: http://localhost:3000)")
	open := fs.Bool("open", false, "open the link in the browser too")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("signin takes no argument (got %s)", gotArgs(pos))
	}
	c, _ := clientFor(*base, *token, stderr)
	var body any
	if *origin != "" {
		body = map[string]string{"origin": *origin}
	}
	b, err := c.Post(context.Background(), "/v1/auth/links", body)
	if err != nil {
		return err
	}
	var doc struct {
		Link      string    `json:"link"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Link == "" {
		return fmt.Errorf("POST /v1/auth/links: an answer with no link: %.200s", b)
	}
	if _, err := fmt.Fprintln(stdout, doc.Link); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "weftbase: one use, until %s\n", doc.ExpiresAt.Local().Format("15:04:05"))
	if *open {
		openBrowser(stdout, doc.Link)
	}
	return nil
}
