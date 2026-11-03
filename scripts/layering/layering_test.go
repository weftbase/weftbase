package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The seeded tree: one module with a package per rule, each breaking
// it once, and a table that classifies them. A broken check must not
// pass silently, so every seeded violation must be reported.
const seedTable = `
module example.com/m
client-module client
layer 0 client/...
layer 1 store tenant/... low/...
layer 2 alpha/... beta/... authz/...
layer 3 store/sqlite/...
layer 4 api/... console/...
layer 5 . cmd/... skipped/...
forbid 4 3
domain alpha beta authz
dag alpha beta
stdlib-only console
newscope authz/... tenant/...
vocab (?i)studio
vocab-ext .go
vocab-skip skipped
`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func cleanTree() map[string]string {
	return map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.26\n",
		"tenant/tenant.go":    "package tenant\n\ntype Scope struct{}\n\nfunc NewScope() Scope { return Scope{} }\n",
		"low/low.go":          "package low\n\nconst X = 1\n",
		"store/store.go":      "package store\n",
		"store/sqlite/s.go":   "package sqlite\n\nimport _ \"example.com/m/alpha\"\n",
		"alpha/alpha.go":      "package alpha\n\nimport _ \"example.com/m/beta\"\nimport _ \"example.com/m/low\"\n",
		"beta/beta.go":        "package beta\n",
		"authz/authz.go":      "package authz\n\nimport \"example.com/m/tenant\"\n\nvar S = tenant.NewScope()\n",
		"api/http/h.go":       "package http\n\nimport _ \"example.com/m/alpha\"\nimport _ \"net/http\"\n",
		"console/console.go":  "package console\n\nimport _ \"embed\"\n",
		"cmd/x/main.go":       "package main\n\nimport _ \"example.com/m/store/sqlite\"\n\nfunc main() {}\n",
		"skipped/note.go":     "package skipped // Studio, skipped on purpose\n",
		"alpha/alpha_test.go": "package alpha\n\nimport \"example.com/m/tenant\"\n\nvar _ = tenant.NewScope()\n",
	}
}

func check(t *testing.T, dir string) []Violation {
	t.Helper()
	cfg, err := ParseConfig(strings.NewReader(seedTable))
	if err != nil {
		t.Fatal(err)
	}
	vs, err := CheckImports(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	more, err := CheckVocab(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return append(vs, more...)
}

func TestCleanTreePasses(t *testing.T) {
	if vs := check(t, writeTree(t, cleanTree())); len(vs) != 0 {
		t.Fatalf("clean tree reported %v", vs)
	}
}

func TestSeededViolationsAreReported(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		body  string
		wants string
	}{
		{"up a layer", "low/up.go", "package low\n\nimport _ \"example.com/m/beta\"\n", "goes up from L1 to L2"},
		{"across the DAG", "beta/back.go", "package beta\n\nimport _ \"example.com/m/authz\"\n", "not in the DAG"},
		{"forbidden layer", "api/http/db.go", "package http\n\nimport _ \"example.com/m/store/sqlite\"\n", "L4 may not import L3"},
		{"stdlib only", "console/dep.go", "package console\n\nimport _ \"example.com/m/low\"\n", "only the standard library"},
		{"NewScope outside authz", "beta/scope.go", "package beta\n\nimport \"example.com/m/tenant\"\n\nvar S = tenant.NewScope()\n", "calls tenant.NewScope"},
		{"unclassified package", "stray/stray.go", "package stray\n", "has no layer"},
		{"retired word", "beta/word.go", "package beta\n\n// the Studio UI\n", "retired word"},
		{"retired word in a path", "beta/studio_x.go", "package beta\n", "path holds the retired word"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := cleanTree()
			files[tc.file] = tc.body
			vs := check(t, writeTree(t, files))
			found := false
			for _, v := range vs {
				if strings.Contains(v.String(), tc.wants) {
					found = true
				}
			}
			if !found {
				t.Fatalf("seeded %s: want a violation containing %q, got %v", tc.name, tc.wants, vs)
			}
		})
	}
}

// TestTheRepositoryTableParses keeps the committed table loadable.
func TestTheRepositoryTableParses(t *testing.T) {
	if _, err := LoadConfig("layers.txt"); err != nil {
		t.Fatal(err)
	}
}
