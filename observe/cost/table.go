// Package cost is weftbase's pricing table (plan A5): the server
// prices usage at read time from it — never stored, never sent
// anywhere, nothing fetched. The table is a plain JSON file; the
// default ships in the repo with the day its rates were read
// (pricing.json, generated from pricingdata by internal/pricinggen),
// and an operator replaces it with an option, WEFT_PRICING=<path> or
// weftbase dev --pricing <path>. The coster that prices the server's
// run rows with a Table lives with the read API.
//
// # The table
//
//	{"dated": "2026-10-10", "note": "…", "models": [
//	  {"provider": "anthropic", "model_glob": "claude-opus-5-5",
//	   "input": 4, "output": 20, "cached_input": 0.2, "cache_write": 5}, …]}
//
// Rates are USD per 1M tokens. A run's model is looked up as
// "provider/model" against each row's "provider/model_glob" with
// path.Match semantics (so either half may be a glob: "*" matches any
// run of characters but "/"); the first match wins, so the most specific
// row comes first. The provider is the table's spelling: a row whose
// provider came from the invoke_agent span's semconv name ("gcp.gemini")
// is looked up as the adapter's ("google"). A row with no provider (or
// provider "*") matches on its model glob alone, whatever the transport:
// {"model_glob": "kimi-k2.7-code", …} prices that model reached through
// the openai adapter at an OpenAI-compatible base URL, a provider
// spelled with a "/", or any other. input and output are
// required; cached_input, cache_write and reasoning are optional, and a
// missing one prices that token kind at the base rate (cached_input and
// cache_write at input, reasoning at output). An override file replaces
// the default table whole — there is no merging; start from
// pricing.json, the template.
//
// # The formula
//
// core.Usage's splits are subsets of its totals (CachedInputTokens and
// CacheWriteTokens of InputTokens, ReasoningTokens of OutputTokens), so
//
//	cost = input       × (InputTokens − CachedInputTokens − CacheWriteTokens)
//	     + cached_input × CachedInputTokens
//	     + cache_write  × CacheWriteTokens
//	     + output      × (OutputTokens − ReasoningTokens)
//	     + reasoning   × ReasoningTokens          (all ÷ 1,000,000)
//
// exact where the adapter reports the splits. When it reported no cache
// split (CachedInputTokens and CacheWriteTokens both zero) every input
// token is priced at the input rate, and the object says cache
// "not_reported" with cost_reason "no cache split reported; a cached
// prompt may be overstated" (an uncached prompt reads the same, its
// figure exact). Figures are float64, rounded to 6 decimals on the wire.
package cost

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weft/core"
)

//go:generate go run ./internal/pricinggen -o pricing.json

// Table is a pricing table: pricing.json's shape (and a
// WEFT_PRICING override file's). Build one in Go, or read one with
// Load; Default is the shipped table.
type Table struct {
	// Dated is the day the rates were read, ISO 8601 (2026-10-10).
	Dated string `json:"dated"`
	// Note is the file's comment: what the rates are and where from.
	Note string `json:"note,omitempty"`
	// Models are the rows, first match wins.
	Models []ModelPrice `json:"models"`

	// Source is where the table came from: SourceDefault, SourceFile
	// (Load) or SourceOption (built in Go); Path is a file's path.
	Source string `json:"-"`
	Path   string `json:"-"`
}

// ModelPrice is one row of a Table: a model glob and its rates
// in USD per 1M tokens. A nil optional rate prices that token kind at
// the base rate (CachedInput and CacheWrite at Input, Reasoning at
// Output).
type ModelPrice struct {
	// Provider is the run's provider the row matches ("anthropic");
	// "*" — what a file's missing provider loads as — matches any, the
	// row then looked up on ModelGlob against the model alone.
	Provider    string   `json:"provider"`
	ModelGlob   string   `json:"model_glob"`
	Input       float64  `json:"input"`
	Output      float64  `json:"output"`
	CachedInput *float64 `json:"cached_input,omitempty"`
	CacheWrite  *float64 `json:"cache_write,omitempty"`
	Reasoning   *float64 `json:"reasoning,omitempty"`
}

// Table sources, as /v1/meta and the settings route report them.
const (
	SourceDefault = "default" // the shipped pricing.json
	SourceFile    = "file"    // WEFT_PRICING / --pricing (LoadPricing)
	SourceOption  = "option"  // a table built in Go and passed to the server
)

//go:embed pricing.json
var defaultJSON []byte

var (
	defaultOnce sync.Once
	defaultVal  Table
)

// Default is the table weftbase ships: pricing.json, generated
// from the vendors' public list prices on its Dated day. Nothing is
// fetched — a newer price arrives with a newer weft, or with an
// override file.
func Default() Table {
	defaultOnce.Do(func() {
		t, err := parse(defaultJSON, "pricing.json (embedded)")
		if err != nil {
			panic("weftbase: " + err.Error()) // pinned by TestDefaultLoads
		}
		t.Source = SourceDefault
		defaultVal = t
	})
	t := defaultVal
	t.Models = slices.Clone(t.Models)
	return t
}

// Load reads a pricing table file (pricing.json's shape). A file
// that does not parse, names an unknown field, misses input or output,
// carries a negative rate, a bad glob or a dated that is not
// YYYY-MM-DD is an error naming the path and the field — never a
// silent fallback to the default. A row's missing provider loads as
// "*": the row matches on its model glob alone, any transport. The table it returns reports source
// "file" and its path.
func Load(file string) (Table, error) {
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return Table{}, fmt.Errorf("pricing %s: %w", file, err)
	}
	t, err := parse(b, file)
	if err != nil {
		return Table{}, err
	}
	t.Source, t.Path = SourceFile, file
	return t, nil
}

// pricingFileShape is what a file may hold: the table, plus the source
// and path the settings route adds (so its export loads back).
type pricingFileShape struct {
	Dated  *string            `json:"dated"`
	Note   string             `json:"note"`
	Models *[]json.RawMessage `json:"models"`
	Source string             `json:"source"`
	Path   string             `json:"path"`
}

type modelPriceFile struct {
	Provider    *string  `json:"provider"`
	ModelGlob   *string  `json:"model_glob"`
	Input       *float64 `json:"input"`
	Output      *float64 `json:"output"`
	CachedInput *float64 `json:"cached_input"`
	CacheWrite  *float64 `json:"cache_write"`
	Reasoning   *float64 `json:"reasoning"`
}

// indexRe turns encoding/json's "models.0.input" into "models[0].input".
var indexRe = regexp.MustCompile(`\.(\d+)`)

// parse decodes and validates a table; name is the file named in
// errors.
func parse(b []byte, name string) (Table, error) {
	fail := func(field, format string, args ...any) (Table, error) {
		return Table{}, fmt.Errorf("pricing %s: %s: "+format, append([]any{name, field}, args...)...)
	}
	// Each row is decoded on its own so the field an error names is
	// "models[i].<field>" whatever encoding/json puts in its own Field
	// (Go 1.26 names "models.input", 1.27 "models.0.input").
	decode := func(raw []byte, v any, prefix string) (Table, error, bool) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			var te *json.UnmarshalTypeError
			if errors.As(err, &te) && te.Field != "" {
				pt, e := fail(prefix+indexRe.ReplaceAllString(te.Field, "[$1]"), "want a %s, got %s", te.Type, te.Value)
				return pt, e, true
			}
			if msg, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
				pt, e := fail(prefix+strings.Trim(msg, `"`), "unknown field (the shape is {dated, note, models: [{provider, model_glob, input, output, cached_input, cache_write, reasoning}]})")
				return pt, e, true
			}
			return Table{}, fmt.Errorf("pricing %s: does not parse: %w", name, err), true
		}
		if dec.More() {
			return Table{}, fmt.Errorf("pricing %s: does not parse: more than one JSON value", name), true
		}
		return Table{}, nil, false
	}
	var f pricingFileShape
	if pt, err, bad := decode(b, &f, ""); bad {
		return pt, err
	}
	if f.Dated == nil || *f.Dated == "" {
		return fail("dated", "missing (the day the rates were read, YYYY-MM-DD)")
	}
	if _, err := time.Parse(time.DateOnly, *f.Dated); err != nil {
		return fail("dated", "%q is not a YYYY-MM-DD date", *f.Dated)
	}
	if f.Models == nil {
		return fail("models", "missing (a list of rows; [] prices nothing)")
	}
	t := Table{Dated: *f.Dated, Note: f.Note, Models: make([]ModelPrice, 0, len(*f.Models))}
	for i, raw := range *f.Models {
		at := func(field string) string { return "models[" + strconv.Itoa(i) + "]." + field }
		var m modelPriceFile
		if pt, err, bad := decode(raw, &m, at("")); bad {
			return pt, err
		}
		if m.Provider == nil || *m.Provider == "" {
			star := AnyProvider
			m.Provider = &star
		}
		if m.ModelGlob == nil || *m.ModelGlob == "" {
			return fail(at("model_glob"), "missing")
		}
		if _, err := path.Match(rowPattern(*m.Provider, *m.ModelGlob), ""); err != nil {
			return fail(at("model_glob"), "%q is not a path.Match pattern: %v", rowPattern(*m.Provider, *m.ModelGlob), err)
		}
		for _, rate := range []struct {
			field    string
			v        *float64
			required bool
		}{{"input", m.Input, true}, {"output", m.Output, true}, {"cached_input", m.CachedInput, false},
			{"cache_write", m.CacheWrite, false}, {"reasoning", m.Reasoning, false}} {
			switch {
			case rate.v == nil && rate.required:
				return fail(at(rate.field), "missing (USD per 1M tokens)")
			case rate.v != nil && (*rate.v < 0 || math.IsNaN(*rate.v) || math.IsInf(*rate.v, 0)):
				return fail(at(rate.field), "%v is not a non-negative rate", *rate.v)
			}
		}
		t.Models = append(t.Models, ModelPrice{
			Provider: *m.Provider, ModelGlob: *m.ModelGlob, Input: *m.Input, Output: *m.Output,
			CachedInput: m.CachedInput, CacheWrite: m.CacheWrite, Reasoning: m.Reasoning,
		})
	}
	return t, nil
}

// AnyProvider is a row's provider when it matches every transport.
const AnyProvider = "*"

// rowPattern is the path.Match pattern a row is looked up with: its
// model glob alone under the any-provider row, else provider/model_glob.
func rowPattern(provider, glob string) string {
	if provider == AnyProvider {
		return glob
	}
	return provider + "/" + glob
}

// Lookup is the first row matching provider/model (an any-provider
// row: the model alone).
func (t *Table) Lookup(provider, model string) (ModelPrice, bool) {
	key := Provider(provider) + "/" + model
	for _, m := range t.Models {
		k := key
		if m.Provider == AnyProvider {
			k = model
		}
		if ok, _ := path.Match(rowPattern(m.Provider, m.ModelGlob), k); ok {
			return m, true
		}
	}
	return ModelPrice{}, false
}

// scriptedProviders are the providers whose every model is scripted:
// wefttest's Script and Replay models. A provider outside this
// repository (a wrapper that may wrap any model) is never assumed $0:
// it ships its own pricing row.
var scriptedProviders = map[string]bool{"wefttest": true}

// Scripted says provider/model is a scripted model nothing bills: the
// scriptedProviders' models and the runtime's scripted engine
// (runtime/scripted.go's ModelInfo{Provider: "weft/runtime", Name:
// "scripted"}).
func Scripted(provider, model string) bool {
	return scriptedProviders[provider] || provider == "weft/runtime" && model == "scripted"
}

// ScriptedReason is a scripted figure's cost_reason.
const ScriptedReason = "a scripted model: nothing bills it, an exact $0"

// providerAliases maps the provider names a run row can carry onto the
// table's: run_start writes the adapter's ModelInfo.Provider ("google"),
// but the invoke_agent span writes the semconv gen_ai.provider.name
// (core/observe.go's providerNames: "google" → "gcp.gemini"; openai and
// anthropic are spelled the same both ways), and obsdb keeps whichever
// arrived first.
var providerAliases = map[string]string{"gcp.gemini": "google"}

// Provider is provider under the table's spelling.
func Provider(p string) string {
	if a, ok := providerAliases[p]; ok {
		return a
	}
	return p
}

// Badges a Quote can carry (the wire's cost_badge).
const (
	BadgeUnpriced = "unpriced" // no row matches the model
	BadgeScripted = "scripted" // a known, exact $0: a scripted model
)

// Cache marks (the wire's cache).
const (
	CacheExact       = "exact"
	CacheNotReported = "not_reported"
)

// Quote is one model call's (or one usage's) price under a Table:
// USD when Known; else Badge and Reason say why not. Models names the
// unpriced provider/model behind an unpriced quote.
type Quote struct {
	USD    float64
	Known  bool
	Badge  string
	Reason string
	Cache  string
	Models []string
}

// CacheOf reads whether the adapter reported a cache split: with
// neither CachedInputTokens nor CacheWriteTokens every input token is
// priced at the input rate, which may overstate a cached prompt.
// (A zero split cannot be told from an unreported one: an uncached
// prompt reads not_reported too, and its figure is exact.) Reasoning
// tokens are left out — they bill at the output rate at every vendor
// the default table lists.
func CacheOf(u core.Usage) string {
	if u.CachedInputTokens == 0 && u.CacheWriteTokens == 0 {
		return CacheNotReported
	}
	return CacheExact
}

// CacheNote is cost_reason's words for a not_reported cache on a
// known figure.
const CacheNote = "no cache split reported; a cached prompt may be overstated"

// Price is u's cost on provider/model under t: the formula above, or
// the unpriced badge naming the model when no row matches.
func (t *Table) Price(provider, model string, u core.Usage) Quote {
	out := Quote{Cache: CacheOf(u)}
	if provider == "" && model == "" {
		out.Badge, out.Reason = BadgeUnpriced, "the run reported no model to price"
		return out
	}
	m, ok := t.Lookup(provider, model)
	if !ok && Scripted(provider, model) {
		return Quote{Known: true, Badge: BadgeScripted, Reason: ScriptedReason, Cache: CacheExact}
	}
	if !ok {
		out.Badge = BadgeUnpriced
		out.Reason = provider + "/" + model + " has no price"
		out.Models = []string{provider + "/" + model}
		return out
	}
	rate := func(r *float64, base float64) float64 {
		if r != nil {
			return *r
		}
		return base
	}
	plainIn := max(u.InputTokens-u.CachedInputTokens-u.CacheWriteTokens, 0)
	plainOut := max(u.OutputTokens-u.ReasoningTokens, 0)
	out.USD = (m.Input*float64(plainIn) +
		rate(m.CachedInput, m.Input)*float64(u.CachedInputTokens) +
		rate(m.CacheWrite, m.Input)*float64(u.CacheWriteTokens) +
		m.Output*float64(plainOut) +
		rate(m.Reasoning, m.Output)*float64(u.ReasoningTokens)) / 1e6
	out.Known = true
	return out
}
