package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	pricing "github.com/weftbase/weftbase/observe/cost"

	"github.com/weftbase/weftbase"
)

// pricingFlag adds --pricing to a flag set: a pricing table file
// (cost.Load; pricing.json's shape), mirroring WEFT_PRICING one
// to one (pricingOptions reads both; the flag wins).
func pricingFlag(fs *flag.FlagSet) *string {
	return fs.String("pricing", "",
		"`path` to a pricing table (pricing.json's shape, USD per 1M tokens) that replaces the shipped one whole (default: $WEFT_PRICING, else the table shipped with this weft)")
}

// pricingOptions turns --pricing / WEFT_PRICING into server options and
// the line the banner prints ("" for the shipped table). A file that
// does not load is a start error naming the path and the field — never
// a server silently pricing with the default.
func pricingOptions(flagPath string) ([]weftbase.Option, string, error) {
	path, from := flagPath, "--pricing"
	if path == "" {
		path, from = os.Getenv("WEFT_PRICING"), "WEFT_PRICING"
	}
	if path == "" {
		return nil, "", nil
	}
	t, err := pricing.Load(path)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", from, err)
	}
	return []weftbase.Option{weftbase.Pricing(t)}, "weftbase: pricing " + path + " (" + from + "), dated " + t.Dated +
		", " + strconv.Itoa(len(t.Models)) + " models", nil
}
