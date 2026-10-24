package lint

import (
	"go/ast"
	"path"

	"golang.org/x/tools/go/analysis"
)

// SweepPkg is the only package that may LIST (R-STORE-100/101, D60).
const SweepPkg = Module + "/internal/store/sweep"

// listNames are the S3 LIST operations; they are matched by name on any
// receiver so every S3 client and wrapper is covered.
var listNames = map[string]bool{
	"ListObjects": true, "ListObjectsV2": true, "NewListObjectsV2Paginator": true,
	"ListObjectVersions": true, "ListMultipartUploads": true,
}

// NoList keeps LIST requests and dynamitedb.Query in internal/store/sweep.
var NoList = &analysis.Analyzer{
	Name: "nolist",
	Doc:  "S3 LIST operations and dynamitedb.Query only in internal/store/sweep",
	Run:  runNoList,
}

func runNoList(pass *analysis.Pass) (any, error) {
	if !inScope(pass.Pkg.Path()) || within(pass.Pkg.Path(), SweepPkg) {
		return nil, nil
	}
	for _, f := range pass.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := sel.Sel.Name
			if listNames[name] {
				pass.Reportf(sel.Sel.Pos(), "%s outside %s: control and fleet state is GET-only (D60, R-STORE-100)", name, SweepPkg)
			}
			if obj := pass.TypesInfo.Uses[sel.Sel]; name == "Query" && obj != nil && obj.Pkg() != nil && path.Base(obj.Pkg().Path()) == "dynamitedb" {
				pass.Reportf(sel.Sel.Pos(), "dynamitedb.Query outside %s (D60, R-STORE-100)", SweepPkg)
			}
			return true
		})
	}
	return nil, nil
}
