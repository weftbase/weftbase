package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/authz"
	"github.com/weftbase/weftbase/obsdb"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/tenant"
)

// The agent inventory: what is deployed and what each agent can do,
// read from the manifests its versions registered.
//
//	GET /v1/agents?include=summary          the list, each agent with its newest version's summary and window stats
//	GET /v1/agents/{name}                   one agent: its versions, each with a manifest summary and what changed
//	GET /v1/agents/{name}/versions/{a}/diff/{b}   two versions' manifests compared
//	GET /v1/tools                           every tool once, with who offers it

func (s *Server) inventoryRoutes() []Route {
	return []Route{
		{"GET", "/v1/agents/{name}", authz.AgentsRead, withID("name", s.serveAgent)},
		{"GET", "/v1/agents/{name}/versions/{a}/diff/{b}", authz.AgentsRead, s.serveVersionDiff},
		{"GET", "/v1/tools", authz.AgentsRead, s.serveTools},
	}
}

// registeredManifest is the part of a registered one-agent manifest
// (core.Manifest's JSON) the inventory reads.
type registeredManifest struct {
	Agents []struct {
		Name         string         `json:"name"`
		Model        core.ModelInfo `json:"model"`
		Instructions string         `json:"instructions"`
		Policy       struct {
			Parallelism     int      `json:"parallelism"`
			MaxSteps        int      `json:"max_steps"`
			MaxResultBytes  int      `json:"max_result_bytes"`
			Timeout         string   `json:"timeout"`
			MaxModelRetries int      `json:"max_model_retries"`
			DetectLoops     int      `json:"detect_loops"`
			StrictInput     bool     `json:"strict_input"`
			StopWhen        []string `json:"stop_when"`
			UsageLimit      *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage_limit"`
		} `json:"policy"`
		Tools []registeredTool `json:"tools"`
	} `json:"agents"`
}

type registeredTool struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	InputSchema     json.RawMessage `json:"input_schema"`
	Timeout         string          `json:"timeout"`
	RequireApproval bool            `json:"require_approval"`
	ReplayPolicy    string          `json:"replay_policy"`
	Sequential      bool            `json:"sequential"`
	Subagent        string          `json:"subagent"`
}

// versionManifest is what the inventory shows of a version's manifest.
// The full document (schemas, middleware, the instructions' text) is
// GET /v1/manifests/{manifest_hash}.
type versionManifest struct {
	Model        core.ModelInfo `json:"model"`
	Instructions struct {
		Present bool `json:"present"`
		Length  int  `json:"length"` // bytes
	} `json:"instructions"`
	Tools     []toolSummary `json:"tools"`
	Subagents []string      `json:"subagents"`
	Limits    limitsSummary `json:"limits"`
}

type toolSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	NeedsApproval bool   `json:"needs_approval"`
	// Subagent names the agent the tool hands off to, when it is one.
	Subagent string `json:"subagent,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

// limitsSummary are the agent's limits; a zero or absent one is not set.
type limitsSummary struct {
	MaxSteps        int      `json:"max_steps"`
	Parallelism     int      `json:"parallelism"`
	Timeout         string   `json:"timeout,omitempty"` // per tool call
	MaxResultBytes  int      `json:"max_result_bytes"`
	MaxModelRetries int      `json:"max_model_retries"`
	DetectLoops     int      `json:"detect_loops,omitempty"`
	StrictInput     bool     `json:"strict_input,omitempty"`
	StopWhen        []string `json:"stop_when,omitempty"`
	MaxInputTokens  int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens int64    `json:"max_output_tokens,omitempty"`
}

func summarize(body json.RawMessage) (versionManifest, []registeredTool, string, bool) {
	var doc registeredManifest
	if json.Unmarshal(body, &doc) != nil || len(doc.Agents) == 0 {
		return versionManifest{Tools: []toolSummary{}, Subagents: []string{}}, nil, "", false
	}
	a := doc.Agents[0]
	out := versionManifest{Model: a.Model, Tools: []toolSummary{}, Subagents: []string{}}
	out.Instructions.Present, out.Instructions.Length = a.Instructions != "", len(a.Instructions)
	for _, t := range a.Tools {
		out.Tools = append(out.Tools, toolSummary{Name: t.Name, Description: t.Description, NeedsApproval: t.RequireApproval,
			Subagent: t.Subagent, Timeout: t.Timeout})
		if t.Subagent != "" {
			out.Subagents = append(out.Subagents, t.Subagent)
		}
	}
	p := a.Policy
	out.Limits = limitsSummary{MaxSteps: p.MaxSteps, Parallelism: p.Parallelism, Timeout: p.Timeout,
		MaxResultBytes: p.MaxResultBytes, MaxModelRetries: p.MaxModelRetries,
		DetectLoops: p.DetectLoops, StrictInput: p.StrictInput, StopWhen: p.StopWhen}
	if p.UsageLimit != nil {
		out.Limits.MaxInputTokens, out.Limits.MaxOutputTokens = p.UsageLimit.InputTokens, p.UsageLimit.OutputTokens
	}
	return out, a.Tools, a.Instructions, true
}

// versionPage is one version in the inventory.
type versionPage struct {
	registry.Version
	Placement string `json:"placement"`
	// Manifest is null when the registered manifest could not be read.
	Manifest *versionManifest `json:"manifest"`
	// Changes compares the version with the one before it (by number);
	// null on an agent's first version.
	Changes *versionDiff `json:"changes"`
	// ManifestDrift is set on GET /v1/agents/{name}'s versions.
	ManifestDrift *versionDrift `json:"manifest_drift,omitempty"`
}

// versionDiff is what changed between two versions' manifests.
type versionDiff struct {
	From string `json:"from"` // version id
	To   string `json:"to"`
	// SameManifest: only the build differs.
	SameManifest        bool          `json:"same_manifest"`
	BuildChanged        bool          `json:"build_changed"`
	ModelChanged        *modelChange  `json:"model_changed"`
	InstructionsChanged bool          `json:"instructions_changed"`
	ToolsAdded          []string      `json:"tools_added"`
	ToolsRemoved        []string      `json:"tools_removed"`
	ToolsChanged        []toolChange  `json:"tools_changed"`
	LimitsChanged       []limitChange `json:"limits_changed"`
}

type modelChange struct {
	From core.ModelInfo `json:"from"`
	To   core.ModelInfo `json:"to"`
}

// toolChange names what differs in a tool both versions have:
// description, input_schema, needs_approval, timeout, replay_policy,
// sequential, subagent.
type toolChange struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

type limitChange struct {
	Field string `json:"field"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

type loaded struct {
	v       registry.Version
	summary versionManifest
	tools   []registeredTool
	prompt  string
	ok      bool
}

// inventory is one request's read of the registry.
type inventory struct {
	s          *Server
	ctx        context.Context
	sc         tenant.Scope
	placements map[string]string
	manifests  map[string]loaded // by manifest hash
}

func (s *Server) inventory(r *http.Request) *inventory {
	_, sc, _ := authz.From(r.Context())
	inv := &inventory{s: s, ctx: r.Context(), sc: sc, manifests: map[string]loaded{}}
	inv.placements, _ = s.registry.Placements(r.Context(), sc)
	return inv
}

func (inv *inventory) load(v registry.Version) loaded {
	l, ok := inv.manifests[v.ManifestHash]
	if !ok {
		if m, err := inv.s.registry.Manifest(inv.ctx, inv.sc, v.ManifestHash); err == nil {
			l.summary, l.tools, l.prompt, l.ok = summarize(m.Body)
		}
		inv.manifests[v.ManifestHash] = l
	}
	l.v = v
	return l
}

func (inv *inventory) doc(v registry.Version) versionPage {
	if v.Labels == nil {
		v.Labels = []string{}
	}
	d := versionPage{Version: v, Placement: inv.placements[v.BuildID]}
	if l := inv.load(v); l.ok {
		sum := l.summary
		d.Manifest = &sum
	}
	return d
}

func diffManifests(a, b loaded) versionDiff {
	d := versionDiff{From: a.v.ID, To: b.v.ID, SameManifest: a.v.ManifestHash == b.v.ManifestHash,
		BuildChanged: a.v.BuildDigest != b.v.BuildDigest,
		ToolsAdded:   []string{}, ToolsRemoved: []string{}, ToolsChanged: []toolChange{}, LimitsChanged: []limitChange{}}
	if d.SameManifest || !a.ok || !b.ok {
		return d
	}
	if a.summary.Model != b.summary.Model {
		d.ModelChanged = &modelChange{From: a.summary.Model, To: b.summary.Model}
	}
	d.InstructionsChanged = a.prompt != b.prompt
	before := map[string]registeredTool{}
	for _, t := range a.tools {
		before[t.Name] = t
	}
	for _, t := range b.tools {
		old, had := before[t.Name]
		if !had {
			d.ToolsAdded = append(d.ToolsAdded, t.Name)
			continue
		}
		delete(before, t.Name)
		var fields []string
		for name, differs := range map[string]bool{
			"description": old.Description != t.Description, "input_schema": !bytes.Equal(old.InputSchema, t.InputSchema),
			"needs_approval": old.RequireApproval != t.RequireApproval, "timeout": old.Timeout != t.Timeout,
			"replay_policy": old.ReplayPolicy != t.ReplayPolicy, "sequential": old.Sequential != t.Sequential,
			"subagent": old.Subagent != t.Subagent,
		} {
			if differs {
				fields = append(fields, name)
			}
		}
		if len(fields) > 0 {
			sort.Strings(fields)
			d.ToolsChanged = append(d.ToolsChanged, toolChange{Name: t.Name, Fields: fields})
		}
	}
	for _, t := range a.tools {
		if _, gone := before[t.Name]; gone {
			d.ToolsRemoved = append(d.ToolsRemoved, t.Name)
		}
	}
	la, lb := a.summary.Limits, b.summary.Limits
	for _, c := range []limitChange{
		{"max_steps", la.MaxSteps, lb.MaxSteps}, {"parallelism", la.Parallelism, lb.Parallelism},
		{"timeout", la.Timeout, lb.Timeout}, {"max_result_bytes", la.MaxResultBytes, lb.MaxResultBytes},
		{"max_model_retries", la.MaxModelRetries, lb.MaxModelRetries},
		{"max_input_tokens", la.MaxInputTokens, lb.MaxInputTokens}, {"max_output_tokens", la.MaxOutputTokens, lb.MaxOutputTokens},
	} {
		if c.From != c.To {
			d.LimitsChanged = append(d.LimitsChanged, c)
		}
	}
	return d
}

// windowStats are an agent's runs in the stats window.
type windowStats struct {
	Since       time.Time  `json:"since"`
	Until       time.Time  `json:"until"`
	Runs        int64      `json:"runs"`
	Succeeded   int64      `json:"succeeded"`
	Failed      int64      `json:"failed"`
	Cancelled   int64      `json:"cancelled"`
	Waiting     int64      `json:"waiting"`
	FailureRate *float64   `json:"failure_rate"` // failed / (succeeded + failed)
	LastRunID   string     `json:"last_run_id,omitempty"`
	LastRunAt   *time.Time `json:"last_run_at"`
}

// agentStats reads the window's runs and failures per agent, and each
// agent's newest run. since (RFC 3339, or 1h / 24h / 7d back from now;
// default 24h) is the window; false when it does not parse (400 sent).
func (s *Server) agentStats(w http.ResponseWriter, r *http.Request, names []string) (map[string]*windowStats, bool) {
	until := s.now().UTC()
	since, err := windowSince(r.URL.Query().Get("since"), until)
	if err != nil {
		badRequest(w, r, err.Error())
		return nil, false
	}
	out := map[string]*windowStats{}
	for _, n := range names {
		out[n] = &windowStats{Since: since, Until: until}
		if page, err := s.db.Runs(r.Context(), obsdb.RunQuery{Agent: n, Limit: 1}); err == nil && len(page.Runs) > 0 {
			started := page.Runs[0].Started
			out[n].LastRunID, out[n].LastRunAt = page.Runs[0].ID, &started
		}
	}
	// The runs by display status, as GET /v1/stats?by=agent's outcomes
	// count them: top-level runs, a parked run and its resume one run,
	// a cancelled run not a failure.
	if pass, err := s.outcomes(r.Context(), s.control(r), obsdb.RunQuery{}, since, until, 1, true); err == nil {
		for name, o := range pass.byAgent {
			if ws := out[name]; ws != nil {
				t := o.total
				ws.Runs, ws.Succeeded, ws.Failed, ws.Cancelled, ws.Waiting, ws.FailureRate = t.Runs, t.Succeeded, t.Failed, t.Cancelled, t.Waiting, t.FailureRate
			}
		}
	}
	return out, true
}

// agentSummary is an agent's row with include=summary: its newest
// version's model, placement and counts, and its window stats.
type agentSummary struct {
	Version       string         `json:"version"` // the newest version's id
	Number        int            `json:"number"`
	Model         core.ModelInfo `json:"model"`
	Placement     string         `json:"placement"`
	Tools         int            `json:"tools"`
	Subagents     int            `json:"subagents"`
	NeedsApproval int            `json:"needs_approval"` // tools that need a person
	Stats         *windowStats   `json:"stats"`
}

// labelRef is the version a label points to.
type labelRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

// labelVersions resolves an agent's labels (label → version id) against
// its versions.
func labelVersions(labels map[string]string, vs []registry.Version) map[string]labelRef {
	out := map[string]labelRef{}
	for label, id := range labels {
		ref := labelRef{ID: id}
		for _, v := range vs {
			if v.ID == id {
				ref.Number = v.Number
			}
		}
		out[label] = ref
	}
	return out
}

// versionDrift says whether runs of a version reported another manifest
// than the one it registered: the build serving it is not the code the
// version was made from (a rebuilt binary registered under one digest,
// an agent built at run time). Checked over the version's newest runs.
type versionDrift struct {
	Drift bool `json:"drift"`
	// RunsChecked are the version's newest runs read (at most 50);
	// RunsDrifted those of them that reported another manifest.
	RunsChecked int `json:"runs_checked"`
	RunsDrifted int `json:"runs_drifted"`
	// Since is the oldest drifted run read; LastRunID the newest.
	Since     *time.Time `json:"since"`
	LastRunID string     `json:"last_run_id,omitempty"`
	// ManifestHash is the manifest the newest drifted run reported.
	ManifestHash string `json:"manifest_hash,omitempty"`
}

func (inv *inventory) drift(v registry.Version) versionDrift {
	var d versionDrift
	page, err := inv.s.db.Runs(inv.ctx, obsdb.RunQuery{Agent: v.Agent, Meta: map[string]string{"weftbase.agent_version": v.ID}, Limit: 50})
	if err != nil {
		return d
	}
	for _, r := range page.Runs { // newest first
		d.RunsChecked++
		if r.ManifestHash == "" || r.ManifestHash == v.ManifestHash {
			continue
		}
		d.Drift = true
		d.RunsDrifted++
		started := r.Started
		d.Since = &started
		if d.LastRunID == "" {
			d.LastRunID, d.ManifestHash = r.ID, r.ManifestHash
		}
	}
	return d
}

type agentListRow struct {
	registry.Agent
	// LabelVersions is labels with each version's number.
	LabelVersions map[string]labelRef `json:"label_versions"`
	// Registered is false on an agent that only runs name: one the
	// window's traces saw and no build registered (a subagent, an app's
	// own agent reporting over OTLP). It has no id, labels or versions.
	Registered bool          `json:"registered"`
	Summary    *agentSummary `json:"summary,omitempty"`
}

// agentTotals is the inventory's header line.
type agentTotals struct {
	Agents     int      `json:"agents"`
	Tools      int      `json:"tools"` // distinct tool names over the agents' newest versions
	Placements []string `json:"placements"`
}

// serveAgentsSummary is GET /v1/agents?include=summary.
func (s *Server) serveAgentsSummary(w http.ResponseWriter, r *http.Request, agents []registry.Agent) {
	inv := s.inventory(r)
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	stats, ok := s.agentStats(w, r, names)
	if !ok {
		return
	}
	rows := make([]agentListRow, 0, len(agents))
	totals := agentTotals{Agents: len(agents), Placements: []string{}}
	tools := map[string]bool{}
	for _, a := range agents {
		row := agentListRow{Agent: a, Registered: true, Summary: &agentSummary{Stats: stats[a.Name]}}
		row.LabelVersions = map[string]labelRef{}
		if vs, err := s.registry.Versions(r.Context(), inv.sc, a.Name); err == nil && len(vs) > 0 {
			row.LabelVersions = labelVersions(a.Labels, vs)
			d := inv.doc(vs[0])
			row.Summary.Version, row.Summary.Number, row.Summary.Placement = d.ID, d.Number, d.Placement
			if d.Placement != "" && !slices.Contains(totals.Placements, d.Placement) {
				totals.Placements = append(totals.Placements, d.Placement)
			}
			if d.Manifest != nil {
				row.Summary.Model, row.Summary.Tools, row.Summary.Subagents = d.Manifest.Model, len(d.Manifest.Tools), len(d.Manifest.Subagents)
				for _, t := range d.Manifest.Tools {
					tools[t.Name] = true
					if t.NeedsApproval {
						row.Summary.NeedsApproval++
					}
				}
			}
		}
		rows = append(rows, row)
	}
	// The agents the window's runs name that the registry does not.
	if agg, ok := s.db.(obsdb.Aggregator); ok {
		until := s.now().UTC()
		since, _ := windowSince(r.URL.Query().Get("since"), until)
		if page, err := agg.Stats(r.Context(), obsdb.StatsQuery{By: obsdb.StatsByAgent, Since: since, Until: until,
			RunQuery: obsdb.RunQuery{ParentRunID: "*"}}); err == nil {
			for _, g := range page.Groups {
				if g.Key == "" || slices.Contains(names, g.Key) {
					continue
				}
				ws := &windowStats{Since: since, Until: until, Runs: g.Runs, Failed: g.Failed, FailureRate: ratio(g.Failed, g.Runs)}
				if p, err := s.db.Runs(r.Context(), obsdb.RunQuery{Agent: g.Key, ParentRunID: "*", Limit: 1}); err == nil && len(p.Runs) > 0 {
					started := p.Runs[0].Started
					ws.LastRunID, ws.LastRunAt = p.Runs[0].ID, &started
				}
				rows = append(rows, agentListRow{Agent: registry.Agent{Name: g.Key, Labels: map[string]string{}}, LabelVersions: map[string]labelRef{},
					Summary: &agentSummary{Stats: ws}})
			}
		}
	}
	totals.Tools = len(tools)
	sort.Strings(totals.Placements)
	writeJSON(w, r, http.StatusOK, map[string]any{"agents": rows, "totals": totals})
}

// agentPage is GET /v1/agents/{name}.
type agentPage struct {
	registry.Agent
	// Placement and Model are the newest version's.
	Placement string         `json:"placement"`
	Model     core.ModelInfo `json:"model"`
	// LabelVersions is labels with each version's number.
	LabelVersions map[string]labelRef `json:"label_versions"`
	// Newest is the newest version; ByLabel the version each label
	// points to. Both are entries of VersionList, repeated so a reader
	// needs no lookup.
	Newest      *versionPage           `json:"newest"`
	ByLabel     map[string]versionPage `json:"by_label"`
	VersionList []versionPage          `json:"version_list"` // newest first
	Stats       *windowStats           `json:"stats"`
}

// serveAgent is GET /v1/agents/{name}.
func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request, name string) {
	if !s.needRegistry(w, r) {
		return
	}
	inv := s.inventory(r)
	agents, err := s.registry.Agents(r.Context(), inv.sc)
	if err != nil {
		controlError(w, r, err)
		return
	}
	i := slices.IndexFunc(agents, func(a registry.Agent) bool { return a.Name == name })
	if i < 0 {
		notFound(w, r, "no agent "+name)
		return
	}
	vs, err := s.registry.Versions(r.Context(), inv.sc, name)
	if err != nil {
		controlError(w, r, err)
		return
	}
	stats, ok := s.agentStats(w, r, []string{name})
	if !ok {
		return
	}
	doc := agentPage{Agent: agents[i], LabelVersions: labelVersions(agents[i].Labels, vs), ByLabel: map[string]versionPage{}, VersionList: make([]versionPage, 0, len(vs)), Stats: stats[name]}
	for k, v := range vs { // newest first
		d := inv.doc(v)
		drift := inv.drift(v)
		d.ManifestDrift = &drift
		if k+1 < len(vs) {
			diff := diffManifests(inv.load(vs[k+1]), inv.load(v))
			d.Changes = &diff
		}
		doc.VersionList = append(doc.VersionList, d)
		for _, l := range d.Labels {
			doc.ByLabel[l] = d
		}
	}
	if len(doc.VersionList) > 0 {
		n := doc.VersionList[0]
		doc.Newest, doc.Placement = &n, n.Placement
		if n.Manifest != nil {
			doc.Model = n.Manifest.Model
		}
	}
	writeJSON(w, r, http.StatusOK, doc)
}

// serveVersionDiff is GET /v1/agents/{name}/versions/{a}/diff/{b}: what
// changed from version a to version b. a and b are version ids
// (agv_…) or numbers.
func (s *Server) serveVersionDiff(w http.ResponseWriter, r *http.Request) {
	if !s.needRegistry(w, r) {
		return
	}
	inv := s.inventory(r)
	name := r.PathValue("name")
	vs, err := s.registry.Versions(r.Context(), inv.sc, name)
	if err != nil {
		controlError(w, r, err)
		return
	}
	find := func(ref string) (registry.Version, bool) {
		n, _ := strconv.Atoi(strings.TrimPrefix(ref, "v"))
		for _, v := range vs {
			if v.ID == ref || (n > 0 && v.Number == n) {
				return v, true
			}
		}
		return registry.Version{}, false
	}
	a, okA := find(r.PathValue("a"))
	b, okB := find(r.PathValue("b"))
	if !okA || !okB {
		notFound(w, r, "agent "+name+" has no such version (a version is its id, agv_…, or its number)")
		return
	}
	writeJSON(w, r, http.StatusOK, diffManifests(inv.load(a), inv.load(b)))
}

// toolPage is one tool of the inventory: who offers it.
type toolPage struct {
	Name        string `json:"name"`
	Description string `json:"description"` // the newest offering version's
	// NeedsApproval: some version that offers it gates it on a person.
	NeedsApproval bool        `json:"needs_approval"`
	Subagent      string      `json:"subagent,omitempty"`
	Agents        []toolAgent `json:"agents"`
	// Stats are the tool's calls in the window (GET /v1/stats?by=tool's
	// group of the same key); null when it was not called, or the
	// database cannot aggregate.
	Stats *toolStats `json:"stats"`
}

type toolAgent struct {
	Agent    string        `json:"agent"`
	Versions []toolVersion `json:"versions"` // newest first
}

type toolVersion struct {
	ID            string   `json:"id"`
	Number        int      `json:"number"`
	Labels        []string `json:"labels"`
	NeedsApproval bool     `json:"needs_approval"`
}

type toolStats struct {
	Calls       int64             `json:"calls"`
	FailedCalls int64             `json:"failed_calls"`
	FailureRate *float64          `json:"failure_rate"`
	DurationMS  *statsPercentiles `json:"duration_ms"`
}

// serveTools is GET /v1/tools: every tool once, by name, with the
// agents and versions that offer it. since (RFC 3339, or 1h / 24h / 7d
// back from now; default 24h) is the stats window.
func (s *Server) serveTools(w http.ResponseWriter, r *http.Request) {
	if !s.needRegistry(w, r) {
		return
	}
	until := s.now().UTC()
	since, err := windowSince(r.URL.Query().Get("since"), until)
	if err != nil {
		badRequest(w, r, err.Error())
		return
	}
	inv := s.inventory(r)
	agents, err := s.registry.Agents(r.Context(), inv.sc)
	if err != nil {
		controlError(w, r, err)
		return
	}
	byName := map[string]*toolPage{}
	for _, a := range agents {
		vs, err := s.registry.Versions(r.Context(), inv.sc, a.Name)
		if err != nil {
			continue
		}
		for _, v := range vs { // newest first
			d := inv.doc(v)
			if d.Manifest == nil {
				continue
			}
			for _, t := range d.Manifest.Tools {
				td := byName[t.Name]
				if td == nil {
					td = &toolPage{Name: t.Name, Description: t.Description, Subagent: t.Subagent, Agents: []toolAgent{}}
					byName[t.Name] = td
				}
				td.NeedsApproval = td.NeedsApproval || t.NeedsApproval
				if n := len(td.Agents); n == 0 || td.Agents[n-1].Agent != a.Name {
					td.Agents = append(td.Agents, toolAgent{Agent: a.Name})
				}
				ta := &td.Agents[len(td.Agents)-1]
				ta.Versions = append(ta.Versions, toolVersion{ID: d.ID, Number: d.Number, Labels: d.Labels, NeedsApproval: t.NeedsApproval})
			}
		}
	}
	if agg, ok := s.db.(obsdb.Aggregator); ok {
		if page, err := agg.Stats(r.Context(), obsdb.StatsQuery{By: obsdb.StatsByTool, Since: since, Until: until,
			RunQuery: obsdb.RunQuery{ParentRunID: "*"}}); err == nil {
			for _, g := range page.Groups {
				td := byName[g.Key]
				if td == nil || g.Calls == 0 {
					continue
				}
				td.Stats = &toolStats{Calls: g.Calls, FailedCalls: g.FailedCalls, FailureRate: ratio(g.FailedCalls, g.Calls)}
				td.Stats.DurationMS = percentilesOf(g.Duration)
			}
		}
	}
	out := make([]toolPage, 0, len(byName))
	for _, td := range byName {
		out = append(out, *td)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, r, http.StatusOK, map[string]any{"tools": out, "since": since, "until": until})
}
