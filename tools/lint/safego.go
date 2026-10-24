package lint

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
)

// SafeGo forbids bare `go` statements in internal/ and cmd/ (AGENTS.md
// §5.3: recover at every goroutine boundary). Goroutines start through
// internal/safe.Go, which recovers and converts a panic to an error; that
// package is the only place allowed to contain a `go` statement.
var SafeGo = &analysis.Analyzer{
	Name: "safego",
	Doc:  "no bare go statement in internal/ or cmd/; use internal/safe.Go",
	Run:  runSafeGo,
}

func runSafeGo(pass *analysis.Pass) (any, error) {
	p := pass.Pkg.Path()
	if !inScope(p) || !(within(p, Module+"/internal") || within(p, Module+"/cmd")) || within(p, Module+"/internal/safe") {
		return nil, nil
	}
	for _, f := range pass.Files {
		if isTestFile(pass, f) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			g, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			if _, fn := enclosingFunc(pass, f, g); fn != "" && allowed("safego", fn) {
				return true
			}
			pass.Reportf(g.Pos(), "bare go statement: start goroutines with internal/safe.Go, which recovers panics (AGENTS.md §5.3)")
			return true
		})
	}
	return nil, nil
}
