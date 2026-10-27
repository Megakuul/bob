// Package boblint holds the static guards of the typed surface and of per-package build
// modules (SPEC R-FW-070, R-FW-071, R-FW-072, R-NIX-031; HARNESS.md §18). It is shared by
// tests/core (framework API and fixture repositories) and tests/docs (documented projects).
// Everything here works on the Go syntax tree only: no type checking, no bob binary, so the
// guards run in every CI stage and cannot be skipped by a missing feature.
package boblint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// APIString is one exported parameter or struct field of a public API package whose type is a
// raw string form (string, []string, ...string, map[string]T).
type APIString struct {
	Key string // "pkg/cc.LibraryOpts.Hdrs", "pkg/nixpkgs.Tool(attr)", "pkg/golang.(*Module).Binary(dir)"
	Pos string // "pkg/cc/cc.go:42"
}

// APIStrings lists every raw-string parameter and field of the exported API of the package
// directories dirs (relative to root, slash-separated). Test files, generated files
// ("// Code generated ... DO NOT EDIT."), and the gen, bop, internal, testdata and diag
// subdirectories are not part of the surface; dirs lists packages, not trees.
func APIStrings(root string, dirs []string) ([]APIString, error) {
	var out []APIString
	for _, dir := range dirs {
		fset := token.NewFileSet()
		ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file := filepath.Join(root, filepath.FromSlash(dir), name)
			f, err := parser.ParseFile(fset, file, nil, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			if generated(f) {
				continue
			}
			pos := func(n ast.Node) string { return fmt.Sprintf("%s/%s:%d", dir, name, fset.Position(n.Pos()).Line) }
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					fn := dir + "." + d.Name.Name
					if d.Recv != nil && len(d.Recv.List) == 1 {
						recv := exprString(d.Recv.List[0].Type)
						if !ast.IsExported(strings.TrimLeft(recv, "*")) {
							continue
						}
						fn = dir + ".(" + recv + ")." + d.Name.Name
						if !strings.HasPrefix(recv, "*") {
							fn = dir + "." + recv + "." + d.Name.Name
						}
					}
					for _, fld := range d.Type.Params.List {
						if !rawString(fld.Type) {
							continue
						}
						names := fld.Names
						if len(names) == 0 {
							out = append(out, APIString{Key: fn + "(_)", Pos: pos(fld)})
						}
						for _, n := range names {
							out = append(out, APIString{Key: fn + "(" + n.Name + ")", Pos: pos(n)})
						}
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						ts, ok := s.(*ast.TypeSpec)
						if !ok || !ts.Name.IsExported() {
							continue
						}
						st, ok := ts.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for _, fld := range st.Fields.List {
							if !rawString(fld.Type) {
								continue
							}
							for _, n := range fld.Names {
								if n.IsExported() {
									out = append(out, APIString{Key: dir + "." + ts.Name.Name + "." + n.Name, Pos: pos(n)})
								}
							}
						}
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// generated reports whether f carries the standard generated-code header.
func generated(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "// Code generated ") && strings.HasSuffix(c.Text, " DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}

// rawString reports whether e is string, []string, ...string or map[string]T.
func rawString(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == "string"
	case *ast.ArrayType:
		return rawString(e.Elt)
	case *ast.Ellipsis:
		return rawString(e.Elt)
	case *ast.MapType:
		if id, ok := e.Key.(*ast.Ident); ok && id.Name == "string" {
			return true
		}
	}
	return false
}

func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.IndexExpr:
		return exprString(e.X)
	case *ast.IndexListExpr:
		return exprString(e.X)
	}
	return fmt.Sprintf("%T", e)
}

// Problem is one violation of the per-package module rules in a bob.go file.
type Problem struct {
	File string // repo-relative path of the bob.go
	Line int
	Rule string // R-FW-070, R-FW-071, R-CC-006, R-NIX-031, R-FW-072
	Msg  string
}

func (p Problem) String() string { return fmt.Sprintf("%s:%d: [%s] %s", p.File, p.Line, p.Rule, p.Msg) }

// Repo is a set of bob.go files of one repository: repo-relative path → content.
type Repo map[string]string

// LoadRepo reads every bob.go below dir (skipping hidden directories, node_modules and
// testdata) into a Repo keyed by the path relative to dir.
func LoadRepo(dir string) (Repo, error) {
	r := Repo{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dir {
			n := d.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "node_modules" || n == "testdata" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "bob.lock")); err == nil {
				return filepath.SkipDir // a nested repository is checked on its own
			}
		}
		if d.IsDir() || d.Name() != "bob.go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		r[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return r, err
}

// unit lists, per framework package, the constructors that define a build unit: a package
// directory has at most one of them (R-FW-071; tests are not units). Methods are written
// ".Name" and match any receiver of a value from that package's constructors.
var unit = map[string][]string{
	"cc":       {"StaticLibrary", "SharedLibrary", "NewBinary", "External"},
	"golang":   {".Binary", ".CArchive", ".CShared"},
	"rust":     {"Library", "ProcMacro", "StaticLib", "CDylib", "Binary"},
	"npm":      {"Library", "NewApp", "Vite"},
	"ocaml":    {"Library", "Executable"},
	"proto":    {"NewLibrary"},
	"graphql":  {"NewSchema"},
	"openapi":  {"NewSpec"},
	"services": {},
}

// sourceFuncs are the bob calls whose string arguments are paths inside the package
// directory (R-FW-070).
var sourceFuncs = map[string]bool{"Glob": true, "Files": true, "Dir": true, "IncludeDir": true}

// goDirMethods are the golang.Module methods that took a directory string before R-FW-070;
// a string argument to them names another directory's Go package.
var goDirMethods = map[string]bool{"Binary": true, "CArchive": true, "CShared": true, "Cgo": true, "Tests": true}

// nixRoles are the nixpkgs functions that took an attribute string before R-NIX-031.
var nixRoles = map[string]bool{"Tool": true, "Lib": true, "Pkg": true, "Exe": true}

// Check applies the per-package rules to every bob.go of r and returns the problems sorted by
// file and line.
func Check(r Repo) []Problem {
	pkgDirs := map[string]bool{}
	for p := range r {
		pkgDirs[path.Dir(p)] = true
	}
	var out []Problem
	for file, src := range r {
		out = append(out, checkFile(file, src, pkgDirs)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func checkFile(file, src string, pkgDirs map[string]bool) []Problem {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return []Problem{{File: file, Rule: "R-FW-001", Msg: "does not parse: " + err.Error()}}
	}
	dir := path.Dir(file)
	// Local names of the framework packages ("cc" → "cc", or an alias).
	fw := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		rest, ok := strings.CutPrefix(p, "github.com/megakuul/bob/pkg/")
		if !ok {
			continue
		}
		base := path.Base(rest)
		local := base
		if imp.Name != nil {
			local = imp.Name.Name
		}
		fw[local] = rest
		if rest == "nixpkgs/nixext" {
			out := Problem{File: file, Line: fset.Position(imp.Pos()).Line, Rule: "R-NIX-031",
				Msg: "imports pkg/nixpkgs/nixext, the extension API of bob's ecosystem packages; use the typed handles of pkg/nixpkgs"}
			return []Problem{out}
		}
	}
	var out []Problem
	add := func(n ast.Node, rule, format string, a ...any) {
		out = append(out, Problem{File: file, Line: fset.Position(n.Pos()).Line, Rule: rule, Msg: fmt.Sprintf(format, a...)})
	}
	units := map[string][]int{} // framework package → lines of unit constructor calls
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, isPkg := sel.X.(*ast.Ident)
		fwPkg := ""
		if isPkg {
			fwPkg = fw[pkgIdent.Name]
		}
		name := sel.Sel.Name
		switch {
		case fwPkg == "bob" && sourceFuncs[name], (fwPkg == "cc" || fwPkg == "rust") && name == "IncludeDir":
			for _, a := range call.Args {
				s, ok := stringArg(a)
				if !ok {
					continue
				}
				if msg := localPath(dir, s, pkgDirs); msg != "" {
					add(a, "R-FW-070", "%s.%s(%q): %s", pkgIdent.Name, name, s, msg)
				}
			}
		case fwPkg == "nixpkgs" && nixRoles[name]:
			for _, a := range call.Args {
				if s, ok := stringArg(a); ok {
					add(a, "R-NIX-031", "nixpkgs.%s names the attribute %q as a string; use the typed handle (nixpkgs.<Name>.%s(p))", name, s, name)
					break
				}
			}
		case !isPkg && goDirMethods[name] && len(call.Args) > 0:
			if s, ok := stringArg(call.Args[0]); ok && (strings.HasPrefix(s, ".") || strings.Contains(s, "/")) {
				add(call.Args[0], "R-FW-070", ".%s(%q) names the Go package of another directory; declare it in that directory's own bob.go", name, s)
			}
		}
		if isPkg && fwPkg != "" {
			for _, u := range unit[path.Base(fwPkg)] {
				if u == name {
					units[fwPkg] = append(units[fwPkg], fset.Position(call.Pos()).Line)
				}
			}
		}
		if !isPkg && fw["golang"] != "" {
			for _, u := range unit["golang"] {
				if u == "."+name && isModuleCall(sel.X) {
					units["golang"] = append(units["golang"], fset.Position(call.Pos()).Line)
				}
			}
		}
		return true
	})
	for pkg, lines := range units {
		if len(lines) > 1 {
			rule := "R-FW-071"
			if pkg == "cc" {
				rule = "R-CC-006"
			}
			out = append(out, Problem{File: file, Line: lines[1], Rule: rule,
				Msg: fmt.Sprintf("%d %s build units in one package directory (lines %v); one library or binary per directory, give the others their own directory and bob.go", len(lines), pkg, lines)})
		}
	}
	return out
}

// isModuleCall reports whether x is a call such as Module(p) or acme.Module(p): the golang
// module handle on which Binary/CArchive/CShared are called.
func isModuleCall(x ast.Expr) bool {
	c, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return strings.HasSuffix(f.Name, "Module")
	case *ast.SelectorExpr:
		return strings.HasSuffix(f.Sel.Name, "Module")
	}
	return false
}

func stringArg(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// localPath explains why the source pattern s of the bob.go in dir is not inside its package
// directory, or returns "". A leading "!" (exclusion) is ignored; patterns are checked up to
// their first wildcard.
func localPath(dir, s string, pkgDirs map[string]bool) string {
	s = strings.TrimPrefix(s, "!")
	if s == "" {
		return ""
	}
	if path.IsAbs(s) {
		return "absolute paths are not sources of a package; sources are relative to the bob.go directory"
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "leaves the package directory; each bob.go builds only files below its own directory (R-FW-070)"
		}
	}
	// The literal prefix before the first wildcard.
	lit := s
	if i := strings.IndexAny(s, "*?["); i >= 0 {
		lit = s[:i]
	}
	segs := strings.Split(path.Clean(lit), "/")
	if !strings.HasSuffix(lit, "/") && strings.ContainsAny(s, "*?[") {
		segs = segs[:len(segs)-1] // the last segment holds the wildcard
	} else if !strings.ContainsAny(s, "*?[") && path.Ext(lit) != "" {
		segs = segs[:len(segs)-1] // a file name
	}
	cur := dir
	for _, seg := range segs {
		if seg == "." || seg == "" {
			continue
		}
		if cur == "." {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		if pkgDirs[cur] {
			return fmt.Sprintf("is inside %s/, which has its own bob.go; use the handles of that package instead of its files", cur)
		}
	}
	return ""
}
