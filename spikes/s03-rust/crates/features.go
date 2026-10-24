package crates

import "strings"

// Expanded is the result of applying a feature set to one crate version.
type Expanded struct {
	Feats    map[string]bool     // enabled feature names of this crate
	Deps     map[string]bool     // enabled optional dependencies (by name used in code)
	DepFeats map[string][]string // features to enable on dependencies (by name used in code)
}

// Expand computes the feature closure of e for the requested features (Cargo reference,
// "Features": `dep:x`, implicit features of optional deps, `x/f`, weak `x?/f`).
// Unknown requested features are ignored here; bob reports them earlier for local crates.
//
// lockMode reproduces cargo's lockfile resolver, which treats weak `x?/f` like `x/f` (the dep is
// locked even though a build only activates it when something else enables it). Observed by
// diffing against `cargo generate-lockfile` (tracing-core `valuable?/std` locks valuable).
func Expand(e *IndexEntry, requested []string, already map[string]bool, lockMode bool) Expanded {
	table := e.AllFeatures()
	optional := map[string]bool{}
	for _, d := range e.Deps {
		if d.Optional {
			optional[d.Name] = true
		}
	}
	// optional deps referenced by "dep:x" anywhere have no implicit feature
	explicit := map[string]bool{}
	for _, vals := range table {
		for _, v := range vals {
			if strings.HasPrefix(v, "dep:") {
				explicit[v[4:]] = true
			}
		}
	}
	x := Expanded{Feats: map[string]bool{}, Deps: map[string]bool{}, DepFeats: map[string][]string{}}
	type weak struct{ dep, feat string }
	var weaks []weak
	var add func(f string)
	add = func(f string) {
		switch {
		case strings.HasPrefix(f, "dep:"):
			x.Deps[f[4:]] = true
		case strings.Contains(f, "/"):
			i := strings.IndexByte(f, '/')
			dep, feat := f[:i], f[i+1:]
			if strings.HasSuffix(dep, "?") && lockMode {
				dep = strings.TrimSuffix(dep, "?")
			} else if strings.HasSuffix(dep, "?") {
				weaks = append(weaks, weak{strings.TrimSuffix(dep, "?"), feat})
				return
			}
			x.DepFeats[dep] = append(x.DepFeats[dep], feat)
			if optional[dep] {
				x.Deps[dep] = true
				if !explicit[dep] {
					add(dep) // the implicit feature of the optional dep
				}
			}
		default:
			if x.Feats[f] {
				return
			}
			if vals, ok := table[f]; ok {
				x.Feats[f] = true
				for _, v := range vals {
					add(v)
				}
			} else if optional[f] && !explicit[f] {
				x.Feats[f] = true // implicit feature
				x.Deps[f] = true
			}
		}
	}
	for f := range already {
		add(f)
	}
	for _, f := range requested {
		add(f)
	}
	for _, w := range weaks {
		if x.Deps[w.dep] || !optional[w.dep] {
			x.DepFeats[w.dep] = append(x.DepFeats[w.dep], w.feat)
		}
	}
	return x
}
