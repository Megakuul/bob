package resolve

import (
	"sort"
	"strings"
)

// featureValue is one entry of a feature's list (Cargo reference, "Features"):
// "f" (a feature), "dep:d" (an optional dependency), "d/f" or weak "d?/f".
type featureValue struct {
	feature string // plain feature, or the dependency's feature for d/f
	dep     string // dependency name for dep:d and d/f
	isDep   bool   // dep:d
	weak    bool   // d?/f
}

func parseFeatureValue(s string) featureValue {
	if d, ok := strings.CutPrefix(s, "dep:"); ok {
		return featureValue{dep: d, isDep: true}
	}
	if d, f, ok := strings.Cut(s, "/"); ok {
		w := strings.HasSuffix(d, "?")
		return featureValue{dep: strings.TrimSuffix(d, "?"), feature: f, weak: w}
	}
	return featureValue{feature: s}
}

// FeatureMap is the published table plus the implicit feature of every optional dependency
// whose name no "dep:" value mentions and that has no explicit feature of its own name.
func FeatureMap(table map[string][]string, deps []Dep) map[string][]string {
	explicit := map[string]bool{}
	for _, vs := range table {
		for _, v := range vs {
			if fv := parseFeatureValue(v); fv.isDep {
				explicit[fv.dep] = true
			}
		}
	}
	m := make(map[string][]string, len(table))
	for k, vs := range table {
		m[k] = vs
	}
	for _, d := range deps {
		if d.Optional && !explicit[d.Name] {
			if _, ok := m[d.Name]; !ok {
				m[d.Name] = []string{"dep:" + d.Name}
			}
		}
	}
	return m
}

// requirements is the closure of a feature request on one crate version as Cargo's lockfile
// resolver computes it: weak "d?/f" counts like "d/f" (it puts d into the lock).
type requirements struct {
	e        *Entry
	features map[string]bool            // features of e that are on
	deps     map[string]map[string]bool // dependency name -> features requested on it
	missing  string                     // first requested feature e does not have
}

func newRequirements(e *Entry) *requirements {
	return &requirements{e: e, features: map[string]bool{}, deps: map[string]map[string]bool{}}
}

func (r *requirements) requireFeature(f string) {
	if r.features[f] {
		return
	}
	r.features[f] = true
	vs, ok := r.e.Features[f]
	if !ok {
		if r.missing == "" {
			r.missing = f
		}
		return
	}
	for _, v := range vs {
		r.requireValue(parseFeatureValue(v))
	}
}

func (r *requirements) requireValue(fv featureValue) {
	switch {
	case fv.isDep:
		r.dep(fv.dep)
	case fv.dep != "":
		if !fv.weak && r.optionalDep(fv.dep) {
			if _, ok := r.e.Features[fv.dep]; ok {
				r.requireFeature(fv.dep)
			}
		}
		r.dep(fv.dep)[fv.feature] = true
	default:
		r.requireFeature(fv.feature)
	}
}

func (r *requirements) dep(name string) map[string]bool {
	m := r.deps[name]
	if m == nil {
		m = map[string]bool{}
		r.deps[name] = m
	}
	return m
}

func (r *requirements) optionalDep(name string) bool {
	for _, d := range r.e.Deps {
		if d.Name == name && d.Optional {
			return true
		}
	}
	return false
}

// activeDep is a dependency the lockfile resolver follows, with the features asked of it.
type activeDep struct {
	index    int // position in Entry.Deps
	features []string
}

// lockDeps returns the dependencies active for a request (Cargo's resolve_features): every
// non-optional dependency and the optional ones the request names; dev-dependencies only when
// dev is set. missing is a requested feature the version lacks ("" when none).
func lockDeps(e *Entry, features []string, all, useDefault, dev bool) (on map[string]bool, deps []activeDep, missing string) {
	r := newRequirements(e)
	if all {
		for _, k := range sortedKeys(e.Features) {
			r.requireFeature(k)
		}
	}
	for _, f := range features {
		r.requireValue(parseFeatureValue(f))
	}
	if useDefault {
		if _, ok := e.Features["default"]; ok {
			r.requireFeature("default")
		}
	}
	if r.missing != "" {
		return nil, nil, r.missing
	}
	for i, d := range e.Deps {
		if d.Kind == "dev" && !dev {
			continue
		}
		req, named := r.deps[d.Name]
		if d.Optional && !named {
			continue
		}
		set := map[string]bool{}
		for f := range req {
			set[f] = true
		}
		for _, f := range d.Features {
			set[f] = true
		}
		deps = append(deps, activeDep{index: i, features: sortedSet(set)})
	}
	return r.features, deps, ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, ok := range m {
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
