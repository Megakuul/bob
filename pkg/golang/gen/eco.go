package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
	"github.com/megakuul/bob/pkg/golang/resolve"

	// The Go API registers the go.* kinds the IR names.
	_ "github.com/megakuul/bob/pkg/golang"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem is the Go ecosystem (SPEC §03.2, ADR 0004): engine.Ecosystem and
// engine.LockFreshness.
type Ecosystem struct{}

// Name is the bob.lock section key and the kind prefix.
func (Ecosystem) Name() string { return lock.SectionGo }

// Plan turns the Go nodes into derivations: std per toolchain and target, one derivation per
// workspace package and per third-party module, the link, test binaries and test runs.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("go planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil {
		return engine.PlanResult{}, fmt.Errorf("go planner: no graph")
	}
	return newPlanner(ctx, req).plan()
}

// Native returns go.mod and go.sum of every module of this repository (§03.2.5).
func (Ecosystem) Native(_ context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	if req.Graph == nil {
		return nil, nil
	}
	pl := newPlanner(context.Background(), req)
	if err := pl.workspace(); err != nil {
		return nil, err
	}
	if err := pl.lockModules(); err != nil {
		return nil, err
	}
	declared := map[string]bool{} // what resolution counts as declared (resolve.Roots)
	for _, m := range pl.mods {
		if m.node != nil && strings.HasPrefix(m.node.Label, "//") {
			for _, r := range directRequires(m) {
				declared[r.Path] = true
			}
		}
	}
	wsDeps, err := workspaceDeps(pl.mods, req.Root)
	if err != nil {
		return nil, err
	}
	var out []engine.GeneratedFile
	for _, mp := range sortedKeys(pl.mods) {
		m := pl.mods[mp]
		if m.node == nil || !strings.HasPrefix(m.node.Label, "//") {
			continue // modules of dependency repositories keep their own files
		}
		var local []localModule
		for _, dp := range wsDeps[mp] {
			if d := pl.mods[dp]; d != nil {
				local = append(local, localModule{path: dp, dir: replaceDir(m.dir, d.dir)})
			}
		}
		var bob bobRequirements
		if m.dir == "" {
			// The root module's go.mod is the root go.mod: it also carries the requirements of
			// the bob.go files (R-FW-043.4, R-FW-022), as RootGoMod writes them without one.
			if bob, err = rootBobRequirements(req.Graph, req.Root, m.spec.Path, req.FrameworkSum); err != nil {
				return nil, err
			}
		}
		out = append(out, goModFiles(m, pl.lock, declared, local, bob)...)
	}
	return out, nil
}

// workspaceDeps finds, per workspace module of this repository, the other workspace modules
// whose packages it imports (resolve.WorkspaceDeps); nothing without the repository root.
func workspaceDeps(mods map[string]*wsModule, root string) (map[string][]string, error) {
	if root == "" {
		return nil, nil
	}
	var wss []resolve.Workspace
	for _, mp := range sortedKeys(mods) {
		m := mods[mp]
		if m.node == nil || !strings.HasPrefix(m.node.Label, "//") {
			continue
		}
		w, err := workspaceOf(m.node, root)
		if err != nil {
			return nil, err
		}
		wss = append(wss, w)
	}
	return resolve.WorkspaceDeps(wss)
}

// replaceDir is the replace target of the module at repo-relative dir to, seen from the
// module at from: a relative path starting with ./ or ../ (the go command reads anything
// else as a module path).
func replaceDir(from, to string) string {
	if from == "" {
		from = "."
	}
	if to == "" {
		to = "."
	}
	rel, err := filepath.Rel(filepath.FromSlash(from), filepath.FromSlash(to))
	if err != nil {
		return "./" + to
	}
	rel = filepath.ToSlash(rel)
	if rel != ".." && !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel
}

// declaration is what the go section is resolved from (R-FW-062): per workspace module its
// path, Go version and direct requirements.
type declaration struct {
	Path    string               `json:"path"`
	Go      string               `json:"go"`
	Require []gospec.Requirement `json:"require"`
}

// declarations reads the workspace modules of g, sorted by path.
func declarations(g *ir.Graph) ([]declaration, map[string]*ir.Node, error) {
	nodes := map[string]*ir.Node{}
	var out []declaration
	for _, n := range g.Nodes {
		if n.Kind != gospec.KindModule || !strings.HasPrefix(n.Label, "//") {
			continue
		}
		var s gospec.Module
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return nil, nil, fmt.Errorf("%s: module spec: %w", n.Label, err)
		}
		if _, dup := nodes[s.Path]; dup {
			continue
		}
		nodes[s.Path] = n
		out = append(out, declaration{Path: s.Path, Go: s.Go, Require: directRequires(&wsModule{spec: s})})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nodes, nil
}
