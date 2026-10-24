package eval

import (
	"go/ast"
	"go/token"
	"sort"
	"strconv"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// Cross-repo use (R-FW-022): bob.Import("<module>", "<version>") in a bob.go names a dependency
// bob repository. The root go.mod that records it is generated from the IR, so it exists only
// after the first evaluation: the evaluator generation reads the calls from the bob.go files
// themselves and treats them as requirements of the root go.mod (bob.go wins over go.mod).

// addBobImports adds the bob.Import calls of the repository's bob.go files to rootMod's
// requirements. String literals only; when several calls import one module, the highest
// version wins. Malformed calls are definition errors.
func addBobImports(rootMod *goMod, own map[string]*bobFile) error {
	found := map[string]string{}
	var errs ir.Errors
	dirs := make([]string, 0, len(own))
	for d := range own {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		errs = append(errs, bobImportsOf(own[d], found)...)
	}
	if len(errs) > 0 {
		return errs
	}
	for mp, v := range found {
		rootMod.require[mp] = v
	}
	return nil
}

// bobImportsOf adds the bob.Import calls of f to found.
func bobImportsOf(f *bobFile, found map[string]string) ir.Errors {
	if f.file == nil {
		return nil // the rule check reports the syntax error
	}
	name := ""
	for _, is := range f.file.Imports {
		if p, _ := strconv.Unquote(is.Path.Value); p == frameworkModule+"/pkg/bob" {
			name = "bob"
			if is.Name != nil {
				name = is.Name.Name
			}
		}
	}
	if name == "" || name == "_" || name == "." {
		return nil
	}
	var errs ir.Errors
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Import" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != name {
			return true
		}
		pos := f.fset.Position(call.Pos())
		bad := func(msg string) {
			errs = append(errs, ir.DefinitionError(f.shown, pos.Line, pos.Column, "", msg))
		}
		lits := make([]string, 0, 2)
		for _, a := range call.Args {
			if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil {
					lits = append(lits, s)
				}
			}
		}
		if len(call.Args) != 2 || len(lits) != 2 {
			bad("bob.Import takes the module path and version as string literals")
			return false
		}
		mp, v := lits[0], lits[1]
		if mp == frameworkModule {
			bad("bob.Import cannot import the bob framework; bob requires it itself")
			return false
		}
		if err := checkModulePath(mp, false); err != nil {
			bad("bob.Import: " + err.Error())
			return false
		}
		if err := checkVersion(mp, v); err != nil {
			bad("bob.Import: " + err.Error())
			return false
		}
		if old, ok := found[mp]; !ok || semverLess(old, v) {
			found[mp] = v
		}
		return false
	})
	return errs
}
