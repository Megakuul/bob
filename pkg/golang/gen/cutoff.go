package gen

import (
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

// Export-data cutoff (R-GO-005, ADR 0004 A11; off by default, BOB_GO_CUTOFF=1). Every group
// compiles with the -linkobj split: $out/pkg/<ip>.a is the object code for the link,
// $out/export/<ip>.a the export data. The planner builds the groups level by level while it
// plans (engine.PlanRealiser), adds each package's export data to the store by content and
// plans the importers against that path. A change that keeps a package's export data
// byte-identical (a function body that is not inlinable) then rebuilds that package and the
// links only: the importers' derivations are unchanged.

// cutoffEnv switches the cutoff mode on.
const cutoffEnv = "BOB_GO_CUTOFF"

// exportFile is the path of ip's export data in a split group's output.
func exportFile(out, ip string) string { return out + "/export/" + ip + ".a" }

// compileImport is what a compile reads for package q of group g: the export data by content
// when the cutoff built g while planning, else g's archive. It records the input in in.
func (r *compiled) compileImport(g *group, q string, in *inputs) string {
	if sp, ok := g.exports[q]; ok {
		in.srcs[sp] = true
		return sp + "/" + q + ".a"
	}
	in.addDrv(g.d, g.drv)
	return archive(g.out, q)
}

// cutoffGroups plans res.groups in the cutoff mode. done is false when a build while planning
// failed: the derivations of the attempt are dropped, the cutoff is off for the rest of the
// run, and the caller plans the groups as usual (the build stage then reports the failure
// through the translator, never raw Nix output).
func (pl *planner) cutoffGroups(t *target, cl *closure, res *compiled, srcPaths map[string]string, tests map[string]bool) (done bool, err error) {
	mark := len(pl.out.Drvs)
	level := map[*group]int{}
	top := 0
	for _, g := range res.groups { // dependencies first
		l := 0
		for _, d := range g.deps {
			l = max(l, level[d]+1)
		}
		level[g] = l
		top = max(top, l)
	}
	for l := 0; l <= top; l++ {
		var lvl []*group
		drvs := map[string]*drv.Derivation{}
		for _, g := range res.groups {
			if level[g] != l {
				continue
			}
			g.split = true
			if err := pl.groupDrv(t, cl, res, g, srcPaths, tests); err != nil {
				return false, err
			}
			pl.ownClosure(g.drv, g.d, drvs)
			lvl = append(lvl, g)
		}
		outs, err := pl.realiser.RealiseNow(pl.ctx, drvs)
		if err != nil {
			pl.cutoffOff(res, mark)
			return false, nil
		}
		var trees []srcs.Source
		var who []*pkg
		var whose []*group
		for _, g := range lvl {
			host := outs[g.drv]["out"]
			if host == "" {
				pl.cutoffOff(res, mark)
				return false, nil
			}
			for _, p := range g.pkgs {
				trees = append(trees, srcs.Source{Name: drv.Name("goexport", p.ip),
					Tree: srcs.Tree{Root: host + "/export", Files: []string{p.ip + ".a"}}})
				who = append(who, p)
				whose = append(whose, g)
			}
		}
		paths, err := pl.req.Store.ImportSources(pl.ctx, trees)
		if err != nil || len(paths) != len(trees) {
			pl.cutoffOff(res, mark)
			return false, nil
		}
		for i, p := range who {
			g := whose[i]
			if g.exports == nil {
				g.exports = map[string]string{}
			}
			g.exports[p.ip] = paths[i]
		}
	}
	return true, nil
}

// ownClosure adds d (at path p) and the derivations planned here that it needs to m.
func (pl *planner) ownClosure(p string, d *drv.Derivation, m map[string]*drv.Derivation) {
	if _, ok := m[p]; ok {
		return
	}
	m[p] = d
	for _, in := range d.InputDrvs {
		if dd, ok := pl.own[in.Path]; ok {
			pl.ownClosure(in.Path, dd, m)
		}
	}
}

// cutoffOff ends the cutoff mode for this run after a failed build while planning: the
// derivations planned since mark are dropped and the groups are reset.
func (pl *planner) cutoffOff(res *compiled, mark int) {
	pl.realiser = nil
	for _, d := range pl.out.Drvs[mark:] {
		if p, err := d.Path(); err == nil {
			delete(pl.own, p)
			delete(pl.modulo, p)
		}
	}
	pl.out.Drvs = pl.out.Drvs[:mark]
	for _, g := range res.groups {
		g.split, g.exports, g.drv, g.d, g.out = false, nil, "", nil, ""
	}
}
