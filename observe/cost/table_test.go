package cost

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weftbase/observe/cost/pricingdata"
)

// TestPricingJSONIsGenerated: the committed pricing.json is exactly the
// generator's output (go generate ./observe/cost/... — the weft.json golden
// pattern), and the generator is deterministic, so a second run
// changes nothing.
func TestPricingJSONIsGenerated(t *testing.T) {
	want, err := pricingdata.JSON()
	if err != nil {
		t.Fatal(err)
	}
	again, err := pricingdata.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, again) {
		t.Fatal("pricingdata.JSON is not deterministic: go generate would not be idempotent")
	}
	got, err := os.ReadFile("pricing.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("observe/cost/pricing.json is stale: edit observe/cost/pricingdata, then run go generate ./observe/cost/... and commit both")
	}
}

// TestDefaultLoads: the shipped table loads (DefaultPricing
// panics otherwise), carries its date, and no row is shadowed by an
// earlier one — every literal model id is matched by its own row first.
func TestDefaultLoads(t *testing.T) {
	d := Default()
	if d.Source != SourceDefault || d.Dated != pricingdata.Dated || len(d.Models) != len(pricingdata.Models) || d.Note == "" {
		t.Fatalf("DefaultPricing = source %q dated %q %d models, want default, %s, %d and a note",
			d.Source, d.Dated, len(d.Models), pricingdata.Dated, len(pricingdata.Models))
	}
	for i, m := range d.Models {
		if strings.ContainsAny(m.ModelGlob, "*?[\\") {
			continue
		}
		got, ok := d.Lookup(m.Provider, m.ModelGlob)
		if !ok || got.ModelGlob != m.ModelGlob || got.Provider != m.Provider {
			t.Errorf("row %d (%s/%s) is shadowed by %s/%s", i, m.Provider, m.ModelGlob, got.Provider, got.ModelGlob)
		}
	}
	if _, ok := d.Lookup("anthropic", "claude-haiku-4-5-20251001"); !ok {
		t.Error("a dated claude-haiku-4-5 snapshot is not priced")
	}
	if _, ok := d.Lookup("openai", "gpt-5.9"); ok {
		t.Error("an unknown future model inherits a price")
	}
}

// TestLoad pins WEFT_PRICING's loader: a file replaces the default
// whole, reports source file and its path; a malformed file is an
// error naming the path and the field.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", `{"dated":"2026-09-01","models":[{"provider":"acme","model_glob":"mystery-*","input":1,"output":2,"cached_input":0.5}]}`)
	tbl, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Source != SourceFile || tbl.Path != good || len(tbl.Models) != 1 {
		t.Fatalf("LoadPricing = %+v", tbl)
	}
	// Replace, not merge: anthropic is unpriced under the file.
	if _, ok := tbl.Lookup("anthropic", "claude-opus-5-5"); ok {
		t.Error("a pricing file merged with the default table; it must replace it")
	}
	if _, ok := tbl.Lookup("acme", "mystery-1"); !ok {
		t.Error("the file's own row does not match")
	}

	for _, c := range []struct{ name, body, field string }{
		{"syntax.json", `{"dated":`, "does not parse"},
		{"nodate.json", `{"models":[]}`, "dated: missing"},
		{"baddate.json", `{"dated":"10/10/2026","models":[]}`, "dated: \"10/10/2026\" is not a YYYY-MM-DD date"},
		{"nomodels.json", `{"dated":"2026-10-10"}`, "models: missing"},
		{"unknown.json", `{"dated":"2026-10-10","models":[{"provider":"a","model_glob":"b","input":1,"output":1,"inptu":2}]}`, "inptu: unknown field"},
		{"extends.json", `{"dated":"2026-10-10","extends":"default","models":[]}`, "extends: unknown field"},
		{"noinput.json", `{"dated":"2026-10-10","models":[{"provider":"a","model_glob":"b","output":1}]}`, "models[0].input: missing"},
		{"negative.json", `{"dated":"2026-10-10","models":[{"provider":"a","model_glob":"b","input":1,"output":1,"cache_write":-1}]}`, "models[0].cache_write: -1 is not a non-negative rate"},
		{"badglob.json", `{"dated":"2026-10-10","models":[{"provider":"a","model_glob":"[","input":1,"output":1}]}`, "models[0].model_glob"},
		{"type.json", `{"dated":"2026-10-10","models":[{"provider":"a","model_glob":"b","input":"1","output":1}]}`, "models[0].input: want a float64"},
	} {
		p := write(c.name, c.body)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), c.field) {
			t.Errorf("Load(%s) = %v, want an error naming %s and %q", c.name, err, p, c.field)
		}
	}
	if _, err := Load(filepath.Join(dir, "absent.json")); err == nil || !strings.Contains(err.Error(), "absent.json") {
		t.Errorf("Load(absent) = %v", err)
	}
}

// TestPricingAnyProvider (U7: UX-A-12's "base_url-aware" half): a row
// without a provider (or with provider "*") matches on its model glob
// alone, whatever the transport — an OpenAI-compatible endpoint reached
// through the openai adapter, a provider spelled with a "/".
func TestPricingAnyProvider(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "any.json")
	if err := os.WriteFile(file, []byte(`{"dated":"2026-10-12","models":[
		{"model_glob":"kimi-k2.7-*","input":1,"output":2},
		{"provider":"*","model_glob":"glm-5","input":3,"output":4}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tbl, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Models[0].Provider != AnyProvider {
		t.Errorf("a row without a provider loads as %q, want %q", tbl.Models[0].Provider, AnyProvider)
	}
	for _, c := range []struct {
		provider, model string
		input           float64
		ok              bool
	}{
		{"openai", "kimi-k2.7-code", 1, true},
		{"moonshot/proxy", "kimi-k2.7-code", 1, true},
		{"openrouter", "glm-5", 3, true},
		{"openai", "gpt-4o", 0, false},
		{"openai", "vendor/kimi-k2.7-code", 0, false}, // "*" never crosses "/"
	} {
		m, ok := tbl.Lookup(c.provider, c.model)
		if ok != c.ok || ok && m.Input != c.input {
			t.Errorf("lookup(%s, %s) = %+v %v, want input %v %v", c.provider, c.model, m, ok, c.input, c.ok)
		}
	}
	if _, err := Load(writeFile(t, dir, "bad.json", `{"dated":"2026-10-12","models":[{"model_glob":"[","input":1,"output":1}]}`)); err == nil ||
		!strings.Contains(err.Error(), "models[0].model_glob") {
		t.Errorf("a bad glob on an any-provider row: %v", err)
	}
}

// TestPricingCasesShared (U24.2-fix 5): testdata/pricing-cases.json is
// priced here to the figures a client restatement must match — globs, the alias, the
// any-provider row, cache splits, reasoning, unknown and scripted.
func TestPricingCasesShared(t *testing.T) {
	b, err := os.ReadFile("testdata/pricing-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Table Table `json:"table"`
		Cases []struct {
			Name, Provider, Model string
			Usage                 core.Usage
			Known, Scripted       bool
			USD                   float64
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Cases {
		got := doc.Table.Price(c.Provider, c.Model, c.Usage)
		if got.Known != c.Known || (c.Known && math.Abs(got.USD-c.USD) > 1e-12) || (got.Badge == BadgeScripted) != c.Scripted {
			t.Errorf("%s: price(%s/%s) = known %v usd %v badge %q, want known %v usd %v scripted %v",
				c.Name, c.Provider, c.Model, got.Known, got.USD, got.Badge, c.Known, c.USD, c.Scripted)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
