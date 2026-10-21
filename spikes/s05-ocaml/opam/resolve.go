package opam

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Pkg is one version of one package in the repository.
type Pkg struct {
	Name, Version string
	F             *File
	Dir           string
}

// Repo is an opam repository checkout (packages/<name>/<name>.<version>/opam), parsed lazily.
type Repo struct {
	Root   string
	cache  map[string][]*Pkg
	Parsed int
}

func OpenRepo(root string) *Repo { return &Repo{Root: root, cache: map[string][]*Pkg{}} }

// Versions returns all versions of name, newest first.
func (r *Repo) Versions(name string) ([]*Pkg, error) {
	if ps, ok := r.cache[name]; ok {
		return ps, nil
	}
	ents, err := os.ReadDir(filepath.Join(r.Root, "packages", name))
	if err != nil {
		r.cache[name] = nil
		return nil, nil
	}
	var ps []*Pkg
	for _, e := range ents {
		ver := strings.TrimPrefix(e.Name(), name+".")
		dir := filepath.Join(r.Root, "packages", name, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "opam"))
		if err != nil {
			continue
		}
		f, err := Parse(string(b))
		r.Parsed++
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %v", name, ver, err)
		}
		ps = append(ps, &Pkg{Name: name, Version: ver, F: f, Dir: dir})
	}
	sort.Slice(ps, func(i, j int) bool { return Compare(ps[i].Version, ps[j].Version) > 0 })
	r.cache[name] = ps
	return ps, nil
}

// Platform holds the global variables bob evaluates opam filters with.
type Platform map[string]string

// Linux/aarch64 with the nixpkgs toolchain.
func DefaultPlatform(ocamlV string) Platform {
	return Platform{"os": "linux", "arch": "arm64", "os-family": "nixos", "os-distribution": "nixos",
		"opam-version": "2.5.1", "jobs": "%{jobs}%", "make": "make", "ocaml:version": ocamlV,
		"ocaml:native": "true", "ocaml:native-dynlink": "true", "ocaml:preinstalled": "false",
		"sys-ocaml-version": ocamlV, "build": "true", "post": "true", "with-test": "false",
		"with-doc": "false", "dev": "false", "with-dev-setup": "false", "pinned": "false"}
}

// Resolver picks one version per package (highest first, depth-first with backtracking).
type Resolver struct {
	Repo     *Repo
	Plat     Platform
	Provided map[string]string // toolchain packages fixed to nixpkgs versions (no own deps)
	Steps    int
	gaveUp   bool
	dead     map[*Pkg]bool
}

type state struct {
	chosen map[string]*Pkg
	order  []string
}

func (r *Resolver) env(p *Pkg, st map[string]*Pkg) Env {
	return func(n string) (string, bool) {
		switch n {
		case "name", "_:name":
			return p.Name, true
		case "version", "_:version":
			return p.Version, true
		}
		if v, ok := r.Plat[n]; ok {
			return v, true
		}
		if pk, vr, ok := strings.Cut(n, ":"); ok {
			if vr == "installed" {
				_, inst := st[pk]
				return fmt.Sprint(inst), true
			}
			if vr == "version" {
				if c := st[pk]; c != nil {
					return c.Version, true
				}
			}
		}
		return "", false
	}
}

// Solution is a resolved set of packages.
type Solution struct {
	Pkgs map[string]*Pkg
}

type goal struct {
	alts []Alt
	from string
}

// Resolve resolves the given root atoms.
func (r *Resolver) Resolve(roots []Atom) (*Solution, error) {
	chosen := map[string]*Pkg{}
	for n, v := range r.Provided {
		chosen[n] = &Pkg{Name: n, Version: v}
	}
	var goals []goal
	for _, a := range roots {
		goals = append(goals, goal{[]Alt{{a}}, "root"})
	}
	if err := r.prune(roots); err != nil {
		return nil, err
	}
	var lastErr error
	ok := r.solve(goals, chosen, &lastErr)
	if !ok {
		if lastErr == nil {
			lastErr = fmt.Errorf("no solution")
		}
		return nil, lastErr
	}
	return &Solution{Pkgs: chosen}, nil
}

func (r *Resolver) available(p *Pkg, chosen map[string]*Pkg) bool {
	if p.F == nil {
		return true
	}
	av := p.F.Fields["available"]
	if av == nil {
		return true
	}
	for _, it := range Items(av) {
		if b, ok := Eval(it, r.env(p, chosen)).Bool(); !ok || !b {
			return false
		}
	}
	return true
}

func (r *Resolver) conflicts(p *Pkg, chosen map[string]*Pkg) bool {
	check := func(owner *Pkg, other *Pkg) bool {
		if owner.F == nil || owner.F.Fields["conflicts"] == nil {
			return false
		}
		f, err := Deps(owner.F.Fields["conflicts"], r.env(owner, chosen))
		if err != nil {
			return false
		}
		for _, alts := range f {
			for _, a := range flat(alts) {
				if a.Name == other.Name && Match(a.Pred, other.Version, r.env(owner, chosen)) {
					return true
				}
			}
		}
		return false
	}
	for _, c := range chosen {
		if check(p, c) || check(c, p) {
			return true
		}
	}
	return false
}

func (r *Resolver) solve(goals []goal, chosen map[string]*Pkg, lastErr *error) bool {
	r.Steps++
	if r.gaveUp {
		return false
	}
	if r.Steps > 200000 {
		*lastErr = fmt.Errorf("resolver gave up after 200000 steps (last conflict: %v)", *lastErr)
		r.gaveUp = true
		return false
	}
	if len(goals) == 0 {
		return true
	}
	g, rest := goals[0], goals[1:]
	// already satisfied?
	for _, alt := range g.alts {
		all := true
		for _, a := range alt {
			if c := chosen[a.Name]; c == nil || !Match(a.Pred, c.Version, r.env(c, chosen)) {
				all = false
			}
		}
		if all {
			return r.solve(rest, chosen, lastErr)
		}
	}
	if len(g.alts) > 1 || len(g.alts[0]) > 1 {
		for _, alt := range g.alts {
			ng := []goal{}
			for _, a := range alt {
				ng = append(ng, goal{[]Alt{{a}}, g.from})
			}
			if r.solve(append(ng, rest...), chosen, lastErr) {
				return true
			}
		}
		return false
	}
	for _, a := range g.alts[0] {
		if c := chosen[a.Name]; c != nil {
			*lastErr = fmt.Errorf("%s needs %s %s, but %s.%s is already chosen", g.from, a.Name, a.Pred, c.Name, c.Version)
			continue
		}
		vs, err := r.Repo.Versions(a.Name)
		if err != nil {
			*lastErr = err
			return false
		}
		if len(vs) == 0 {
			*lastErr = fmt.Errorf("%s needs %s, which is not in the opam repository", g.from, a.Name)
		}
		for _, p := range vs {
			if r.dead[p] || !Match(a.Pred, p.Version, r.env(p, chosen)) || !r.available(p, chosen) || r.conflicts(p, chosen) {
				continue
			}
			f, err := Deps(p.F.Fields["depends"], r.env(p, chosen))
			if err != nil {
				*lastErr = fmt.Errorf("%s.%s: %v", p.Name, p.Version, err)
				continue
			}
			if !r.forwardOK(f, p, chosen) {
				continue
			}
			chosen[p.Name] = p
			// breadth-first goal order (measured: depth-first order blows up on dream)
			ng := append([]goal{}, rest...)
			for _, alts := range f {
				ng = append(ng, goal{alts, p.Name + "." + p.Version})
			}
			if r.solve(ng, chosen, lastErr) {
				return true
			}
			delete(chosen, p.Name)
		}
		if *lastErr == nil {
			*lastErr = fmt.Errorf("%s needs %s %s: no available version matches", g.from, a.Name, a.Pred)
		}
	}
	return false
}

// BuildDeps returns the packages p must be built after: depends (all flags) plus depopts
// present in the solution. Provided toolchain packages are excluded.
func (r *Resolver) BuildDeps(p *Pkg, s *Solution) []string {
	seen := map[string]bool{}
	var out []string
	for _, field := range []string{"depends", "depopts"} {
		f, _ := Deps(p.F.Fields[field], r.env(p, s.Pkgs))
		for _, alts := range f {
			for _, a := range flat(alts) {
				if c := s.Pkgs[a.Name]; c != nil && c.F != nil && !seen[a.Name] {
					seen[a.Name] = true
					out = append(out, a.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// Env exposes the evaluation environment of p in solution s.
func (r *Resolver) Env(p *Pkg, s *Solution) Env { return r.env(p, s.Pkgs) }

func flat(alts []Alt) []Atom {
	var out []Atom
	for _, a := range alts {
		out = append(out, a...)
	}
	return out
}

// forwardOK rejects p early when one of its clauses can only be met by packages that are
// already chosen at non-matching versions (e.g. dune >= 3.22 with dune fixed to 3.21.1).
func (r *Resolver) forwardOK(f Formula, p *Pkg, chosen map[string]*Pkg) bool {
	for _, alts := range f {
		possible := false
		for _, alt := range alts {
			ok := true
			for _, a := range alt {
				if c := chosen[a.Name]; c != nil && !Match(a.Pred, c.Version, r.env(p, chosen)) {
					ok = false
				}
			}
			if ok {
				possible = true
				break
			}
		}
		if !possible {
			return false
		}
	}
	return true
}

// prune marks package versions that can never be installed with the fixed toolchain:
// unavailable, or a dependency clause with no live candidate. Fixpoint over all versions
// reachable from the roots. This is what keeps plain depth-first search fast.
func (r *Resolver) prune(roots []Atom) error {
	r.dead = map[*Pkg]bool{}
	deps := map[*Pkg]Formula{}
	seen := map[string]bool{}
	var all []*Pkg
	queue := []string{}
	for _, a := range roots {
		queue = append(queue, a.Name)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if seen[n] || r.Provided[n] != "" {
			continue
		}
		seen[n] = true
		vs, err := r.Repo.Versions(n)
		if err != nil {
			return err
		}
		for _, p := range vs {
			all = append(all, p)
			f, err := Deps(p.F.Fields["depends"], r.env(p, nil))
			if err != nil || !r.available(p, nil) {
				r.dead[p] = true
				continue
			}
			deps[p] = f
			for _, alts := range f {
				for _, a := range flat(alts) {
					queue = append(queue, a.Name)
				}
			}
		}
	}
	live := func(a Atom, owner *Pkg) bool {
		if v := r.Provided[a.Name]; v != "" {
			return Match(a.Pred, v, r.env(owner, nil))
		}
		vs, _ := r.Repo.Versions(a.Name)
		for _, c := range vs {
			if !r.dead[c] && Match(a.Pred, c.Version, r.env(owner, nil)) {
				return true
			}
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		for _, p := range all {
			if r.dead[p] {
				continue
			}
			for _, alts := range deps[p] {
				ok := false
				for _, alt := range alts {
					altOK := true
					for _, a := range alt {
						if !live(a, p) {
							altOK = false
							break
						}
					}
					if altOK {
						ok = true
						break
					}
				}
				if !ok {
					r.dead[p] = true
					changed = true
					break
				}
			}
		}
	}
	return nil
}
