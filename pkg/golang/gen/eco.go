package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
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
	var out []engine.GeneratedFile
	for _, mp := range sortedKeys(pl.mods) {
		m := pl.mods[mp]
		if m.node == nil || !strings.HasPrefix(m.node.Label, "//") {
			continue // modules of dependency repositories keep their own files
		}
		out = append(out, goModFiles(m, pl.lock)...)
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

// Lock writes the go section: the declarations' hash, the proxy, a build list per workspace
// module and the module entries. Resolving third-party requirements (MVS over the proxy's
// .mod files, R-GO-002b) is task M5.3; until it exists, requirements that the previous
// section does not already resolve fail with BOB-E-9008.
func (Ecosystem) Lock(_ context.Context, req engine.LockRequest) (json.RawMessage, error) {
	decls, _, err := declarations(req.Graph)
	if err != nil {
		return nil, err
	}
	inputs, err := lock.Inputs(decls)
	if err != nil {
		return nil, err
	}
	var prev *lock.GoSection
	if len(req.Prev) > 0 {
		if v, err := lock.DecodeSection(lock.SectionGo, req.Prev); err == nil {
			prev = v.(*lock.GoSection)
		}
	}
	sec := &lock.GoSection{Inputs: inputs, Proxy: defaultProxy, Workspaces: map[string]lock.GoBuildList{}, Modules: []lock.GoModule{}}
	used := map[string]bool{}
	for _, d := range decls {
		bl := lock.GoBuildList{Go: goDirective(d.Go), Modules: []string{}}
		if len(d.Require) > 0 {
			if prev == nil || !resolves(prev.Workspaces[d.Path], d.Require) || len(req.Update) > 0 {
				return nil, diag.NotImplemented("Go module resolution for " + d.Path + " (golang.Require, task M5.3)")
			}
			bl.Modules = append(bl.Modules, prev.Workspaces[d.Path].Modules...)
		}
		for _, mv := range bl.Modules {
			used[mv] = true
		}
		sec.Workspaces[d.Path] = bl
	}
	if prev != nil {
		sec.Proxy = prev.Proxy
		for _, m := range prev.Modules {
			if used[m.Path+"@"+m.Version] {
				sec.Modules = append(sec.Modules, m)
			}
		}
	}
	return lock.EncodeSection(lock.SectionGo, sec)
}

// resolves reports whether a workspace build list contains every requirement.
func resolves(bl lock.GoBuildList, reqs []gospec.Requirement) bool {
	have := map[string]bool{}
	for _, mv := range bl.Modules {
		have[mv] = true
	}
	for _, r := range reqs {
		if !have[r.Path+"@"+r.Version] {
			return false
		}
	}
	return true
}

// Stale reports the first workspace module of the graph whose declarations the section does
// not reflect (R-FW-062): a module without a build list, another Go version, or a
// requirement that is not in its build list. Only modules of the graph are compared, so a
// build of part of the repository sees the same answer as one of all of it.
func (Ecosystem) Stale(_ context.Context, req engine.LockRequest) (string, ir.Pos, error) {
	decls, nodes, err := declarations(req.Graph)
	if err != nil {
		return "", ir.Pos{}, err
	}
	v, err := lock.DecodeSection(lock.SectionGo, req.Prev)
	if err != nil {
		return "the go section cannot be read: " + err.Error(), ir.Pos{}, nil
	}
	sec := v.(*lock.GoSection)
	for _, d := range decls {
		at := nodes[d.Path].DefinedAt
		bl, ok := sec.Workspaces[d.Path]
		switch {
		case !ok:
			return fmt.Sprintf("Go module %s is not locked", d.Path), at, nil
		case bl.Go != goDirective(d.Go):
			return fmt.Sprintf("Go module %s declares Go %s but is locked for %s", d.Path, d.Go, bl.Go), at, nil
		}
		for _, r := range d.Require {
			if !resolves(bl, []gospec.Requirement{r}) {
				return fmt.Sprintf("Go module %s requires %s %s, which is not locked", d.Path, r.Path, r.Version), at, nil
			}
		}
	}
	return "", ir.Pos{}, nil
}
