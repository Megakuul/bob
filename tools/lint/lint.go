// Package lint holds bob's go/analysis analyzers (HARNESS.md §1.5). They
// enforce the never-crash rules (AGENTS.md §5.3), the LIST-only-in-sweep
// rule (D60) and the direct import boundaries (R-ARCH-002, R-STORE-005).
// The layer table of R-ARCH-003 lives in pkg/layering_test.go.
package lint

import (
	_ "embed"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Module is bob's module path. Analyzers only judge shipped code: packages
// of this module outside the harness trees tools/ and tests/.
const Module = "github.com/megakuul/bob"

// Analyzers is every analyzer boblint runs.
var Analyzers = []*analysis.Analyzer{NoPanic, NoExit, NoList, ImportBoundary, SafeGo, DBUsage}

//go:embed allow.txt
var allowTxt string

// allowed reports whether allow.txt lists fn ("pkgpath.Func") for analyzer.
func allowed(analyzer, fn string) bool {
	for _, line := range strings.Split(allowTxt, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && f[0] == analyzer && f[1] == fn {
			return true
		}
	}
	return false
}

func inScope(pkgPath string) bool {
	// Generated test mains (".test") call os.Exit by design.
	return within(pkgPath, Module) && !within(pkgPath, Module+"/tools") && !within(pkgPath, Module+"/tests") && !strings.HasSuffix(pkgPath, ".test")
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// isTestFile skips _test.go files: tests fail through testing.T, and test
// helpers may panic on broken fixtures.
func isTestFile(pass *analysis.Pass, f *ast.File) bool {
	return strings.HasSuffix(pass.Fset.File(f.Pos()).Name(), "_test.go")
}

// enclosingFunc returns "pkgpath.Func" (or "pkgpath.Type.Method") for the
// declaration containing pos, or "" at package level.
func enclosingFunc(pass *analysis.Pass, f *ast.File, n ast.Node) (*ast.FuncDecl, string) {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || n.Pos() < fd.Pos() || n.End() > fd.End() {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			t := fd.Recv.List[0].Type
			if s, ok := t.(*ast.StarExpr); ok {
				t = s.X
			}
			if id, ok := t.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		return fd, pass.Pkg.Path() + "." + name
	}
	return nil, ""
}

// calleeObj returns the package-level function or builtin a call targets.
func calleeObj(pass *analysis.Pass, call *ast.CallExpr) types.Object {
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return pass.TypesInfo.Uses[fn]
	case *ast.SelectorExpr:
		return pass.TypesInfo.Uses[fn.Sel]
	}
	return nil
}
