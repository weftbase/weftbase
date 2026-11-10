// Package rest is the Go client of a weftbase server's HTTP API: one
// GET per documented route, the server's error shape decoded into a
// StatusError. `weftbase runs`, `open`, `export`, `doctor` and the MCP
// tool set (api/mcp) read through it.
//
// It is hand-written for now over the /v1 read routes; a later step
// generates it from the /v1 OpenAPI contract (api/openapi/v1.yaml). It
// imports the standard library only. The token is an API key
// (wb_sk_…), sent as the bearer.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Client talks to one server's API.
type Client struct {
	base  string // the server's root URL, no trailing slash
	token string
	http  *http.Client
}

// New is a client of the server at base (http(s)://host:port), sending
// token as the bearer when it is not "". Its http.Client bounds a
// request at two minutes and never follows a redirect: the client talks
// to base only (Go keeps the bearer on a same-host other-port hop and
// on https→http).
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http: &http.Client{
			// An export is a whole run: the bound is generous, never absent.
			Timeout:       2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// With is a client of another base and token over the same
// http.Client (its transport, timeout and redirect rule).
func (c *Client) With(base, token string) *Client {
	nc := *c
	nc.base, nc.token = strings.TrimRight(base, "/"), token
	return &nc
}

// Base is the server's root URL, no trailing slash.
func (c *Client) Base() string { return c.base }

// Token is the bearer the client sends ("" for none).
func (c *Client) Token() string { return c.token }

// HTTP is the client's http.Client: a caller may wrap its Transport
// (api/mcp walls it to GET and HEAD).
func (c *Client) HTTP() *http.Client { return c.http }

// Redacted is the server's URL for a message: a password in its
// userinfo is never printed.
func (c *Client) Redacted() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return "the URL given"
	}
	return u.Redacted()
}

// MaxBody bounds what the client reads from one response.
const MaxBody = 512 << 20

// Get answers GET path?q with the response body; a non-2xx answer is a
// *StatusError carrying the server's own error message, a request that
// got no answer the transport's error.
func (c *Client) Get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weftbase not reachable at %s: %w (weftbase dev starts one; --url or WEFTBASE_URL points at another)", c.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("GET %s: the response exceeds %d MiB", path, MaxBody>>20)
	}
	if resp.StatusCode/100 == 3 {
		loc := resp.Header.Get("Location")
		if loc == "" {
			loc = "(no Location)"
		}
		return nil, fmt.Errorf("GET %s: %d, a redirect to %s, not followed: point --url at the server itself", path, resp.StatusCode, loc)
	}
	if resp.StatusCode/100 != 2 {
		return nil, apiError(path, resp.StatusCode, body)
	}
	return body, nil
}

// Post answers POST path with body as JSON (nil: no body) and returns
// the response body; errors as Get's.
func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weftbase not reachable at %s: %w (weftbase dev starts one; --url or WEFTBASE_URL points at another)", c.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		err := apiError(path, resp.StatusCode, out)
		var se *StatusError
		if errors.As(err, &se) {
			se.Line = "POST" + strings.TrimPrefix(se.Line, "GET")
		}
		return nil, err
	}
	return out, nil
}

// apiError is a non-2xx answer as one line: the server's error message
// when the body is its error shape, the status otherwise.
func apiError(path string, status int, body []byte) error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = printable(e.Error.Message)
	}
	var err error = &StatusError{Status: status, Body: body, Line: fmt.Sprintf("GET %s: %d %s", path, status, msg)}
	if status == http.StatusUnauthorized {
		err = fmt.Errorf("%w (--token or WEFTBASE_TOKEN)", err)
	}
	return err
}

// StatusError is a non-2xx answer: its one line ("GET <path>: <status>
// <message>"), plus the status and the body the server sent (its
// error, and a badge with a reason when the route gave one) for a
// caller that passes them on — the MCP tools do.
type StatusError struct {
	Status int
	Body   []byte
	Line   string
}

func (e *StatusError) Error() string { return e.Line }

// printable replaces every control and bidirectional formatting rune of
// s with a space (and invalid UTF-8 with U+FFFD): a server's message is
// printed to a terminal.
func printable(s string) string {
	bad := func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }
	if strings.IndexFunc(s, bad) < 0 && utf8.ValidString(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if bad(r) {
			return ' '
		}
		return r
	}, s)
}

// RunPath is /v1/runs/<id>: a run id may carry slashes (a subagent's
// child id), percent-encoded; the server decodes the segment once.
func RunPath(id string) string { return "/v1/runs/" + url.PathEscape(id) }

// PageLink is a Console page, <base><page>. It carries no credential:
// the Console signs in with a one-time link (`weftbase dev` prints one).
func PageLink(base, page string) string { return base + page }

// RunLink is a run's page in the Console, <base>/runs/<id>.
func RunLink(base, id string) string {
	return PageLink(base, "/runs/"+url.PathEscape(id))
}
