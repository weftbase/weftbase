// Command pricinggen writes weftbase's default pricing table,
// observe/cost/pricing.json, from observe/cost/pricingdata (plan A5). It
// runs from observe/cost/table.go's go:generate line; the output depends on
// the table alone, so a second run changes nothing.
//
//	go run ./internal/pricinggen -o pricing.json
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/weftbase/weftbase/observe/cost/pricingdata"
)

func main() {
	out := flag.String("o", "pricing.json", "the file to write")
	flag.Parse()
	b, err := pricingdata.JSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pricinggen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pricinggen:", err)
		os.Exit(1)
	}
}
