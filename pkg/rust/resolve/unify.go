package resolve

import (
	"fmt"
	"sort"
	"strings"
)

// Evaluation-time features: Cargo's feature resolver for resolver "2" and "3" (they differ
// only in lock-time rust-version handling). Features unify per (crate, role): build
// dependencies, proc-macros and everything below them are host units; platform-specific
// dependencies are filtered by the cfg table of the unit's platform; weak "d?/f" only applies
// when d is enabled by something else; dev-dependencies count only for members asked to test.

// Root is a workspace crate built at evaluation.
type Root struct {
	Member   string
	Features []string // e.g. BinOpts.Enable plus variant features
	Default  bool     // enable the "default" feature
	Dev      bool     // its tests are built: dev-dependencies count
}

// UnitKey identifies a unit: a crate ("name@version"; workspace crates "name@version" with
// Local set) in its role.
type UnitKey struct {
	Crate string
	Local bool
	Host  bool
}

func (k UnitKey) String() string {
	s := k.Crate
	if k.Local {
		s += " (local)"
	}
	if k.Host {
		s += " [host]"
	}
	return s
}

// UnitDep is an active dependency of a unit.
type UnitDep struct {
	Name string // name in Cargo.toml (rename key)
	Kind string // "" | "build" | "dev"
	To   UnitKey
}

// Unit is one crate in one role with its unified features and active dependencies.
type Unit struct {
	Key      UnitKey
	Features []string
	Deps     []UnitDep
}

// unifyCrate is a crate of the locked graph with resolved dependency targets.
type unifyCrate struct {
	key       UnitKey // Host unset
	deps      []Dep
	to        []*unifyCrate // to[i] resolves deps[i]
	features  map[string][]string
	procMacro bool
}

type unifyDep struct {
	d    Dep
	to   UnitKey
	crat *unifyCrate
}

type unifier struct {
	crates     map[UnitKey]*unifyCrate // by key with Host=false
	host, tgt  *Platform
	dev        map[string]bool // members whose dev-dependencies count
	features   map[UnitKey]map[string]bool
	activeDeps map[UnitKey]map[string]bool
	processed  map[UnitKey]bool
	deferred   map[UnitKey]map[string]map[string]bool // unit -> dep name -> features
}

// Unify computes the units of a build from bob.lock's crates (with Meta) and the members.
// host and target are the platforms of host and target units (the same for native builds).
func Unify(members []Member, crates []Crate, roots []Root, host, target *Platform) ([]Unit, error) {
	u := &unifier{crates: map[UnitKey]*unifyCrate{}, host: host, tgt: target, dev: map[string]bool{},
		features: map[UnitKey]map[string]bool{}, activeDeps: map[UnitKey]map[string]bool{},
		processed: map[UnitKey]bool{}, deferred: map[UnitKey]map[string]map[string]bool{}}
	if err := u.load(members, crates); err != nil {
		return nil, err
	}
	for _, r := range roots {
		if r.Dev {
			u.dev[r.Member] = true
		}
	}
	for _, r := range roots {
		var m *unifyCrate
		for k, c := range u.crates {
			if k.Local && strings.HasPrefix(k.Crate, r.Member+"@") {
				m = c
			}
		}
		if m == nil {
			return nil, fmt.Errorf("unknown workspace crate %s", r.Member)
		}
		var fvs []featureValue
		for _, f := range r.Features {
			fvs = append(fvs, parseFeatureValue(f))
		}
		if _, ok := m.features["default"]; ok && r.Default {
			fvs = append(fvs, featureValue{feature: "default"})
		}
		for _, f := range fvs {
			if f.dep == "" && !f.isDep {
				if _, ok := m.features[f.feature]; !ok {
					return nil, fmt.Errorf("crate %s has no feature %q", r.Member, f.feature)
				}
			}
		}
		key := m.key
		if m.procMacro {
			// Cargo also activates a proc-macro root for normal dependencies
			if err := u.activatePkg(key, fvs); err != nil {
				return nil, err
			}
			key.Host = true
		}
		if err := u.activatePkg(key, fvs); err != nil {
			return nil, err
		}
	}
	return u.units()
}

func (u *unifier) load(members []Member, crates []Crate) error {
	byKey := map[string]*unifyCrate{}
	byName := map[string][]*Crate{}
	for i := range crates {
		c := &crates[i]
		k := c.Name + "@" + c.Version
		if byKey[k] != nil {
			return fmt.Errorf("bob.lock holds %s twice", k)
		}
		var deps []Dep
		for _, d := range c.Deps {
			n, _, _ := strings.Cut(d.Crate, "@")
			deps = append(deps, Dep{Name: d.Name, Package: n, Kind: d.Kind, Target: d.Target, Optional: d.Optional,
				Default: !d.NoDefault, Features: d.Features})
		}
		uc := &unifyCrate{key: UnitKey{Crate: k}, deps: deps, features: FeatureMap(c.Features, deps), procMacro: c.Meta.ProcMacro}
		byKey[k] = uc
		byName[c.Name] = append(byName[c.Name], c)
		u.crates[uc.key] = uc
	}
	for i := range crates {
		c := &crates[i]
		uc := byKey[c.Name+"@"+c.Version]
		for _, d := range c.Deps {
			to := byKey[d.Crate]
			if to == nil {
				return fmt.Errorf("bob.lock: %s@%s depends on %s, which bob.lock does not hold", c.Name, c.Version, d.Crate)
			}
			uc.to = append(uc.to, to)
		}
	}
	local := map[string]*unifyCrate{}
	for _, m := range members {
		v := m.Version
		if v == "" {
			v = "0.0.0"
		}
		uc := &unifyCrate{key: UnitKey{Crate: m.Name + "@" + v, Local: true}, deps: m.Deps,
			features: FeatureMap(m.Features, m.Deps), procMacro: m.ProcMacro}
		local[m.Name] = uc
		u.crates[uc.key] = uc
	}
	for _, m := range members {
		uc := local[m.Name]
		for _, d := range m.Deps {
			var to *unifyCrate
			if d.Local {
				to = local[d.Package]
				if to == nil {
					return fmt.Errorf("crate %s depends on unknown workspace crate %s", m.Name, d.Package)
				}
			} else {
				c := MatchLocked(byName[d.Package], d.Req)
				if c == nil {
					return fmt.Errorf("crate %s depends on %s %s, which bob.lock does not hold (run bob lock)", m.Name, d.Package, d.Req)
				}
				to = byKey[c.Name+"@"+c.Version]
			}
			uc.to = append(uc.to, to)
		}
	}
	return nil
}

// deps lists the dependencies of a unit that its platform and role allow (optional ones
// included; the caller decides on them).
func (u *unifier) deps(k UnitKey) ([]unifyDep, error) {
	base := k
	base.Host = false
	c := u.crates[base]
	var out []unifyDep
	for i, d := range c.deps {
		if d.Kind == "dev" && !(k.Local && u.dev[strings.SplitN(k.Crate, "@", 2)[0]]) {
			continue
		}
		if d.Target != "" {
			p := u.tgt
			if k.Host || d.Kind == "build" {
				p = u.host
			}
			ok, err := p.Matches(d.Target)
			if err != nil {
				return nil, fmt.Errorf("%s: dependency %s: %w", k.Crate, d.Name, err)
			}
			if !ok {
				continue
			}
		}
		to := c.to[i]
		tk := to.key
		tk.Host = k.Host || d.Kind == "build" || to.procMacro
		out = append(out, unifyDep{d: d, to: tk, crat: to})
	}
	return out, nil
}

func (u *unifier) activatePkg(k UnitKey, fvs []featureValue) error {
	if !u.processed[k] {
		u.processed[k] = true
		if u.features[k] == nil {
			u.features[k] = map[string]bool{}
		}
		ds, err := u.deps(k)
		if err != nil {
			return err
		}
		for _, d := range ds {
			if d.d.Optional {
				continue
			}
			if err := u.activatePkg(d.to, fvsFromDep(d)); err != nil {
				return err
			}
		}
	}
	for _, fv := range fvs {
		if err := u.activateFV(k, fv); err != nil {
			return err
		}
	}
	return nil
}

func fvsFromDep(d unifyDep) []featureValue {
	var out []featureValue
	for _, f := range d.d.Features {
		out = append(out, parseFeatureValue(f))
	}
	if _, ok := d.crat.features["default"]; ok && d.d.Default {
		out = append(out, featureValue{feature: "default"})
	}
	return out
}

func (u *unifier) activateFV(k UnitKey, fv featureValue) error {
	switch {
	case fv.isDep:
		return u.activateDependency(k, fv.dep)
	case fv.dep != "":
		return u.activateDepFeature(k, fv.dep, fv.feature, fv.weak)
	}
	return u.activateRec(k, fv.feature)
}

func (u *unifier) activateRec(k UnitKey, f string) error {
	set := u.features[k]
	if set == nil {
		set = map[string]bool{}
		u.features[k] = set
	}
	if set[f] {
		return nil
	}
	set[f] = true
	base := k
	base.Host = false
	vs, ok := u.crates[base].features[f]
	if !ok {
		return fmt.Errorf("%s has no feature %q", k.Crate, f)
	}
	for _, v := range vs {
		if err := u.activateFV(k, parseFeatureValue(v)); err != nil {
			return err
		}
	}
	return nil
}

func (u *unifier) activateDependency(k UnitKey, name string) error {
	ad := u.activeDeps[k]
	if ad == nil {
		ad = map[string]bool{}
		u.activeDeps[k] = ad
	}
	toEnable := u.deferred[k][name]
	if ad[name] && toEnable == nil {
		return nil // same request as before: nothing new
	}
	ad[name] = true
	delete(u.deferred[k], name)
	ds, err := u.deps(k)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.d.Name != name {
			continue
		}
		for _, f := range sortedSet(toEnable) {
			if err := u.activateRec(d.to, f); err != nil {
				return err
			}
		}
		if err := u.activatePkg(d.to, fvsFromDep(d)); err != nil {
			return err
		}
	}
	return nil
}

func (u *unifier) activateDepFeature(k UnitKey, name, feat string, weak bool) error {
	ds, err := u.deps(k)
	if err != nil {
		return err
	}
	base := k
	base.Host = false
	for _, d := range ds {
		if d.d.Name != name {
			continue
		}
		if d.d.Optional {
			if weak && !u.activeDeps[k][name] {
				if u.deferred[k] == nil {
					u.deferred[k] = map[string]map[string]bool{}
				}
				if u.deferred[k][name] == nil {
					u.deferred[k][name] = map[string]bool{}
				}
				u.deferred[k][name][feat] = true
				continue
			}
			if err := u.activateDependency(k, name); err != nil {
				return err
			}
			if _, ok := u.crates[base].features[name]; !weak && ok {
				if err := u.activateRec(k, name); err != nil {
					return err
				}
			}
		}
		if err := u.activateRec(d.to, feat); err != nil {
			return err
		}
	}
	return nil
}

func (u *unifier) units() ([]Unit, error) {
	var out []Unit
	for k := range u.processed {
		un := Unit{Key: k, Features: sortedSet(u.features[k])}
		ds, err := u.deps(k)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if d.d.Optional && !u.activeDeps[k][d.d.Name] {
				continue
			}
			un.Deps = append(un.Deps, UnitDep{Name: d.d.Name, Kind: d.d.Kind, To: d.to})
		}
		out = append(out, un)
	}
	sort.Slice(out, func(i, j int) bool { return unitLess(out[i].Key, out[j].Key) })
	return out, nil
}

func unitLess(a, b UnitKey) bool {
	if a.Crate != b.Crate {
		return a.Crate < b.Crate
	}
	if a.Local != b.Local {
		return a.Local
	}
	return !a.Host && b.Host
}
