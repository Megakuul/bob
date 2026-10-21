package lint

import (
	"go/ast"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/analysis"
)

// NoExit allows os.Exit only inside func main of cmd/*/main.go, so deferred
// cleanup everywhere else always runs (AGENTS.md §5.3).
var NoExit = &analysis.Analyzer{
	Name: "noexit",
	Doc:  "os.Exit only inside func main of cmd/*/main.go (and TestMain)",
	Run:  runNoExit,
}

func runNoExit(pass *analysis.Pass) (any, error) {
	if !inScope(pass.Pkg.Path()) {
		return nil, nil
	}
	// TestMain may exit with m.Run's code after its own cleanup.
	mainPkg := pass.Pkg.Name() == "main" && within(pass.Pkg.Path(), Module+"/cmd")
	for _, f := range pass.Files {
		mainFile := mainPkg && filepath.Base(pass.Fset.File(f.Pos()).Name()) == "main.go"
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := calleeObj(pass, call).(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "os" || fn.Name() != "Exit" {
				return true
			}
			decl, _ := enclosingFunc(pass, f, call)
			if decl != nil && decl.Recv == nil &&
				(mainFile && decl.Name.Name == "main" || isTestFile(pass, f) && decl.Name.Name == "TestMain") {
				return true
			}
			pass.Reportf(call.Pos(), "os.Exit outside func main of cmd/*/main.go (AGENTS.md §5.3)")
			return true
		})
	}
	return nil, nil
}
