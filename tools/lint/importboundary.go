package lint

import (
	"path"
	"strconv"

	"golang.org/x/tools/go/analysis"
)

// ImportBoundary reports direct imports that cross a structural boundary,
// with file and line:
//   - the framework (pkg/, cmd/bob, cmd/bob-builder) never imports server
//     code (R-ARCH-002, AGENTS.md §5.6);
//   - only internal/store imports dynamitedb (R-STORE-005).
//
// TestLayering (pkg/layering_test.go) checks the same server boundary
// transitively and the full R-ARCH-003 layer table.
var ImportBoundary = &analysis.Analyzer{
	Name: "importboundary",
	Doc:  "framework never imports server code; only internal/store imports dynamitedb",
	Run:  runImportBoundary,
}

// serverPrefixes are the packages the framework must not depend on.
var serverPrefixes = []string{Module + "/internal", Module + "/proto", Module + "/cmd/bob-server"}

// IsFramework reports whether pkg is framework code (R-ARCH-002).
func IsFramework(pkg string) bool {
	return within(pkg, Module+"/pkg") || within(pkg, Module+"/cmd/bob") || within(pkg, Module+"/cmd/bob-builder")
}

// IsServer reports whether pkg is server code the framework may not import.
func IsServer(pkg string) bool {
	for _, p := range serverPrefixes {
		if within(pkg, p) {
			return true
		}
	}
	return false
}

func runImportBoundary(pass *analysis.Pass) (any, error) {
	from := pass.Pkg.Path()
	if !inScope(from) {
		return nil, nil
	}
	for _, f := range pass.Files {
		for _, imp := range f.Imports {
			to, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if IsFramework(from) && IsServer(to) {
				pass.Reportf(imp.Pos(), "framework package imports server code %s (R-ARCH-002)", to)
			}
			if path.Base(to) == "dynamitedb" && !within(to, Module) && !within(from, Module+"/internal/store") {
				pass.Reportf(imp.Pos(), "only internal/store imports dynamitedb; use the store package (R-STORE-005)")
			}
		}
	}
	return nil, nil
}
