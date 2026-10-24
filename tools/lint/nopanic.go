package lint

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// NoPanic forbids panic, log.Fatal* and log.Panic* outside package main
// under cmd/ (AGENTS.md §5.3). Errors are returned instead.
var NoPanic = &analysis.Analyzer{
	Name: "nopanic",
	Doc:  "no panic, log.Fatal* or log.Panic* outside package main under cmd/",
	Run:  runNoPanic,
}

func runNoPanic(pass *analysis.Pass) (any, error) {
	if !inScope(pass.Pkg.Path()) || (pass.Pkg.Name() == "main" && within(pass.Pkg.Path(), Module+"/cmd")) {
		return nil, nil
	}
	for _, f := range pass.Files {
		if isTestFile(pass, f) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			what := ""
			switch obj := calleeObj(pass, call).(type) {
			case *types.Builtin:
				if obj.Name() == "panic" {
					what = "panic"
				}
			case *types.Func:
				if obj.Pkg() != nil && obj.Pkg().Path() == "log" &&
					(strings.HasPrefix(obj.Name(), "Fatal") || strings.HasPrefix(obj.Name(), "Panic")) {
					what = "log." + obj.Name()
				}
			}
			if what == "" {
				return true
			}
			if _, fn := enclosingFunc(pass, f, call); fn != "" && allowed("nopanic", fn) {
				return true
			}
			pass.Reportf(call.Pos(), "%s outside package main: return an error instead (AGENTS.md §5.3)", what)
			return true
		})
	}
	return nil, nil
}
