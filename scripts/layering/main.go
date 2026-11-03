// Command layering checks weftbase's import layering (docs/architecture.md):
//
//   - every package of the server module has a layer in layers.txt, and
//     no non-test import goes up a layer;
//   - a domain imports another domain only along the DAG in layers.txt;
//   - packages marked stdlib-only import nothing but the standard library;
//   - tenant.NewScope is called only from the packages layers.txt names
//     (test files excepted);
//   - the client module imports nothing of the server module, and only
//     modules on its allow-list;
//   - the retired product words appear in no Go file and no path.
//
// Run from the repository root:
//
//	go run ./scripts/layering               # every check
//	go run ./scripts/layering -client-only  # the client module's dependencies
//	go run ./scripts/layering -vocab-only   # the vocabulary audit
//
// It exits 1 and prints one line per violation.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	clientOnly := flag.Bool("client-only", false, "check only the client module's dependencies")
	vocabOnly := flag.Bool("vocab-only", false, "run only the vocabulary audit")
	root := flag.String("root", ".", "the repository root")
	table := flag.String("table", "", "the layers table (default <root>/scripts/layering/layers.txt)")
	flag.Parse()

	if *table == "" {
		*table = filepath.Join(*root, "scripts", "layering", "layers.txt")
	}
	cfg, err := LoadConfig(*table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "layering:", err)
		os.Exit(2)
	}
	var vs []Violation
	switch {
	case *clientOnly:
		vs, err = CheckClient(*root, cfg)
	case *vocabOnly:
		vs, err = CheckVocab(*root, cfg)
	default:
		var more []Violation
		if vs, err = CheckImports(*root, cfg); err == nil {
			if more, err = CheckClient(*root, cfg); err == nil {
				vs = append(vs, more...)
				if more, err = CheckVocab(*root, cfg); err == nil {
					vs = append(vs, more...)
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "layering:", err)
		os.Exit(2)
	}
	for _, v := range vs {
		fmt.Println(v)
	}
	if len(vs) > 0 {
		fmt.Fprintf(os.Stderr, "layering: %d violation(s)\n", len(vs))
		os.Exit(1)
	}
}
