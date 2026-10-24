// Command bobeval is the spike S1 evaluator: it finds every bob.go of a repo, generates one
// evaluator main package plus a go-command overlay, builds it with `-tags bob` and runs it.
//
//	bobeval [-root DIR] [-check] [-o graph.json] [-timing]
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type defFile struct {
	dir     string // relative, slash-separated ("" = root)
	pkgName string // package clause of bob.go
	others  []string
	sibling string // package name of the other non-test Go files ("" if none)
	imports []string
	names   map[string]int    // top-level identifiers of bob.go -> line
	clashes map[string]string // identifier -> file:line in the directory's Go code
	foreign []string
}

// Files the go command would try to compile into the package (and which therefore must be
// hidden from the evaluator; the C-family ones also break `go list` in gopls).
var goToolExts = map[string]bool{".go": true, ".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".m": true,
	".f": true, ".F": true, ".for": true, ".f90": true, ".s": true, ".S": true, ".sx": true, ".swig": true, ".swigcxx": true, ".syso": true}
var editorBreaking = map[string]bool{".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".m": true, ".f": true, ".F": true, ".for": true, ".f90": true, ".swig": true, ".swigcxx": true}

func main() {
	root := flag.String("root", ".", "repo root (directory with the bob-generated go.mod)")
	out := flag.String("o", "", "write graph JSON here (default stdout)")
	check := flag.Bool("check", false, "fail on layouts that break gopls (C sources next to bob.go, package-name mismatch)")
	timing := flag.Bool("timing", false, "print phase timings to stderr")
	flag.Parse()
	if err := run(*root, *out, *check, *timing); err != nil {
		fmt.Fprintln(os.Stderr, "bob:", err)
		os.Exit(1)
	}
}

func run(root, out string, check, timing bool) error {
	t0 := time.Now()
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	modPath, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	defs, err := scan(root)
	if err != nil {
		return err
	}
	var problems []string
	for _, d := range defs {
		if d.sibling != "" && d.sibling != d.pkgName {
			problems = append(problems, fmt.Sprintf("%s: package %s, but the Go code in this directory is package %s; bob.go must use package %s", filepath.Join(d.dir, "bob.go"), d.pkgName, d.sibling, d.sibling))
		}
		if len(d.foreign) > 0 && check {
			problems = append(problems, fmt.Sprintf("%s: %s next to bob.go make gopls report \"C source files not allowed\"; move them into a subdirectory (e.g. src/)", d.dir+"/", strings.Join(d.foreign, ", ")))
		}
	}
	mainDirs := map[string]string{}
	for _, d := range defs {
		if d.pkgName == "main" {
			mainDirs[joinImport(modPath, d.dir)] = d.dir
		}
	}
	for _, d := range defs {
		for _, imp := range d.imports {
			if md, ok := mainDirs[imp]; ok {
				problems = append(problems, fmt.Sprintf("%s: imports the build definitions of %s/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory", path.Join(d.dir, "bob.go"), md))
			}
		}
		for id, at := range d.clashes {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is also declared at %s; bob.go shares its package with that code, rename one of them", path.Join(d.dir, "bob.go"), d.names[id], id, at))
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	t1 := time.Now()

	evalDir := filepath.Join(root, ".bob", "eval")
	if err := os.MkdirAll(filepath.Join(evalDir, "rename"), 0o755); err != nil {
		return err
	}
	replace := map[string]string{}
	var imports []string
	var renames []string // compiler errors name the overlay copy; map them back
	for i, d := range defs {
		abs := filepath.Join(root, filepath.FromSlash(d.dir))
		for _, o := range d.others {
			replace[filepath.Join(abs, o)] = "" // hide real code: the evaluator compiles bob.go only
		}
		imp := modPath
		if d.dir != "" {
			imp += "/" + d.dir
		}
		if d.pkgName == "main" {
			// a main package cannot be imported: give the evaluator a copy with another
			// package name; line numbers stay identical, and positions keep the original path
			src, err := os.ReadFile(filepath.Join(abs, "bob.go"))
			if err != nil {
				return err
			}
			renamed, err := renamePackage(src, fmt.Sprintf("bobmain%d", i))
			if err != nil {
				return fmt.Errorf("%s/bob.go: %w", d.dir, err)
			}
			cp := filepath.Join(evalDir, "rename", fmt.Sprintf("%d.go", i))
			if err := writeIfChanged(cp, renamed); err != nil {
				return err
			}
			replace[filepath.Join(abs, "bob.go")] = cp
			renames = append(renames, "./"+filepath.ToSlash(mustRel(root, cp)), d.dir+"/bob.go")
		}
		imports = append(imports, imp)
	}
	// bob.go files of other bob repos (R-FW-022, imported as Go modules): hide their real code
	// too. They are compiled only if a bob.go of this repo imports them.
	depDirs, err := depModuleDirs(root)
	if err != nil {
		return err
	}
	// The go command refuses overlays below GOMODCACHE, so such modules are copied (go.mod +
	// bob.go files only) and replaced through a generated -modfile.
	var modReplaces []string
	for _, md := range depDirs {
		ds, err := scan(md)
		if err != nil {
			return err
		}
		if len(ds) == 0 {
			continue
		}
		mp, err := modulePath(filepath.Join(md, "go.mod"))
		if err != nil {
			return err
		}
		cp := filepath.Join(evalDir, "mods", strings.ReplaceAll(mp, "/", "!"))
		os.RemoveAll(cp)
		if err := copyFile(filepath.Join(md, "go.mod"), filepath.Join(cp, "go.mod")); err != nil {
			return err
		}
		for _, d := range ds {
			if err := copyFile(filepath.Join(md, filepath.FromSlash(d.dir), "bob.go"), filepath.Join(cp, filepath.FromSlash(d.dir), "bob.go")); err != nil {
				return err
			}
		}
		modReplaces = append(modReplaces, fmt.Sprintf("replace %s => %s", mp, cp))
	}
	modfile := filepath.Join(root, "go.mod")
	if len(modReplaces) > 0 {
		gm, err := os.ReadFile(modfile)
		if err != nil {
			return err
		}
		gm = append(gm, []byte("\n// added by bob: dependency bob.go files only\n"+strings.Join(modReplaces, "\n")+"\n")...)
		modfile = filepath.Join(evalDir, "go.mod")
		if err := writeIfChanged(modfile, gm); err != nil {
			return err
		}
		if sum, err := os.ReadFile(filepath.Join(root, "go.sum")); err == nil {
			if err := writeIfChanged(filepath.Join(evalDir, "go.sum"), sum); err != nil {
				return err
			}
		}
	}
	var mainSrc bytes.Buffer
	mainSrc.WriteString("//go:build bob\n\n// Code generated by bob. DO NOT EDIT.\npackage main\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n")
	for _, imp := range imports {
		fmt.Fprintf(&mainSrc, "\t_ %q\n", imp)
	}
	fmt.Fprintf(&mainSrc, ")\n\nfunc main() {\n\tbob.MainModule = %q\n\tbob.Main()\n}\n", modPath)
	if err := writeIfChanged(filepath.Join(evalDir, "main.go"), mainSrc.Bytes()); err != nil {
		return err
	}
	// the evaluator main is a virtual package of the main module (overlay only, nothing in the tree)
	replace[filepath.Join(root, "_bobeval", "main.go")] = filepath.Join(evalDir, "main.go")
	ov, _ := json.MarshalIndent(map[string]any{"Replace": replace}, "", " ")
	if err := writeIfChanged(filepath.Join(evalDir, "overlay.json"), ov); err != nil {
		return err
	}
	t2 := time.Now()

	bin := filepath.Join(evalDir, "bob-eval")
	cmd := exec.Command("go", "build", "-tags=bob", "-trimpath", "-modfile="+modfile, "-overlay="+filepath.Join(evalDir, "overlay.json"), "-o", bin, "./_bobeval")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compiling build definitions failed:\n%s", strings.NewReplacer(renames...).Replace(stderr.String()))
	}
	t3 := time.Now()

	run := exec.Command(bin)
	run.Dir = root
	run.Stderr = os.Stderr
	graph, err := run.Output()
	if err != nil {
		return fmt.Errorf("evaluating build definitions failed: %w", err)
	}
	t4 := time.Now()
	if out == "" {
		os.Stdout.Write(graph)
	} else if err := os.WriteFile(out, graph, 0o644); err != nil {
		return err
	}
	if timing {
		fmt.Fprintf(os.Stderr, "bobeval: files=%d scan=%.1fms gen=%.1fms build=%.1fms run=%.1fms total=%.1fms\n", len(defs),
			ms(t1.Sub(t0)), ms(t2.Sub(t1)), ms(t3.Sub(t2)), ms(t4.Sub(t3)), ms(t4.Sub(t0)))
	}
	return nil
}

func depModuleDirs(root string) ([]string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{if not .Main}}{{.Dir}}{{end}}", "all")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m all: %w", err)
	}
	var dirs []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dirs = append(dirs, l)
		}
	}
	return dirs, nil
}

func joinImport(mod, dir string) string {
	if dir == "" {
		return mod
	}
	return mod + "/" + dir
}

func topNames(fset *token.FileSet, f *ast.File) map[string]int {
	out := map[string]int{}
	add := func(id *ast.Ident) {
		if id != nil && id.Name != "_" {
			out[id.Name] = fset.Position(id.Pos()).Line
		}
	}
	for _, decl := range f.Decls {
		switch x := decl.(type) {
		case *ast.FuncDecl:
			if x.Recv == nil && x.Name.Name != "init" {
				add(x.Name)
			}
		case *ast.GenDecl:
			for _, sp := range x.Specs {
				switch y := sp.(type) {
				case *ast.ValueSpec:
					for _, n := range y.Names {
						add(n)
					}
				case *ast.TypeSpec:
					add(y.Name)
				}
			}
		}
	}
	return out
}

func mustRel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return os.WriteFile(path, data, 0o644)
}

func modulePath(gomod string) (string, error) {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", fmt.Errorf("%s: no module line", gomod)
}

func renamePackage(src []byte, name string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bob.go", src, parser.PackageClauseOnly)
	if err != nil {
		return nil, err
	}
	off := fset.Position(f.Name.Pos()).Offset
	end := off + len(f.Name.Name)
	return append(append(append([]byte{}, src[:off]...), name...), src[end:]...), nil
}

func scan(root string) ([]defFile, error) {
	var defs []defFile
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			return nil
		}
		n := e.Name()
		if path != root && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "node_modules" || n == "testdata") {
			return filepath.SkipDir
		}
		if path != root {
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir // nested module: not part of this evaluation (spike)
			}
		}
		bobgo := filepath.Join(path, "bob.go")
		if _, err := os.Stat(bobgo); err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			rel = ""
		}
		d, err := inspect(path, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		defs = append(defs, d)
		return nil
	})
	sort.Slice(defs, func(i, j int) bool { return defs[i].dir < defs[j].dir })
	return defs, err
}

func inspect(abs, rel string) (defFile, error) {
	d := defFile{dir: rel}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(abs, "bob.go"), nil, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return d, err
	}
	tagged := false
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				x, err := constraint.Parse(c.Text)
				if err == nil && x.String() == "bob" {
					tagged = true
				}
			}
		}
	}
	if !tagged && os.Getenv("BOB_TREE") == "" { // fallback layout (bob/ module): no tag needed
		return d, fmt.Errorf("%s/bob.go: the first line must be \"//go:build bob\" so that go build, go vet and go test ignore it", rel)
	}
	d.pkgName = f.Name.Name
	for _, im := range f.Imports {
		d.imports = append(d.imports, strings.Trim(im.Path.Value, `"`))
	}
	d.names = topNames(fset, f)
	d.clashes = map[string]string{}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return d, err
	}
	for _, e := range ents {
		n := e.Name()
		ext := filepath.Ext(n)
		if e.IsDir() || n == "bob.go" || !goToolExts[ext] || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		d.others = append(d.others, n)
		if editorBreaking[ext] {
			d.foreign = append(d.foreign, n)
		}
		if ext == ".go" && !strings.HasSuffix(n, "_test.go") {
			if g, err := parser.ParseFile(fset, filepath.Join(abs, n), nil, parser.SkipObjectResolution); err == nil {
				for id, line := range topNames(fset, g) {
					if _, ok := d.names[id]; ok {
						d.clashes[id] = fmt.Sprintf("%s:%d", path.Join(rel, n), line)
					}
				}
			}
		}
		if ext == ".go" && !strings.HasSuffix(n, "_test.go") && d.sibling == "" {
			g, err := parser.ParseFile(fset, filepath.Join(abs, n), nil, parser.SkipObjectResolution)
			if err == nil {
				d.sibling = g.Name.Name
			}
		}
	}
	return d, nil
}
