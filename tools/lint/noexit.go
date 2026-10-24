package lint

import (
	"go/ast"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/analysis"
)

// NoExit allows os.Exit (and syscall.Exit, unix.Exit) only inside func main of
// cmd/*/main.go, so deferred cleanup everywhere else always runs (AGENTS.md §5.3).
var NoExit = &analysis.Analyzer{
	Name: "noexit",
	Doc:  "os.Exit, syscall.Exit and unix.Exit only inside func main of cmd/*/main.go (and TestMain); syscall.Exit also in the listed init-hook files",
	Run:  runNoExit,
}

// exitPkgs are the packages whose Exit function the analyzer checks.
var exitPkgs = map[string]bool{"os": true, "syscall": true, "golang.org/x/sys/unix": true}

// syscallExitFiles are the init-hook files that may call syscall.Exit (package path → file
// name): code that runs when bob re-executes itself as a helper process, before any bob code
// that could have cleanup pending (A70, decisions.md).
var syscallExitFiles = map[string]string{
	Module + "/pkg/engine/store":        "ns.go",    // the user-namespace child (nsChild)
	Module + "/pkg/engine/eval/evalbox": "stage.go", // the evaluation sandbox stages
}

func runNoExit(pass *analysis.Pass) (any, error) {
	if !inScope(pass.Pkg.Path()) {
		return nil, nil
	}
	// TestMain may exit with m.Run's code after its own cleanup.
	mainPkg := pass.Pkg.Name() == "main" && within(pass.Pkg.Path(), Module+"/cmd")
	for _, f := range pass.Files {
		base := filepath.Base(pass.Fset.File(f.Pos()).Name())
		mainFile := mainPkg && base == "main.go"
		hookFile := syscallExitFiles[pass.Pkg.Path()] == base
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := calleeObj(pass, call).(*types.Func)
			if !ok || fn.Pkg() == nil || !exitPkgs[fn.Pkg().Path()] || fn.Name() != "Exit" {
				return true
			}
			if hookFile && fn.Pkg().Path() == "syscall" {
				return true
			}
			decl, _ := enclosingFunc(pass, f, call)
			if decl != nil && decl.Recv == nil &&
				(mainFile && decl.Name.Name == "main" || isTestFile(pass, f) && decl.Name.Name == "TestMain") {
				return true
			}
			pass.Reportf(call.Pos(), "%s.Exit outside func main of cmd/*/main.go (AGENTS.md §5.3)", fn.Pkg().Name())
			return true
		})
	}
	return nil, nil
}
