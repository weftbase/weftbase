package studio

import (
	"os"
	"testing"
)

// TestMain turns the discovery file off for this package's tests: no
// test run joins (and pours its runs into) a Studio the developer
// happens to be running — exporter.Install/Start read the file when
// WEFTBASE_URL is unset (plan B3).
func TestMain(m *testing.M) {
	_ = os.Setenv("WEFTBASE_DISCOVERY", "off")
	os.Exit(m.Run())
}
