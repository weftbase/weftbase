// Package weftsrc finds the source of the weft framework module this
// build resolves — the go.work checkout in development, the module
// cache for a tagged weft. The platform's tests read files weft owns:
// the record contract (docs/record-contract.md) the store is pinned
// to, the adapters' recorded fixtures and the workspace a generated
// test is compiled in.
package weftsrc

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The framework's two modules: the root (facade, adapters, thread,
// otel, …) and the loop.
const (
	Module     = "github.com/weftbase/weft"
	CoreModule = "github.com/weftbase/weft/core"
)

var (
	mu   sync.Mutex
	dirs = map[string]string{}
)

// ModuleDir is module's directory, as `go list -m` resolves it from the
// working directory. A test that cannot resolve it fails.
func ModuleDir(t testing.TB, module string) string {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if d, ok := dirs[module]; ok {
		return d
	}
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	d := strings.TrimSpace(string(out))
	if err != nil || d == "" {
		t.Fatalf("weftsrc: cannot resolve %s's directory (go list -m): %v", module, err)
	}
	dirs[module] = d
	return d
}

// Dir is the weft root module's directory.
func Dir(t testing.TB) string {
	t.Helper()
	return ModuleDir(t, Module)
}

// CoreDir is the core module's directory: a module of its own, so the
// root module's directory in the module cache does not hold it.
func CoreDir(t testing.TB) string {
	t.Helper()
	return ModuleDir(t, CoreModule)
}

// Path is name — slash-separated, relative to the weft repository's
// root — on disk: "core" and "core/…" resolve inside the core module,
// everything else inside the root module.
func Path(t testing.TB, name string) string {
	t.Helper()
	if name == "core" || strings.HasPrefix(name, "core/") {
		return filepath.Join(CoreDir(t), filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(name, "core"), "/")))
	}
	return filepath.Join(Dir(t), filepath.FromSlash(name))
}
