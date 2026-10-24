package build

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/materialise"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// Working-tree outputs (SPEC §03.6.6, R-FW-007; npm node_modules §03.4.3). A local bob build,
// bob test or bob run mirrors every planned engine.Materialisation whose output is valid after
// the build; bob gen (CmdGen) builds those of the requested packages first and, for the whole
// repository, prunes owned directories nothing produces any more. Conflicts (BOB-E-1016) are
// errors in bob gen and warnings elsewhere: the build itself succeeded.

// materialise runs after the realise stage.
func (r *runner) materialise(ctx context.Context, st *store.Store, p *planner, res *Result) error {
	if r.o.Untrusted || r.o.NoMaterialise {
		return nil
	}
	set := uniqueMaterialisations(p.materialise)
	if r.o.Command == CmdGen {
		set = r.genSet(p.g, p)
	}
	outs := make([]string, len(set))
	for i, m := range set {
		outs[i] = m.Output
	}
	invalid, err := st.Invalid(ctx, outs)
	if err != nil {
		return err
	}
	missing := map[string]bool{}
	for _, o := range invalid {
		missing[o] = true
	}
	var roots []materialise.Root
	digests := newDigester(r.o.Root, p.g, p.lockf, func(n *ir.Node) (string, error) { return p.srcs[n.ID], nil })
	for _, m := range set {
		if missing[m.Output] {
			continue
		}
		inputs, err := digests.of(m.Label)
		if err != nil {
			inputs = "" // shown as stale by bob status; the output itself is fine
		}
		roots = append(roots, materialise.Root{Dir: m.Dir, Label: m.Label, Source: m.Output,
			Tree: hostPath(st, m.Output), LinkStore: filepath.Join(st.Home(), "nix", "store"), Keep: m.Keep, Record: m.Record,
			Inputs: inputs})
	}
	// Pruning needs the complete set: a root whose output failed to build is not stale.
	prune := r.o.Command == CmdGen && wholeRepo(r.o.Patterns) && len(invalid) == 0
	if len(roots) == 0 && !prune {
		return nil
	}
	mres, err := materialise.Apply(ctx, materialise.Options{Repo: r.o.Root, Force: r.o.Force, Prune: prune}, roots)
	if err != nil {
		if _, ok := diagErrors(err); ok && res.Report != nil {
			res.Report.Errors = append(res.Report.Errors, flatten(err)...)
			return nil
		}
		return err
	}
	for _, c := range mres.Conflicts {
		if r.o.Command != CmdGen {
			c.Severity = diag.SeverityWarning
			res.Report.Warnings = append(res.Report.Warnings, c)
			r.emit(Event{Event: "warning", Error: c, Hint: c.Hint()})
			continue
		}
		res.Report.Errors = append(res.Report.Errors, c)
		r.emit(Event{Event: "error", Error: c, Hint: c.Hint()})
	}
	return nil
}

func flatten(err error) []*diag.Error {
	errs, _ := diagErrors(err)
	return errs
}

// genSet are the working-tree outputs bob gen builds: those of the requested packages (all of
// them for the whole repository).
func (r *runner) genSet(g *ir.Graph, p *planner) []engine.Materialisation {
	all := uniqueMaterialisations(p.materialise)
	if wholeRepo(r.o.Patterns) {
		return all
	}
	want := map[string]bool{}
	byID := nodesByID(g)
	for _, id := range g.Roots {
		if n := byID[id]; n != nil {
			want[n.Label] = true
		}
	}
	var out []engine.Materialisation
	for _, m := range all {
		if want[m.Label] {
			out = append(out, m)
		}
	}
	return out
}

// genContext makes bob gen's translator judge each package by its working-tree output: the
// materialisation's derivation stands for the package, whose own derivation is not built.
func (r *runner) genContext(c *translate.Context, g *ir.Graph, p *planner) {
	for _, m := range r.genSet(g, p) {
		var pk translate.Pkg
		for d, x := range c.Packages {
			if x.Label == m.Label && x.Role == "" {
				pk = x
				delete(c.Packages, d)
			}
		}
		if pk.Label == "" {
			pk = translate.Pkg{Label: m.Label}
		}
		c.Packages[m.Drv] = pk
	}
}

// uniqueMaterialisations drops repeated entries (ecosystems plan in waves) and sorts by Dir.
func uniqueMaterialisations(ms []engine.Materialisation) []engine.Materialisation {
	seen := map[string]bool{}
	var out []engine.Materialisation
	for _, m := range ms {
		k := m.Dir + "\x00" + m.Output
		if !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Dir < out[j].Dir || out[i].Dir == out[j].Dir && out[i].Output < out[j].Output
	})
	return out
}

// outputName is the name of the output of m's derivation that is m.Output ("out" if unknown).
func outputName(p *planner, m engine.Materialisation) string {
	if d := p.byPath[m.Drv]; d != nil {
		for _, o := range d.Outputs {
			if o.Path == m.Output {
				return o.Name
			}
		}
	}
	return "out"
}

// materialises reports whether an ecosystem of g puts outputs into the working tree.
func materialises(g *ir.Graph) (bool, error) {
	ecos, err := ecosystemsOf(g)
	if err != nil {
		return false, err
	}
	for _, e := range ecos {
		if m, ok := e.(engine.Materialiser); ok && m.Materialises(g) {
			return true, nil
		}
	}
	return false, nil
}

// genMaterialise is bob gen's second half: build and materialise the working-tree outputs, or,
// when the repository has none, only prune what an earlier bob gen left.
func genMaterialise(ctx context.Context, o Options, g *ir.Graph, force bool, prev *runner) error {
	has, err := materialises(g)
	if err != nil {
		return err
	}
	if !has {
		if len(o.Patterns) > 0 {
			return nil
		}
		res, err := materialise.Apply(ctx, materialise.Options{Repo: o.Root, Force: force, Prune: true}, nil)
		if err != nil {
			return err
		}
		return joinDiags(res.Conflicts)
	}
	o.Command, o.Force = CmdGen, force
	res, err := runAfter(ctx, o, prev)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return joinDiags(res.Report.Errors)
	}
	return nil
}

func joinDiags(errs []*diag.Error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return diagList(errs)
}

// diagList keeps each diagnostic visible to diagErrors (Unwrap []error).
type diagList []*diag.Error

func (l diagList) Error() string { return l[0].Error() }

func (l diagList) Unwrap() []error {
	out := make([]error, len(l))
	for i, e := range l {
		out[i] = e
	}
	return out
}
