package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/weftbase/weftbase/internal/termsafe"
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

// `weftbase signout --all`: end every Console session of the server's
// project and env, and every unused sign-in link (DELETE
// /v1/auth/sessions; a secret key). For a leaked cookie or link, or an
// operator who leaves. Signing out of one session is the Console's own
// button.
func runSignOut(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("signout", stderr)
	base, token := clientFlags(fs)
	all := fs.Bool("all", false, "end every Console session and unused sign-in link (required: the CLI holds no session of its own)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("signout takes no argument (got %s)", gotArgs(pos))
	}
	if !*all {
		return usageError("signout needs --all: it ends every Console session (the Console's Sign out button ends one)")
	}
	c, _ := clientFor(*base, *token, stderr)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, c.Base()+"/v1/auth/sessions", nil)
	if err != nil {
		return err
	}
	if c.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token())
	}
	resp, err := c.HTTP().Do(req)
	if err != nil {
		return fmt.Errorf("weftbase not reachable at %s: %w", c.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var doc struct {
		SessionsEnded int `json:"sessions_ended"`
		Error         struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &doc)
	if resp.StatusCode != http.StatusOK {
		msg := doc.Error.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("DELETE /v1/auth/sessions: %d %s", resp.StatusCode, termsafe.Text(msg))
	}
	_, err = fmt.Fprintf(stdout, "weftbase: %d Console session(s) ended; unused sign-in links revoked\n", doc.SessionsEnded)
	return err
}
