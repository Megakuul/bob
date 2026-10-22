package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

// Shared plumbing of the code generators (SPEC §03.6): connect, graphql, openapi. A codegen
// node is created by its consumer (a go.module or an npm importer) from a recipe
// (golang.Generated, npm.Generated) in the consumer's directory; its output is exactly the
// content of one owned directory below the consumer.

var (
	codegenMu    sync.Mutex
	codegenKinds = map[string]bool{}
)

// RegisterCodegenKind marks a node kind as a code generator: a failed build of it is reported
// as `✗ <label> code generation failed (defined at <bob.go:line>)` with the generator's own
// messages (R-GEN-001a). Generator packages call it from init.
func RegisterCodegenKind(kind string) {
	codegenMu.Lock()
	defer codegenMu.Unlock()
	codegenKinds[kind] = true
}

// IsCodegenKind reports whether kind was registered with RegisterCodegenKind.
func IsCodegenKind(kind string) bool {
	codegenMu.Lock()
	defer codegenMu.Unlock()
	return codegenKinds[kind]
}

// ConsumerOf returns the node of g that realises the codegen node id (the go.module or npm
// importer that lists it as a dependency) of one of kinds, or nil.
func ConsumerOf(g *ir.Graph, id string, kinds ...string) *ir.Node {
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if len(kinds) > 0 && !contains(kinds, n.Kind) {
			continue
		}
		if contains(n.Deps, id) {
			return n
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// LabelDir is the repository-relative directory of a label of this repository ("" for the
// root and for labels of other repositories).
func LabelDir(label string) string {
	rest, ok := strings.CutPrefix(label, "//")
	if !ok {
		return ""
	}
	dir, _, _ := strings.Cut(rest, ":")
	return dir
}

// maxGenFiles bounds the files of one codegen output that bob imports and materialises.
const maxGenFiles = 20000

// Codegen is the planned derivation of one codegen node: Drv builds Out, whose content becomes
// the owned directory Dir (repository-relative).
type Codegen struct {
	Node *ir.Node
	Drv  *drv.Derivation
	Path string // drv path
	Out  string // its "out" store path
	Dir  string // owned directory, repository-relative
}

// PlanCodegen finishes a codegen node (§03.6.3, R-GEN-005): the derivation is built while
// planning (engine.PlanRealiser), its output is added to the store by content as
// `gen-<label slug>` and the node's Resolved names that copy in Cutoff, so consumers depend on
// the content instead of the derivation and a byte-identical regeneration rebuilds nothing
// downstream. When the build fails (or the store cannot build while planning) the node
// resolves to the derivation itself: the build stage then builds it and reports the
// generator's failure, and consumers that need its files plan blocked (see Blocked). The
// Materialisation puts the output into the working tree after the build.
func PlanCodegen(ctx context.Context, req PlanRequest, c Codegen) (Resolved, Materialisation) {
	rvs, ms := PlanCodegens(ctx, req, []Codegen{c})
	return rvs[0], ms[0]
}

// PlanCodegens is PlanCodegen for several nodes with one build and one source import (the
// no-change cost of the two-phase planning, R-GEN-005). A failed build retries the nodes one
// by one, so only the failing ones lose the cutoff.
func PlanCodegens(ctx context.Context, req PlanRequest, cs []Codegen) ([]Resolved, []Materialisation) {
	rvs := make([]Resolved, len(cs))
	ms := make([]Materialisation, len(cs))
	for i, c := range cs {
		rvs[i] = Resolved{Drv: c.Path, Outputs: map[string]string{"out": c.Out}}
		ms[i] = Materialisation{Dir: c.Dir, Label: c.Node.Label, Drv: c.Path, Output: c.Out}
	}
	rz, ok := req.Store.(PlanRealiser)
	if !ok || len(cs) == 0 {
		return rvs, ms
	}
	drvs := map[string]*drv.Derivation{}
	for _, c := range cs {
		drvs[c.Path] = c.Drv
	}
	outs, err := rz.RealiseNow(ctx, drvs)
	if err != nil {
		if len(cs) > 1 {
			for i, c := range cs {
				rvs[i], ms[i] = PlanCodegen(ctx, req, c)
			}
		}
		return rvs, ms
	}
	var trees []srcs.Source
	var which []int
	for i, c := range cs {
		host := outs[c.Path]["out"]
		files, err := genFiles(host)
		if err != nil {
			continue
		}
		slug := strings.NewReplacer("//", "", "/", "-", ":", "-").Replace(c.Node.Label)
		trees = append(trees, srcs.Source{Name: "gen-" + slug, Tree: srcs.Tree{Root: host, Files: files}})
		which = append(which, i)
	}
	if len(trees) == 0 {
		return rvs, ms
	}
	paths, err := req.Store.ImportSources(ctx, trees)
	if err != nil || len(paths) != len(trees) {
		return rvs, ms
	}
	for k, i := range which {
		rvs[i].Cutoff = map[string]string{"out": paths[k]}
		ms[i].Output = paths[k]
	}
	return rvs, ms
}

// genFiles lists the regular files of a codegen output, sorted (the output of a generator is
// regular files only, §03.6.2).
func genFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root || d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == ".gitignore" {
			return fmt.Errorf("%s is not a generated source file (regular files only, no .gitignore)", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) > maxGenFiles {
			return fmt.Errorf("more than %d generated files", maxGenFiles)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// CodegenOutput returns where a consumer reads the generated files of rv while planning, and
// the store path it depends on: the content-addressed copy PlanCodegen made. ok is false when
// there is none (the generator failed while planning; a failed build may leave an empty,
// invalid output directory behind, so the derivation's output is never read): the consumer
// then plans Blocked.
func CodegenOutput(st PlanStore, rv Resolved) (host, storePath string, ok bool) {
	storePath, ok = rv.Cutoff["out"]
	if st == nil || !ok || storePath == "" {
		return "", storePath, false
	}
	host = st.HostPath(storePath)
	if _, err := os.Stat(host); err != nil {
		return "", storePath, false
	}
	return host, storePath, true
}

// Blocked plans the derivation of a package that cannot be planned because codegen it needs
// failed while planning: it depends on the failing codegen derivations, so the build stage
// builds those first and reports their failure, and the package itself is skipped. It never
// succeeds.
func Blocked(jobs *Jobs, label, name string, codegen []string) (drvPath, out string, err error) {
	drvs := map[string][]string{}
	for _, d := range codegen {
		drvs[d] = []string{"out"}
	}
	msg := "code generation this package needs failed while planning"
	return jobs.Add(Job{Name: name, Kind: "run", Label: label, Drvs: drvs, Args: map[string]any{"steps": []any{
		map[string]any{"op": "exec", "args": map[string]any{"argv": []string{"false", msg}, "path": []string{}}},
	}}})
}

// OwnedDir checks a codegen output directory relative to its consumer and returns it
// repository-relative: clean, relative, inside the consumer, never in .git or .bob.
func OwnedDir(consumerDir, dir string) (string, error) {
	if dir == "" || path.IsAbs(dir) || path.Clean(dir) != dir || dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		return "", fmt.Errorf("generated directory %q must be a clean relative path inside %s", dir, orRoot(consumerDir))
	}
	full := path.Join(consumerDir, dir)
	for _, e := range strings.Split(full, "/") {
		if e == ".git" || e == ".bob" || e == "node_modules" {
			return "", fmt.Errorf("generated directory %q may not be inside %s", full, e)
		}
	}
	return full, nil
}

func orRoot(dir string) string {
	if dir == "" {
		return "the repository root"
	}
	return dir + "/"
}

// RuntimeMismatch is the BOB-E-1021 error of a generator whose runtime library an explicit pin
// contradicts (§03.6.4, R-GEN-006), raised before anything is built:
//
//	✗ //svc:gen-graph: generator gqlgen 0.17.90 (bob's nixpkgs pin) does not match its runtime: the generated code needs github.com/99designs/gqlgen 0.17.90, but svc/bob.go requires v0.17.89
func RuntimeMismatch(label, generator, version, runtime, need, file, have string, at ir.Pos) *diag.Error {
	reason := fmt.Sprintf("generator %s %s (bob's nixpkgs pin) does not match its runtime: the generated code needs %s %s, but %s requires %s",
		generator, version, runtime, need, file, have)
	e := diag.New(1021, "label", label, "reason", reason, "runtime", runtime)
	e.Label = label
	e.DefinedAt = diag.Pos{File: at.File, Line: at.Line}
	return e
}
