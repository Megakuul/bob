package resolve

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// DefaultMaxSteps bounds the search (hostile or pathological inputs). The 20-graph corpus
// needs far fewer nodes; see solve_test.go.
const DefaultMaxSteps = 200000

// Sentinel errors callers act on (wrapped; use errors.Is).
var (
	// ErrUnsatisfiable: no set of package versions meets the requests (E-RESOLVE, BOB-E-1015).
	ErrUnsatisfiable = errors.New("cannot resolve opam packages")
	// ErrTooComplex: the search hit Options.MaxSteps before finding any solution.
	ErrTooComplex = errors.New("opam resolution too complex")
)

// UnsatError explains an unsatisfiable request with the conflicting constraint chain.
type UnsatError struct {
	Reason string // "ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (…)"
}

func (e *UnsatError) Error() string { return ErrUnsatisfiable.Error() + ": " + e.Reason }

// Unwrap makes errors.Is(err, ErrUnsatisfiable) hold.
func (e *UnsatError) Unwrap() error { return ErrUnsatisfiable }

// Options configures a resolution.
type Options struct {
	Arch     string            // opam arch variable: "arm64" or "x86_64"
	Provided map[string]string // toolchain packages fixed at these versions (Toolchain)
	MaxSteps int               // search budget; 0 = DefaultMaxSteps
}

// Request is one root package with an opam version requirement ("" = any; ">= 5.7").
type Request struct {
	Name, Constraint string
}

// Toolchain returns the packages bob provides instead of choosing them from the repository
// (SPEC §03.5.2), at the versions of the pinned nixpkgs toolchain.
func Toolchain(ocaml, dune, ocamlfind string) map[string]string {
	config := "3" // ocaml-config 3 belongs to OCaml 5.x, 2 to 4.x
	if CompareVersions(ocaml, "5") < 0 {
		config = "2"
	}
	m := map[string]string{
		"ocaml": ocaml, "ocaml-base-compiler": ocaml, "ocaml-compiler": ocaml,
		"ocaml-config": config, "ocaml-options-vanilla": "1",
		"dune": dune, "ocamlfind": ocamlfind,
	}
	for _, b := range []string{"unix", "threads", "bigarray", "domains", "nnp", "effects"} {
		m["base-"+b] = "base"
	}
	return m
}

// globals are the opam global variables and flags bob resolves with (SPEC §03.5.2).
func globals(arch, ocaml string) map[string]string {
	return map[string]string{
		"os": "linux", "arch": arch, "os-family": "nixos", "os-distribution": "nixos",
		"opam-version": "2.5.1", "make": "make", "jobs": "1",
		"ocaml:version": ocaml, "ocaml:native": "true", "ocaml:native-dynlink": "true",
		"ocaml:preinstalled": "false", "sys-ocaml-version": ocaml,
		"build": "true", "post": "true", "with-test": "false", "with-doc": "false",
		"dev": "false", "with-dev-setup": "false", "pinned": "false",
	}
}

// cost is opam's default optimisation criteria for a fresh install, compared
// lexicographically: -count[avoid-version,changed], -count[version-lag,request],
// -count[version-lag,changed], -changed (no package is removed; depexts are not modelled).
type cost [4]int

func (c cost) add(o cost) cost {
	for i := range c {
		c[i] += o[i]
	}
	return c
}

func (c cost) less(o cost) bool {
	for i := range c {
		if c[i] != o[i] {
			return c[i] < o[i]
		}
	}
	return false
}

// catom is an Atom compiled against the cone: target package index and matching versions.
type catom struct {
	n   int // -1: the target is not an opam package (never satisfiable)
	set bitset
	src Atom
}

type cclause struct {
	alts  [][]catom
	src   Clause
	owner pref // the package version whose formula this is; n = -1 for a request
}

type ver struct {
	version   string
	src       *Version // nil for a provided package missing from the repository
	avail     bool
	why       string // why the version is not available
	deps      []cclause
	conflicts []catom
	classes   []int
	cost      cost
	dead      bool
	deadAt    int // clause of deps without a live alternative; -1: unavailable or conflicting
	env       Env
}

type pkg struct {
	name     string
	provided bool
	root     bool
	vers     []*ver   // newest first
	order    []int    // version indices in branching order (cheapest first)
	revConf  [][]pref // per version: package versions that declare a conflict with it
	live     bitset
}

type pref struct{ n, i int }

type member struct {
	n   int
	set bitset
}

type solver struct {
	ctx      context.Context
	repo     *Repository
	glob     map[string]string
	provided map[string]string
	pkgs     []*pkg
	index    map[string]int
	classes  map[string]int
	members  [][]member // per class
	roots    []cclause

	// search state, undone through the trail
	dom    []bitset
	val    []int // -1 undecided, else the chosen version index
	req    []bool
	reqs   []int // required packages, in the order they became required
	open   []*cclause
	cur    cost
	trail  []undo
	steps  int
	max    int
	best   []int
	bestC  cost
	found  bool
	failAt int
	fail   string
	seen   []int // lowerBound's visited marks
	stamp  int
	queue  []int // scratch for lowerBound
	common []int // scratch for implied
}

// Solve resolves reqs against repo: it returns one version per needed package, minimising
// opam's default criteria (newest versions, then fewest packages) among valid solutions.
// Provided packages are fixed and not part of the result.
func Solve(ctx context.Context, repo *Repository, opts Options, reqs []Request) (*Solution, error) {
	if opts.Arch != "arm64" && opts.Arch != "x86_64" {
		return nil, fmt.Errorf("opam resolution: unsupported arch %q", opts.Arch)
	}
	if len(reqs) == 0 {
		return &Solution{Optimal: true}, nil
	}
	s := &solver{ctx: ctx, repo: repo, provided: opts.Provided, index: map[string]int{},
		classes: map[string]int{}, max: opts.MaxSteps}
	if s.max <= 0 {
		s.max = DefaultMaxSteps
	}
	s.glob = globals(opts.Arch, opts.Provided["ocaml"])
	if err := s.build(reqs); err != nil {
		return nil, err
	}
	s.prune()
	for _, rc := range s.roots {
		if a := rc.alts[0][0]; a.n < 0 || !a.set.intersects(s.pkgs[a.n].live) {
			return nil, &UnsatError{Reason: s.explainRoot(rc.src[0][0])}
		}
	}
	if err := s.search(); err != nil {
		return nil, err
	}
	if !s.found {
		if s.steps >= s.max {
			return nil, fmt.Errorf("%w: no solution within %d search steps", ErrTooComplex, s.max)
		}
		return nil, &UnsatError{Reason: s.fail}
	}
	return s.solution()
}

func (s *solver) env(name, version string) Env {
	return func(v string) (string, bool) {
		switch v {
		case "name", "_:name":
			return name, true
		case "version", "_:version":
			return version, true
		}
		if x, ok := s.glob[v]; ok {
			return x, true
		}
		if p, field, ok := strings.Cut(v, ":"); ok {
			pv, prov := s.provided[p]
			switch field {
			case "installed":
				if prov {
					return "true", true
				}
			case "version":
				if prov {
					return pv, true
				}
			}
		}
		return "", false
	}
}

// intern returns the index of package name, loading it on first use (-1: not a valid name).
func (s *solver) intern(name string, queue *[]int) (int, error) {
	if i, ok := s.index[name]; ok {
		return i, nil
	}
	if !ValidName(name) {
		return -1, nil
	}
	if err := s.ctx.Err(); err != nil {
		return 0, fmt.Errorf("opam resolution: %w", err)
	}
	vs, err := s.repo.Versions(name)
	if err != nil {
		return 0, err
	}
	p := &pkg{name: name}
	if pv, ok := s.provided[name]; ok {
		p.provided = true
		v := &ver{version: pv, avail: true}
		for _, x := range vs {
			if x.Version == pv && x.File != nil {
				v.src = x
			}
		}
		p.vers = []*ver{v}
	} else {
		for _, x := range vs {
			p.vers = append(p.vers, &ver{version: x.Version, src: x})
		}
	}
	s.index[name] = len(s.pkgs)
	s.pkgs = append(s.pkgs, p)
	*queue = append(*queue, len(s.pkgs)-1)
	return len(s.pkgs) - 1, nil
}

// build loads the cone of packages reachable from the requests and compiles every formula.
func (s *solver) build(reqs []Request) error {
	var queue []int
	// The toolchain is always installed, so its conflicts and conflict classes always apply.
	for _, name := range slices.Sorted(maps.Keys(s.provided)) {
		if _, err := s.intern(name, &queue); err != nil {
			return err
		}
	}
	var rootAtoms []Atom
	for _, r := range reqs {
		if !ValidName(r.Name) {
			return fmt.Errorf("%w: %q", ErrBadName, r.Name)
		}
		c, err := ParseConstraint(r.Constraint)
		if err != nil {
			return fmt.Errorf("opam package %s: %w", r.Name, err)
		}
		if _, err := s.intern(r.Name, &queue); err != nil {
			return err
		}
		rootAtoms = append(rootAtoms, Atom{Name: r.Name, C: c})
	}
	type raw struct{ deps, conflicts Formula }
	raws := map[*ver]raw{}
	for len(queue) > 0 {
		p := s.pkgs[queue[0]]
		queue = queue[1:]
		for _, v := range p.vers {
			v.env = s.env(p.name, v.version)
			v.deadAt = -1
			f := (*File)(nil)
			if v.src != nil {
				f = v.src.File
			}
			if !p.provided {
				v.avail, v.why = s.available(v)
				if !v.avail {
					continue
				}
			}
			if f == nil {
				continue
			}
			var r raw
			var err error
			if !p.provided {
				if r.deps, err = Deps(f.Fields["depends"], v.env); err != nil {
					v.avail, v.why = false, err.Error()
					continue
				}
			}
			if r.conflicts, err = Deps(f.Fields["conflicts"], v.env); err != nil && !p.provided {
				v.avail, v.why = false, err.Error()
				continue
			}
			raws[v] = r
			for _, a := range r.deps.Atoms() {
				if _, err := s.intern(a.Name, &queue); err != nil {
					return err
				}
			}
			for _, c := range Items(f.Fields["conflict-class"]) {
				if c.Kind == Str {
					v.classes = append(v.classes, s.class(c.S))
				}
			}
		}
	}
	s.members = make([][]member, len(s.classes))
	for n, p := range s.pkgs {
		p.revConf = make([][]pref, len(p.vers))
		p.root = slices.ContainsFunc(rootAtoms, func(a Atom) bool { return a.Name == p.name })
		byClass := map[int]bitset{}
		for i, v := range p.vers {
			for _, c := range v.classes {
				if byClass[c] == nil {
					byClass[c] = newBitset(len(p.vers))
				}
				byClass[c].set(i)
			}
		}
		for c, set := range byClass {
			s.members[c] = append(s.members[c], member{n, set})
		}
	}
	for n, p := range s.pkgs {
		for i, v := range p.vers {
			r := raws[v]
			for _, cl := range r.deps {
				s.compileClause(n, i, cl)
			}
			for _, a := range r.conflicts.Atoms() {
				ca := s.compile(a, v.env)
				if ca.n < 0 || ca.set.empty() {
					continue
				}
				v.conflicts = append(v.conflicts, ca)
				for j := range s.pkgs[ca.n].vers {
					if ca.set.has(j) {
						s.pkgs[ca.n].revConf[j] = append(s.pkgs[ca.n].revConf[j], pref{n, i})
					}
				}
			}
		}
		s.costs(p)
	}
	rootEnv := s.env("", "")
	for _, a := range rootAtoms {
		ca := s.compile(a, rootEnv)
		s.roots = append(s.roots, cclause{alts: [][]catom{{ca}}, src: Clause{{a}}, owner: pref{-1, -1}})
	}
	return nil
}

func (s *solver) class(c string) int {
	if i, ok := s.classes[c]; ok {
		return i
	}
	s.classes[c] = len(s.classes)
	return len(s.classes) - 1
}

func (s *solver) available(v *ver) (bool, string) {
	if v.src == nil || v.src.File == nil {
		if v.src != nil && v.src.ParseErr != nil {
			return false, "its opam file does not parse (" + v.src.ParseErr.Error() + ")"
		}
		return false, "it has no opam file"
	}
	for _, it := range Items(v.src.File.Fields["available"]) {
		if b, ok := EvalFilter(it, v.env); !ok || !b {
			return false, "not available on linux/" + s.glob["arch"]
		}
	}
	return true, ""
}

func (s *solver) compile(a Atom, env Env) catom {
	n, ok := s.index[a.Name]
	if !ok {
		return catom{n: -1, src: a}
	}
	p := s.pkgs[n]
	set := newBitset(len(p.vers))
	for i, v := range p.vers {
		if a.C.Matches(v.version) {
			set.set(i)
		}
	}
	return catom{n: n, set: set, src: a}
}

func (s *solver) compileClause(n, i int, cl Clause) {
	v := s.pkgs[n].vers[i]
	cc := cclause{src: cl, owner: pref{n, i}}
	for _, alt := range cl {
		var ca []catom
		for _, a := range alt {
			ca = append(ca, s.compile(a, v.env))
		}
		cc.alts = append(cc.alts, ca)
	}
	v.deps = append(v.deps, cc)
}

// costs sets each version's cost and the branching order. The version lag counts the newer
// versions that are available on the platform, whether or not they are installable with the
// toolchain (opam computes it over its whole universe).
func (s *solver) costs(p *pkg) {
	if p.provided {
		p.order = []int{0}
		return
	}
	lag := 0
	for i, v := range p.vers {
		var c cost
		if v.src != nil && v.src.File != nil && avoid(v.src.File) {
			c[0] = 1
		}
		if p.root {
			c[1] = lag
		}
		c[2], c[3] = lag, 1
		v.cost = c
		p.order = append(p.order, i)
		if v.avail {
			lag++
		}
	}
	slices.SortStableFunc(p.order, func(a, b int) int {
		ca, cb := p.vers[a].cost, p.vers[b].cost
		switch {
		case ca.less(cb):
			return -1
		case cb.less(ca):
			return 1
		}
		return 0
	})
}

func avoid(f *File) bool {
	for _, it := range Items(f.Fields["flags"]) {
		if it.Kind == Ident && (it.S == "avoid-version" || it.S == "deprecated") {
			return true
		}
	}
	return false
}

// prune marks versions that can never be installed with the provided toolchain: unavailable,
// conflicting with the toolchain, or with a dependency clause without any live alternative
// (fixpoint). This shrinks the search space before the search starts.
func (s *solver) prune() {
	for _, p := range s.pkgs {
		p.live = newBitset(len(p.vers))
		for i, v := range p.vers {
			v.dead = !v.avail || (!p.provided && s.conflictsToolchain(p, i))
			if !v.dead {
				p.live.set(i)
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range s.pkgs {
			for i, v := range p.vers {
				if v.dead {
					continue
				}
				for k := range v.deps {
					if !s.liveClause(&v.deps[k]) {
						v.dead, v.deadAt, changed = true, k, true
						p.live[i/64] &^= 1 << (i % 64)
						break
					}
				}
			}
		}
	}
}

func (s *solver) liveClause(c *cclause) bool {
	for _, alt := range c.alts {
		ok := true
		for _, a := range alt {
			if a.n < 0 || !a.set.intersects(s.pkgs[a.n].live) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (s *solver) conflictsToolchain(p *pkg, i int) bool {
	v := p.vers[i]
	for _, r := range p.revConf[i] {
		if s.pkgs[r.n].provided {
			return true
		}
	}
	for _, a := range v.conflicts {
		if s.pkgs[a.n].provided && a.set.has(0) {
			return true
		}
	}
	for _, c := range v.classes {
		for _, m := range s.members[c] {
			if s.pkgs[m.n].provided {
				return true
			}
		}
	}
	return false
}
