package gen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"
)

// The repository's root go.mod belongs to the module of the bob.go files (§03.2.5: "the
// build-definition requirements §01 contributes", R-FW-022). bob generates it like the Go
// modules' go.mod: the framework at bob's version and every bob.Import of the bob.go files.

const (
	// frameworkModule is bob's own module, which bob.go files import.
	frameworkModule = "github.com/megakuul/bob"
	// frameworkGo is the language version of the framework module (bob's go.mod; the
	// evaluator compiles bob.go with it). A test keeps it equal to bob's go.mod.
	frameworkGo = "1.26"
	// Scan bounds (R-FW-002 caps).
	maxBobFiles   = 10_000
	maxBobGoBytes = 1 << 20
)

// FrameworkDir is the repository-relative directory bob keeps the framework module's source in
// (A550): the root go.mod replaces github.com/megakuul/bob by it, so the go command and gopls
// resolve the bob.go imports with no proxy, no network and no bob shim. Not committed.
const FrameworkDir = ".bob/framework"

// FrameworkReplace is the replace target in the root go.mod, seen from the repository root.
const FrameworkReplace = "./" + FrameworkDir

// FrameworkTree returns the files of the framework module (module-relative path → content, as
// the evaluator compiles against it) under FrameworkDir, when the root go.mod of graph g
// requires the framework; none for bob's own repository, which is the framework.
func FrameworkTree(g *ir.Graph, files map[string][]byte) []engine.GeneratedFile {
	if g == nil || g.Module == "" || g.Module == frameworkModule || !resolve.ValidVersion(g.BobVersion) {
		return nil
	}
	out := make([]engine.GeneratedFile, 0, len(files))
	for _, name := range sortedKeys(files) {
		out = append(out, engine.GeneratedFile{Path: FrameworkDir + "/" + name, Content: files[name], Mode: 0o444,
			Owner: "//:bob.go"})
	}
	return out
}

// RootGoMod returns the root go.mod of the repository at root for graph g: module g.Module,
// go <framework language>.0, and one requirement block with the framework at g.BobVersion
// (when it is a module version) and the modules of every bob.Import call. ok is false when g
// names no module.
func RootGoMod(g *ir.Graph, root string) (f engine.GeneratedFile, ok bool, err error) {
	if g == nil || g.Module == "" {
		return f, false, nil
	}
	imports, err := BobImports(root)
	if err != nil {
		return f, false, err
	}
	reqs := map[string]string{}
	for _, m := range imports {
		reqs[m.Path] = m.Version
	}
	if resolve.ValidVersion(g.BobVersion) {
		reqs[frameworkModule] = g.BobVersion
	}
	var b strings.Builder
	b.WriteString(engine.GoModHeader + "\n// Edit bob.go and run bob lock.\n")
	fmt.Fprintf(&b, "module %s\n\ngo %s\n", g.Module, goDirective(frameworkGo))
	if len(reqs) > 0 {
		b.WriteString("\nrequire (\n")
		for _, p := range sortedKeys(reqs) {
			fmt.Fprintf(&b, "\t%s %s\n", p, reqs[p])
		}
		b.WriteString(")\n")
	}
	if reqs[frameworkModule] != "" {
		fmt.Fprintf(&b, "\nreplace %s => %s\n", frameworkModule, FrameworkReplace)
	}
	return engine.GeneratedFile{Path: "go.mod", Content: []byte(b.String()), Mode: 0o444, Owner: "//:bob.go",
		Committed: true, Explain: explainRoot(g.Module, reqs)}, true, nil
}

// RootModule reports whether a Go module of the repository of g has its root at the repository
// root (a go.module node labelled "//:<name>"): its go.mod is then the root go.mod.
func RootModule(g *ir.Graph) bool {
	if g == nil {
		return false
	}
	for _, n := range g.Nodes {
		if n.Kind == gospec.KindModule && strings.HasPrefix(n.Label, "//:") {
			return true
		}
	}
	return false
}

// RootGoSum returns the root go.sum for graph g of the repository at root: the go.sum lines
// of the framework module (frameworkSum, as the evaluator writes them for bob's own version)
// when the root go.mod requires the framework at that version, and the lines of every
// bob.Import module the Go module cache holds (the evaluator downloads them there with the
// user's Go settings, which verified them; a module not downloaded yet gets its lines from the
// go command). ok is false when there is nothing to write.
func RootGoSum(g *ir.Graph, root, frameworkSum string) (f engine.GeneratedFile, ok bool) {
	if g == nil || g.Module == "" {
		return f, false
	}
	lines := frameworkSumLines(g.BobVersion, frameworkSum)
	if imports, err := BobImports(root); err == nil {
		lines = append(lines, importSumLines(root, imports)...)
	}
	if len(lines) == 0 {
		return f, false
	}
	sort.Strings(lines)
	sum := []byte(strings.Join(lines, "\n") + "\n")
	return engine.GeneratedFile{Path: "go.sum", Content: sum, Mode: 0o444, Owner: "//:bob.go", Committed: true,
		Explain: explainGoSum(sum)}, true
}

// frameworkSumLines are the lines of frameworkSum for the framework at version (none when
// version is not a module version).
func frameworkSumLines(version, frameworkSum string) []string {
	if !resolve.ValidVersion(version) {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(frameworkSum, "\n") {
		if fs := strings.Fields(l); len(fs) == 3 && fs[0] == frameworkModule && strings.TrimSuffix(fs[1], "/go.mod") == version {
			lines = append(lines, strings.Join(fs, " "))
		}
	}
	return lines
}

// bobRequirements are the requirements of the bob.go files and their go.sum lines.
type bobRequirements struct {
	reqs []gospec.Requirement
	sum  []string
}

// rootBobRequirements returns what the root go.mod needs for the bob.go files of the repository
// at root when a Go module of the repository (module path module) owns that go.mod: the
// framework at g.BobVersion, also for a source build (v0.0.0-dev, which bob env serves), and
// the bob.Import modules. bob's own repository is the framework module and does not require it.
func rootBobRequirements(g *ir.Graph, root, module, frameworkSum string) (bobRequirements, error) {
	var out bobRequirements
	imports, err := BobImports(root)
	if err != nil {
		return out, err
	}
	for _, m := range imports {
		out.reqs = append(out.reqs, gospec.Requirement{Path: m.Path, Version: m.Version})
	}
	out.sum = importSumLines(root, imports)
	if g != nil && module != frameworkModule && resolve.ValidVersion(g.BobVersion) {
		out.reqs = append(out.reqs, gospec.Requirement{Path: frameworkModule, Version: g.BobVersion})
		out.sum = append(out.sum, frameworkSumLines(g.BobVersion, frameworkSum)...)
	}
	return out, nil
}

// mergeSum adds lines to the go.sum content sum: sorted bytewise, each line once.
func mergeSum(sum []byte, lines []string) []byte {
	if len(lines) == 0 {
		return sum
	}
	all := slices.Concat(strings.Split(strings.TrimSpace(string(sum)), "\n"), lines)
	all = slices.DeleteFunc(all, func(l string) bool { return strings.TrimSpace(l) == "" })
	sort.Strings(all)
	all = slices.Compact(all)
	return []byte(strings.Join(all, "\n") + "\n")
}

// importSumLines are the go.sum lines of mods: from the Go module cache's download records
// (<v>.ziphash and <v>.mod) for the modules it holds, else the lines the root go.sum at root
// already has for them (another machine or bob home downloaded them), so the file does not
// change with the state of one module cache.
func importSumLines(root string, mods []resolve.Module) []string {
	dir := goModCache()
	have := map[string][]string{} // "<path> <version>" → its lines in the current go.sum
	if b, err := os.ReadFile(filepath.Join(root, "go.sum")); err == nil && len(b) <= maxBobGoBytes {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) == 3 && strings.HasPrefix(f[2], "h1:") {
				k := f[0] + " " + strings.TrimSuffix(f[1], "/go.mod")
				have[k] = append(have[k], strings.Join(f, " "))
			}
		}
	}
	var out []string
	for _, m := range mods {
		if dir != "" {
			base := filepath.Join(dir, "cache", "download", filepath.FromSlash(resolve.EscapePath(m.Path)), "@v", m.Version)
			zh, err1 := os.ReadFile(base + ".ziphash")
			mod, err2 := os.ReadFile(base + ".mod")
			h1 := strings.TrimSpace(string(zh))
			if err1 == nil && err2 == nil && strings.HasPrefix(h1, "h1:") && !strings.ContainsAny(h1, " \t\n") {
				out = append(out, m.Path+" "+m.Version+" "+h1, m.Path+" "+m.Version+"/go.mod "+resolve.ModH1(mod))
				continue
			}
		}
		out = append(out, have[m.Path+" "+m.Version]...)
	}
	return out
}

// goModCache is the go command's module cache directory: GOMODCACHE, else the first GOPATH
// entry's pkg/mod, else $HOME/go/pkg/mod (cmd/go's defaults).
func goModCache() string {
	if d := os.Getenv("GOMODCACHE"); d != "" {
		return d
	}
	if gp := filepath.SplitList(os.Getenv("GOPATH")); len(gp) > 0 && gp[0] != "" {
		return filepath.Join(gp[0], "pkg", "mod")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "go", "pkg", "mod")
	}
	return ""
}

// explainRoot maps an edited root go.mod to bob.go terms. Only what a user changed counts:
// another module line or requirements no bob.go declares. A go.mod written before bob (go mod
// init) or by an older bob lacks the framework and import lines; it is adopted, not drift.
func explainRoot(module string, want map[string]string) func([]byte) []engine.DriftLine {
	return func(onDisk []byte) []engine.DriftLine {
		got, err := resolve.ParseModFile(onDisk)
		if err != nil {
			return []engine.DriftLine{{Op: "~", Text: "go.mod cannot be read: " + err.Error()}}
		}
		var out []engine.DriftLine
		if got.Module != module {
			out = append(out, engine.DriftLine{Op: "~", Text: fmt.Sprintf("module %s → %s (the bob.go module is %s)", module, orNone(got.Module), module)})
		}
		for _, r := range got.Require {
			if _, ok := want[r.Path]; !ok {
				out = append(out, engine.DriftLine{Op: "+", Text: fmt.Sprintf("%s %s  (not declared in bob.go; use bob.Import)", r.Path, r.Version)})
			}
		}
		return out
	}
}

// BobImports returns the modules of every bob.Import("<module>", "<version>") call in the
// bob.go files of the repository at root (string literals only; the highest version wins when
// several files import one module). Directories are walked as the evaluator scans them: names
// starting with "." or "_", testdata, vendor and node_modules are skipped, and so are other Go
// modules (a go.mod bob did not generate) and nested repositories (their own bob.lock).
func BobImports(root string) ([]resolve.Module, error) {
	if root == "" {
		return nil, nil
	}
	found := map[string]string{}
	files := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			n := d.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "testdata" || n == "vendor" || n == "node_modules" {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "bob.lock")); err == nil {
				return filepath.SkipDir // a nested repository (R-FW-002 scan)
			}
			if gm, err := os.ReadFile(filepath.Join(p, "go.mod")); err == nil && !bytes.HasPrefix(gm, []byte(engine.GoModHeader)) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "bob.go" || !d.Type().IsRegular() {
			return nil
		}
		if files++; files > maxBobFiles {
			return fmt.Errorf("more than %d bob.go files", maxBobFiles)
		}
		rel, _ := filepath.Rel(root, p)
		return importsOf(p, filepath.ToSlash(rel), found)
	})
	if err != nil {
		return nil, err
	}
	out := make([]resolve.Module, 0, len(found))
	for _, p := range sortedKeys(found) {
		out = append(out, resolve.Module{Path: p, Version: found[p]})
	}
	return out, nil
}

// importsOf adds the bob.Import calls of one bob.go to found.
func importsOf(file, shown string, found map[string]string) error {
	info, err := os.Stat(file)
	if err != nil || info.Size() > maxBobGoBytes {
		return nil // the evaluator reports unreadable and oversized files
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, shown, src, parser.SkipObjectResolution)
	if err != nil {
		return nil // the evaluator reports syntax errors
	}
	name := ""
	for _, is := range f.Imports {
		if p, _ := strconv.Unquote(is.Path.Value); p == frameworkModule+"/pkg/bob" {
			name = "bob"
			if is.Name != nil {
				name = is.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil
	}
	var bad error
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || bad != nil {
			return bad == nil
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Import" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != name {
			return true
		}
		pos := fset.Position(call.Pos())
		lits := make([]string, 0, 2)
		for _, a := range call.Args {
			if bl, ok := a.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil {
					lits = append(lits, s)
				}
			}
		}
		if len(call.Args) != 2 || len(lits) != 2 {
			bad = importError(pos, "bob.Import takes the module path and version as string literals")
			return false
		}
		if err := resolve.CheckPath(lits[0]); err != nil {
			bad = importError(pos, err.Error())
			return false
		}
		if !resolve.ValidVersion(lits[1]) {
			bad = importError(pos, fmt.Sprintf("version %q of %s is not a module version (\"v1.2.0\")", lits[1], lits[0]))
			return false
		}
		if resolve.Compare(lits[1], found[lits[0]]) > 0 || found[lits[0]] == "" {
			found[lits[0]] = lits[1]
		}
		return true
	})
	return bad
}

func importError(pos token.Position, reason string) error {
	e := diag.New(1020, "label", fmt.Sprintf("%s:%d", pos.Filename, pos.Line), "option", "bob.Import", "reason", reason)
	e.DefinedAt = diag.Pos{File: pos.Filename, Line: pos.Line, Col: pos.Column}
	return e
}

// ServeImports copies the bob.Import modules of the repository at root that the Go module
// cache holds (the evaluator downloads them there) into the GOPROXY directory proxy, so the dev
// shell's go and gopls, which read modules only from bob's proxy (§03.2.5), resolve the
// dependency bob repositories the bob.go files import (R-FW-022). Modules the cache does not
// hold are skipped (the go command reports them).
func ServeImports(root, proxy string) error {
	mods, err := BobImports(root)
	if err != nil || len(mods) == 0 {
		return err
	}
	cache := goModCache()
	if cache == "" {
		return nil
	}
	for _, m := range mods {
		esc := filepath.FromSlash(resolve.EscapePath(m.Path))
		from := filepath.Join(cache, "cache", "download", esc, "@v")
		to := filepath.Join(proxy, esc, "@v")
		var files [][2]string
		for _, ext := range []string{".info", ".mod", ".zip"} {
			b, err := os.ReadFile(filepath.Join(from, m.Version+ext))
			if err != nil {
				files = nil
				break
			}
			files = append(files, [2]string{m.Version + ext, string(b)})
		}
		if files == nil {
			continue
		}
		if err := os.MkdirAll(to, 0o755); err != nil {
			return err
		}
		old, _ := os.ReadFile(filepath.Join(to, "list"))
		versions := strings.Fields(string(old))
		if !slices.Contains(versions, m.Version) {
			versions = append(versions, m.Version)
		}
		sort.Strings(versions)
		files = append(files, [2]string{"list", strings.Join(versions, "\n") + "\n"})
		for _, f := range files {
			p := filepath.Join(to, f[0])
			if cur, err := os.ReadFile(p); err == nil && string(cur) == f[1] {
				continue
			}
			if err := os.WriteFile(p+".tmp", []byte(f[1]), 0o644); err != nil {
				return err
			}
			if err := os.Rename(p+".tmp", p); err != nil {
				return err
			}
		}
	}
	return nil
}
