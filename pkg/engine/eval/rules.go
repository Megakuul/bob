package eval

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// bob.go rules R1–R7 (SPEC R-FW-002), checked before compiling. All violations are reported
// together, sorted by path.

// bobFile is one parsed bob.go.
type bobFile struct {
	dir     string // directory relative to its module root ("." = root)
	shown   string // path in messages: repo-relative, or "<module>@<version>/<dir>/bob.go"
	src     []byte
	fset    *token.FileSet
	file    *ast.File // nil when the file does not parse
	parse   error
	pkg     string
	imports []bobImport
}

type bobImport struct {
	path string
	pos  token.Position
}

func parseBob(dir, shown string, src []byte) *bobFile {
	f := &bobFile{dir: dir, shown: shown, src: src, fset: token.NewFileSet()}
	af, err := parser.ParseFile(f.fset, shown, src, parser.SkipObjectResolution|parser.ParseComments)
	if af != nil {
		f.pkg = af.Name.Name
		for _, imp := range af.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr == nil {
				f.imports = append(f.imports, bobImport{p, f.fset.Position(imp.Pos())})
			}
		}
	}
	if err != nil {
		f.parse = err
		return f
	}
	f.file = af
	return f
}

// goPkg is the real (untagged) Go code of a directory.
type goPkg struct {
	name    string
	nonTest bool // the directory has non-test Go files (not only _test.go files)
	imports []string
	files   []string // non-test and in-package test files, for R4
}

// realGo returns the Go package of dir as go build sees it without the bob tag; nil when the
// directory has no buildable Go code.
func realGo(root, dir string) *goPkg {
	ctx := build.Default
	ctx.CgoEnabled = true
	ctx.BuildTags = nil
	bp, err := ctx.ImportDir(filepath.Join(root, filepath.FromSlash(dir)), build.ImportComment)
	var mp *build.MultiplePackageError
	switch {
	case errors.As(err, &mp):
		return &goPkg{name: mp.Packages[0], nonTest: true}
	case err != nil:
		return nil
	}
	g := &goPkg{name: bp.Name, imports: bp.Imports, nonTest: len(bp.GoFiles)+len(bp.CgoFiles) > 0}
	for _, list := range [][]string{bp.GoFiles, bp.CgoFiles, bp.TestGoFiles} {
		for _, f := range list {
			if f != "bob.go" {
				g.files = append(g.files, f)
			}
		}
	}
	sort.Strings(g.files)
	return g
}

// cgoExts are the files that make R5 materialise zz_bob_cgo.go.
var cgoExts = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".m": true, ".f": true,
	".F": true, ".f90": true, ".swig": true, ".swigcxx": true}

// expectedPkg is R2's package for a non-root directory dir without Go code: the directory's
// name, sanitised (SPEC R-FW-002 R2). The root has no expected name (rootPkgOK).
func expectedPkg(dir string) string {
	base := path.Base(dir)
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		s = "x" + s
	}
	return s
}

// ruleCheck is the input of the rule check.
type ruleCheck struct {
	root   string
	module string
	scan   *repoScan
	own    map[string]*bobFile // repo-relative dir → bob.go of this repo
	deps   *depSet
	fwPkgs map[string]bool
}

// check returns the rule violations (sorted by path) and the directories that need the R5
// stub.
func (c *ruleCheck) check() (ir.Errors, []string) {
	type violation struct {
		path string
		line int
		errs ir.Errors
	}
	var vs []violation
	add := func(p string, line int, es ir.Errors) { vs = append(vs, violation{p, line, es}) }
	real := map[string]*goPkg{}
	realOf := func(dir string) *goPkg {
		g, ok := real[dir]
		if !ok {
			g = realGo(c.root, dir)
			real[dir] = g
		}
		return g
	}
	var stubs []string
	for _, dir := range c.scan.bobDirs {
		f := c.own[dir]
		// R1
		first, _, _ := bytes.Cut(f.src, []byte("\n"))
		if string(bytes.TrimSuffix(first, []byte("\r"))) != "//go:build bob" {
			add(f.shown, 0, ir.Errors{ir.RuleError("R1", "dir", dir)})
		}
		if f.parse != nil {
			for _, e := range syntaxErrors(f.parse) {
				add(f.shown, e.line, e.errs)
			}
			continue
		}
		g := realOf(dir)
		// R2. The root without non-test Go code accepts any package name (A131): it is never
		// imported by name, and the checkout directory's name differs between machines (a
		// worker unpacks sources into work/src).
		want := expectedPkg(dir)
		if g != nil {
			want = g.name
		}
		anyName := dir == "." && (g == nil || !g.nonTest)
		if f.pkg != want && !anyName {
			add(f.shown, 0, ir.Errors{ir.RuleError("R2", "dir", dir, "have", f.pkg, "want", want)})
		}
		// R4
		if g != nil {
			c.checkR4(f, dir, g, func(line int, es ir.Errors) { add(f.shown, line, es) })
		}
		// R5
		for _, n := range c.scan.goDirs[dir] {
			if cgoExts[path.Ext(n)] {
				stubs = append(stubs, dir)
				break
			}
		}
		for _, imp := range f.imports {
			c.checkImport(f, dir, imp, realOf, func(es ir.Errors) { add(f.shown, imp.pos.Line, es) })
		}
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].path != vs[j].path {
			return vs[i].path < vs[j].path
		}
		return vs[i].line < vs[j].line
	})
	var out ir.Errors
	for _, v := range vs {
		out = append(out, v.errs...)
	}
	return out, stubs
}

// checkImport checks R3, R6 and R7 for one import of f.
func (c *ruleCheck) checkImport(f *bobFile, d string, imp bobImport, realOf func(string) *goPkg, add func(ir.Errors)) {
	p := imp.path
	switch {
	case isStd(p):
		return
	case p == frameworkModule || strings.HasPrefix(p, frameworkModule+"/"):
		if !c.fwPkgs[p] {
			add(ir.Errors{ir.RuleError("R7", "dir", d, "path", p)})
		}
		return
	}
	if dir, ok := c.ownDir(p); ok {
		target, isBob := c.own[dir]
		if !isBob {
			add(ir.Errors{ir.RuleError("R7", "dir", d, "path", p)})
			return
		}
		if target.pkg == "main" {
			add(ir.Errors{ir.RuleError("R3", "dir", d, "maindir", dir)})
			return
		}
		// R6: the imported directory's real Go code reaches this directory.
		if c.reaches(dir, f.dir, realOf) {
			add(ir.Errors{ir.RuleError("R6", "dir", d, "dir2", dir)})
		}
		return
	}
	if m, rel, ok := c.deps.lookup(p); ok {
		target, isBob := m.bob[rel]
		if !isBob {
			add(ir.Errors{ir.RuleError("R7", "dir", d, "path", p)})
			return
		}
		if target.pkg == "main" {
			add(ir.Errors{ir.RuleError("R3", "dir", d, "maindir", m.path+"@"+m.version+"/"+rel)})
		}
		return
	}
	add(ir.Errors{ir.RuleError("R7", "dir", d, "path", p)})
}

// ownDir maps an import path of this repository to its directory: a path of the root module,
// or of a Go module of this repository whose go.mod bob generates (golang.NewModule; its
// directories belong to the root module's build definitions, A103), the longest module path
// first.
func (c *ruleCheck) ownDir(p string) (string, bool) {
	var nested map[string]string
	if c.scan != nil {
		nested = c.scan.nested
	}
	return ownDirOf(c.module, nested, p)
}

// ownDirOf is ownDir for a root module path and the nested modules of a scan.
func ownDirOf(module string, nested map[string]string, p string) (string, bool) {
	best, bestDir := "", ""
	for mp, dir := range nested {
		if (p == mp || strings.HasPrefix(p, mp+"/")) && len(mp) > len(best) {
			best, bestDir = mp, dir
		}
	}
	if best != "" && len(best) > len(module) || best != "" && !(p == module || strings.HasPrefix(p, module+"/")) {
		if p == best {
			return bestDir, true
		}
		return path.Join(bestDir, strings.TrimPrefix(p, best+"/")), true
	}
	if p == module {
		return ".", true
	}
	if rel, ok := strings.CutPrefix(p, module+"/"); ok {
		return rel, true
	}
	return "", false
}

// reaches reports whether the real Go code of from imports to, directly or through other real
// Go imports of this module.
func (c *ruleCheck) reaches(from, to string, realOf func(string) *goPkg) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[d] {
			continue
		}
		seen[d] = true
		g := realOf(d)
		if g == nil {
			continue
		}
		for _, imp := range g.imports {
			next, ok := c.ownDir(imp)
			if !ok {
				continue
			}
			if next == to {
				return true
			}
			stack = append(stack, next)
		}
	}
	return false
}

// checkR4 reports top-level identifiers of bob.go that the directory's Go code declares too.
func (c *ruleCheck) checkR4(f *bobFile, d string, g *goPkg, add func(int, ir.Errors)) {
	other := map[string]string{} // name → "file:line"
	for _, name := range g.files {
		rel := path.Join(f.dir, name)
		src, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil || af.Name.Name != g.name {
			continue
		}
		for name, pos := range topLevel(af) {
			if _, dup := other[name]; !dup {
				p := fset.Position(pos)
				other[name] = fmt.Sprintf("%s:%d", rel, p.Line)
			}
		}
	}
	names := topLevel(f.file)
	keys := slices.Sorted(maps.Keys(names))
	for _, n := range keys {
		at, ok := other[n]
		if !ok {
			continue
		}
		file, line, _ := strings.Cut(at, ":")
		bl := f.fset.Position(names[n]).Line
		add(bl, ir.Errors{ir.RuleError("R4", "dir", d, "line", strconv.Itoa(bl), "name", n, "file", file, "fileline", line)})
	}
}

// topLevel returns the package-level identifiers a file declares (no methods, init or _).
func topLevel(f *ast.File) map[string]token.Pos {
	out := map[string]token.Pos{}
	add := func(id *ast.Ident) {
		if id.Name != "_" && id.Name != "init" {
			if _, ok := out[id.Name]; !ok {
				out[id.Name] = id.Pos()
			}
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				add(d.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n)
					}
				}
			}
		}
	}
	return out
}

type posErr struct {
	line int
	errs ir.Errors
}

// syntaxErrors maps a parser error of a bob.go to definition errors at their positions.
func syntaxErrors(err error) []posErr {
	var list scanner.ErrorList
	if errors.As(err, &list) {
		var out []posErr
		for _, e := range list {
			out = append(out, posErr{e.Pos.Line, ir.Errors{ir.DefinitionError(e.Pos.Filename, e.Pos.Line, e.Pos.Column, "", e.Msg)}})
		}
		return out
	}
	return []posErr{{0, ir.Errors{ir.DefinitionError("", 0, 0, "", err.Error())}}}
}

// isStd reports whether an import path is a standard library path (no dot in the first
// element).
func isStd(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return !strings.Contains(first, ".")
}
