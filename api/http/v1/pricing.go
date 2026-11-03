package v1

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weftbase/obsdb"
	pricing "github.com/weftbase/weftbase/observe/cost"
)

// Cost (plan A5): the server prices usage at read time from a pricing
// table (observe/cost: the table, its file format and the formula) —
// never stored, never sent anywhere, nothing fetched. An operator
// replaces the default with Pricing, WEFT_PRICING=<path> or weftbase
// dev --pricing <path>.
//
// # On the wire
//
// Every priced object carries cost_usd (null when unknown — never 0 for
// an unpriced model), cost_known, cache ("exact" | "not_reported"), and
// when unknown cost_badge ("unpriced": no row matches the model;
// "unknown": the figure cannot be computed yet or at all) with
// cost_reason naming why in one short line ("openai/x has no price";
// a set's "2 of 9 turns unpriced: no price for openai/x, acme/y" — never
// one clause per run), and unpriced_models: the provider/model pairs
// without a price behind the figure, deduped and sorted (a run row's
// delegated_unpriced_models the same for its subagents), so a page can
// say it once over its rows. Both lists, and the reason's, stop at five
// models: a sixth and more read as one last element "(+n)".
//
// A scripted model — every model of the wefttest provider, and the
// runtime's scripted engine — that no row
// of the table matches is priced by a built-in
// rule: an exact $0 (cost_known true, cache "exact") with cost_badge
// "scripted". Nothing bills it; the rule is Go code, not a pricing.json
// row, so an override file cannot lose it (a row that matches — a test
// pricing its scripted model — wins, as every first match does). Any
// other provider that wraps a scripted model ships its own row: the rule
// covers only the models this repository knows nothing bills.
//
// A running run is "unknown" ("running: usage
// arrives at run end" — the core reports a run's usage once, at its
// end), as is one whose end never arrived (no run_finish, no
// invoke_agent span: nothing recorded its usage).
//
// Run rows (lists, documents, children, search hits, live frames,
// the export) and a step's children[] add
// delegated_cost_usd / delegated_cost_known / delegated_cost_reason. A
// run's usage includes its core Subagent children's (rule 13: the core
// rolls them up) — the runs whose id is <run id>/<step>/<call> — so
// cost_usd is the run's own model calls: its usage minus those direct
// children's, priced at the run's model (a step that a Fallback or
// UseModel answered with another model is priced at the run's model in
// this figure; the step route prices it at the answering model), and
// delegated_cost_usd is the sum of its Subagent descendants' own costs —
// two numbers, never folded (null and known false when any is unpriced
// or running). The runs linked to it by weft.caller.run.id (O11) — a
// thread/pool child session's run, another agent's Generate inside a
// tool handler; before weft 0.18 linked by weft.parent.run.id too —
// were never rolled up: they are neither subtracted nor delegated, and
// carry their own figures on their own rows.
//
// A session row's cost_usd is its turns' own + delegated (sessionCost:
// every run of the session that is not a Subagent child of another of
// its runs — a pool child session's run is a turn of the child
// session). The pool child's cost is on its own session, never folded
// into the parent session's row; the parent's session document carries
// its delegated sessions' figures beside its own
// (delegated_sessions_cost_usd, U14 — thread's Usage().Delegated, read
// from obsdb's sessions). A step's cost_usd prices its own model call
// (step_finish's usage) at the model that answered it, else the model
// it requested, else the run's.

// Pricing sets the table the server prices usage with (plan A5).
// Without it New uses cost.Default. The table replaces the default
// whole.
func Pricing(t pricing.Table) Option {
	return func(c *config) {
		t.Models = slices.Clone(t.Models)
		if t.Source == "" {
			t.Source = pricing.SourceOption
		}
		c.pricing = &t
	}
}

// resolvePricing is New's rule: the Pricing option, else the default
// table. Reading WEFT_PRICING is the CLI's business, never the
// server's.
func resolvePricing(c *config) {
	if c.pricing != nil {
		// A Go-built row without a provider is the any-provider row, as
		// a file's missing provider loads.
		for i := range c.pricing.Models {
			if c.pricing.Models[i].Provider == "" {
				c.pricing.Models[i].Provider = pricing.AnyProvider
			}
		}
		return
	}
	t := pricing.Default()
	c.pricing = &t
}

// Cost badges (cost_badge).
const (
	costUnpriced = pricing.BadgeUnpriced // no row matches the model
	costUnknown  = "unknown"             // the figure could not be computed
	costScripted = pricing.BadgeScripted // a known, exact $0: a scripted model
	// costEstimate is a known figure priced at a stand-in model's rates:
	// a summarizer call whose own model was not recorded, priced at its
	// turn's (U15.1-fix, UX-B-13).
	costEstimate = "estimate"
)

// estimateReason is cost_reason's words on a summarizer figure priced
// at its turn's model (costEstimate).
const estimateReason = "priced at the turn's model: the summarizer's model was not recorded"

// Cache marks (cache) and the words on a scripted figure.
const (
	cacheExact       = pricing.CacheExact
	cacheNotReported = pricing.CacheNotReported
	scriptedReason   = pricing.ScriptedReason
	cacheNote        = pricing.CacheNote
)

// cacheOf is observe/cost's CacheOf.
func cacheOf(u core.Usage) string { return pricing.CacheOf(u) }

// costFields is the cost block every priced object carries.
type costFields struct {
	CostUSD    *float64 `json:"cost_usd"`
	CostKnown  bool     `json:"cost_known"`
	CostBadge  string   `json:"cost_badge,omitempty"`
	CostReason string   `json:"cost_reason,omitempty"`
	Cache      string   `json:"cache"`
	// UnpricedModels are the provider/model pairs without a price
	// behind the figure, deduped in order (absent when none).
	UnpricedModels []string `json:"unpriced_models,omitempty"`
	// CostPartial is an unknown set figure's known part (a session's;
	// UX-H-07): the parts priced, of how many, and their sum — "≥ $x · k
	// of n turns priced". Absent on a known figure, on a single run's, and
	// when no part is priced.
	CostPartial *costPartial `json:"cost_partial,omitempty"`
}

// costPartial is cost_partial: usd is the priced parts' sum (rounded
// as cost_usd), priced of of the set's parts are known.
type costPartial struct {
	USD    float64 `json:"usd"`
	Priced int     `json:"priced"`
	Of     int     `json:"of"`
}

// delegatedFields is a run's subagent bill beside its own.
type delegatedFields struct {
	DelegatedCostUSD    *float64 `json:"delegated_cost_usd"`
	DelegatedCostKnown  bool     `json:"delegated_cost_known"`
	DelegatedCostReason string   `json:"delegated_cost_reason,omitempty"`
	// DelegatedUnpricedModels are the subagents' unpriced models.
	DelegatedUnpricedModels []string `json:"delegated_unpriced_models,omitempty"`
}

// cost is one computed figure before it goes on the wire.
type cost struct {
	usd           float64
	known         bool
	badge, reason string
	cache         string
	// models are the unpriced models behind the figure (deduped). A
	// tally's figure also counts its runs, those not known yet and
	// those without a price; why is its first unknown part's reason
	// ("<run id>: running: …"), cause the same without the run id.
	models                  []string
	runs, unknown, unpriced int
	why, cause              string
	// A tally's known part (UX-H-07): floor sums, and priced counts,
	// the parts whose whole bill (own + delegated) is known; never counts
	// the unknown parts that will never be known (a derived run whose
	// stock spans report no usage), so "not known yet" is not said of
	// them.
	floor         float64
	priced, never int
}

// addModels adds ms to the deduped list, kept sorted: a set's (or a
// run's subagents') models read in one order whatever order the runs
// were summed in (a children read's order is not the page's), so
// unpriced_models, delegated_unpriced_models and a reason's list are
// stable — and the five a cap keeps are the same five.
func addModels(list []string, ms ...string) []string {
	for _, m := range ms {
		if i, found := slices.BinarySearch(list, m); !found {
			list = slices.Insert(list, i, m)
		}
	}
	return list
}

// maxModels caps a model list on the wire and in a reason: the first
// maxModels, then "(+n)" for the rest — the truncation said, never
// silent.
const maxModels = 5

// capModels is list cut at maxModels, its last element "(+n)" when cut.
func capModels(list []string) []string {
	if len(list) <= maxModels {
		return list
	}
	return append(slices.Clone(list[:maxModels]), fmt.Sprintf("(+%d)", len(list)-maxModels))
}

// fields is c on the wire: rounded, null when unknown.
func (c cost) fields() costFields {
	out := costFields{CostKnown: c.known, CostBadge: c.badge, CostReason: c.reason, Cache: c.cache, UnpricedModels: capModels(c.models)}
	if !c.known && c.priced > 0 {
		out.CostPartial = &costPartial{USD: round6(c.floor), Priced: c.priced, Of: c.runs}
	}
	if c.known && c.cache == cacheNotReported && out.CostReason == "" {
		out.CostReason = cacheNote
	}
	if c.known {
		v := round6(c.usd)
		out.CostUSD = &v
	}
	return out
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// price is u's cost on provider/model under t (observe/cost's
// formula), as the figure the coster sums.
func price(t *pricing.Table, provider, model string, u core.Usage) cost {
	q := t.Price(provider, model, u)
	return cost{usd: q.USD, known: q.Known, badge: q.Badge, reason: q.Reason, cache: q.Cache, models: q.Models}
}

// sub is u − o field by field, never below zero: a parent's own usage
// once its children's (rolled up into it by the core) are taken out.
func sub(u, o core.Usage) core.Usage {
	return core.Usage{
		InputTokens:       max(u.InputTokens-o.InputTokens, 0),
		OutputTokens:      max(u.OutputTokens-o.OutputTokens, 0),
		CachedInputTokens: max(u.CachedInputTokens-o.CachedInputTokens, 0),
		CacheWriteTokens:  max(u.CacheWriteTokens-o.CacheWriteTokens, 0),
		ReasoningTokens:   max(u.ReasoningTokens-o.ReasoningTokens, 0),
	}
}

// coster prices run rows for one request: it reads each run's subagent
// children once (obsdb's Runs with the run as parent) and memoizes each
// run's split, so a page of rows and their trees costs one children
// read per run.
type coster struct {
	ctx   context.Context
	db    obsdb.DB
	table *pricing.Table
	kids  map[string][]obsdb.RunRow
	memo  map[string]runCost
	// partKids marks the runs whose children in kids are a part only:
	// a set's (setCost) whose one-level read (primeChildren) failed —
	// the row's children_count is then not known (children_unknown).
	partKids map[string]bool
	// capped marks the runs whose children primeTrees stopped short of
	// (its primeChildCap, or ChildRuns' obsdb.ErrChildRunsCapped): their
	// figures and counts read not known, never read one at a time.
	capped map[string]bool
	primed int // child rows primeTrees read so far
}

// runCost is one run's own and delegated figures.
type runCost struct {
	own, delegated cost
	id             string // the run's, named in a tally's reason
	// split says the usage below is known: the children were read and
	// the run has a core Subagent child; ownUsage
	// is the run's usage less theirs, delUsage its Subagent
	// children's summed (each already its whole subtree's, rule 13).
	split              bool
	ownUsage, delUsage core.Usage
}

// total is own + delegated: the run's whole bill (search's sort key).
func (rc runCost) total() (float64, bool) {
	return rc.own.usd + rc.delegated.usd, rc.own.known && rc.delegated.known
}

func (s *Server) coster(ctx context.Context) *coster {
	return &coster{ctx: ctx, db: s.db, table: s.pricing, kids: map[string][]obsdb.RunRow{}, memo: map[string]runCost{}, partKids: map[string]bool{}, capped: map[string]bool{}}
}

// prime records a run's children already read (a run document's).
func (c *coster) prime(id string, kids []obsdb.RunRow) {
	if kids == nil {
		kids = []obsdb.RunRow{}
	}
	c.kids[id] = kids
}

// primeTrees reads the subagent trees of a page of runs before they are
// priced (U9.1, UX-E-24): one obsdb.TreeReader.ChildRuns read per
// level of the trees — the page's runs, then their core Subagent
// children, … — instead of a children read per run, so a runs list or
// a search page costs as many reads as its deepest tree has levels. A
// run already primed is not read again. Without a TreeReader, or when
// a level's read fails, it stops: the runs left unprimed read their
// children one at a time (children), as before. Bounded: once
// primeChildCap child rows have been read for this coster (or a level
// holds more than ChildRuns reads, obsdb.ErrChildRunsCapped), the
// level's runs are capped — their cost and children counts read not
// known (children_unknown), not read again run by run.
func (c *coster) primeTrees(recs []obsdb.RunRow) {
	tr, ok := c.db.(obsdb.TreeReader)
	if !ok {
		return
	}
	var ids []string
	for _, r := range recs {
		if _, done := c.kids[r.ID]; !done {
			ids = append(ids, r.ID)
		}
	}
	for len(ids) > 0 {
		kids, err := tr.ChildRuns(c.ctx, ids)
		n := 0
		for _, k := range kids {
			n += len(k)
		}
		if errors.Is(err, obsdb.ErrChildRunsCapped) || (err == nil && c.primed+n > primeChildCap) {
			slog.DebugContext(c.ctx, "weftbase: a page's subagent trees hold too many child runs to read: their cost and counts are not known",
				"cap", primeChildCap, "runs", len(ids))
			for _, id := range ids {
				c.capped[id] = true
			}
			return
		}
		if err != nil {
			slog.DebugContext(c.ctx, "weftbase: a page's subagent children could not be read at once; reading them per run", "err", err)
			return
		}
		c.primed += n
		var next []string
		for _, id := range ids {
			c.prime(id, kids[id])
			for _, k := range subagents(id, c.kids[id]) { // the scope's: a hidden child is never walked
				if _, done := c.kids[k.ID]; !done {
					next = append(next, k.ID)
				}
			}
		}
		ids = next
	}
}

// primeChildren reads the runs' children, one level, in one
// obsdb.TreeReader.ChildRuns read, over what a set primed (setCost: a
// session's runs only — a thread/pool child session's run or a nested
// Generate carries another session id, so the set misses it). The
// cost is unchanged (it reads the core Subagent children, which the
// set holds); the rows' children_count / children_failed count every
// parent-linked run, as the runs list's do. Without a TreeReader, or
// when the read fails, each run's count is marked unknown.
func (c *coster) primeChildren(recs []obsdb.RunRow) {
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return
	}
	tr, ok := c.db.(obsdb.TreeReader)
	var kids map[string][]obsdb.RunRow
	var err error
	if ok {
		kids, err = tr.ChildRuns(c.ctx, ids)
	}
	if !ok || err != nil {
		slog.DebugContext(c.ctx, "weftbase: a set's child runs could not be read: their counts are unknown", "err", err)
		for _, id := range ids {
			c.partKids[id] = true
		}
		return
	}
	for _, id := range ids {
		c.prime(id, kids[id])
	}
}

// primeChildCap bounds the child rows primeTrees reads for one coster
// (a request): obsdb.ChildRunsCap, the bound the per-run walk had
// (costerChildPages × 500). A variable for tests.
var primeChildCap = obsdb.ChildRunsCap

// errChildrenCapped is children's answer for a run primeTrees capped.
var errChildrenCapped = fmt.Errorf("more than %d child runs under the runs read: too many to read at once", obsdb.ChildRunsCap)

// costerChildPages bounds the children walk of one run: 20 pages of 500.
const costerChildPages = 20

// children is the run's direct subagent children.
func (c *coster) children(id string) ([]obsdb.RunRow, error) {
	if k, ok := c.kids[id]; ok {
		return k, nil
	}
	if c.capped[id] {
		return nil, errChildrenCapped
	}
	var out []obsdb.RunRow
	q := obsdb.RunQuery{ParentRunID: id, Limit: 500}
	for pages := 0; ; pages++ {
		if pages == costerChildPages {
			return nil, fmt.Errorf("more than %d children", costerChildPages*500)
		}
		page, err := c.db.Runs(c.ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Runs...)
		if page.NextBefore == nil || len(page.Runs) == 0 {
			break
		}
		q.Before, q.BeforeID = *page.NextBefore, page.NextBeforeID
	}
	c.prime(id, out)
	return c.kids[id], nil
}

// cost is rec's own and delegated figures (memoized).
func (c *coster) cost(rec obsdb.RunRow) runCost {
	if rc, ok := c.memo[rec.ID]; ok {
		return rc
	}
	c.memo[rec.ID] = runCost{} // a cycle (never written by the core) reads as zero, not a hang
	rc := c.compute(rec)
	c.memo[rec.ID] = rc
	return rc
}

func (c *coster) compute(rec obsdb.RunRow) runCost {
	all, err := c.children(rec.ID)
	if err != nil {
		slog.DebugContext(c.ctx, "weftbase: a run's subagent children could not be read: its cost is not known", "run", rec.ID, "err", err)
		why := "the run's subagent children could not be read, so its own usage cannot be told from theirs"
		if errors.Is(err, errChildrenCapped) {
			why = "the run's subagent children are past the " + strconv.Itoa(obsdb.ChildRunsCap) + " child runs one read takes, so its own usage cannot be told from theirs"
		}
		return runCost{
			own:       cost{badge: costUnknown, reason: why, cache: cacheOf(rec.Usage)},
			delegated: cost{badge: costUnknown, reason: why, why: rec.ID + ": " + why, cause: why},
		}
	}
	own := rec.Usage
	var parts []runCost
	var delUsage core.Usage
	subs := subagents(rec.ID, all)
	for _, k := range subs {
		own = sub(own, k.Usage)
		delUsage = delUsage.Add(k.Usage)
		kc := c.cost(k)
		kc.id = k.ID
		parts = append(parts, kc)
	}
	// The cause is carried: one child whose figure is unknown (running,
	// its end never arrived, its children unreadable) makes the
	// delegated figure unknown, not unpriced.
	del := tally(parts, "child run", true)
	split := len(subs) > 0
	if why := usagePending(rec); why != "" {
		// A running run carries no usage yet: no split to show.
		return runCost{own: cost{badge: costUnknown, reason: why, cache: cacheOf(own)}, delegated: del}
	}
	return runCost{own: price(c.table, rec.Provider, rec.Model, own), delegated: del, split: split, ownUsage: own, delUsage: delUsage}
}

// tally sums a set of runs' whole bills (each part's own + delegated)
// into one figure, known when every part is. Otherwise its badge is
// "unknown" when any part is not known yet (a run still running, its
// end never arrived: it has a price, its usage has not arrived), else
// "unpriced"; and its reason is one line counting the runs — never one
// clause per run: "2 of 3 child runs unpriced: no price for
// openai/x, acme/y", "1 of 3 child runs' cost not known yet (p/0/c:
// running: …)". noun names a run of the set ("child run", "turn",
// "run"). nested counts each part's own subagents as runs of the set (a
// run's delegated figure covers every descendant); without it a part is
// one run, unknown or unpriced by its whole bill (a session's turn, an
// set's run).
func tally(parts []runCost, noun string, nested bool) cost {
	out := cost{known: true}
	for _, p := range parts {
		out.usd += p.own.usd + p.delegated.usd
		// The floor is the k priced parts' sum (U33.2-fix): a part whose
		// delegated cost is unknown adds nothing, its own figure included.
		if p.own.known && p.delegated.known {
			out.floor += p.own.usd + p.delegated.usd
			out.priced++
		}
		if p.own.reason == derivedUsageUnknown {
			out.never++
		}
		out.models = addModels(out.models, p.own.models...)
		out.models = addModels(out.models, p.delegated.models...)
		out.runs++
		if p.own.badge == costUnknown && out.why == "" {
			out.why, out.cause = p.id+": "+p.own.reason, p.own.reason
		}
		if out.why == "" {
			out.why, out.cause = p.delegated.why, p.delegated.cause
		}
		switch {
		case p.own.badge == costUnknown, !nested && !p.delegated.known && p.delegated.badge == costUnknown:
			out.unknown++
		case !p.own.known, !nested && !p.delegated.known:
			out.unpriced++
		}
		if nested {
			out.runs += p.delegated.runs
			out.unknown += p.delegated.unknown
			out.unpriced += p.delegated.unpriced
			if !p.delegated.known && p.delegated.unknown+p.delegated.unpriced == 0 {
				out.unknown++ // its children could not be read: none counted
			}
		}
	}
	if out.unknown+out.unpriced == 0 {
		return out
	}
	out.known, out.usd = false, 0
	if out.unknown > 0 {
		out.badge = costUnknown
		// A subagent's reason names the child (one run of a tree the
		// page does not list); a session's names
		// none — its turns are the rows beside it (UX-B-27).
		why := out.cause
		if nested {
			why = out.why
		}
		// A derived run whose spans report no usage is never known
		// (the run row's own words, README's rule): "not known", not
		// "not known yet", when every unknown part is one.
		yet := " yet"
		if out.never >= out.unknown {
			yet = ""
		}
		out.reason = fmt.Sprintf("%d of %d %ss' cost not known%s (%s)", out.unknown, out.runs, noun, yet, why)
		if out.unpriced > 0 {
			out.reason += fmt.Sprintf("; %d unpriced", out.unpriced)
		}
	} else {
		out.badge = costUnpriced
		out.reason = fmt.Sprintf("%d of %d %ss unpriced", out.unpriced, out.runs, noun)
	}
	if len(out.models) > 0 {
		out.reason += ": no price for " + strings.Join(capModels(out.models), ", ")
	}
	return out
}

// subagents is the core Subagent children among a run's parent-linked
// runs: those whose id is <parent>/<step>/<call> (core's childRunID) —
// the only ones whose usage the core rolled into the parent's (rule
// 13). Since O11 the core links nothing else by weft.parent.run.id; a
// thread/pool child session's run or a nested Generate recorded before
// weft 0.18 is linked so too but was never rolled up: neither
// subtracted nor counted as delegated — the id rule keeps old records
// right.
func subagents(id string, kids []obsdb.RunRow) []obsdb.RunRow {
	var out []obsdb.RunRow
	for _, k := range kids {
		if strings.HasPrefix(k.ID, id+"/") {
			out = append(out, k)
		}
	}
	return out
}

// usagePending says why a run's usage is not its bill yet: the core
// reports a run's usage once, at its end (run_finish, the invoke_agent
// span), so a running run — or one whose end never arrived (Finished
// unset: the same records set both) — carries none to price; a derived
// run whose stock spans report no usage has none either
// (derivedUsageUnknown). "" when the usage is recorded.
func usagePending(rec obsdb.RunRow) string {
	switch {
	case rec.Status == obsdb.StatusRunning:
		return "running: usage arrives at run end"
	case rec.Finished == nil:
		return "the run's end (run_finish or its invoke_agent span) never arrived: its usage is not recorded"
	case rec.Derived && rec.Usage == (core.Usage{}):
		return derivedUsageUnknown
	}
	return ""
}

// derivedUsageUnknown is the cost reason of a derived run (or step)
// whose stock spans report no usage, or only zeros (docs/record-contract.md
// §9): its cost is unknown, never a known $0. A derived run's row holds
// the fold, which cannot tell absent keys from zero ones, so both read
// this way — on the row and on its steps alike.
const derivedUsageUnknown = "the stock spans report no (or zero) usage (gen_ai.usage.* absent or 0 on the root and its chat spans): the cost is unknown"

// row is row(rec) with its cost.
func (c *coster) row(rec obsdb.RunRow) runRow {
	out := row(rec)
	c.fill(&out, rec)
	return out
}

// fill sets a run row's cost blocks.
func (c *coster) fill(out *runRow, rec obsdb.RunRow) {
	rc := c.cost(rec)
	out.costFields = rc.own.fields()
	out.delegatedFields = rc.delegated.delegatedFields()
	if rc.split {
		own, del := rc.ownUsage, rc.delUsage
		out.OwnUsage, out.DelegatedUsage = &own, &del
	}
	// The children the cost read (primed for a page, else read for this
	// row): their count and failures ride on the row (H9), no read more.
	// When that read failed (or only a set's part is held) the count is
	// not known: children_unknown, never an absent count that reads as
	// "no subagents".
	kids, read := c.kids[rec.ID]
	if !read || c.partKids[rec.ID] {
		out.ChildrenUnknown = true
	}
	for _, k := range kids {
		out.ChildrenCount++
		if k.Status == obsdb.StatusFailed {
			out.ChildrenFailed++
		}
	}
}

func (c cost) delegatedFields() delegatedFields {
	out := delegatedFields{DelegatedCostKnown: c.known, DelegatedCostReason: c.reason, DelegatedUnpricedModels: capModels(c.models)}
	if c.known {
		v := round6(c.usd)
		out.DelegatedCostUSD = &v
	}
	return out
}

// sessionCostPages bounds a session's runs walk: 20 pages of 500.
const sessionCostPages = 20

// tooManyRuns is the cost of a session past sessionCostPages × 500 runs.
func tooManyRuns() cost {
	return cost{badge: costUnknown, reason: fmt.Sprintf("the session holds more than %d runs: too many to price at read time", sessionCostPages*500)}
}

// sessionCost is a session's cost: its turns' own + delegated, from
// one walk of every run carrying the session id (Subagent children
// inherit it), so the tree costs no children reads. A turn is every run
// of the set that is not a core Subagent child of another run in it: a
// top-level turn, a thread/pool child session's run (its parent run is
// in the parent's session), and another agent's Generate inside a tool
// handler (linked but never rolled up, so no other figure carries it).
// The coster it returns is primed with that tree for the turn rows. The
// runs it read come back too: nil when the walk failed or passed its
// bound, non-nil and maybe empty when it read the whole set.
func (s *Server) sessionCost(ctx context.Context, id string) (cost, *coster, []obsdb.RunRow) {
	c := s.coster(ctx)
	q := obsdb.RunQuery{SessionID: id, ParentRunID: "*", Limit: 500}
	runs := []obsdb.RunRow{} // non-nil: a whole walk, even of no run
	total := 0
	for pages := 0; ; pages++ {
		if pages == sessionCostPages {
			// The bound: whole only when the last count says every run
			// was read (a set of exactly 10,000 runs is whole, never
			// "more"). Only here: below it a count that fell between
			// pages (a concurrent delete) would end the walk early.
			if len(runs) >= total {
				break
			}
			return tooManyRuns(), c, nil
		}
		page, err := s.db.Runs(ctx, q)
		if err != nil {
			slog.DebugContext(ctx, "weftbase: a session's runs could not be read to price it", "session", id, "err", err)
			return cost{badge: costUnknown, reason: "the session's runs could not be read"}, c, nil
		}
		if pages == 0 && page.Total > sessionCostPages*500 {
			return tooManyRuns(), c, nil // the count says so: no 19 reads more
		}
		total = page.Total
		runs = append(runs, page.Runs...)
		// The last page: no cursor, or an empty page.
		if page.NextBefore == nil || len(page.Runs) == 0 {
			break
		}
		q.Before, q.BeforeID = *page.NextBefore, page.NextBeforeID
	}
	return c.setCost(runs, "turn"), c, runs
}

// sessionCostBatch is how many sessions one batched runs read of
// sessionCosts starts with.
const sessionCostBatch = 50

// sessionCosts is sessionCost for several sessions at once: their runs
// in batched paged reads (RunQuery.SessionIDs), each session's set priced by setCost on a coster of
// its own — what sessionCost answers per session, a read per page of a
// batch rather than per session. A batch holds sessionCostBatch
// sessions; one whose runs pass sessionCostPages × 500 (its first
// page's Total says so) is halved and each half read again, down to one
// session, so a session is priced whole up to sessionCost's own bound
// and a level of many small sessions prices every one. The whole call
// reads at most sessionCostPages Runs pages per batch it starts with,
// and sessionCostPages more for the halvings (sessionCostReads: 40 for
// 50 sessions, 220 for 500), so a level of sessions every one past the
// bound costs that budget, never O(sessions × 20). Every id gets an answer: a session
// alone past the bound, or one whose read fails, is unknown with
// sessionCost's reason; once the budget is spent the sessions not yet
// priced are unknown with costReadBudget's.
func (s *Server) sessionCosts(ctx context.Context, ids []string) map[string]cost {
	out := make(map[string]cost, len(ids))
	n := sessionCostReads(len(ids))
	budget := &readBudget{left: n, total: n}
	for len(ids) > 0 {
		n := min(sessionCostBatch, len(ids))
		s.priceSessionBatch(ctx, ids[:n], out, budget)
		ids = ids[n:]
	}
	return out
}

// readBudget is sessionCosts' read budget: the Runs pages left to read,
// of total.
type readBudget struct{ left, total int }

// sessionCostReads is sessionCosts' read budget for n sessions:
// sessionCostPages Runs pages per batch of sessionCostBatch, and one
// batch's more for the halvings (a batch of 10,500 runs reads 23).
func sessionCostReads(n int) int {
	return sessionCostPages * ((n+sessionCostBatch-1)/sessionCostBatch + 1)
}

// costReadBudget is the cost of a session sessionCosts' read budget —
// reads Runs pages of 500, sessionCostReads' figure — left unpriced.
func costReadBudget(reads int) cost {
	return cost{badge: costUnknown, reason: fmt.Sprintf("the delegated sessions hold too many runs to price at read time (%d pages of 500 read)", reads)}
}

// priceSessionBatch prices one batch of sessionCosts into out, halving
// it when its runs pass sessionCost's bound, each read taken from
// budget; with the budget spent the batch's sessions are unknown.
func (s *Server) priceSessionBatch(ctx context.Context, ids []string, out map[string]cost, budget *readBudget) {
	q := obsdb.RunQuery{SessionIDs: ids, ParentRunID: "*", Limit: 500}
	bySession := make(map[string][]obsdb.RunRow, len(ids))
	read, total := 0, 0
	for pages := 0; ; pages++ {
		if pages == sessionCostPages {
			// The bound: whole only when the last count says every run
			// was read (exactly 10,000 runs are whole, never "more");
			// else the count grew past it while the batch was read.
			// Only here: below it a count that fell between pages (a
			// concurrent delete) would end the walk early.
			if read >= total {
				break
			}
			s.splitSessionBatch(ctx, ids, out, budget)
			return
		}
		if budget.left <= 0 {
			for _, id := range ids {
				out[id] = costReadBudget(budget.total)
			}
			return
		}
		budget.left--
		page, err := s.db.Runs(ctx, q)
		if err != nil {
			slog.DebugContext(ctx, "weftbase: delegated sessions' runs could not be read to price them", "sessions", len(ids), "err", err)
			for _, id := range ids {
				out[id] = cost{badge: costUnknown, reason: "the session's runs could not be read"}
			}
			return
		}
		if pages == 0 && page.Total > sessionCostPages*500 {
			s.splitSessionBatch(ctx, ids, out, budget)
			return
		}
		for _, r := range page.Runs {
			bySession[r.SessionID] = append(bySession[r.SessionID], r)
		}
		read, total = read+len(page.Runs), page.Total
		// The last page: no cursor, or an empty page.
		if page.NextBefore == nil || len(page.Runs) == 0 {
			break
		}
		q.Before, q.BeforeID = *page.NextBefore, page.NextBeforeID
	}
	for _, id := range ids {
		runs := bySession[id]
		if runs == nil {
			runs = []obsdb.RunRow{}
		}
		out[id] = s.coster(ctx).setCost(runs, "turn")
	}
}

// splitSessionBatch is priceSessionBatch past the bound: two halves
// read again, or one session's tooManyRuns.
func (s *Server) splitSessionBatch(ctx context.Context, ids []string, out map[string]cost, budget *readBudget) {
	if len(ids) == 1 {
		out[ids[0]] = tooManyRuns()
		return
	}
	half := len(ids) / 2
	s.priceSessionBatch(ctx, ids[:half], out, budget)
	s.priceSessionBatch(ctx, ids[half:], out, budget)
}

// setCost is the bill of a set of runs read whole — a session's, an
// set's — with their subagent children among them: each root's
// own + delegated, where a root is every run of the set that is not a
// core Subagent child of another run in it. It primes c with the tree,
// so pricing the roots costs no children reads. noun names a root in
// the reason ("turn", "run": tally's words). A set whose every root is
// a scripted model's, its subagents $0, is the scripted badge's exact $0.
func (c *coster) setCost(runs []obsdb.RunRow, noun string) cost {
	kids := map[string][]obsdb.RunRow{}
	inSet := map[string]bool{}
	for _, r := range runs {
		inSet[r.ID] = true
		if r.ParentRunID != "" {
			kids[r.ParentRunID] = append(kids[r.ParentRunID], r)
		}
	}
	for _, r := range runs {
		c.prime(r.ID, kids[r.ID])
	}
	var usage core.Usage
	var parts []runCost
	allScripted := true
	for _, r := range runs {
		if inSet[r.ParentRunID] && strings.HasPrefix(r.ID, r.ParentRunID+"/") {
			continue // a Subagent child: in its parent's delegated figure
		}
		usage = usage.Add(r.Usage)
		rc := c.cost(r)
		rc.id = r.ID
		parts = append(parts, rc)
		allScripted = allScripted && rc.own.badge == costScripted && rc.delegated.known && rc.delegated.usd == 0
	}
	out := tally(parts, noun, false)
	out.cache = cacheOf(usage)
	switch {
	case len(parts) > 0 && allScripted:
		// Every root a scripted model's — the scripted
		// engine reports zero usage, so this precedes the zero case:
		// the set's $0 is exact too.
		out.badge, out.reason, out.cache = costScripted, scriptedReason, cacheExact
	case usage == (core.Usage{}):
		// No usage at all (a run-less set): a $0 that nothing
		// can have overstated, so no cache note.
		out.cache = cacheExact
	}
	return out
}

// ── /v1/meta and GET /v1/settings/pricing ─────────────────────────

// metaPricing is /v1/meta's pricing block.
type metaPricing struct {
	Source string `json:"source"`
	// Path is the override file's, under db.path's rule (mayReadDBPath);
	// absent for the default table, a Go-built one, or another caller.
	Path   string `json:"path,omitempty"`
	Dated  string `json:"dated"`
	Models int    `json:"models"`
}

func (s *Server) metaPricing(r *http.Request) metaPricing {
	out := metaPricing{Source: s.pricing.Source, Dated: s.pricing.Dated, Models: len(s.pricing.Models)}
	if hostDetails(r) {
		out.Path = s.pricing.Path
	}
	return out
}

// pricingDoc is GET /v1/settings/pricing: the effective table in the
// override file's shape (so it saves as a WEFT_PRICING file and loads
// back — LoadPricing accepts the source and path keys), plus where it
// came from.
type pricingDoc struct {
	Source string               `json:"source"`
	Path   string               `json:"path,omitempty"`
	Dated  string               `json:"dated"`
	Note   string               `json:"note,omitempty"`
	Models []pricing.ModelPrice `json:"models"`
}

// servePricing answers GET /v1/settings/pricing (capability pricing):
// every valid identity may read it — prices are not secrets — and the
// file's path follows db.path's rule.
func (s *Server) servePricing(w http.ResponseWriter, r *http.Request) {
	doc := pricingDoc{Source: s.pricing.Source, Dated: s.pricing.Dated, Note: s.pricing.Note, Models: s.pricing.Models}
	if doc.Models == nil {
		doc.Models = []pricing.ModelPrice{}
	}
	if hostDetails(r) {
		doc.Path = s.pricing.Path
	}
	writeJSON(w, r, http.StatusOK, doc)
}
