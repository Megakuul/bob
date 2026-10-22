package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"

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
	var out []engine.GeneratedFile
	for _, mp := range sortedKeys(pl.mods) {
		m := pl.mods[mp]
		if m.node == nil || !strings.HasPrefix(m.node.Label, "//") {
			continue // modules of dependency repositories keep their own files
		}
		out = append(out, goModFiles(m, pl.lock, declared)...)
	}
	return out, nil
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
