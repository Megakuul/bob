package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
	"github.com/megakuul/bob/pkg/npm/resolve"
)

// ToolImporter is the one importer of a tool lock (§03.6.5).
const ToolImporter = "tool"

// Tool is the planned node_modules of a bob tool lock (an npm-based generator bob ships, SPEC
// §03.6.5) for the host.
type Tool struct {
	Dir     string            // the node_modules directory (packages at Dir/<name>)
	Drv     string            // the derivation whose output Dir is
	Drvs    []*drv.Derivation // the installer's derivations: add them to the PlanResult
	Fetches []fetch.Request   // the tarballs: add them to the PlanResult
	own     map[string]*drv.Derivation
	next    drv.Resolver
}

// ToolModules plans the node_modules of tool lock tl with the §03.4 installer: one fetch per
// tarball, one derivation per package instance, the isolated layout, no lifecycle scripts. n is
// the codegen node that uses the tool; node and bash are the indices of its nodejs and bash
// dependencies.
func ToolModules(ctx context.Context, req engine.PlanRequest, n *ir.Node, name string, tl *lock.ToolLock, node, bash int) (t *Tool, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("npm tool %s: internal error: %v", name, v)
		}
	}()
	if tl == nil || len(tl.Importers) != 1 {
		return nil, fmt.Errorf("npm tool %s: the tool lock must have exactly one importer %q", name, ToolImporter)
	}
	if _, ok := tl.Importers[ToolImporter]; !ok {
		return nil, fmt.Errorf("npm tool %s: the tool lock must have exactly one importer %q", name, ToolImporter)
	}
	// The tarballs come from the registry bob uses ($BOB_NPM_REGISTRY: mirrors and test
	// registries); the lock's integrity pins their content.
	sec := &lock.NpmSection{Registry: registryURL(), Importers: tl.Importers, Packages: tl.Packages}
	pl := &planner{ctx: ctx, req: req, sec: sec, nodes: map[string]*ir.Node{}, done: map[string]engine.Resolved{},
		own: map[string]*drv.Derivation{}, modulo: map[string][32]byte{}, inst: map[string]instRef{},
		nm: map[string]nmRef{}, fetched: map[string]bool{}, plats: map[string]*platInfo{},
		out: engine.PlanResult{Nodes: map[string]engine.Resolved{}}}
	pi, err := hostPlatInfo(pl)
	if err != nil {
		return nil, err
	}
	im := &importer{node: &ir.Node{ID: n.ID, Label: n.Label + " " + name, Deps: n.Deps}, dir: ToolImporter,
		spec: npmspec.Importer{Name: name, Options: npmspec.Options{AllowScripts: []string{}, Extend: []npmspec.Extend{}},
			Tools: npmspec.Tools{Node: node, Bash: bash, Runtime: -1, Esbuild: -1, Scripts: []int{}}}}
	r, err := pl.nodeModules(im.node, im, pi)
	if err != nil {
		return nil, err
	}
	// pl.out.Materialise is dropped: a tool's node_modules never reaches the working tree.
	return &Tool{Dir: r.out, Drv: r.drv, Drvs: pl.out.Drvs, Fetches: pl.out.Fetches, own: pl.own, next: req.Resolver}, nil
}

// Modulo resolves the tool's derivations, then the request's (a drv.Resolver for the jobs
// that use the tool).
func (t *Tool) Modulo(p string) ([32]byte, error) {
	if d, ok := t.own[p]; ok {
		return d.Modulo(t)
	}
	if t.next == nil {
		return [32]byte{}, fmt.Errorf("no resolver for input derivation %s", p)
	}
	return t.next.Modulo(p)
}

// Prepare makes the tool valid while planning (its tarballs fetched, its derivations built),
// so a codegen job that uses it can be built while planning (early cutoff, §03.6.3). It is best
// effort: on failure the build stage fetches and builds the tool and reports what failed.
func (t *Tool) Prepare(ctx context.Context, st engine.PlanStore) {
	rz, ok := st.(engine.PlanRealiser)
	if !ok {
		return
	}
	for _, f := range t.Fetches {
		if _, err := st.Ensure(ctx, f); err != nil {
			return
		}
	}
	drvs := map[string]*drv.Derivation{}
	for p, d := range t.own {
		drvs[p] = d
	}
	_, _ = rz.RealiseNow(ctx, drvs)
}

// hostPlatInfo is the host platform as npm names it.
func hostPlatInfo(pl *planner) (*platInfo, error) {
	p := string(engine.HostPlatform())
	goos, goarch, _ := strings.Cut(p, "-")
	cpu := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if cpu == "" || goos != "linux" {
		return nil, fmt.Errorf("npm tools cannot run on %s", p)
	}
	pi := &platInfo{os: goos, cpu: cpu, libc: "glibc", system: arch + "-" + goos}
	pi.sccs = pl.cycles(pi)
	return pi, nil
}

// ResolveToolLock resolves a tool lock from the public registry: deps are the tool's direct
// dependencies (name → range), node the Node.js version of bob's nixpkgs pin. bob's tool
// packages call it when the locks are refreshed at bob release time (their -update-tool-lock
// tests), never during a build.
func ResolveToolLock(ctx context.Context, deps map[string]string, node string) (*lock.ToolLock, error) {
	im := resolve.Importer{Dir: ToolImporter, Name: "bob-tool"}
	for _, name := range sortedKeys(deps) {
		im.Deps = append(im.Deps, resolve.Dep{Alias: name, Req: deps[name]})
	}
	res, err := resolve.Resolve(ctx, resolve.Input{Importers: []resolve.Importer{im}},
		resolve.Options{Registry: &resolve.Registry{URL: resolve.DefaultRegistry}, Root: "the tool lock", NodeVersion: node})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(res.Section)
	if err != nil {
		return nil, err
	}
	var tl lock.ToolLock
	if err := json.Unmarshal(raw, &tl); err != nil {
		return nil, err
	}
	return &tl, nil
}
