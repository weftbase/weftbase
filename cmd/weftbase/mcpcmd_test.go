package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/weftbase/weftbase/api/mcp"
	"github.com/weftbase/weftbase/client/discovery"
)

// texts are a tool result's text items.
func texts(t *testing.T, res *sdk.CallToolResult) []string {
	t.Helper()
	var out []string
	for _, c := range res.Content {
		tc, ok := c.(*sdk.TextContent)
		if !ok {
			t.Fatalf("content %T, want text", c)
		}
		out = append(out, tc.Text)
	}
	return out
}

// TestMCPCommand pins the command line: -h lists the tools (exit 0),
// --read-only=false is refused (there is no write mode), arguments are
// refused, and with no server answering /v1/meta it serves, WARNs
// once, and a tool call isErrors with the line saying so.
func TestMCPCommand(t *testing.T) {
	t.Setenv("WEFTBASE_URL", "")
	t.Setenv("WEFTBASE_TOKEN", "")
	_, errb, code := cli("mcp", "-h")
	for _, name := range mcp.ToolNames() {
		if !strings.Contains(errb, "  "+name+" ") {
			t.Errorf("mcp -h lacks %s", name)
		}
	}
	if code != 0 || !strings.Contains(errb, "claude mcp add weft -- weftbase mcp\n") || !strings.Contains(errb, `"args":["mcp"]`) ||
		!strings.Contains(errb, "claude mcp add weft -- weftbase mcp --url http://127.0.0.1:7331") {
		t.Errorf("mcp -h = exit %d\n%s", code, errb)
	}
	if _, errb, code := cli("mcp", "--read-only=false"); code != 2 || !strings.Contains(errb, "no write mode") {
		t.Errorf("--read-only=false = exit %d %q", code, errb)
	}
	if _, errb, code := cli("mcp", "extra"); code != 2 || !strings.Contains(errb, "mcp takes no arguments") {
		t.Errorf("an argument = exit %d %q", code, errb)
	}
	// No server answering /v1/meta: it serves anyway (an MCP client
	// starts its servers once, maybe before the server), says so in one
	// WARN, and each tool call isErrors with the line and its fix.
	cmd := exec.Command(os.Args[0], "mcp", "--url", "http://127.0.0.1:1", "--token", "secret-tok")
	cmd.Env = append(os.Environ(), "WEFT_MCP_HELPER=1", "WEFTBASE_DISCOVERY=off", "WEFTBASE_URL=", "WEFTBASE_TOKEN=")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("no server: connect: %v (stderr %q), want it to serve", err, stderr.String())
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "run", Arguments: map[string]any{"id": "r_ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(t, res); !res.IsError || !strings.Contains(got[0], "no server answers /v1/meta: weftbase not reachable at http://127.0.0.1:1") ||
		!strings.Contains(got[0], "the next call retries the --url given") || strings.Contains(got[0], "secret-tok") {
		t.Errorf("a tool call with no server = %v %q, want the isError line with its fix", res.IsError, got)
	}
	_ = cs.Close()
	if errb := stderr.String(); strings.Count(errb, "level=WARN") != 1 || !strings.Contains(errb, "no server answers /v1/meta yet; tools will retry per call") ||
		!strings.Contains(errb, "read-only tools over stdio") || strings.Contains(errb, "secret-tok") {
		t.Errorf("no server: stderr %q, want one WARN and the start line, never the token", errb)
	}
}

// TestMCPServerResolution pins where the server and token come from:
// --url and --token first, then the env, then a trusted discovery file
// (its token only for the server it names), WEFTBASE_DISCOVERY=off skipping
// it, then the default address with no token.
func TestMCPServerResolution(t *testing.T) {
	t.Setenv("WEFTBASE_URL", "")
	t.Setenv("WEFTBASE_TOKEN", "")
	t.Setenv("WEFTBASE_DISCOVERY", "")
	w, err := discovery.Write(discovery.Info{URL: "http://127.0.0.1:7339", Token: "disc-tok", PID: os.Getpid(), Started: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Remove()
	for _, c := range []struct {
		name, url, tok, env, envTok, disc string
		wantURL, wantTok, wantFrom        string
	}{
		{"discovery", "", "", "", "", "", "http://127.0.0.1:7339", "disc-tok", "from the discovery file"},
		{"url names the file's server", "http://127.0.0.1:7339/", "", "", "", "", "http://127.0.0.1:7339/", "disc-tok", "from the discovery file"},
		{"another url: no file token", "http://127.0.0.1:7000", "", "", "", "", "http://127.0.0.1:7000", "", "none"},
		{"env url", "", "", "http://127.0.0.1:7001", "", "", "http://127.0.0.1:7001", "", "none"},
		{"flags win", "http://h:1", "f", "http://127.0.0.1:7001", "e", "", "http://h:1", "f", "from --token"},
		{"env token", "", "", "", "e", "", "http://127.0.0.1:7339", "e", "from WEFTBASE_TOKEN"},
		{"discovery off", "", "", "", "", "off", defaultURL, "", "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WEFTBASE_URL", c.env)
			t.Setenv("WEFTBASE_TOKEN", c.envTok)
			t.Setenv("WEFTBASE_DISCOVERY", c.disc)
			u, tok, from := mcpServer(c.url, c.tok)
			if u != c.wantURL || tok != c.wantTok || from != c.wantFrom {
				t.Errorf("mcpServer(%q, %q) = %q %q %q, want %q %q %q", c.url, c.tok, u, tok, from, c.wantURL, c.wantTok, c.wantFrom)
			}
		})
	}
}
