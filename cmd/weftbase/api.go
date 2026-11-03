package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/weftbase/weftbase/client/rest"

	"github.com/weftbase/weftbase/internal/termsafe"
	"github.com/weftbase/weftbase/obsdb"
)

// The API clients: runs, open and export are thin clients of the
// server's JSON API over client/rest. Each request is one documented
// route; nothing here computes what the server serves.

// parseArgs parses fs over args with the positional arguments allowed
// anywhere — `weftbase export <id> --format jsonl` — and returns them. A
// "--" ends the flags: everything after it is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := parse(fs, args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// ── weftbase runs ──────────────────────────────────────────────────────

// runRow is the part of GET /api/runs' row the table prints; --json
// prints the row as server serves it.
type runRow struct {
	ID          string     `json:"id"`
	Agent       string     `json:"agent"`
	Status      string     `json:"status"`
	Started     time.Time  `json:"started"`
	Finished    *time.Time `json:"finished"`
	Steps       int        `json:"steps"`
	StoppedBy   string     `json:"stopped_by"`
	CancelCause string     `json:"cancel_cause"`
	Prompt      string     `json:"prompt_preview"`
	PublicID    string     `json:"public_id"`
	Err         string     `json:"err"`
	ErrorType   string     `json:"error_type"`
	CostUSD     *float64   `json:"cost_usd"`
	CostBadge   string     `json:"cost_badge"`
}

// errorCells is the ERROR cell's width: the error cut to this many
// runes, "…" marking the cut.
const errorCells = 60

// errorCell is the ERROR cell (UX-S5a-05): a failed run's kind
// (error_type) and its error's last clause — what a terminal triages
// on — cut to errorCells runes; "-" when the run carries none. The
// error is untrusted text (an OTLP sender's), never printed raw.
func (r runRow) errorCell() string {
	msg := strings.TrimSpace(termsafe.Text(firstLine(r.Err)))
	// "weft: run failed at step 0: model stream: mw: giving up …": the
	// loop's wrapping prefixes say where (the run page shows them), the
	// rest says why.
	msg = errPrefix.ReplaceAllString(msg, "")
	switch {
	case r.ErrorType != "" && msg != "":
		msg = r.ErrorType + ": " + msg
	case r.ErrorType != "":
		msg = r.ErrorType
	}
	if msg == "" {
		return "-"
	}
	if rs := []rune(msg); len(rs) > errorCells {
		return strings.TrimSpace(string(rs[:errorCells-1])) + "…"
	}
	return msg
}

// errPrefix is the loop's and the middleware's wrapping of a run error.
var errPrefix = regexp.MustCompile(`^(weft: )?(run failed at step \d+: )?(model stream: )?(mw: )?`)

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// duration is the DUR cell: finished − started, "-" while running.
func (r runRow) duration() string {
	if r.Finished == nil || r.Finished.Before(r.Started) {
		return "-"
	}
	d := r.Finished.Sub(r.Started)
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// cost is the COST cell: the run's own cost in USD, "$0" for a
// scripted model's exact zero, "-" when unpriced or unknown (server's
// cost_badge says why; --json carries it).
func (r runRow) cost() string {
	if r.CostUSD == nil {
		return "-"
	}
	if *r.CostUSD == 0 {
		return "$0"
	}
	if *r.CostUSD < 0.01 {
		return fmt.Sprintf("$%.4f", *r.CostUSD)
	}
	return fmt.Sprintf("$%.2f", *r.CostUSD)
}

// promptCells is the PROMPT cell's width: the preview's first line cut
// to this many runes, "…" marking the cut.
const promptCells = 40

// prompt is the PROMPT cell: the run's prompt_preview — its first line,
// every control character (tabs, escape sequences, carriage returns) a
// space, cut to promptCells runes with "…" — or "-" when the row
// carries none (a content-off run stores no prompt; server's run page
// badges why). The preview is untrusted text — an app's prompt, an
// OTLP sender's record — and never reaches the terminal raw.
func (r runRow) prompt() string {
	line, _, _ := strings.Cut(strings.TrimSpace(r.Prompt), "\n")
	line = strings.TrimSpace(termsafe.Text(line))
	if line == "" {
		return "-"
	}
	if rs := []rune(line); len(rs) > promptCells {
		return strings.TrimSpace(string(rs[:promptCells-1])) + "…"
	}
	return line
}

// stoppedBy is the STOPPED BY cell: what ended the run as server names
// it (stopped_by), a canceled run's cause beside it, "-" when nothing
// recorded one (a model error, a run still going, an older run).
func (r runRow) stoppedBy() string {
	switch {
	case r.StoppedBy == "":
		return "-"
	case r.CancelCause != "":
		return cell(r.StoppedBy + " (" + r.CancelCause + ")")
	}
	return cell(r.StoppedBy)
}

// cell is a table cell from free text server served (an agent name, a
// cause, an id an OTLP sender chose): control characters as spaces,
// "-" for nothing — the one rule every printed cell follows.
func cell(s string) string {
	s = strings.TrimSpace(termsafe.Text(s))
	if s == "" {
		return "-"
	}
	return s
}

// runsPageSize is the page each GET /api/runs asks for (a variable so
// a test pages through the cursor).
var runsPageSize = 100

// runRuns is `weftbase runs`: the newest top-level runs, one row each,
// paged through GET /api/runs's cursor.
func runRuns(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("runs", stderr)
	base, token := clientFlags(fs)
	agent := fs.String("agent", "", "only this agent's runs (the API's agent=)")
	since := fs.String("since", "", "only runs started after this: a `duration` back from now (2h, 30m) or an RFC 3339 time")
	failed := fs.Bool("failed", false, "only failed runs (the API's status=failed)")
	status := fs.String("status", "", "only runs of this `status`: running, succeeded, failed, interrupted, parked or cancelled (the API's status=)")
	limit := fs.Int("limit", 50, "print at most this many runs (0: every matching run)")
	asJSON := fs.Bool("json", false, "print the rows as server serves them, a JSON array")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("runs takes no arguments (got %s)", gotArgs(pos))
	}
	if *limit < 0 {
		return usageError("--limit must be 0 or more")
	}
	var after time.Time
	if *since != "" {
		if after, err = parseSince(*since, time.Now()); err != nil {
			return err
		}
	}
	c, _ := clientFor(*base, *token, stderr)
	q := url.Values{}
	if *agent != "" {
		q.Set("agent", *agent)
	}
	st, err := obsdb.ParseStatus(*status)
	if err != nil {
		return usageError("--status: %v", err)
	}
	if *failed {
		// --failed narrows nothing a failed word does not: --status
		// cancelled (a failed run whose context ended) keeps its word.
		if st != "" && st.Stored() != obsdb.StatusFailed {
			return usageError("--failed and --status %s ask for different runs", st)
		}
		if st == "" {
			st = obsdb.StatusFailed
		}
	}
	if st != "" {
		q.Set("status", string(st))
	}
	var raws []json.RawMessage
	var rows []runRow
	ctx := context.Background()
	cut := false // stopped at --limit with more runs to list
	seen := map[string]bool{}
pages:
	for {
		q.Set("limit", strconv.Itoa(runsPageSize))
		body, err := c.Get(ctx, "/api/runs", q)
		if err != nil {
			return err
		}
		var page struct {
			Runs         []json.RawMessage `json:"runs"`
			NextBefore   *time.Time        `json:"next_before"`
			NextBeforeID *string           `json:"next_before_id"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("GET /api/runs: %w", err)
		}
		for i, raw := range page.Runs {
			var r runRow
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("GET /api/runs: %w", err)
			}
			// Newest first: the first run older than --since ends it.
			if !after.IsZero() && r.Started.Before(after) {
				break pages
			}
			raws, rows = append(raws, raw), append(rows, r)
			if *limit > 0 && len(rows) == *limit {
				cut = i < len(page.Runs)-1 || page.NextBefore != nil
				break pages
			}
		}
		// A page with no runs, or a cursor this listing already
		// followed, ends it: a cursor that does not move never loops.
		if page.NextBefore == nil || len(page.Runs) == 0 {
			break
		}
		before, beforeID := page.NextBefore.Format(time.RFC3339Nano), ""
		if page.NextBeforeID != nil {
			beforeID = *page.NextBeforeID
		}
		if seen[before+"\x00"+beforeID] {
			_, _ = fmt.Fprintln(stderr, "weftbase: the runs cursor did not move; stopping")
			break
		}
		seen[before+"\x00"+beforeID] = true
		q.Set("before", before)
		q.Del("before_id")
		if beforeID != "" {
			q.Set("before_id", beforeID)
		}
	}
	// Truncation is visible: the limit says so when it hid runs.
	if cut {
		_, _ = fmt.Fprintf(stderr, "weftbase: showing the newest %d; --limit 0 lists all\n", *limit)
	}
	if *asJSON {
		if raws == nil {
			raws = []json.RawMessage{}
		}
		b, err := json.MarshalIndent(raws, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", b)
		return err
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(stderr, "weftbase: no runs match")
		return nil
	}
	// The PUBLIC ID column appears when a row carries one (the id the
	// app's logs show; weftbase open takes it).
	pub := false
	for _, r := range rows {
		pub = pub || r.PublicID != ""
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	head := "ID\tAGENT\tSTATUS\tSTARTED\tSTEPS\tDUR\tCOST\t"
	if pub {
		head += "PUBLIC ID\t"
	}
	_, _ = fmt.Fprintln(tw, head+"PROMPT\tSTOPPED BY\tERROR")
	for _, r := range rows {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s\t", cell(r.ID), cell(r.Agent), cell(r.Status),
			r.Started.Local().Format("2006-01-02 15:04:05 -0700"), r.Steps, r.duration(), r.cost())
		if pub {
			line += cell(r.PublicID) + "\t"
		}
		_, _ = fmt.Fprintf(tw, "%s%s\t%s\t%s\n", line, r.prompt(), r.stoppedBy(), r.errorCell())
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// The next step, on stderr: stdout stays the table.
	_, _ = fmt.Fprintln(stderr, "weftbase: weftbase open <id> shows one in server; weftbase export <id> --format jsonl writes it out")
	return nil
}

// parseSince reads --since: a duration back from now, or an RFC 3339
// time.
func parseSince(v string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return time.Time{}, usageError("--since %s: a duration back from now is positive", v)
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, usageError("--since %q: want a duration (2h, 30m) or an RFC 3339 time (2026-10-08T09:00:00Z)", v)
}

// ── weftbase open ──────────────────────────────────────────────────────

// runOpen is `weftbase open [id]`: the page of a run, a conversation (a
// public id), a session or a trace — the id is tried as each kind in
// turn against the API (UX-S5a-06) — or, with no id, the server root
// (UX-S5a-02: on a fresh server with no run, the one signed-in link a
// terminal can print). The link is printed and with --open opened.
func runOpen(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("open", stderr)
	base, token := clientFlags(fs)
	open := fs.Bool("open", false, "open the link in the browser too, the token in its fragment")
	withToken := fs.Bool("with-token", false, "print the link with the token in its fragment (#token=), signed in; keep it out of logs")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || len(pos) == 1 && pos[0] == "" {
		return usageError("open takes at most one id — a run id, a public id, a session id or a trace id (got %s)", gotArgs(pos))
	}
	c, _ := clientFor(*base, *token, stderr)
	ctx := context.Background()
	page := "/"
	if len(pos) == 1 {
		if page, err = pageOf(ctx, c, pos[0]); err != nil {
			return err
		}
	} else if _, err := c.Get(ctx, "/api/meta", nil); err != nil {
		// The root: checked the server answers and the token opens it.
		return err
	}
	// The printed link is bare unless asked: a fixed token stays out of
	// logs (serveBoot's banner rule). The browser gets it in the fragment.
	printed := rest.PageLink(c.Base(), page, "")
	if *withToken {
		printed = rest.PageLink(c.Base(), page, c.Token())
	}
	if _, err := fmt.Fprintln(stdout, printed); err != nil {
		return err
	}
	if *open {
		openBrowser(stdout, rest.PageLink(c.Base(), page, c.Token()))
	}
	return nil
}

// idKinds are the ids `weftbase open` takes, in the order it tries them:
// the API route that knows the id and the UI page that shows it.
var idKinds = []struct{ noun, route, page string }{
	{"run", "/api/runs/", "/runs/"},
	{"public id", "/api/public/", "/conversations/"},
	{"session", "/api/sessions/", "/sessions/"},
	{"trace", "/api/traces/", "/traces/"},
}

// pageOf is the UI page of id: the first kind whose route knows it. A
// 404 tries the next kind; any other failure is the error. An id no
// kind knows is an error naming what was tried.
func pageOf(ctx context.Context, c *rest.Client, id string) (string, error) {
	var tried []string
	for _, k := range idKinds {
		_, err := c.Get(ctx, k.route+url.PathEscape(id), nil)
		var se *rest.StatusError
		switch {
		case err == nil:
			return k.page + url.PathEscape(id), nil
		case errors.As(err, &se) && se.Status == http.StatusNotFound:
			tried = append(tried, k.noun)
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("%s is no %s this server knows (%s)", termsafe.Text(id), strings.Join(tried, ", "), c.Redacted())
}

// ── weftbase export ────────────────────────────────────────────────────

// runExport is `weftbase export <id>`: GET /api/runs/<id>/export to
// stdout, as json, jsonl or otlp.
func runExport(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("export", stderr)
	base, token := clientFlags(fs)
	format := fs.String("format", "json", "the export written to stdout: json, jsonl or otlp")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] == "" {
		return usageError("export needs exactly one run id (got %s)", gotArgs(pos))
	}
	id := pos[0]
	switch *format {
	case "json", "jsonl", "otlp":
	default:
		return usageError("--format must be json, jsonl or otlp")
	}
	c, _ := clientFor(*base, *token, stderr)
	body, err := c.Get(context.Background(), rest.RunPath(id)+"/export", url.Values{"format": {*format}})
	if err != nil {
		return err
	}
	_, err = stdout.Write(body)
	return err
}
