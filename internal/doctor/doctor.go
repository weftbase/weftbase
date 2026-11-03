// Package doctor is `weftbase doctor` (plan B5): it asks a running
// server what it is and how it is wired, and prints one line per
// check. Every line reads fields of GET /v1/meta (Lines is the table)
// or GET /readyz — beside them, for the discovery check, the discovery
// file this shell reads (Target.Discovery, the command's resolver's). The doctor
// computes nothing the server does not serve: a second source of truth
// would drift.
//
// The logic lives here, apart from the command wiring (cmd/weftbase), so
// the command stays a thin client of it.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/weftbase/weftbase/internal/termsafe"
)

// Timeout bounds the one request the doctor makes: an unreachable or
// hung server is a clear first line, never a hang.
const Timeout = 5 * time.Second

// Line is one check the doctor prints: its label and the /v1/meta
// fields (or "local:discovery" for the discovery file it reads,
// "readyz" for GET /readyz, or "status" for the meta response itself)
// it reads.
type Line struct {
	Label  string
	Fields []string
}

// Lines is the doctor's line table, in print order.
var Lines = []Line{
	{"server", []string{"status", "weft_version", "server_version"}},
	{"key", []string{"status", "principal.kind", "principal.id", "principal.scope"}},
	{"ready", []string{"status", "readyz"}},
	{"discovery", []string{"pid", "db.path", "local:discovery"}},
	{"db", []string{"db.kind", "db.path", "db.size"}},
	{"content", []string{"content.ingest", "content.latest.run_id", "content.latest.mark", "content.latest.note", "content.latest.fix", "content.error"}},
	{"data", []string{"content.latest.started", "content.latest.weft_version", "content.latest.service", "weft_version"}},
	{"otlp", []string{"capabilities"}},
	{"ingest", []string{"ingest.dropped", "ingest.accepted", "ingest.refused", "ingest.last_error", "ingest.last_at"}},
	{"manifest", []string{"manifest_check.source", "manifest_check.agents", "manifest_check.checked", "manifest_check.stale", "manifest_check.error"}},
}

// meta is the slice of GET /v1/meta the doctor reads.
type meta struct {
	WeftVersion   string          `json:"weft_version"`
	ServerVersion string          `json:"server_version"`
	DB            json.RawMessage `json:"db"`
	Capabilities  []string        `json:"capabilities"`
	// Principal is who the key authenticated as.
	Principal struct {
		Kind    string `json:"kind"`
		ID      string `json:"id"`
		KeyKind string `json:"key_kind"`
		Scope   struct {
			Project string `json:"project"`
			Env     string `json:"env"`
			Tenant  string `json:"tenant"`
		} `json:"scope"`
	} `json:"principal"`
	// PID is the server's process id (served to a caller allowed the
	// host's details).
	PID     int `json:"pid"`
	Content struct {
		Ingest string `json:"ingest"`
		Error  string `json:"error"`
		Latest *struct {
			RunID string `json:"run_id"`
			Mark  string `json:"mark"`
			Note  string `json:"note"`
			Fix   string `json:"fix"`
			// Started, WeftVersion and Service are the newest run's own:
			// the data line (absent from a server older than it).
			Started     time.Time `json:"started"`
			WeftVersion string    `json:"weft_version"`
			Service     string    `json:"service"`
		} `json:"latest"`
	} `json:"content"`
	// Ingest is the ingest health (U5): absent from a server older
	// than it.
	Ingest *struct {
		Dropped []struct {
			Service   string    `json:"service"`
			Records   int64     `json:"records"`
			LastHeard time.Time `json:"last_heard"`
		} `json:"dropped"`
		// Accepted and Refused count the OTLP batches server answered
		// 2xx and 4xx/5xx since it started, LastError / LastAt the last
		// refusal (UX-E-37): nil from a server older than them.
		Accepted  *int64     `json:"accepted"`
		Refused   *int64     `json:"refused"`
		LastError string     `json:"last_error"`
		LastAt    *time.Time `json:"last_at"`
	} `json:"ingest"`
	ManifestCheck *struct {
		Source  string   `json:"source"` // file
		Agents  int      `json:"agents"`
		Checked int      `json:"checked"`
		Stale   []string `json:"stale"`
		Error   string   `json:"error"`
		// Latest is each checked agent's latest run's manifest hash.
		Latest map[string]string `json:"latest"`
	} `json:"manifest_check"`
}

// Target is the server the doctor checks, found by cmd/weftbase's one
// resolver (--url, WEFTBASE_URL, the discovery file, the default
// address), which names the source on stderr itself: the doctor's own
// lines do not repeat it.
type Target struct {
	// URL is the server's root URL.
	URL string
	// Token is the bearer to send, "" for none.
	Token string
	// Discovery is the discovery file this shell reads.
	Discovery Discovery
}

// Discovery is the discovery file the doctor's shell reads (the
// resolver's view of it, read with internal/discovery's trust rules).
type Discovery struct {
	// Off is WEFTBASE_DISCOVERY=off in this shell: no file is read.
	Off bool
	// Path is the trusted file found, "" when none.
	Path string
	// URL, DB and PID are what the file names.
	URL, DB string
	PID     int
}

// ErrUnhealthy is Run's error when any check failed: the command exits 1.
var ErrUnhealthy = errors.New("doctor: a check failed")

// Run checks the server t names (t.Token "" sends none) and prints one
// line per check to w. It returns ErrUnhealthy when a check failed —
// the server not reachable, the token refused — and nil otherwise;
// warnings (content stripped, a stale weft.json, a discovery file
// naming another server) do not fail it.
func Run(ctx context.Context, w io.Writer, t Target) error {
	url, token := strings.TrimRight(t.URL, "/"), t.Token
	p := printer{w: w}
	where := url
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/meta", nil)
	if err != nil {
		p.raw("weftbase not reachable at %s: %v", url, err)
		return ErrUnhealthy
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// The doctor talks to --url only: a redirect is reported (the
	// default branch below), never followed.
	client := &http.Client{
		Timeout:       Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		p.raw("weftbase not reachable at %s: %v", where, err)
		p.sub("start one with `weftbase dev`, or point --url (WEFTBASE_URL) at the one you run")
		return ErrUnhealthy
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		p.raw("weftbase not reachable at %s: %v", url, err)
		return ErrUnhealthy
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		p.line(ok, "server", "reachable at %s", url)
		if token == "" {
			p.line(fail, "key", "the server requires an API key: `weftbase dev` keeps its bootstrap key in <data>/bootstrap.key (the start banner names it); set WEFTBASE_TOKEN or --token to it")
			p.sub("or run from the directory the server started in, where its discovery file is found")
		} else {
			p.line(fail, "key", "refused: not a live key of this server (WEFTBASE_TOKEN / --token)")
		}
		return ErrUnhealthy
	default:
		p.line(ok, "server", "reachable at %s", url)
		msg := errorMessage(body)
		if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 {
			msg = "a redirect to " + loc + ", not followed: point --url at the server itself"
		}
		p.line(fail, "key", "/v1/meta answered %d: %s", resp.StatusCode, msg)
		return ErrUnhealthy
	}
	var m meta
	if err := json.Unmarshal(body, &m); err != nil {
		p.raw("weftbase not reachable at %s: /v1/meta is not JSON: %v", url, err)
		return ErrUnhealthy
	}

	p.line(ok, "server", "reachable at %s (weft %s, server %s)", where, m.WeftVersion, m.ServerVersion)
	who := m.Principal.Kind
	if m.Principal.KeyKind != "" {
		who += " " + m.Principal.KeyKind
	}
	if m.Principal.ID != "" {
		who += " " + m.Principal.ID
	}
	p.line(ok, "key", "accepted: %s in %s/%s/%s", who, m.Principal.Scope.Project, m.Principal.Scope.Env, m.Principal.Scope.Tenant)
	p.ready(ctx, client, url)
	p.discovery(m, url, t.Discovery)
	p.db(m.DB)
	p.content(m)
	p.data(m)
	p.otlp(m, url)
	p.ingest(m)
	p.manifest(m)
	if p.failed {
		return ErrUnhealthy
	}
	return nil
}

const (
	ok   = "ok"
	warn = "warn"
	fail = "FAIL"
)

// printer writes the doctor's lines. Every line passes termsafe.Text:
// what it prints comes from server (service names, run ids, notes,
// error text an OTLP sender may have chosen), and a control character
// never reaches the terminal raw.
type printer struct {
	w      io.Writer
	failed bool
}

// line prints one check: status, label, text.
func (p *printer) line(status, label, format string, args ...any) {
	if status == fail {
		p.failed = true
	}
	_, _ = fmt.Fprintf(p.w, "%-4s %-9s %s\n", status, label, termsafe.Text(fmt.Sprintf(format, args...)))
}

// sub prints a continuation of the previous line's check.
func (p *printer) sub(format string, args ...any) {
	_, _ = fmt.Fprintf(p.w, "               - %s\n", termsafe.Text(fmt.Sprintf(format, args...)))
}

// raw prints a line outside the table (the unreachable first line).
func (p *printer) raw(format string, args ...any) {
	_, _ = fmt.Fprintln(p.w, termsafe.Text(fmt.Sprintf(format, args...)))
}

func (p *printer) db(raw json.RawMessage) {
	var db struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
		Size *int64 `json:"size"`
	}
	if err := json.Unmarshal(raw, &db); err != nil {
		// A server older than this doctor serves db as the kind alone.
		var kind string
		_ = json.Unmarshal(raw, &kind)
		p.line(ok, "db", "%s (this server predates db.path)", kind)
		return
	}
	switch {
	case db.Path != "" && db.Size != nil:
		p.line(ok, "db", "%s %s (%s)", db.Kind, db.Path, humanBytes(*db.Size))
	case db.Path != "":
		p.line(ok, "db", "%s %s", db.Kind, db.Path)
	default:
		p.line(ok, "db", "%s (no file path served to this caller)", db.Kind)
	}
}

func (p *printer) content(m meta) {
	policy := "server stores content as received"
	if m.Content.Ingest != "as_received" {
		policy = "server ingest policy " + m.Content.Ingest
	}
	l := m.Content.Latest
	if m.Content.Error != "" {
		p.line(warn, "content", "%s; the latest run could not be read: %s", policy, m.Content.Error)
		if l != nil {
			p.sub("latest run %s: %s — %s", l.RunID, l.Mark, l.Note)
		}
		return
	}
	if l == nil {
		p.line(ok, "content", "%s; no run stored yet", policy)
		return
	}
	status := ok
	if l.Mark != "full" {
		status = warn
	}
	p.line(status, "content", "%s; latest run %s: %s — %s", policy, l.RunID, l.Mark, l.Note)
	if l.Fix != "" {
		p.sub("fix: %s", l.Fix)
	}
}

// refusalWindow is how recent the last refused batch must be for the
// ingest line to warn.
const refusalWindow = time.Hour

// ingest prints the ingest health: the OTLP batches this server
// accepted and refused (and the last refusal), then the services whose
// heartbeats said their destination lost records on the way
// (weft.records.dropped). A warning, not a failure: the runs say which
// of theirs are missing (the gap badge, "N of M events were lost").
// Refusals warn only within refusalWindow of the last one.
func (p *printer) ingest(m meta) {
	if m.Ingest == nil {
		p.line(ok, "ingest", "not served (no ingest.dropped): a server older than ingest health")
		return
	}
	in := m.Ingest
	counts := ""
	refused := in.Refused != nil && *in.Refused > 0
	if in.Accepted != nil && in.Refused != nil {
		counts = fmt.Sprintf("%d OTLP batches accepted, %d refused; ", *in.Accepted, *in.Refused)
	}
	// A refusal warns while it is recent: the last one within the hour
	// (refusalWindow), or one with no time said. The counts are since
	// Server started, so one refused batch long ago reads ok, its line
	// still printed below.
	if refused && in.LastAt != nil && time.Since(*in.LastAt) > refusalWindow {
		refused = false
	}
	status := ok
	if refused || len(in.Dropped) > 0 {
		status = warn
	}
	if len(in.Dropped) == 0 {
		p.line(status, "ingest", "%sno app reported lost records (weft.records.dropped on its heartbeats and run brackets)", counts)
	} else {
		p.line(status, "ingest", "%s%d service(s) reported records lost on the way here", counts, len(in.Dropped))
	}
	if in.LastError != "" {
		at := ""
		if in.LastAt != nil {
			at = " at " + in.LastAt.Format(time.RFC3339)
		}
		p.sub("last refused%s: %s", at, in.LastError)
		p.sub("fix: server's log names each refused batch with its request id (WARN \"weftbase: ingest batch refused\"); a 401 is the app's API key (exporter.Remote(url, key))")
	}
	if len(in.Dropped) > 0 {
		for _, d := range in.Dropped {
			svc := d.Service
			if svc == "" {
				svc = "(no service.name)"
			}
			p.sub("%s: %d records dropped by its destination (last heard %s)", svc, d.Records, d.LastHeard.Format(time.RFC3339))
		}
		// weft.records.dropped counts every lost record — a full queue
		// and failed exports alike — so the fix names both; the app's
		// WARN says which it was.
		p.sub("fix: queue full or exports failing — the app's WARN names which (\"weft/otel: destination queue full\": raise it with otel.QueueSize(n), or use otel.Blocking() under WEFT_ENV=dev (or otel.DevMode()); \"weft/otel: destination dropped records\": its err is the export's failure)")
	}
}

func (p *printer) manifest(m meta) {
	// The label is the check, the source is the first words (UX-S5b-09):
	// "weft.json".
	c := m.ManifestCheck
	switch {
	case c == nil:
		p.line(ok, "manifest", "none: no weft.json configured (weftbase.Manifest, or --manifest / WEFT_MANIFEST), nothing to compare")
	case c.Error != "":
		p.line(warn, "manifest", "the check failed: %s", c.Error)
	case len(c.Stale) > 0:
		p.line(warn, "manifest", "weft.json: stale for %s: weft.json and the latest runs disagree%s: regenerate weft.json, or redeploy the app if weft.json is newer", strings.Join(c.Stale, ", "), lastRunHash(c.Stale, c.Latest))
	case c.Checked == 0:
		p.line(ok, "manifest", "weft.json: %s; none has a recorded run to compare yet", agentsN(c.Agents))
	default:
		p.line(ok, "manifest", "weft.json: %s, %d checked against their latest runs: current", agentsN(c.Agents), c.Checked)
	}
}

// agentsN is "1 agent" / "n agents".
func agentsN(n int) string {
	if n == 1 {
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// lastRunHash names the first stale agent's latest run's manifest hash
// (manifest_check.latest), 8 characters: " (the latest run of orders
// ran manifest d2151022)"; "" when meta served none.
func lastRunHash(stale []string, latest map[string]string) string {
	if len(stale) == 0 || latest[stale[0]] == "" {
		return ""
	}
	h := latest[stale[0]]
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf(" (the latest run of %s ran manifest %s)", stale[0], h)
}

// discovery prints how apps find this server: the discovery file this
// shell reads, against the one this server says it wrote. Another
// Server's file is a warning — apps that set no WEFTBASE_URL send
// there — and two servers on one database a second one.
func (p *printer) discovery(m meta, url string, d Discovery) {
	var dbPath string
	var db struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(m.DB, &db) == nil {
		dbPath = db.Path
	}
	switch {
	case d.Off:
		p.line(ok, "discovery", "WEFTBASE_DISCOVERY=off in this shell: no file read; apps and this command need WEFTBASE_URL=%s", url)
	case d.Path == "":
		p.line(warn, "discovery", "no discovery file: an app that sets no WEFTBASE_URL finds no server (weftbase dev writes one; WEFTBASE_DISCOVERY=off writes none)")
		p.sub("./.weftbase is read from the working directory: run from the directory the server started in")
	case strings.TrimRight(d.URL, "/") == url || m.PID > 0 && m.PID == d.PID:
		// This server, however its host is spelled (localhost,
		// 127.0.0.1): the file it wrote, or its pid.
		p.line(ok, "discovery", "%s -> %s (this server)", d.Path, d.URL)
	default:
		p.line(warn, "discovery", "%s -> %s, ANOTHER server (pid %d, db %s): an app that sets no WEFTBASE_URL sends there, not to %s", d.Path, d.URL, d.PID, orNone(d.DB), url)
		if dbPath != "" && d.DB != "" && filepath.Clean(d.DB) == filepath.Clean(dbPath) {
			p.sub("two servers serve %s: stop one (each has its own live stream; an app's live frames reach only the one it dials)", dbPath)
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// data prints the newest stored run: when it arrived, which weft and
// service sent it — a warning when that weft is not this server's (runs
// from an older weft lack what it added; their gaps are badged).
func (p *printer) data(m meta) {
	l := m.Content.Latest
	switch {
	case m.Content.Error != "":
		p.line(warn, "data", "the newest run could not be read (the content line says why)")
		return
	case l == nil:
		p.line(ok, "data", "no run stored yet: an app that records to this server fills it")
		return
	case l.Started.IsZero():
		p.line(ok, "data", "newest run %s (this server predates content.latest.started)", l.RunID)
		return
	}
	ago := time.Since(l.Started)
	if ago < time.Minute {
		ago = ago.Round(time.Second)
	} else {
		ago = ago.Round(time.Minute)
	}
	svc := ""
	if l.Service != "" {
		svc = " (service " + l.Service + ")"
	}
	switch {
	case l.WeftVersion == "":
		p.line(ok, "data", "newest run %s ago%s carries no weft version: a non-weft exporter", ago, svc)
	case l.WeftVersion != m.WeftVersion:
		p.line(warn, "data", "newest run %s ago by weft %s%s, this server is weft %s: the versions differ — what one records and the other does not read is badged on the run", ago, l.WeftVersion, svc, m.WeftVersion)
	default:
		p.line(ok, "data", "newest run %s ago by weft %s%s", ago, l.WeftVersion, svc)
	}
}

// otlp prints the OTLP ingest's door: served by this node (the ingest
// role, an API key as the bearer), or not.
func (p *printer) otlp(m meta, url string) {
	if !slices.Contains(m.Capabilities, "ingest") {
		p.line(warn, "otlp", "this node runs no ingest role: an app's spans cannot reach it here (serve --roles=…,ingest)")
		return
	}
	p.line(ok, "otlp", "OTLP at %s/otlp/v1/traces and /otlp/v1/logs, an API key as the bearer (exporter.Remote(url, key); OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer%%20<key>)", url)
}

// ready prints GET /readyz: whether the node's stores answer.
func (p *printer) ready(ctx context.Context, client *http.Client, url string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/readyz", nil)
	if err != nil {
		p.line(fail, "ready", "%v", err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		p.line(fail, "ready", "GET /readyz: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		p.line(fail, "ready", "GET /readyz answered %d: %s", resp.StatusCode, errorMessage(body))
		return
	}
	p.line(ok, "ready", "the stores answer (GET /readyz)")
}

// errorMessage reads a server error body's message, or the body.
func errorMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(body))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
