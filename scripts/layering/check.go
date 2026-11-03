package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Config is layers.txt, parsed.
type Config struct {
	Module      string      // the server module path
	ClientDir   string      // the client module's directory, relative to the root
	Layers      []LayerRule // pattern → layer; the longest pattern wins
	Domains     []string    // the L2 domain roots the DAG constrains
	DAG         map[string][]string
	Peers       [][2]string      // same-layer edges outside L2 (patterns)
	Forbid      [][2]int         // layer a must not import layer b although b < a
	StdlibOnly  []string         // patterns whose packages import the standard library only
	NewScope    []string         // patterns allowed to call tenant.NewScope
	ClientAllow []string         // module path prefixes the client module may depend on
	Vocab       []*regexp.Regexp // retired words
	VocabSkip   []string         // path prefixes the vocabulary audit skips
	VocabExts   map[string]bool  // file extensions the audit reads (besides Makefile)
}

// LayerRule maps a package pattern to a layer.
type LayerRule struct {
	Pattern string
	Layer   int
}

// Violation is one finding.
type Violation struct {
	Where string
	What  string
}

func (v Violation) String() string { return v.Where + ": " + v.What }

// LoadConfig reads a layers table. Each non-blank line is a directive
// and its fields; "#" starts a comment.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseConfig(f)
}

// ParseConfig parses a layers table from r.
func ParseConfig(r io.Reader) (*Config, error) {
	c := &Config{DAG: map[string][]string{}, VocabExts: map[string]bool{}}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fs := strings.Fields(line)
		if len(fs) == 0 {
			continue
		}
		bad := func(msg string) error { return fmt.Errorf("layers table line %d: %s", n, msg) }
		switch fs[0] {
		case "module":
			if len(fs) != 2 {
				return nil, bad("module <path>")
			}
			c.Module = fs[1]
		case "client-module":
			if len(fs) != 2 {
				return nil, bad("client-module <dir>")
			}
			c.ClientDir = fs[1]
		case "layer":
			if len(fs) < 3 {
				return nil, bad("layer <n> <pattern>...")
			}
			l, err := strconv.Atoi(fs[1])
			if err != nil {
				return nil, bad("layer number: " + err.Error())
			}
			for _, p := range fs[2:] {
				c.Layers = append(c.Layers, LayerRule{p, l})
			}
		case "domain":
			c.Domains = append(c.Domains, fs[1:]...)
		case "dag":
			if len(fs) < 2 {
				return nil, bad("dag <from> <to>...")
			}
			c.DAG[fs[1]] = append(c.DAG[fs[1]], fs[2:]...)
		case "peer":
			if len(fs) != 3 {
				return nil, bad("peer <from> <to>")
			}
			c.Peers = append(c.Peers, [2]string{fs[1], fs[2]})
		case "forbid":
			if len(fs) != 3 {
				return nil, bad("forbid <layer> <layer>")
			}
			a, err1 := strconv.Atoi(fs[1])
			b, err2 := strconv.Atoi(fs[2])
			if err1 != nil || err2 != nil {
				return nil, bad("forbid takes two layer numbers")
			}
			c.Forbid = append(c.Forbid, [2]int{a, b})
		case "stdlib-only":
			c.StdlibOnly = append(c.StdlibOnly, fs[1:]...)
		case "newscope":
			c.NewScope = append(c.NewScope, fs[1:]...)
		case "client-allow":
			c.ClientAllow = append(c.ClientAllow, fs[1:]...)
		case "vocab":
			for _, w := range fs[1:] {
				re, err := regexp.Compile(w)
				if err != nil {
					return nil, bad("vocab: " + err.Error())
				}
				c.Vocab = append(c.Vocab, re)
			}
		case "vocab-skip":
			c.VocabSkip = append(c.VocabSkip, fs[1:]...)
		case "vocab-ext":
			for _, e := range fs[1:] {
				c.VocabExts[e] = true
			}
		default:
			return nil, bad("unknown directive " + strconv.Quote(fs[0]))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if c.Module == "" {
		return nil, fmt.Errorf("layers table: no module line")
	}
	return c, nil
}

// match reports whether rel (a package path relative to the module,
// "." for the root) matches pattern: "x/..." is x and below, "." the
// root, anything else exactly.
func match(pattern, rel string) bool {
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return rel == base || strings.HasPrefix(rel, base+"/")
	}
	return pattern == rel
}

// rel is path relative to the module ("." for the module itself), or
// false when path is outside it.
func (c *Config) rel(path string) (string, bool) {
	if path == c.Module {
		return ".", true
	}
	r, ok := strings.CutPrefix(path, c.Module+"/")
	return r, ok
}

// layerOf is the layer of the longest pattern matching rel.
func (c *Config) layerOf(rel string) (int, bool) {
	best, layer := -1, 0
	for _, lr := range c.Layers {
		if match(lr.Pattern, rel) && len(lr.Pattern) > best {
			best, layer = len(lr.Pattern), lr.Layer
		}
	}
	return layer, best >= 0
}

func (c *Config) any(patterns []string, rel string) bool {
	for _, p := range patterns {
		if match(p, rel) {
			return true
		}
	}
	return false
}

// root is a package's first path element: the domain a package of an
// L2 tree belongs to (observe/query → observe).
func root(rel string) string {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

// goPackage is the part of `go list -json` the checks read.
type goPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
	Standard   bool
	Module     *struct{ Path string }
}

func goList(dir string, args ...string) ([]goPackage, error) {
	cmd := exec.Command("go", append([]string{"list", "-json"}, args...)...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list in %s: %v\n%s", dir, err, errb.String())
	}
	var pkgs []goPackage
	dec := json.NewDecoder(&out)
	for dec.More() {
		var p goPackage
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// CheckImports runs the layer, DAG, stdlib-only and NewScope checks
// over the server module's packages (non-test files).
func CheckImports(rootDir string, c *Config) ([]Violation, error) {
	pkgs, err := goList(rootDir, "./...")
	if err != nil {
		return nil, err
	}
	var vs []Violation
	for _, p := range pkgs {
		from, ok := c.rel(p.ImportPath)
		if !ok {
			continue
		}
		lf, ok := c.layerOf(from)
		if !ok {
			vs = append(vs, Violation{p.ImportPath, "has no layer in the layers table"})
			continue
		}
		stdOnly := c.any(c.StdlibOnly, from)
		for _, imp := range p.Imports {
			if stdOnly && !isStdlib(imp) {
				vs = append(vs, Violation{p.ImportPath, "imports " + imp + ": only the standard library is allowed"})
				continue
			}
			to, ok := c.rel(imp)
			if !ok {
				continue
			}
			lt, ok := c.layerOf(to)
			if !ok {
				vs = append(vs, Violation{p.ImportPath, "imports " + imp + ", which has no layer"})
				continue
			}
			if msg := c.edge(from, lf, to, lt); msg != "" {
				vs = append(vs, Violation{p.ImportPath, "imports " + imp + ": " + msg})
			}
		}
		more, err := c.checkNewScope(p, from)
		if err != nil {
			return nil, err
		}
		vs = append(vs, more...)
	}
	return vs, nil
}

// edge judges one import; "" means allowed.
func (c *Config) edge(from string, lf int, to string, lt int) string {
	if lt > lf {
		return fmt.Sprintf("goes up from L%d to L%d", lf, lt)
	}
	for _, f := range c.Forbid {
		if f[0] == lf && f[1] == lt {
			return fmt.Sprintf("L%d may not import L%d", lf, lt)
		}
	}
	if lt < lf {
		return ""
	}
	rf, rt := root(from), root(to)
	if rf == rt {
		return ""
	}
	if slices.Contains(c.Domains, rf) && slices.Contains(c.Domains, rt) {
		if slices.Contains(c.DAG[rf], rt) {
			return ""
		}
		return "domain " + rf + " may not import domain " + rt + " (not in the DAG)"
	}
	for _, pe := range c.Peers {
		if match(pe[0], from) && match(pe[1], to) {
			return ""
		}
	}
	return fmt.Sprintf("same-layer import (L%d) not declared as a peer", lf)
}

// checkNewScope parses the package's non-test files and reports every
// call of tenant.NewScope from a package not allowed to make one.
func (c *Config) checkNewScope(p goPackage, from string) ([]Violation, error) {
	if c.any(c.NewScope, from) {
		return nil, nil
	}
	tenantPath := c.Module + "/tenant"
	var vs []Violation
	for _, name := range p.GoFiles {
		path := filepath.Join(p.Dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		local := ""
		for _, im := range f.Imports {
			if v, _ := strconv.Unquote(im.Path.Value); v == tenantPath {
				local = "tenant"
				if im.Name != nil {
					local = im.Name.Name
				}
			}
		}
		if local == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewScope" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
					vs = append(vs, Violation{fset.Position(sel.Pos()).String(),
						"calls tenant.NewScope; only " + strings.Join(c.NewScope, ", ") + " may build a Scope"})
				}
			}
			return true
		})
	}
	return vs, nil
}

// isStdlib reports whether an import path is the standard library's:
// no dot in its first element.
func isStdlib(path string) bool {
	return !strings.Contains(root(path), ".")
}

// CheckClient lists the client module's dependencies and reports any
// package of the server module and any module off the allow-list.
func CheckClient(rootDir string, c *Config) ([]Violation, error) {
	if c.ClientDir == "" {
		return nil, nil
	}
	dir := filepath.Join(rootDir, c.ClientDir)
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil, nil
	}
	pkgs, err := goList(dir, "-deps", "./...")
	if err != nil {
		return nil, err
	}
	clientPath := c.Module + "/" + c.ClientDir
	var vs []Violation
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		if p.ImportPath == clientPath || strings.HasPrefix(p.ImportPath, clientPath+"/") {
			continue
		}
		if p.ImportPath == c.Module || strings.HasPrefix(p.ImportPath, c.Module+"/") {
			vs = append(vs, Violation{c.ClientDir, "depends on server package " + p.ImportPath})
			continue
		}
		mod := p.ImportPath
		if p.Module != nil {
			mod = p.Module.Path
		}
		if seen[mod] {
			continue
		}
		seen[mod] = true
		allowed := false
		for _, a := range c.ClientAllow {
			if mod == a || strings.HasPrefix(mod, strings.TrimSuffix(a, "/")+"/") {
				allowed = true
			}
		}
		if !allowed {
			vs = append(vs, Violation{c.ClientDir, "depends on module " + mod + ", which is not on the client allow-list"})
		}
	}
	return vs, nil
}

// CheckVocab reports every retired word in a path or in a text file's
// lines under rootDir, skipping VocabSkip prefixes, .git and
// node_modules.
func CheckVocab(rootDir string, c *Config) ([]Violation, error) {
	var vs []Violation
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootDir, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		for _, s := range c.VocabSkip {
			if rel == s || strings.HasPrefix(rel, strings.TrimSuffix(s, "/")+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		for _, re := range c.Vocab {
			if re.MatchString(d.Name()) {
				vs = append(vs, Violation{rel, "path holds the retired word " + strconv.Quote(re.FindString(d.Name()))})
			}
		}
		if d.IsDir() || !c.VocabExts[filepath.Ext(path)] && d.Name() != "Makefile" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, re := range c.Vocab {
				if w := re.FindString(line); w != "" {
					vs = append(vs, Violation{fmt.Sprintf("%s:%d", rel, i+1), "retired word " + strconv.Quote(w)})
				}
			}
		}
		return nil
	})
	return vs, err
}
