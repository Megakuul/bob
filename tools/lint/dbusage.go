package lint

import (
	_ "embed"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Library import paths governed by the dbusage rule (docs/dynamitedb-lake-usage.md).
const (
	dynamitedbPath = "github.com/megakuul/dynamitedb"
	lakePath       = "github.com/megakuul/lake"
	s3SDKPath      = "github.com/aws/aws-sdk-go-v2/service/s3"
)

// importAllow lists, per governed library, the packages (and their subtrees)
// that may import it. _test.go files are exempt from the import rule.
var importAllow = map[string][]string{
	dynamitedbPath: {Module + "/internal/store"},
	lakePath:       {Module + "/internal/logstore"},
	// Artifact and staging objects, the store probe/sweep, and the
	// orchestrator's fsck/staging controllers talk S3 directly; never to
	// reimplement dynamitedb records.
	s3SDKPath: {
		Module + "/internal/store", Module + "/internal/cache", Module + "/internal/promote",
		Module + "/internal/orchestrator", Module + "/internal/worker", Module + "/internal/logstore",
	},
}

// filterOps are dynamitedb filter operators (Get/Query/Delete values); they
// panic inside Create/Put/Update.
var filterOps = nameSet("Eq", "NotEq", "In", "NotIn", "Includes", "Contains", "Has", "Before", "After",
	"GreaterThan", "GreaterOrEqThan", "LessThan", "LessOrEqThan", "CustomFilter")

// updateOps are dynamitedb update operators (Create/Put/Update values); they
// panic inside Get/Query/Delete.
var updateOps = nameSet("Set", "Increment", "Multiply", "Toggle", "Add", "Append", "Remove", "Emplace", "CustomUpdate")

func nameSet(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// writeCalls take an update value; readCalls take a filter value. Both take
// the value as third argument (ctx, bucket, value).
var (
	writeCalls = nameSet("Create", "Put", "Update")
	readCalls  = nameSet("Get", "Query", "Delete")
)

// dbusageTodo is the temporary allowlist of known findings
// (tools/lint/dbusage.todo). Format: "<import|call> <repo-relative file> <reason>".
// Matching findings are printed to stderr as "expected" instead of failing.
//
//go:embed dbusage.todo
var dbusageTodo string

// DBUsage enforces docs/dynamitedb-lake-usage.md.
var DBUsage = &analysis.Analyzer{
	Name: "dbusage",
	Doc:  "dynamitedb/lake/S3 import restrictions, fresh update/filter values for dynamitedb calls, Query only in sweep",
	Run:  runDBUsage,
}

func todoListed(kind, filename string) bool {
	filename = strings.ReplaceAll(filename, "\\", "/")
	for _, line := range strings.Split(dbusageTodo, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && !strings.HasPrefix(f[0], "#") && f[0] == kind && strings.HasSuffix(filename, "/"+f[1]) {
			return true
		}
	}
	return false
}

func runDBUsage(pass *analysis.Pass) (any, error) {
	pkg := pass.Pkg.Path()
	if !inScope(pkg) {
		return nil, nil
	}
	report := func(kind string, pos token.Pos, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		p := pass.Fset.Position(pos)
		if todoListed(kind, p.Filename) {
			fmt.Fprintf(os.Stderr, "%s: dbusage: expected (dbusage.todo): %s\n", p, msg)
			return
		}
		pass.Report(analysis.Diagnostic{Pos: pos, Message: msg})
	}
	for _, f := range pass.Files {
		test := isTestFile(pass, f)
		if !test {
			for _, imp := range f.Imports {
				to, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				lib := governed(to)
				if lib == "" {
					continue
				}
				ok := false
				for _, dir := range importAllow[lib] {
					ok = ok || within(pkg, dir)
				}
				if !ok {
					report("import", imp.Pos(), "import of %s is not allowed in %s (docs/dynamitedb-lake-usage.md)", to, pkg)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := dynamitedbFunc(pass, call)
			if name == "Query" && !test && !within(pkg, SweepPkg) {
				report("call", call.Pos(), "dynamitedb.Query outside %s (D60)", SweepPkg)
			}
			if (writeCalls[name] || readCalls[name]) && len(call.Args) >= 3 {
				checkValue(pass, report, name, call.Args[2])
			}
			return true
		})
	}
	return nil, nil
}

// governed returns the governed library path that import path to belongs to.
func governed(to string) string {
	for lib := range importAllow {
		if within(to, lib) {
			return lib
		}
	}
	return ""
}

// dynamitedbFunc returns the name of the dynamitedb package function call
// targets, or "".
func dynamitedbFunc(pass *analysis.Pass, call *ast.CallExpr) string {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) { // explicit instantiation: Update[T](...)
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	var id *ast.Ident
	switch x := ast.Unparen(fun).(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return ""
	}
	fn, ok := pass.TypesInfo.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != dynamitedbPath {
		return ""
	}
	if sig, ok := fn.Type().(*types.Signature); !ok || sig.Recv() != nil {
		return ""
	}
	return fn.Name()
}

// checkValue judges the value argument of a dynamitedb call. A fresh value is
// a composite literal (&T{...}) or the result of a call (a builder function
// such as func(id string) *JobRecord); a variable could hold a fetched record,
// which panics in writes ("incorrect DataField usage").
func checkValue(pass *analysis.Pass, report func(string, token.Pos, string, ...any), name string, arg ast.Expr) {
	write := writeCalls[name]
	bad, what := filterOps, "filter"
	if !write {
		bad, what = updateOps, "update"
	}
	e := ast.Unparen(arg)
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = ast.Unparen(u.X)
	}
	switch e.(type) {
	case *ast.CompositeLit, *ast.CallExpr:
	default:
		if write {
			report("call", arg.Pos(), "dynamitedb.%s takes a fresh composite literal (&T{...}), not a variable that may hold a fetched record", name)
		}
		return
	}
	ast.Inspect(e, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if op := dynamitedbFunc(pass, c); bad[op] {
			report("call", c.Pos(), "%s operator dynamitedb.%s in dynamitedb.%s: %s values take %s operators only (panics at run time)", what, op, name, wantKind(write), wantKind(write))
		}
		return true
	})
}

func wantKind(write bool) string {
	if write {
		return "update"
	}
	return "filter"
}
