package gen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// requirePos is the bob.go position of the requirement of module path mod that the go.module
// node n declares (R-FW-062: a stale-lock error names the changed declaration): the
// `<pkg>.Require("<mod>", …)` call inside n's constructor call, else its `Require:` option (mod
// "" or a removed requirement); the nearest one below the definition when the constructor call
// is not found. It falls back to n.DefinedAt when the file
// cannot be read (root "") or holds neither; the evaluator reports unreadable files.
func requirePos(root string, n *ir.Node, mod string) ir.Pos {
	at := n.DefinedAt
	if root == "" || at.File == "" {
		return at
	}
	file := filepath.Join(root, filepath.FromSlash(at.File))
	if info, err := os.Stat(file); err != nil || info.Size() > maxBobGoBytes {
		return at
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return at
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, at.File, src, parser.SkipObjectResolution)
	if err != nil {
		return at
	}
	// The search covers the module's constructor call (the definition's position), else the
	// whole file.
	var scope ast.Node = f
	ast.Inspect(f, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok && scope == ast.Node(f) {
			p := fset.Position(c.Pos())
			if p.Line == at.Line && (at.Col == 0 || p.Column == at.Col) {
				scope = c
				return false
			}
		}
		return scope == ast.Node(f)
	})
	call, key := 0, 0
	nearest := func(best *int, line int) {
		if line >= at.Line && (*best == 0 || line < *best) {
			*best = line
		}
	}
	ast.Inspect(scope, func(x ast.Node) bool {
		switch x := x.(type) {
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Require" || len(x.Args) == 0 || mod == "" {
				return true
			}
			if bl, ok := x.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil && s == mod {
					nearest(&call, fset.Position(x.Pos()).Line)
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && id.Name == "Require" {
				nearest(&key, fset.Position(x.Pos()).Line)
			}
		}
		return true
	})
	switch {
	case call != 0:
		at.Line, at.Col = call, 0
	case key != 0:
		at.Line, at.Col = key, 0
	}
	return at
}
