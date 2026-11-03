package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/weftbase/weftbase/api/mcp"
	"github.com/weftbase/weftbase/client/rest"
)

// runMCP is `weftbase mcp`: resolve the server (--url, WEFTBASE_URL,
// the discovery file, the default address), then host api/mcp's read
// tools over stdio until stdin closes. A server that does not answer
// yet is no reason to exit: an MCP client starts its servers once, at
// session start, before the developer's `weftbase dev` may be up — so
// it WARNs and serves, and each tool call looks again.
// stdout is the protocol: every line of ours goes to stderr.
func runMCP(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("mcp", stderr)
	base := fs.String("url", "", "the weftbase server to talk to (default: $WEFTBASE_URL, else the discovery file's, else "+defaultURL+")")
	token := fs.String("token", "", "API token (default: $WEFTBASE_TOKEN, else the discovery file's when it names that server; never printed)")
	readOnly := fs.Bool("read-only", true, "implied and the only mode: every tool is a GET; write verbs (approve, deny, steer, cancel, delete, …) are not offered")
	fs.Usage = func() {
		_, _ = fmt.Fprint(fs.Output(), `Usage: weftbase mcp [--url] [--token]

A stdio MCP server over a running weftbase server's read API, for a coding
agent (Claude Code, Cursor) to read runs while you debug. Read only: every
tool is a GET; nothing is approved, denied, steered, cancelled or deleted.

  claude mcp add weft -- weftbase mcp
  .cursor/mcp.json: {"mcpServers":{"weft":{"command":"weftbase","args":["mcp"]}}}

The running server is found through its discovery file; to name another:
  claude mcp add weft -- weftbase mcp --url http://127.0.0.1:7331
  .cursor/mcp.json: {"mcpServers":{"weft":{"command":"weftbase","args":["mcp","--url","http://127.0.0.1:7331"]}}}

The server: --url, else $WEFTBASE_URL, else the discovery file a running
weftbase dev wrote (WEFTBASE_DISCOVERY=off ignores it), else `+defaultURL+`.
The token: --token, else $WEFTBASE_TOKEN, else the discovery file's (when it
names that server); sent in the Authorization header only, never printed.

Tools (each output is the route's JSON — compact rows on the list tools and
spans/trace, full=true as served — cut at max_bytes, default 64 KiB, with a note):
`+mcp.ToolTable()+`
Flags:
`)
		printDefaults(fs)
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("mcp takes no arguments (got %s)", gotArgs(pos))
	}
	if !*readOnly {
		return usageError("--read-only=false: weftbase mcp has no write mode (write verbs stay out until the approval story for agents exists)")
	}
	u, tok, from := mcpServer(*base, *token)
	o := mcp.Options{Client: rest.New(u, tok), TokenFrom: from, Stderr: stderr}
	if *base == "" {
		// No --url: every look re-reads WEFTBASE_URL and the discovery
		// file — a server started later, or `weftbase dev` on 7332–7340,
		// is found without a restart. An explicit --url is retried as
		// given.
		o.Resolve = func() (string, string) { u, tok, _ := mcpServer("", *token); return u, tok }
		// The token too comes from the discovery file when neither
		// --token nor WEFTBASE_TOKEN named one: a server restarted on
		// the same port with a new token answers 401 to the old one,
		// and the next call re-reads the file.
		o.RereadToken = *token == "" && os.Getenv("WEFTBASE_TOKEN") == ""
	}
	return mcp.Serve(context.Background(), o, os.Stdin, stdout)
}

// mcpServer resolves the server and its token through the one
// resolver every client shares (resolveServer: --url, WEFTBASE_URL,
// the discovery file, the default address; the token --token,
// WEFTBASE_TOKEN, else the discovery file's when it names that
// server) and says where the token came from (never the token).
func mcpServer(urlFlag, tokenFlag string) (base, token, from string) {
	t := resolveServer(urlFlag, tokenFlag)
	if t.tokenFrom == "none" {
		return t.base, "", "none"
	}
	return t.base, t.token, "from " + t.tokenFrom
}
