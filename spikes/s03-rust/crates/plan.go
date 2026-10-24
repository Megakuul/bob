package crates

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Unit is one crate compiled for one platform with one feature set: one compile derivation
// (plus a build-script compile and run derivation when the crate has build.rs).
type Unit struct {
	Key       string // name@version/host|target
	Name      string
	Version   string
	Locked    *LockedCrate // registry crate
	Ws        *WsCrate     // local crate
	Host      bool         // compiled for the build machine (proc-macro, build-dependency)
	Features  []string
	Deps      []UnitDep // normal deps -> --extern
	BuildDeps []UnitDep // build-dependencies -> --extern of the build script
	Metadata  string    // -C metadata / extra-filename hash
}

type UnitDep struct {
	Extern string // name used in code, '-' -> '_'
	Unit   *Unit
}

func (u *Unit) ProcMacro() bool {
	if u.Ws != nil {
		return u.Ws.Type == "proc-macro"
	}
	return u.Locked.Meta != nil && u.Locked.Meta.ProcMacro
}

func (u *Unit) BuildScript() string {
	if u.Ws != nil {
		return u.Ws.BuildRS
	}
	if u.Locked.Meta != nil {
		return u.Locked.Meta.Build
	}
	return ""
}

func (u *Unit) Links() string {
	if u.Locked != nil {
		return u.Locked.Links
	}
	return ""
}

// LibName is the crate name rustc sees.
func (u *Unit) LibName() string {
	if u.Locked != nil && u.Locked.Meta != nil && u.Locked.Meta.LibName != "" {
		return u.Locked.Meta.LibName
	}
	return strings.ReplaceAll(u.Name, "-", "_")
}

func (u *Unit) Edition() string {
	if u.Ws != nil {
		return u.Ws.Edition
	}
	return u.Locked.Meta.Edition
}

// Plan is the build graph for one root crate on one target platform.
type Plan struct {
	Units  []*Unit // dependency order
	Root   *Unit
	Target *Platform
	HostP  *Platform
}

type planDep struct {
	name, pkgKey, kind, target string
	optional, noDefault        bool
	features                   []string
}

// MakePlan applies cargo's resolver-2 feature rules to the locked graph: features unify per
// (crate, platform); build-dependencies and proc-macros (and their dependencies) are host units;
// target-specific dependencies are filtered by the unit's platform; dev-deps are skipped.
func MakePlan(w *Workspace, l *Lock, root string, target, host *Platform, rustc string) (*Plan, error) {
	if w.Get(root) == nil {
		return nil, fmt.Errorf("unknown crate %s", root)
	}
	lockIdx := map[string]*LockedCrate{}
	for _, c := range l.Crates {
		lockIdx[c.Name+"@"+c.Version] = c
	}
	type node struct {
		u     *Unit
		feats map[string]bool
		ex    Expanded
		edges []planDep
		table map[string][]string
	}
	nodes := map[string]*node{}
	// edges of a package, resolved to lock keys
	edgesOf := func(key string) ([]planDep, *IndexEntry, error) {
		if c := lockIdx[key]; c != nil {
			e := &IndexEntry{Name: c.Name, Vers: c.Version, Features: c.Features}
			var ds []planDep
			for _, d := range c.Deps {
				e.Deps = append(e.Deps, IndexDep{Name: d.Name, Optional: d.Optional, Kind: d.Kind})
				ds = append(ds, planDep{name: d.Name, pkgKey: d.Crate, kind: d.Kind, target: d.Target, optional: d.Optional, noDefault: d.NoDefault, features: d.Features})
			}
			return ds, e, nil
		}
		name := strings.SplitN(key, "@", 2)[0]
		wc := w.Get(name)
		if wc == nil {
			return nil, nil, fmt.Errorf("%s is not in bob.lock (run bob lock)", key)
		}
		e := &IndexEntry{Name: wc.Name, Vers: wc.Version, Features: wc.Features}
		var ds []planDep
		for _, d := range wc.Deps {
			if d.Kind == "dev" {
				continue
			}
			pk := ""
			if d.Local {
				pk = d.Name + "@" + w.Get(d.Name).Version
			} else {
				pk = bestLocked(l, d)
				if pk == "" {
					return nil, nil, fmt.Errorf("%s: dependency %s %s is not in bob.lock (run bob lock)", wc.Name, d.Name, d.Req)
				}
			}
			e.Deps = append(e.Deps, IndexDep{Name: d.Name, Optional: d.Optional, Kind: d.Kind})
			ds = append(ds, planDep{name: d.Name, pkgKey: pk, kind: d.Kind, optional: d.Optional, noDefault: d.NoDefault, features: d.Features})
		}
		return ds, e, nil
	}
	entries := map[string]*IndexEntry{}
	get := func(key string, isHost bool) (*node, error) {
		uk := key + "/target"
		if isHost {
			uk = key + "/host"
		}
		if n := nodes[uk]; n != nil {
			return n, nil
		}
		ds, e, err := edgesOf(key)
		if err != nil {
			return nil, err
		}
		entries[key] = e
		parts := strings.SplitN(key, "@", 2)
		u := &Unit{Key: uk, Name: parts[0], Version: parts[1], Locked: lockIdx[key], Host: isHost}
		if u.Locked == nil {
			u.Ws = w.Get(parts[0])
		} else if u.Locked.Meta == nil {
			return nil, fmt.Errorf("%s has no metadata in bob.lock (run bob lock)", key)
		}
		n := &node{u: u, feats: map[string]bool{}, edges: ds}
		nodes[uk] = n
		return n, nil
	}
	type work struct {
		key   string
		host  bool
		feats []string
		deflt bool
	}
	rootKey := root + "@" + w.Get(root).Version
	queue := []work{{rootKey, false, w.Get(root).Enable, false}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		n, err := get(it.key, it.host)
		if err != nil {
			return nil, err
		}
		if n.u.ProcMacro() && !it.host {
			queue = append(queue, work{it.key, true, it.feats, it.deflt})
			continue
		}
		req := append([]string{}, it.feats...)
		if it.deflt {
			req = append(req, "default")
		}
		grew := n.ex.Feats == nil
		for _, f := range req {
			if !n.feats[f] {
				grew = true
			}
		}
		if !grew {
			continue
		}
		for _, f := range req {
			n.feats[f] = true
		}
		n.ex = Expand(entries[it.key], nil, n.feats, false)
		for f := range n.ex.Feats {
			n.feats[f] = true
		}
		pl := target
		if n.u.Host {
			pl = host
		}
		for _, d := range n.edges {
			if d.optional && !n.ex.Deps[d.name] {
				continue
			}
			ok, err := pl.MatchTarget(d.target)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", it.key, err)
			}
			if !ok {
				continue
			}
			childHost := n.u.Host || d.kind == "build"
			queue = append(queue, work{d.pkgKey, childHost, append(append([]string{}, d.features...), n.ex.DepFeats[d.name]...), !d.noDefault})
		}
	}
	// materialise units with edges (now that features are final)
	p := &Plan{Target: target, HostP: host}
	done := map[string]bool{}
	var visit func(n *node) error
	visit = func(n *node) error {
		if done[n.u.Key] {
			return nil
		}
		done[n.u.Key] = true
		for f := range n.ex.Feats {
			n.u.Features = append(n.u.Features, f)
		}
		sort.Strings(n.u.Features)
		pl := target
		if n.u.Host {
			pl = host
		}
		for _, d := range n.edges {
			if d.optional && !n.ex.Deps[d.name] {
				continue
			}
			if ok, _ := pl.MatchTarget(d.target); !ok {
				continue
			}
			childHost := n.u.Host || d.kind == "build"
			c := nodes[d.pkgKey+"/target"]
			if childHost || c == nil || c.u.ProcMacro() {
				c = nodes[d.pkgKey+"/host"]
			}
			if c == nil {
				return fmt.Errorf("internal: unit %s missing", d.pkgKey)
			}
			if err := visit(c); err != nil {
				return err
			}
			ud := UnitDep{Extern: strings.ReplaceAll(d.name, "-", "_"), Unit: c.u}
			if d.kind == "build" {
				n.u.BuildDeps = append(n.u.BuildDeps, ud)
			} else {
				n.u.Deps = append(n.u.Deps, ud)
			}
		}
		n.u.Metadata = metadataHash(n.u, rustc)
		p.Units = append(p.Units, n.u)
		return nil
	}
	rn := nodes[rootKey+"/target"]
	if err := visit(rn); err != nil {
		return nil, err
	}
	p.Root = rn.u
	return p, nil
}

// bestLocked finds the lock entry a local crate's dependency resolves to (highest matching).
func bestLocked(l *Lock, d WsDep) string {
	name := d.Name
	if d.Crate != "" {
		name = d.Crate
	}
	req, err := ParseReq(d.Req)
	if err != nil {
		return ""
	}
	best := ""
	var bv Version
	for _, c := range l.Crates {
		if c.Name != name {
			continue
		}
		v, err := ParseVersion(c.Version)
		if err == nil && req.Matches(v) && (best == "" || v.Compare(bv) > 0) {
			best, bv = c.Name+"@"+c.Version, v
		}
	}
	return best
}

// metadataHash identifies a unit like cargo's -C metadata: name, version, source, features,
// platform, profile, toolchain and the dependencies' hashes.
func metadataHash(u *Unit, rustc string) string {
	h := sha256.New()
	src := "local"
	if u.Locked != nil {
		src = u.Locked.Checksum
	}
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%v\x00%s\x00%s\x00", u.Name, u.Version, src, u.Host, strings.Join(u.Features, ","), rustc)
	for _, d := range append(append([]UnitDep{}, u.Deps...), u.BuildDeps...) {
		fmt.Fprintf(h, "%s=%s\x00", d.Extern, d.Unit.Metadata)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
